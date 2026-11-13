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

func TestGatewayModelsMuseVideoPurposeHonorsGroupPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const groupID int64 = 9245
	for _, tc := range []struct {
		name, query string
		allowlist   service.GroupModelAllowlist
		want        []string
	}{
		{"all Muse capabilities", "", service.GroupModelAllowlist{}, []string{"muse-image", "muse-spark", "muse-video"}},
		{"video picker", "?purpose=video", service.GroupModelAllowlist{}, []string{"muse-video"}},
		{"restricted key", "?purpose=video", service.GroupModelAllowlist{Enabled: true, Models: []string{"muse-spark"}}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{groupID: {{
				ID: 1, Platform: service.PlatformGrok, Type: service.AccountTypeAPIKey,
				Credentials: map[string]any{"media_provider": "muse", "model_mapping": map[string]any{
					"muse-image": "muse-image", "muse-spark": "muse-spark", "muse-video": "muse-video",
				}},
			}}}})
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/models"+tc.query, nil)
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{
				ID: groupID, Platform: service.PlatformGrok, ModelAllowlist: tc.allowlist,
			}})
			h.Models(c)
			require.Equal(t, 200, rec.Code)
			var result gatewayModelsResponseForTest
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
			require.Equal(t, tc.want, modelIDsForTest(result.Data))
			for _, model := range result.Data {
				require.Equal(t, "muse", model.OwnedBy)
			}
		})
	}
}
