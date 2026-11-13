//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type affiliateRechargeCacheBlocker struct {
	APIKeyAuthCacheInvalidator
	entered chan struct{}
	resume  chan struct{}
}

func (b *affiliateRechargeCacheBlocker) InvalidateAuthCacheByUserID(ctx context.Context, _ int64) {
	close(b.entered)
	select {
	case <-b.resume:
	case <-ctx.Done():
	}
}

func bindAfterRechargeCacheBlocks(t *testing.T, ctx context.Context, blocker *affiliateRechargeCacheBlocker, repo *affiliateBindRepoStub) time.Time {
	t.Helper()
	select {
	case <-blocker.entered:
	case <-ctx.Done():
		t.Fatal("recharge did not reach cache invalidation")
	}
	boundAt := time.Now()
	inviterID := repo.inviter.UserID
	repo.self.InviterID = &inviterID
	repo.self.InviterBoundAt = &boundAt
	close(blocker.resume)
	return boundAt
}

func TestAdminService_UpdateUserBalance_AffiliateBindingDuringCacheInvalidationDoesNotRebatePastRecharge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo := &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 7, Balance: 10}}}
	affiliateRepo := &affiliateBindRepoStub{self: &AffiliateSummary{UserID: 7, CreatedAt: time.Now().Add(-time.Hour)}, inviter: &AffiliateSummary{UserID: 9}}
	blocker := &affiliateRechargeCacheBlocker{entered: make(chan struct{}), resume: make(chan struct{})}
	settings := NewSettingService(&settingRepoStub{values: map[string]string{
		SettingKeyAffiliateEnabled: "true", SettingKeyAffiliateAdminRechargeEnabled: "true",
	}}, nil)
	svc := &adminServiceImpl{
		userRepo: repo, redeemCodeRepo: &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}},
		settingService: settings, affiliateService: NewAffiliateService(affiliateRepo, settings, nil, nil), authCacheInvalidator: blocker,
	}
	done := make(chan error, 1)
	go func() { _, err := svc.UpdateUserBalance(ctx, 7, 5, "add", ""); done <- err }()
	bindAfterRechargeCacheBlocks(t, ctx, blocker, affiliateRepo)
	require.NoError(t, <-done)
	require.Equal(t, 15.0, repo.userRepoStub.user.Balance)
	require.Zero(t, affiliateRepo.accrueCalls, "recharge completed before binding despite delayed cache invalidation")

	svc.authCacheInvalidator = nil
	_, err := svc.UpdateUserBalance(ctx, 7, 5, "add", "")
	require.NoError(t, err)
	require.Equal(t, 1, affiliateRepo.accrueCalls, "a subsequent recharge remains eligible")
}

func TestRedeemService_AffiliateBindingDuringCacheInvalidationDoesNotRebatePastRecharge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := newPaymentConfigServiceTestClient(t)
	code := &RedeemCode{ID: 103, Code: "SUPPLEMENT-CACHE-BOUNDARY", Type: RedeemTypeBalance, Value: 20, Status: StatusUnused}
	redeemRepo := &paymentFulfillmentRedeemRepo{paymentOrderLifecycleRedeemRepo: paymentOrderLifecycleRedeemRepo{codesByCode: map[string]*RedeemCode{code.Code: code}}}
	userRepo := &mockUserRepo{getByIDUser: &User{ID: 7}}
	userRepo.updateBalanceFn = func(context.Context, int64, float64) error { return nil }
	affiliateRepo := &affiliateBindRepoStub{self: &AffiliateSummary{UserID: 7, CreatedAt: time.Now().Add(-time.Hour)}, inviter: &AffiliateSummary{UserID: 9}}
	blocker := &affiliateRechargeCacheBlocker{entered: make(chan struct{}), resume: make(chan struct{})}
	svc := NewRedeemService(redeemRepo, userRepo, nil, nil, nil, client, blocker, newAffiliateBindTestService(affiliateRepo, true))
	type result struct {
		code *RedeemCode
		err  error
	}
	done := make(chan result, 1)
	go func() { code, err := svc.Redeem(ctx, 7, code.Code); done <- result{code, err} }()
	boundAt := bindAfterRechargeCacheBlocks(t, ctx, blocker, affiliateRepo)
	got := <-done
	require.NoError(t, got.err)
	require.NotNil(t, got.code.UsedAt)
	require.True(t, got.code.UsedAt.Before(boundAt))
	require.Equal(t, StatusUsed, got.code.Status)
	require.Zero(t, affiliateRepo.accrueCalls, "persisted redemption time must precede the later invitation binding")
}
