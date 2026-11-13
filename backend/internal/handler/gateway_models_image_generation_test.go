package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayModels_ImageGenerationPurposeUsesLocalGroupPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const groupID int64 = 9090
	for _, tc := range []struct {
		name      string
		mapping   map[string]any
		allowlist service.GroupModelAllowlist
		want      []string
	}{
		{
			name: "pinned OAuth group retains supported image model",
			mapping: map[string]any{
				"gpt-image-2": "gpt-image-2",
				"gpt-5.6-sol": "gpt-5.6-sol",
			},
			want: []string{"gpt-5.6-sol", "gpt-image-2"},
		},
		{
			name: "group allowlist can exclude image generation",
			mapping: map[string]any{
				"gpt-image-2": "gpt-image-2",
				"gpt-5.6-sol": "gpt-5.6-sol",
			},
			allowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.6-sol"}},
			want:      []string{"gpt-5.6-sol"},
		},
		{
			name: "group allowlist can select image generation only",
			mapping: map[string]any{
				"gpt-image-2": "gpt-image-2",
				"gpt-5.6-sol": "gpt-5.6-sol",
			},
			allowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-image-2"}},
			want:      []string{"gpt-image-2"},
		},
		{
			name:    "text only mapping never gains an image model",
			mapping: map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"},
			want:    []string{"gpt-5.6-sol"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{
				byGroup: map[int64][]service.Account{groupID: {{
					ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
					Credentials: map[string]any{"model_mapping": tc.mapping},
				}}},
			})
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/models?purpose=image-generation", nil)
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{
				ID: groupID, Platform: service.PlatformOpenAI, ModelAllowlist: tc.allowlist,
				CodexModelsManifestConfig: service.GroupCodexModelsManifestConfig{Enabled: true},
			}})

			h.Models(c)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var got gatewayModelsResponseForTest
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.Equal(t, "list", got.Object)
			require.Equal(t, tc.want, modelIDsForTest(got.Data))
		})
	}
}

func TestGatewayModels_OtherPurposesRetainPinnedDiscovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/models", "/v1/models?purpose=other"} {
		t.Run(path, func(t *testing.T) {
			h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{})
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, path, nil)
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{
				ID: 9090, Platform: service.PlatformOpenAI,
				CodexModelsManifestConfig: service.GroupCodexModelsManifestConfig{Enabled: true},
			}})

			h.Models(c)

			// The test has no upstream discovery service: regular requests must
			// still reach that path instead of falling back to a local catalog.
			require.Equal(t, http.StatusInternalServerError, rec.Code)
			require.Contains(t, rec.Body.String(), "OpenAI model discovery is not configured")
		})
	}
}
