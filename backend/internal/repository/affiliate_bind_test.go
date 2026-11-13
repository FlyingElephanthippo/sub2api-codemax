package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func affiliateBindSummaryRows(userID int64, inviterID any) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"user_id", "aff_code", "aff_code_custom", "aff_rebate_rate_percent", "inviter_id", "inviter_bound_at", "aff_count", "aff_quota", "aff_frozen_quota", "aff_history_quota", "created_at", "updated_at"}).
		AddRow(userID, "TESTCODE", false, nil, inviterID, nil, 0, 0, 0, 0, time.Now(), time.Now())
}

func TestAffiliateRepositoryBindInviter_AtomicValidationAndCount(t *testing.T) {
	for _, tc := range []struct {
		name         string
		alreadyBound bool
		cycle        bool
		affected     int64
		countErr     error
		wantBound    bool
		wantErr      error
	}{
		{name: "bind and count in one transaction", affected: 1, wantBound: true},
		{name: "existing inviter cannot change", alreadyBound: true},
		{name: "reject ancestor cycle", cycle: true, wantErr: service.ErrAffiliateCodeInvalid},
		{name: "concurrent compare and set lost", affected: 0},
		{name: "count failure rolls back relationship", affected: 1, countErr: errors.New("count failed"), wantErr: errors.New("count failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(hashtext('sub2api:affiliate:inviter-bind'))")).WillReturnResult(sqlmock.NewResult(0, 1))
			var existingInviter any
			if tc.alreadyBound {
				existingInviter = int64(3)
			}
			mock.ExpectQuery(regexp.QuoteMeta("WHERE user_id = $1")).WithArgs(int64(1)).WillReturnRows(affiliateBindSummaryRows(1, existingInviter))
			if !tc.alreadyBound {
				mock.ExpectQuery(regexp.QuoteMeta("WHERE user_id = $1")).WithArgs(int64(2)).WillReturnRows(affiliateBindSummaryRows(2, nil))
				mock.ExpectQuery("WITH RECURSIVE ancestors").WithArgs(int64(2), int64(1)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tc.cycle))
				if !tc.cycle {
					mock.ExpectExec(regexp.QuoteMeta("UPDATE user_affiliates SET inviter_id = $1, inviter_bound_at = clock_timestamp(), updated_at = NOW() WHERE user_id = $2 AND inviter_id IS NULL")).WithArgs(int64(2), int64(1)).WillReturnResult(sqlmock.NewResult(0, tc.affected))
					if tc.affected > 0 {
						expect := mock.ExpectExec(regexp.QuoteMeta("UPDATE user_affiliates SET aff_count = aff_count + 1, updated_at = NOW() WHERE user_id = $1")).WithArgs(int64(2))
						if tc.countErr != nil {
							expect.WillReturnError(tc.countErr)
						} else {
							expect.WillReturnResult(sqlmock.NewResult(0, 1))
						}
					}
				}
			}
			if tc.wantErr != nil {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			bound, err := NewAffiliateRepository(client, db).BindInviter(context.Background(), 1, 2)
			if tc.wantErr != nil {
				require.ErrorContains(t, err, tc.wantErr.Error())
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantBound, bound)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAffiliateRepositoryBindInviter_RejectsSelfBeforeTransaction(t *testing.T) {
	repo := &affiliateRepository{}
	bound, err := repo.BindInviter(context.Background(), 1, 1)
	require.False(t, bound)
	require.ErrorIs(t, err, service.ErrAffiliateCodeInvalid)
}

func TestAffiliateRepositoryAccrueQuota_ChecksPaymentAtBindingBoundaryAtomically(t *testing.T) {
	for _, tc := range []struct {
		name        string
		affected    int64
		freezeHours int
	}{
		{name: "old payment or wrong invitee is skipped"},
		{name: "eligible payment enters available quota", affected: 1},
		{name: "eligible payment enters frozen quota", affected: 1, freezeHours: 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			mock.ExpectBegin()
			mock.ExpectExec(`(?s)UPDATE user_affiliates SET .*AND EXISTS \(.*po.id = \$4 AND po.user_id = invitee.user_id.*invitee.user_id = \$3 AND invitee.inviter_id = \$2.*po.paid_at >= COALESCE\(invitee.inviter_bound_at, invitee.created_at\)`).
				WithArgs(2.0, int64(9), int64(7), int64(99)).WillReturnResult(sqlmock.NewResult(0, tc.affected))
			if tc.affected > 0 {
				insert := mock.ExpectExec("INSERT INTO user_affiliate_ledger")
				if tc.freezeHours > 0 {
					insert.WithArgs(int64(9), 2.0, int64(7), int64(99), tc.freezeHours)
				} else {
					insert.WithArgs(int64(9), 2.0, int64(7), int64(99))
				}
				insert.WillReturnResult(sqlmock.NewResult(1, 1))
			}
			mock.ExpectCommit()
			orderID := int64(99)
			applied, err := NewAffiliateRepository(client, db).AccrueQuota(context.Background(), 9, 7, 2, tc.freezeHours, &orderID)
			require.NoError(t, err)
			require.Equal(t, tc.affected > 0, applied)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
