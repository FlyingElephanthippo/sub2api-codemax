package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type domesticIPGrantRepoStub struct {
	snapshot *DomesticIPGrantSnapshot
	result   *DomesticIPGrantMutation
	changes  []DomesticIPGrantChange
	marked   string
	markedCh chan string
	allowed  bool
	err      error
}

func (r *domesticIPGrantRepoStub) Snapshot(context.Context, int64) (*DomesticIPGrantSnapshot, error) {
	return r.snapshot, r.err
}
func (r *domesticIPGrantRepoStub) Mutate(_ context.Context, c DomesticIPGrantChange) (*DomesticIPGrantMutation, error) {
	r.changes = append(r.changes, c)
	return r.result, r.err
}
func (r *domesticIPGrantRepoStub) HasActiveGrant(context.Context, string) (bool, error) {
	return r.allowed, r.err
}
func (r *domesticIPGrantRepoStub) MarkNotification(_ context.Context, _ int64, status string) error {
	r.marked = status
	if r.markedCh != nil {
		r.markedCh <- status
	}
	return nil
}

type domesticIPEmailStub struct {
	err  error
	sent int
}

func (e *domesticIPEmailStub) SendEmail(context.Context, string, string, string) error {
	e.sent++
	return e.err
}

type domesticIPUserStub struct{}

func (domesticIPUserStub) GetByID(context.Context, int64) (*User, error) {
	return &User{ID: 1, Email: "test@example.invalid"}, nil
}

func TestDomesticIPStatusPaymentBoundaryAndInvalidSource(t *testing.T) {
	for _, tc := range []struct {
		paid      int64
		eligible  bool
		remaining float64
	}{{19900, false, 1}, {20000, true, 0}, {20001, true, 0}} {
		repo := &domesticIPGrantRepoStub{snapshot: &DomesticIPGrantSnapshot{PaidCents: tc.paid, ChangesUsed: 5}}
		svc := NewDomesticIPGrantService(repo, nil, nil)
		state, err := svc.Status(context.Background(), 1, "192.168.1.1", true)
		require.NoError(t, err)
		require.Equal(t, tc.eligible, state.Eligible)
		require.Equal(t, tc.remaining, state.RemainingAmountCNY)
		require.False(t, state.CurrentIPValid)
		require.Empty(t, state.CurrentIP)
		require.NotNil(t, state.Entries)
		require.Zero(t, state.ChangesRemaining)
	}
}

func TestDomesticIPMutationsRejectNonPublicSourceBeforeWriting(t *testing.T) {
	for _, ip := range []string{"", "127.0.0.1", "10.0.0.1", "100.64.0.1", "2001:db8::1", "8.8.8.0/24", "fe80::1%eth0"} {
		repo := &domesticIPGrantRepoStub{}
		svc := NewDomesticIPGrantService(repo, nil, nil)
		_, err := svc.Add(context.Background(), 1, ip)
		require.ErrorIs(t, err, ErrDomesticIPGrantInvalidIP)
		_, err = svc.Replace(context.Background(), 1, 2, ip)
		require.ErrorIs(t, err, ErrDomesticIPGrantInvalidIP)
		_, err = svc.Delete(context.Background(), 1, 2, ip)
		require.ErrorIs(t, err, ErrDomesticIPGrantInvalidIP)
		require.Empty(t, repo.changes)
	}
}

func TestDomesticIPNotificationFailureDoesNotFailCommittedMutation(t *testing.T) {
	repo := &domesticIPGrantRepoStub{result: &DomesticIPGrantMutation{Changed: true, EventID: 12, Operation: "add", NewIP: "8.8.8.8", NotificationStatus: "pending"}, markedCh: make(chan string, 1)}
	email := &domesticIPEmailStub{err: errors.New("delivery unavailable")}
	svc := &DomesticIPGrantService{repo: repo, email: email, users: domesticIPUserStub{}, notificationSlots: make(chan struct{}, 4)}
	result, err := svc.Add(context.Background(), 1, "::ffff:8.8.8.8")
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, "pending", result.NotificationStatus)
	select {
	case status := <-repo.markedCh:
		require.Equal(t, "failed", status)
	case <-time.After(time.Second):
		t.Fatal("delivery status was not recorded")
	}
	require.Len(t, repo.changes, 1)
	require.Equal(t, "8.8.8.8", repo.changes[0].SourceIP)
	require.Equal(t, 1, email.sent)
}

func TestDomesticIPIdempotentRetryDoesNotSendNotification(t *testing.T) {
	repo := &domesticIPGrantRepoStub{result: &DomesticIPGrantMutation{Changed: false}}
	email := &domesticIPEmailStub{}
	svc := &DomesticIPGrantService{repo: repo, email: email, users: domesticIPUserStub{}}
	result, err := svc.Add(context.Background(), 1, "8.8.8.8")
	require.NoError(t, err)
	require.False(t, result.Changed)
	require.Zero(t, email.sent)
	require.Empty(t, repo.marked)
}

func TestDomesticIPNotificationConcurrencyIsBounded(t *testing.T) {
	repo := &domesticIPGrantRepoStub{result: &DomesticIPGrantMutation{Changed: true, EventID: 13, Operation: "add", NotificationStatus: "pending"}}
	email := &domesticIPEmailStub{}
	svc := &DomesticIPGrantService{repo: repo, email: email, users: domesticIPUserStub{}, notificationSlots: make(chan struct{}, 4)}
	for i := 0; i < cap(svc.notificationSlots); i++ {
		svc.notificationSlots <- struct{}{}
	}
	result, err := svc.Add(context.Background(), 1, "8.8.8.8")
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Zero(t, email.sent)
	require.Equal(t, "failed", result.NotificationStatus, "a saturated notifier must be visible in the audit, not fail the committed mutation")
}

func TestDomesticIPGrantLookupFailsClosedOnRepositoryError(t *testing.T) {
	repo := &domesticIPGrantRepoStub{err: errors.New("database unavailable")}
	allowed, err := NewDomesticIPGrantService(repo, nil, nil).HasActiveGrant(context.Background(), "8.8.8.8")
	require.Error(t, err)
	require.False(t, allowed)
}
