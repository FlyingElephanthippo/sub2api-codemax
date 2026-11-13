package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var domesticIPTestTime = time.Date(2026, 11, 13, 16, 0, 1, 0, time.UTC)

func domesticIPTestRepository(t *testing.T) (service.DomesticIPGrantRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()); _ = db.Close() })
	return NewDomesticIPGrantRepository(db), mock
}

func domesticIPExpectLockedState(mock sqlmock.Sqlmock, ids ...int64) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(domesticIPLockUserQuery)).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT clock_timestamp()")).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(domesticIPTestTime))
	rows := sqlmock.NewRows([]string{"id", "ip", "created_at", "updated_at"})
	for _, id := range ids {
		rows.AddRow(id, "8.8.8.8", domesticIPTestTime, domesticIPTestTime)
	}
	mock.ExpectQuery(regexp.QuoteMeta(domesticIPEntriesQuery)).WithArgs(int64(7)).WillReturnRows(rows)
}

func domesticIPExpectPaid(mock sqlmock.Sqlmock, cents int64) {
	mock.ExpectQuery(regexp.QuoteMeta(domesticIPPaidQuery)).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"paid"}).AddRow(cents))
}

func domesticIPExpectQuota(mock sqlmock.Sqlmock, used int) {
	mock.ExpectQuery(regexp.QuoteMeta(domesticIPQuotaQuery)).WithArgs(int64(7), domesticIPTestTime).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(used))
}

func TestDomesticIPFourthAddressAndSixthChangeAreRejected(t *testing.T) {
	t.Run("fourth address", func(t *testing.T) {
		repo, mock := domesticIPTestRepository(t)
		domesticIPExpectLockedState(mock, 1, 2, 3)
		domesticIPExpectPaid(mock, 20000)
		mock.ExpectRollback()
		_, err := repo.Mutate(context.Background(), service.DomesticIPGrantChange{UserID: 7, SourceIP: "1.1.1.1", Operation: "add"})
		require.ErrorIs(t, err, service.ErrDomesticIPGrantMaxIPs)
	})
	t.Run("sixth change", func(t *testing.T) {
		repo, mock := domesticIPTestRepository(t)
		domesticIPExpectLockedState(mock)
		domesticIPExpectPaid(mock, 20000)
		domesticIPExpectQuota(mock, 5)
		mock.ExpectRollback()
		_, err := repo.Mutate(context.Background(), service.DomesticIPGrantChange{UserID: 7, SourceIP: "1.1.1.1", Operation: "add"})
		require.ErrorIs(t, err, service.ErrDomesticIPGrantDailyLimit)
	})
}

func TestDomesticIPCannotMutateAnotherUsersEntry(t *testing.T) {
	for _, operation := range []string{"replace", "delete"} {
		t.Run(operation, func(t *testing.T) {
			repo, mock := domesticIPTestRepository(t)
			domesticIPExpectLockedState(mock, 12)
			mock.ExpectRollback()
			_, err := repo.Mutate(context.Background(), service.DomesticIPGrantChange{UserID: 7, EntryID: 999, SourceIP: "1.1.1.1", Operation: operation})
			require.ErrorIs(t, err, service.ErrDomesticIPGrantNotFound)
		})
	}
}

func TestDomesticIPDuplicateAddDoesNotConsumeQuota(t *testing.T) {
	repo, mock := domesticIPTestRepository(t)
	domesticIPExpectLockedState(mock, 12)
	mock.ExpectCommit()
	result, err := repo.Mutate(context.Background(), service.DomesticIPGrantChange{UserID: 7, SourceIP: "8.8.8.8", Operation: "add"})
	require.NoError(t, err)
	require.False(t, result.Changed)
	require.Zero(t, result.EventID)
}

func TestDomesticIPAuditFailurePreventsGrantWrite(t *testing.T) {
	repo, mock := domesticIPTestRepository(t)
	domesticIPExpectLockedState(mock)
	domesticIPExpectPaid(mock, 20000)
	domesticIPExpectQuota(mock, 4)
	mock.ExpectQuery("INSERT INTO user_ip_grant_events").WillReturnError(errors.New("audit unavailable"))
	mock.ExpectRollback()
	_, err := repo.Mutate(context.Background(), service.DomesticIPGrantChange{UserID: 7, SourceIP: "1.1.1.1", Operation: "add"})
	require.Error(t, err)
}

func TestDomesticIPDeleteAfterRefundStillAuditsAndConsumesChange(t *testing.T) {
	repo, mock := domesticIPTestRepository(t)
	domesticIPExpectLockedState(mock, 12)
	// There is intentionally no payment query for revoking one's own grant.
	domesticIPExpectQuota(mock, 0)
	mock.ExpectQuery("INSERT INTO user_ip_grant_events").
		WithArgs(int64(7), "delete", "8.8.8.8", nil, "1.1.1.1", domesticIPTestTime).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(51))
	mock.ExpectExec("DELETE FROM user_ip_grants WHERE id = \\$1 AND user_id = \\$2").
		WithArgs(int64(12), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	result, err := repo.Mutate(context.Background(), service.DomesticIPGrantChange{UserID: 7, EntryID: 12, SourceIP: "1.1.1.1", Operation: "delete"})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.EqualValues(t, 51, result.EventID)
	require.Equal(t, "pending", result.NotificationStatus)
}
