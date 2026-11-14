package admin

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

type adminDomesticIPHandlerRepo struct {
	service.AdminDomesticIPRepository
	actor service.AdminDomesticIPActor
	ip    string
	adds  int
}

func (r *adminDomesticIPHandlerRepo) Add(_ context.Context, ip, note string, actor service.AdminDomesticIPActor) (*service.AdminDomesticIPEntry, error) {
	r.actor, r.ip = actor, ip
	r.adds++
	return &service.AdminDomesticIPEntry{ID: 1, IP: ip, Note: note}, nil
}

func (r *adminDomesticIPHandlerRepo) List(context.Context, int, int, string) ([]service.AdminDomesticIPEntry, int64, error) {
	return nil, 0, nil
}

func TestAdminDomesticIPHandlerRequiresAdministratorAndUsesServerIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	data := []byte(`{"version":1,"generated_at":"2026-11-14T00:00:00Z","cn_cidrs":["1.0.1.0/24","240e::/20"],"manual_ips":[]}`)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	require.NoError(t, os.WriteFile(path, data, 0600))
	digest := sha256.Sum256(data)
	policy, err := domesticip.NewPolicy(domesticip.Options{Enabled: true, SnapshotFile: path, SnapshotSHA256: hex.EncodeToString(digest[:])})
	require.NoError(t, err)
	for _, tc := range []struct {
		name, role string
		id         int64
		status     int
	}{{"anonymous", "", 0, 401}, {"ordinary user", "user", 7, 403}, {"administrator", "admin", 7, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &adminDomesticIPHandlerRepo{}
			h := NewDomesticIPHandler(service.NewAdminDomesticIPService(repo, policy))
			r := gin.New()
			require.NoError(t, r.SetTrustedProxies(nil))
			r.Use(func(c *gin.Context) {
				if tc.id > 0 {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: tc.id})
					c.Set(string(middleware.ContextKeyUserRole), tc.role)
					c.Set("auth_method", "jwt")
				}
			})
			r.POST("/admin/domestic-ip/grants", h.Add)
			r.GET("/admin/domestic-ip/grants", h.List)
			request := httptest.NewRequest("POST", "/admin/domestic-ip/grants", strings.NewReader(`{"ip":"1.0.1.7","note":"reviewed","user_id":999,"actor_type":"user","source_ip":"1.0.1.7"}`))
			request.Header.Set("Content-Type", "application/json")
			request.RemoteAddr = "8.8.8.8:1234"
			w := httptest.NewRecorder()
			r.ServeHTTP(w, request)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == 200 {
				require.Equal(t, "1.0.1.7", repo.ip)
				require.Equal(t, int64(7), repo.actor.UserID)
				require.Equal(t, "jwt", repo.actor.AuthMethod)
				require.Equal(t, "8.8.8.8", repo.actor.SourceIP)
			} else {
				require.Zero(t, repo.adds)
			}
			w = httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/admin/domestic-ip/grants", nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
		})
	}
}
