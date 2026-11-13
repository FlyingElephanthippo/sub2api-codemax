//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type userAffiliateBindRepoStub struct {
	service.AffiliateRepository
	boundUserID int64
	bindErr     error
}

func (r *userAffiliateBindRepoStub) EnsureUserAffiliate(_ context.Context, userID int64) (*service.AffiliateSummary, error) {
	return &service.AffiliateSummary{UserID: userID}, nil
}

func (r *userAffiliateBindRepoStub) GetAffiliateByCode(_ context.Context, _ string) (*service.AffiliateSummary, error) {
	return &service.AffiliateSummary{UserID: 9}, nil
}

func (r *userAffiliateBindRepoStub) BindInviter(_ context.Context, userID, _ int64) (bool, error) {
	r.boundUserID = userID
	return r.bindErr == nil, r.bindErr
}

type userAffiliateBindSettingsStub struct{ service.SettingRepository }

func (s *userAffiliateBindSettingsStub) GetValue(context.Context, string) (string, error) {
	return "true", nil
}

func TestUserHandlerBindAffiliate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name    string
		body    string
		userID  int64
		bindErr error
		status  int
		reason  string
	}{
		{name: "uses authenticated identity", body: `{"aff_code":"VIP2026","user_id":888}`, userID: 7, status: http.StatusOK},
		{name: "unauthenticated", body: `{"aff_code":"VIP2026"}`, status: http.StatusUnauthorized},
		{name: "missing code", body: `{}`, userID: 7, status: http.StatusBadRequest},
		{name: "invalid json", body: `{`, userID: 7, status: http.StatusBadRequest},
		{name: "already bound", body: `{"aff_code":"VIP2026"}`, userID: 7, bindErr: service.ErrAffiliateAlreadyBound, status: http.StatusConflict, reason: "AFFILIATE_ALREADY_BOUND"},
		{name: "invalid or cyclic invitation", body: `{"aff_code":"VIP2026"}`, userID: 7, bindErr: service.ErrAffiliateCodeInvalid, status: http.StatusBadRequest, reason: "AFFILIATE_CODE_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &userAffiliateBindRepoStub{bindErr: tc.bindErr}
			h := &UserHandler{affiliateService: service.NewAffiliateService(repo, service.NewSettingService(&userAffiliateBindSettingsStub{}, nil), nil, nil)}
			router := gin.New()
			router.POST("/api/v1/user/aff/bind", func(c *gin.Context) {
				if tc.userID > 0 {
					c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: tc.userID})
				}
				h.BindAffiliate(c)
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/user/aff/bind", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			var resp struct {
				Reason string          `json:"reason"`
				Data   json.RawMessage `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			if tc.reason != "" {
				require.Equal(t, tc.reason, resp.Reason)
			}
			if tc.status == http.StatusOK {
				require.JSONEq(t, `{"bound":true}`, string(resp.Data))
				require.EqualValues(t, 7, repo.boundUserID)
			} else if tc.bindErr == nil {
				require.Zero(t, repo.boundUserID)
			}
		})
	}
}
