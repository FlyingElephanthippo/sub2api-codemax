//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type affiliateBindSettingsStub struct {
	SettingRepository
	enabled bool
}

func (s *affiliateBindSettingsStub) GetValue(_ context.Context, key string) (string, error) {
	if key == SettingKeyAffiliateEnabled && s.enabled {
		return "true", nil
	}
	return "", ErrSettingNotFound
}

type affiliateBindRepoStub struct {
	AffiliateRepository
	self         *AffiliateSummary
	inviter      *AffiliateSummary
	lookupCode   string
	lookupErr    error
	bindErr      error
	bindResult   bool
	bindCalls    int
	accrueCalls  int
	boundUserID  int64
	boundInviter int64
}

func (r *affiliateBindRepoStub) EnsureUserAffiliate(_ context.Context, userID int64) (*AffiliateSummary, error) {
	if userID == r.self.UserID {
		return r.self, nil
	}
	return r.inviter, nil
}

func (r *affiliateBindRepoStub) GetAffiliateByCode(_ context.Context, code string) (*AffiliateSummary, error) {
	r.lookupCode = code
	return r.inviter, r.lookupErr
}

func (r *affiliateBindRepoStub) BindInviter(_ context.Context, userID, inviterID int64) (bool, error) {
	r.bindCalls++
	r.boundUserID, r.boundInviter = userID, inviterID
	if r.bindErr == nil && r.bindResult {
		r.self.InviterID = &inviterID
	}
	return r.bindResult, r.bindErr
}

func (r *affiliateBindRepoStub) AccrueQuota(_ context.Context, _, _ int64, _ float64, _ int, _ *int64) (bool, error) {
	r.accrueCalls++
	return true, nil
}

func newAffiliateBindTestService(repo *affiliateBindRepoStub, enabled bool) *AffiliateService {
	return NewAffiliateService(repo, NewSettingService(&affiliateBindSettingsStub{enabled: enabled}, nil), nil, nil)
}

func TestSupplementInviterByCode_BindsOnceWithoutIssuingPastRebates(t *testing.T) {
	createdAt := time.Now().AddDate(0, 0, -10)
	repo := &affiliateBindRepoStub{
		self:    &AffiliateSummary{UserID: 7, CreatedAt: createdAt},
		inviter: &AffiliateSummary{UserID: 9}, bindResult: true,
	}
	svc := newAffiliateBindTestService(repo, true)
	require.NoError(t, svc.SupplementInviterByCode(context.Background(), 7, "  vip2026  "))
	require.Equal(t, "VIP2026", repo.lookupCode)
	require.EqualValues(t, 7, repo.boundUserID)
	require.EqualValues(t, 9, repo.boundInviter)
	require.Equal(t, createdAt, repo.self.CreatedAt, "binding must not reset the rebate duration")
	require.Zero(t, repo.accrueCalls, "supplementing must not issue historical rebates")

	require.ErrorIs(t, svc.SupplementInviterByCode(context.Background(), 7, "OTHER"), ErrAffiliateAlreadyBound)
	require.Equal(t, 1, repo.bindCalls)
	rebate, err := svc.AccrueInviteRebate(context.Background(), 7, 100)
	require.NoError(t, err)
	require.Equal(t, 20.0, rebate, "subsequent recharge follows the existing rebate rules")
	require.Equal(t, 1, repo.accrueCalls)
}

func TestSupplementInviterByCode_RejectsInvalidBindings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    string
		inviter *AffiliateSummary
		err     error
	}{
		{name: "empty", code: "   ", inviter: &AffiliateSummary{UserID: 9}},
		{name: "invalid format", code: "BAD CODE", inviter: &AffiliateSummary{UserID: 9}},
		{name: "missing code", code: "MISSING", err: ErrAffiliateProfileNotFound},
		{name: "self invitation", code: "SELF", inviter: &AffiliateSummary{UserID: 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &affiliateBindRepoStub{self: &AffiliateSummary{UserID: 7}, inviter: tc.inviter, lookupErr: tc.err, bindResult: true}
			require.ErrorIs(t, newAffiliateBindTestService(repo, true).SupplementInviterByCode(context.Background(), 7, tc.code), ErrAffiliateCodeInvalid)
			require.Zero(t, repo.bindCalls)
		})
	}
}

func TestSupplementInviterByCode_ReportsDisabledAndConcurrentBinding(t *testing.T) {
	repo := &affiliateBindRepoStub{self: &AffiliateSummary{UserID: 7}, inviter: &AffiliateSummary{UserID: 9}}
	require.ErrorIs(t, newAffiliateBindTestService(repo, false).SupplementInviterByCode(context.Background(), 7, "VIP2026"), ErrAffiliateDisabled)
	require.Zero(t, repo.bindCalls)
	require.ErrorIs(t, newAffiliateBindTestService(repo, true).SupplementInviterByCode(context.Background(), 7, "VIP2026"), ErrAffiliateAlreadyBound)
	require.Equal(t, 1, repo.bindCalls)
}

func TestSupplementInviterByCode_PropagatesBindingErrors(t *testing.T) {
	for _, want := range []error{ErrAffiliateCodeInvalid, errors.New("database unavailable")} {
		repo := &affiliateBindRepoStub{self: &AffiliateSummary{UserID: 7}, inviter: &AffiliateSummary{UserID: 9}, bindErr: want}
		require.ErrorIs(t, newAffiliateBindTestService(repo, true).SupplementInviterByCode(context.Background(), 7, "VIP2026"), want)
	}
}

func TestBindInviterByCode_PreservesOptionalRegistrationBehavior(t *testing.T) {
	inviterID := int64(9)
	repo := &affiliateBindRepoStub{self: &AffiliateSummary{UserID: 7, InviterID: &inviterID}}
	require.NoError(t, newAffiliateBindTestService(repo, true).BindInviterByCode(context.Background(), 7, ""))
	require.NoError(t, newAffiliateBindTestService(repo, false).BindInviterByCode(context.Background(), 7, "VIP2026"))
	require.NoError(t, newAffiliateBindTestService(repo, true).BindInviterByCode(context.Background(), 7, "OTHER"))
	require.Zero(t, repo.bindCalls)
}

func TestAffiliateService_AccrueInviteRebateAt_UsesRechargeTimeInsteadOfProcessingTime(t *testing.T) {
	boundAt := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name        string
		rechargedAt time.Time
		wantRebate  float64
	}{
		{name: "recharge before binding", rechargedAt: boundAt.Add(-time.Nanosecond)},
		{name: "same event boundary", rechargedAt: boundAt, wantRebate: 20},
		{name: "recharge after binding", rechargedAt: boundAt.Add(time.Nanosecond), wantRebate: 20},
		{name: "missing event time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inviterID := int64(9)
			repo := &affiliateBindRepoStub{
				self:    &AffiliateSummary{UserID: 7, InviterID: &inviterID, InviterBoundAt: &boundAt, CreatedAt: boundAt.Add(-time.Hour)},
				inviter: &AffiliateSummary{UserID: inviterID},
			}
			rebate, err := newAffiliateBindTestService(repo, true).AccrueInviteRebateAt(context.Background(), 7, 100, tc.rechargedAt)
			require.NoError(t, err)
			require.Equal(t, tc.wantRebate, rebate)
			if tc.wantRebate == 0 {
				require.Zero(t, repo.accrueCalls)
			} else {
				require.Equal(t, 1, repo.accrueCalls)
			}
		})
	}
}
