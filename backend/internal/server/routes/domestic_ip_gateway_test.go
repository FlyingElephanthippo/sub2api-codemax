package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/domesticip"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type domesticGatewayKeyRepo struct {
	service.APIKeyRepository
	key *service.APIKey
}

func (r *domesticGatewayKeyRepo) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	if key != r.key.Key {
		return nil, service.ErrAPIKeyNotFound
	}
	clone := *r.key
	return &clone, nil
}
func (r *domesticGatewayKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

type domesticGatewayGrantRepo struct {
	service.DomesticIPGrantRepository
	allowed bool
	err     error
	calls   int
}

func (r *domesticGatewayGrantRepo) HasActiveGrant(context.Context, string) (bool, error) {
	r.calls++
	return r.allowed, r.err
}

func domesticGatewayFixture(t *testing.T, runMode string, grants *domesticGatewayGrantRepo) (*gin.Engine, *service.APIKeyService, *config.Config) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	data := []byte(`{"version":1,"generated_at":"2026-11-13T00:00:00Z","cn_cidrs":["1.0.1.0/24","240e::/20"],"manual_ips":["1.0.1.8"]}`)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	require.NoError(t, os.WriteFile(path, data, 0600))
	digest := sha256.Sum256(data)
	policy, err := domesticip.NewPolicy(domesticip.Options{Enabled: true, SnapshotFile: path, SnapshotSHA256: hex.EncodeToString(digest[:])})
	require.NoError(t, err)
	cfg := &config.Config{RunMode: runMode, DomesticIP: config.DomesticIPConfig{Enabled: true}, Gateway: config.GatewayConfig{MaxBodySize: 1 << 20, TextMaxBodySize: 1 << 20}}
	group := &service.Group{ID: 42, Status: service.StatusActive, Hydrated: true, Platform: service.PlatformOpenAI, SubscriptionType: service.SubscriptionTypeStandard}
	key := &service.APIKey{ID: 99, UserID: 7, Key: "domestic-route-synthetic-key", Status: service.StatusActive,
		User: &service.User{ID: 7, Status: service.StatusActive, Role: service.RoleUser, Balance: 10}, GroupID: &group.ID, Group: group}
	grantService := service.NewDomesticIPGrantService(grants, nil, nil)
	access := service.NewDomesticIPAccessService(policy, grantService)
	keyService := service.ProvideAPIKeyService(&domesticGatewayKeyRepo{key: key}, nil, nil, nil, nil, nil, cfg, nil, nil, access)
	r := gin.New()
	RegisterGatewayRoutes(r, &handler.Handlers{Gateway: &handler.GatewayHandler{}, OpenAIGateway: &handler.OpenAIGatewayHandler{}, AsyncImage: handler.NewAsyncImageHandler(nil, nil)},
		servermiddleware.NewAPIKeyAuthMiddleware(keyService, nil, cfg), keyService, nil, nil, nil, nil, cfg)
	return r, keyService, cfg
}

func domesticGatewayRequest(method, path, source string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(`{"model":"synthetic"}`))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer domestic-route-synthetic-key")
	r.Header.Set("X-Forwarded-For", source)
	r.Header.Set("CF-Connecting-IP", "1.0.1.8")
	r.Header.Set("X-Real-IP", "1.0.1.8")
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestDomesticIPGatewayAliasesCannotBypass(t *testing.T) {
	paths := []struct{ method, path string }{
		{"POST", "/v1/responses"}, {"POST", "/responses"}, {"GET", "/responses"},
		{"POST", "/backend-api/codex/responses/compact"}, {"GET", "/backend-api/codex/models"},
		{"POST", "/v1/chat/completions"}, {"POST", "/chat/completions"},
		{"POST", "/v1/images/generations/async"}, {"GET", "/v1/images/tasks/synthetic"},
		{"GET", "/images/tasks/synthetic"}, {"GET", "/v1/images/batches/synthetic/download"},
		{"GET", "/v1/videos/synthetic/content"}, {"GET", "/realtime"},
		{"GET", "/v1/usage"}, {"GET", "/v1/sub2api/billing"},
		{"POST", "/v1beta/models/gemini:generateContent"}, {"GET", "/v1beta/cachedContents"},
		{"POST", "/antigravity/v1/messages"}, {"GET", "/antigravity/models"},
		{"POST", "/antigravity/v1beta/models/gemini:generateContent"},
		{"POST", "/api/v3/contents/generations/tasks"}, {"GET", "/v3/contents/generations/tasks/synthetic"},
	}
	for _, mode := range []string{config.RunModeStandard, config.RunModeSimple} {
		t.Run(mode, func(t *testing.T) {
			repo := &domesticGatewayGrantRepo{}
			r, _, _ := domesticGatewayFixture(t, mode, repo)
			for _, route := range paths {
				t.Run(route.method+route.path, func(t *testing.T) {
					w := httptest.NewRecorder()
					r.ServeHTTP(w, domesticGatewayRequest(route.method, route.path, "1.0.1.7"))
					require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
					require.Contains(t, w.Body.String(), "当前国内 IP 尚未放行")
				})
			}
			require.Equal(t, len(paths), repo.calls)
		})
	}
}

func TestDomesticIPAuthSucceedsOnlyWithExpectedGateDecision(t *testing.T) {
	for _, google := range []bool{false, true} {
		for _, tc := range []struct {
			name, ip, peer string
			allowed        bool
			lookupErr      error
			status         int
		}{
			{"manual", "1.0.1.8", "127.0.0.1:1234", false, nil, 200},
			{"foreign", "8.8.8.8", "127.0.0.1:1234", false, nil, 200},
			{"granted IPv4", "1.0.1.7", "127.0.0.1:1234", true, nil, 200},
			{"granted IPv6", "240e::7", "127.0.0.1:1234", true, nil, 200},
			{"lookup unavailable", "1.0.1.7", "127.0.0.1:1234", false, errors.New("offline"), 503},
			{"forged direct peer", "1.0.1.8", "8.8.8.8:1234", true, nil, 400},
		} {
			t.Run(tc.name+map[bool]string{false: "-openai", true: "-google"}[google], func(t *testing.T) {
				repo := &domesticGatewayGrantRepo{allowed: tc.allowed, err: tc.lookupErr}
				_, svc, cfg := domesticGatewayFixture(t, config.RunModeSimple, repo)
				r := gin.New()
				if google {
					r.Use(servermiddleware.APIKeyAuthWithSubscriptionGoogle(svc, nil, cfg))
				} else {
					r.Use(gin.HandlerFunc(servermiddleware.NewAPIKeyAuthMiddleware(svc, nil, cfg)))
				}
				reached := false
				r.GET("/check", func(c *gin.Context) { reached = true; c.Status(200) })
				w := httptest.NewRecorder()
				req := domesticGatewayRequest("GET", "/check", tc.ip)
				req.RemoteAddr = tc.peer
				r.ServeHTTP(w, req)
				require.Equal(t, tc.status, w.Code, w.Body.String())
				require.Equal(t, tc.status == 200, reached)
			})
		}
	}
}
