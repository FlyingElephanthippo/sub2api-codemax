//go:build domestic_ip_integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// This suite only accepts an explicitly supplied local test database. It creates
// a fresh schema and cannot accidentally run against a production hostname.
func domesticIPIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DOMESTIC_IP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set DOMESTIC_IP_TEST_DATABASE_URL to a local disposable PostgreSQL database")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"localhost", "127.0.0.1", "::1"}, u.Hostname(), "only local disposable PostgreSQL is permitted")
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("domestic_ip_test_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	db.SetMaxOpenConns(16)
	t.Cleanup(func() {
		_ = db.Close()
		_, dropErr := admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		require.NoError(t, dropErr)
		_ = admin.Close()
	})
	_, err = db.Exec(`CREATE TABLE users (id bigint PRIMARY KEY, status text NOT NULL DEFAULT 'active', deleted_at timestamptz, total_recharged numeric DEFAULT 0);
        CREATE TABLE payment_orders (id bigserial PRIMARY KEY, user_id bigint NOT NULL, order_type text NOT NULL DEFAULT 'balance',
            paid_at timestamptz, status text NOT NULL DEFAULT 'COMPLETED', amount numeric NOT NULL, pay_amount numeric NOT NULL,
            bonus_amount numeric NOT NULL DEFAULT 0, refund_amount numeric, refund_at timestamptz, provider_snapshot jsonb);
        INSERT INTO users(id,total_recharged) VALUES (1,100000),(2,100000),(3,100000),(4,100000);`)
	require.NoError(t, err)
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "251_user_ip_grants.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	return db
}

