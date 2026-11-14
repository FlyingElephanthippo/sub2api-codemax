//go:build domestic_ip_integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/domesticip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAdminDomesticIPPostgresImmediateGrantWithoutPaymentOrOwner(t *testing.T) {
	db := domesticIPIntegrationDB(t)
	_, err := db.Exec(`UPDATE users SET role = 'admin' WHERE id = 1`)
	require.NoError(t, err)
	actor := service.AdminDomesticIPActor{UserID: 1, AuthMethod: "jwt", SourceIP: "127.0.0.1", RequestID: "operator-test"}
	admin := NewAdminDomesticIPRepository(db)
	users := NewDomesticIPGrantRepository(db)
	data := []byte(`{"version":1,"generated_at":"2026-11-14T00:00:00Z","cn_cidrs":["1.0.1.0/24","240e::/20"],"manual_ips":["1.0.1.8"]}`)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	require.NoError(t, os.WriteFile(path, data, 0600))
	digest := sha256.Sum256(data)
	policy, err := domesticip.NewPolicy(domesticip.Options{Enabled: true, SnapshotFile: path, SnapshotSHA256: hex.EncodeToString(digest[:])})
	require.NoError(t, err)
	access := service.NewDomesticIPAccessService(policy, service.NewDomesticIPGrantService(users, nil, nil))
	ctx := context.Background()
	for _, ip := range []string{"1.0.1.7", "240e::7"} {
		r := httptest.NewRequest("GET", "/v1/images/tasks/example", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Forwarded-For", ip)
		require.ErrorIs(t, access.Check(r), service.ErrDomesticIPDenied)
		entry, err := admin.Add(ctx, ip, "operator permit", actor)
		require.NoError(t, err)
		require.NoError(t, access.Check(r), "same service instance observes the committed grant immediately")
		require.NoError(t, admin.Delete(ctx, entry.ID, actor))
		require.ErrorIs(t, access.Check(r), service.ErrDomesticIPDenied, "same instance observes revocation immediately")
		require.NoError(t, admin.Delete(ctx, entry.ID, actor), "a repeated deletion is idempotent")
	}
	var userGrants, payments, auditEvents int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM user_ip_grants`).Scan(&userGrants))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM payment_orders`).Scan(&payments))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM admin_domestic_ip_grant_events WHERE actor_user_id=1 AND auth_method='jwt' AND source_ip='127.0.0.1'::inet AND request_id='operator-test'`).Scan(&auditEvents))
	require.Zero(t, userGrants)
	require.Zero(t, payments)
	require.Equal(t, 4, auditEvents)
	// Static and foreign rules continue to bypass the grant lookup during DB failure.
	require.NoError(t, db.Close())
	for _, ip := range []string{"1.0.1.8", "8.8.8.8"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Forwarded-For", ip)
		require.NoError(t, access.Check(r))
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "1.0.1.7")
	require.ErrorIs(t, access.Check(r), service.ErrDomesticIPUnavailable)
}

func TestAdminDomesticIPPostgresConcurrentIdempotenceAndAuditRollback(t *testing.T) {
	db := domesticIPIntegrationDB(t)
	_, err := db.Exec(`UPDATE users SET role = 'admin' WHERE id = 1`)
	require.NoError(t, err)
	repo := NewAdminDomesticIPRepository(db)
	actor := service.AdminDomesticIPActor{UserID: 1, AuthMethod: "admin_api_key", SourceIP: "127.0.0.1"}
	ctx := context.Background()
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	ids := make(chan int64, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry, err := repo.Add(ctx, "1.0.1.7", "same request", actor)
			errors <- err
			if err == nil {
				ids <- entry.ID
			}
		}()
	}
	wg.Wait()
	close(errors)
	close(ids)
	for err := range errors {
		require.NoError(t, err)
	}
	var id int64
	for value := range ids {
		if id == 0 {
			id = value
		}
		require.Equal(t, id, value)
	}
	var events int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM admin_domestic_ip_grant_events`).Scan(&events))
	require.Equal(t, 1, events, "duplicate adds cannot duplicate audit events")
	_, err = db.Exec(`CREATE FUNCTION reject_operator_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit write failed'; END $$;
        CREATE TRIGGER reject_operator_audit BEFORE INSERT ON admin_domestic_ip_grant_events FOR EACH ROW EXECUTE FUNCTION reject_operator_audit();`)
	require.NoError(t, err)
	_, err = repo.Add(ctx, "1.0.1.9", "must roll back", actor)
	require.Error(t, err)
	require.Error(t, repo.Delete(ctx, id, actor))
	items, total, err := repo.List(ctx, 1, 20, "")
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	require.Equal(t, "1.0.1.7", items[0].IP, "audit failure rolls back both insertion and deletion")
	_, err = db.Exec(`DROP TRIGGER reject_operator_audit ON admin_domestic_ip_grant_events`)
	require.NoError(t, err)
	_, err = repo.Add(ctx, "1.0.1.9", "no admin", service.AdminDomesticIPActor{UserID: 2, AuthMethod: "jwt"})
	require.Error(t, err)
	_, err = db.Exec(`UPDATE users SET status='disabled' WHERE id=1`)
	require.NoError(t, err)
	require.Error(t, repo.Delete(ctx, id, actor), "inactive administrator cannot mutate")
}

func TestAdminDomesticIPPostgresDeletionPreservesIndependentUserGrant(t *testing.T) {
	db := domesticIPIntegrationDB(t)
	_, err := db.Exec(`UPDATE users SET role='admin' WHERE id=1;
        INSERT INTO payment_orders(user_id,amount,pay_amount,paid_at) VALUES(2,100,200,now())`)
	require.NoError(t, err)
	ctx := context.Background()
	users := NewDomesticIPGrantRepository(db)
	_, err = users.Mutate(ctx, service.DomesticIPGrantChange{UserID: 2, Operation: "add", SourceIP: "1.0.1.7"})
	require.NoError(t, err)
	admin := NewAdminDomesticIPRepository(db)
	actor := service.AdminDomesticIPActor{UserID: 1, AuthMethod: "jwt"}
	entry, err := admin.Add(ctx, "1.0.1.7", "temporary parallel permit", actor)
	require.NoError(t, err)
	require.NoError(t, admin.Delete(ctx, entry.ID, actor))
	allowed, err := users.HasActiveGrant(ctx, "1.0.1.7")
	require.NoError(t, err)
	require.True(t, allowed)
	_, err = db.Exec(`UPDATE payment_orders SET status='REFUNDED'`)
	require.NoError(t, err)
	allowed, err = users.HasActiveGrant(ctx, "1.0.1.7")
	require.NoError(t, err)
	require.False(t, allowed)
	for i := 1; i <= 3; i++ {
		_, err = admin.Add(ctx, fmt.Sprintf("1.0.1.%d", i), fmt.Sprintf("office %d", i), actor)
		require.NoError(t, err)
	}
	items, total, err := admin.List(ctx, 2, 1, "office")
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Len(t, items, 1)
	require.Equal(t, "1.0.1.2", items[0].IP)
}
