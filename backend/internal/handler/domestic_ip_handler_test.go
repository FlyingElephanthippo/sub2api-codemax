package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/domesticip"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type domesticIPHandlerRepo struct {
	changed []service.DomesticIPGrantChange
}

func (r *domesticIPHandlerRepo) Snapshot(context.Context, int64) (*service.DomesticIPGrantSnapshot, error) {
	return &service.DomesticIPGrantSnapshot{PaidCents: 20000}, nil
}
func (r *domesticIPHandlerRepo) Mutate(_ context.Context, change service.DomesticIPGrantChange) (*service.DomesticIPGrantMutation, error) {
	r.changed = append(r.changed, change)
	return &service.DomesticIPGrantMutation{}, nil
}
func (r *domesticIPHandlerRepo) HasActiveGrant(context.Context, string) (bool, error) {
	return false, nil
}
func (r *domesticIPHandlerRepo) MarkNotification(context.Context, int64, string) error { return nil }

func TestDomesticIPHandlerUsesOnlyObservedIP(t *testing.T) {
	data := []byte(`{"version":1,"generated_at":"2026-11-13T00:00:00Z","cn_cidrs":["1.0.1.0/24","240e::/20"],"manual_ips":[]}`)
	path := filepath.Join(t.TempDir(), "policy.json")
	require.NoError(t, os.WriteFile(path, data, 0600))
	digest := sha256.Sum256(data)
	policy, err := domesticip.NewPolicy(domesticip.Options{Enabled: true, SnapshotFile: path, SnapshotSHA256: hex.EncodeToString(digest[:])})
	require.NoError(t, err)
	for _, tc := range []struct {
		name, body, peer string
		code             int
	}{
		{"confirmed current", `{"expected_ip":"1.0.1.7"}`, "127.0.0.1:1", 200},
		{"changed network", `{"expected_ip":"1.0.1.8"}`, "127.0.0.1:1", 409},
		{"manual IP rejected", `{"expected_ip":"1.0.1.7","ip":"1.0.1.8"}`, "127.0.0.1:1", 400},
		{"confirmation required", `{}`, "127.0.0.1:1", 409},
		{"trailing body", `{"expected_ip":"1.0.1.7"}{}`, "127.0.0.1:1", 400},
		{"direct spoof", `{"expected_ip":"1.0.1.7"}`, "8.8.8.8:1", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &domesticIPHandlerRepo{}
			svc := service.NewDomesticIPGrantService(repo, nil, nil)
			h := NewDomesticIPHandler(svc, policy, service.NewDomesticIPAccessService(policy, svc))
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
				c.Next()
			})
			router.POST("/ip-grants", h.Add)
			req := httptest.NewRequest("POST", "/ip-grants", strings.NewReader(tc.body))
			req.RemoteAddr = tc.peer
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Forwarded-For", "1.0.1.7")
			req.Header.Set("CF-Connecting-IP", "1.0.1.8")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, tc.code, rec.Code, rec.Body.String())
			if tc.code == 200 {
				require.Len(t, repo.changed, 1)
				require.Equal(t, "1.0.1.7", repo.changed[0].SourceIP)
				require.EqualValues(t, 42, repo.changed[0].UserID)
			} else {
				require.Empty(t, repo.changed)
			}
		})
	}
}

func TestDomesticIPNetworkAccessFailsClosedWithoutEnabledPolicy(t *testing.T) {
	disabled, err := domesticip.NewPolicy(domesticip.Options{})
	require.NoError(t, err)
	for _, h := range []*DomesticIPHandler{nil, {}, {policy: disabled, access: service.NewDomesticIPAccessService(disabled, nil)}} {
		router := gin.New()
		router.GET("/api/v1/internal/domestic-ip/access", h.NetworkAccess)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/internal/domestic-ip/access", nil))
		require.Equal(t, 503, rec.Code)
	}
	var missing *DomesticIPHandler
	router := gin.New()
	router.GET("/content", missing.RequireAccess, func(c *gin.Context) { c.Status(200) })
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/content", nil))
	require.Equal(t, 503, rec.Code)
}