func TestDomesticIPPostgresNetPaymentAndRefundSemantics(t *testing.T) {
	db := domesticIPIntegrationDB(t)
	repo := NewDomesticIPGrantRepository(db)
	for _, tc := range []struct {
		name                                    string
		pay, credited, refund, currency, status string
		paid, refunded                          bool
		want                                    int64
	}{
		{"199 boundary", "199", "1000", "0", "CNY", "COMPLETED", true, false, 19900},
		{"200 includes credited bonus but not as payment", "200", "1000", "0", "CNY", "COMPLETED", true, false, 20000},
		{"partial refund in credited units", "200", "1000", "250", "CNY", "PARTIALLY_REFUNDED", true, true, 15000},
		{"rounded partial gateway refund", "200", "333", "50", "CNY", "PARTIALLY_REFUNDED", true, true, 16997},
		{"full refund", "200", "1000", "1000", "CNY", "REFUNDED", true, true, 0},
		{"terminal refund with missing legacy timestamp", "200", "1000", "1000", "CNY", "REFUNDED", true, false, 0},
		{"requested refund is not successful", "200", "1000", "1000", "CNY", "REFUND_REQUESTED", true, false, 20000},
		{"failed refund is not successful", "200", "1000", "1000", "CNY", "REFUND_FAILED", true, false, 20000},
		{"paid fulfillment failure", "200", "1000", "0", "CNY", "FAILED", true, false, 20000},
		{"unpaid failure", "200", "1000", "0", "CNY", "FAILED", false, false, 0},
		{"foreign currency cannot be assumed CNY", "200", "1000", "0", "USD", "COMPLETED", true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec("TRUNCATE payment_orders")
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO payment_orders(user_id,amount,pay_amount,bonus_amount,refund_amount,status,paid_at,refund_at,provider_snapshot)
                VALUES(1,$1,$2,800,$3,$4,CASE WHEN $5 THEN now() END,CASE WHEN $6 THEN now() END,jsonb_build_object('currency',$7::text))`,
				tc.credited, tc.pay, tc.refund, tc.status, tc.paid, tc.refunded, tc.currency)
			require.NoError(t, err)
			state, err := repo.Snapshot(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, tc.want, state.PaidCents)
		})
	}
	_, err := db.Exec(`TRUNCATE payment_orders; INSERT INTO payment_orders(user_id,order_type,amount,pay_amount,paid_at)
        VALUES (1,'subscription',100,500,now()),(1,'balance',100,199,now()),(1,'balance',100,1,now())`)
	require.NoError(t, err)
	state, err := repo.Snapshot(context.Background(), 1)
	require.NoError(t, err)
	require.EqualValues(t, 20000, state.PaidCents, "only paid balance orders count; old missing currency snapshots follow CNY default")
}

func TestDomesticIPPostgresConcurrentLimitsAndSharedRefund(t *testing.T) {
	db := domesticIPIntegrationDB(t)
	repo := NewDomesticIPGrantRepository(db)
	_, err := db.Exec(`INSERT INTO payment_orders(user_id,amount,pay_amount,paid_at) VALUES (1,1000,200,now()),(2,1000,200,now())`)
	require.NoError(t, err)
	var successful atomic.Int32
	var wg sync.WaitGroup
	errors := make(chan error, 10)
	for i := 1; i <= 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := repo.Mutate(context.Background(), service.DomesticIPGrantChange{UserID: 1, Operation: "add", SourceIP: fmt.Sprintf("8.8.4.%d", i)})
			if err == nil && result.Changed {
				successful.Add(1)
			} else {
				errors <- err
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	require.EqualValues(t, 3, successful.Load())
	for err := range errors {
		require.ErrorIs(t, err, service.ErrDomesticIPGrantMaxIPs)
	}
	state, err := repo.Snapshot(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, state.Entries, 3)
	require.Equal(t, 3, state.ChangesUsed)
	successful.Store(0)
	errors = make(chan error, 10)
	for i := 1; i <= 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := repo.Mutate(context.Background(), service.DomesticIPGrantChange{UserID: 1, EntryID: state.Entries[0].ID, Operation: "replace", SourceIP: fmt.Sprintf("1.1.1.%d", i)})
			if err == nil && result.Changed {
				successful.Add(1)
			} else {
				errors <- err
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	require.EqualValues(t, 2, successful.Load())
	for err := range errors {
		require.ErrorIs(t, err, service.ErrDomesticIPGrantDailyLimit)
	}
	state, err = repo.Snapshot(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 5, state.ChangesUsed)
	shared := state.Entries[1].IP
	_, err = repo.Mutate(context.Background(), service.DomesticIPGrantChange{UserID: 2, Operation: "add", SourceIP: shared})
	require.NoError(t, err)
	_, err = db.Exec("UPDATE payment_orders SET status='REFUNDED',refund_amount=amount,refund_at=now() WHERE user_id=1")
	require.NoError(t, err)
	allowed, err := repo.HasActiveGrant(context.Background(), shared)
	require.NoError(t, err)
	require.True(t, allowed, "another qualifying owner still authorizes this shared IP")
	allowed, err = repo.HasActiveGrant(context.Background(), state.Entries[0].IP)
	require.NoError(t, err)
	require.False(t, allowed, "refund qualification is enforced immediately")
	_, err = db.Exec("UPDATE payment_orders SET status='REFUNDED',refund_amount=amount,refund_at=now() WHERE user_id=2")
	require.NoError(t, err)
	allowed, err = repo.HasActiveGrant(context.Background(), shared)
	require.NoError(t, err)
	require.False(t, allowed)
	other, err := repo.Snapshot(context.Background(), 2)
	require.NoError(t, err)
	_, err = repo.Mutate(context.Background(), service.DomesticIPGrantChange{UserID: 2, EntryID: other.Entries[0].ID, Operation: "delete", SourceIP: shared})
	require.NoError(t, err, "refund must not prevent revoking one's own record")
}

func TestDomesticIPPostgresDailyResetAndAuditAtomicity(t *testing.T) {
	db := domesticIPIntegrationDB(t)
	repo := NewDomesticIPGrantRepository(db)
	_, err := db.Exec(`INSERT INTO payment_orders(user_id,amount,pay_amount,paid_at) VALUES (1,1000,200,now());
        INSERT INTO user_ip_grant_events(user_id,operation,new_ip,source_ip,created_at,local_day)
        SELECT 1,'add','8.8.8.8','8.8.8.8',now()-interval '1 day',(now() AT TIME ZONE 'Asia/Shanghai')::date-1 FROM generate_series(1,5)`)
	require.NoError(t, err)
	_, err = repo.Mutate(context.Background(), service.DomesticIPGrantChange{UserID: 1, Operation: "add", SourceIP: "8.8.8.8"})
	require.NoError(t, err)
	state, err := repo.Snapshot(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, state.ChangesUsed)
	_, err = db.Exec(`ALTER TABLE user_ip_grants ADD CONSTRAINT reject_test_write CHECK (ip <> '1.1.1.1'::inet)`)
	require.NoError(t, err)
	_, err = repo.Mutate(context.Background(), service.DomesticIPGrantChange{UserID: 1, Operation: "add", SourceIP: "1.1.1.1"})
	require.Error(t, err)
	state, err = repo.Snapshot(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, state.ChangesUsed, "failed grant write must roll back its audit and quota change")
	require.Len(t, state.Entries, 1)
}
