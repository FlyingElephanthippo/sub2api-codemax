package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type domesticIPGrantRepository struct {
	db *sql.DB
}

var _ service.DomesticIPGrantRepository = (*domesticIPGrantRepository)(nil)

func NewDomesticIPGrantRepository(db *sql.DB) service.DomesticIPGrantRepository {
	return &domesticIPGrantRepository{db: db}
}

// Mirrors calculateGatewayRefundAmount for CNY, including its one-cent full
// refund tolerance and per-order rounding. refund_amount uses credited USD,
// whereas pay_amount uses CNY. A requested/failed refund has not returned money.
const domesticIPNetPaidCentsExpression = `
COALESCE(ROUND(SUM(GREATEST(0,
    CASE
        WHEN p.status = 'REFUNDED' THEN 0
        WHEN p.status = 'PARTIALLY_REFUNDED' THEN
            p.pay_amount - CASE
                WHEN p.amount <= 0 OR COALESCE(p.refund_amount, 0) <= 0 THEN 0
                WHEN ABS(p.refund_amount - p.amount) <= 0.01 THEN ROUND(p.pay_amount, 2)
                ELSE ROUND(p.pay_amount * p.refund_amount / p.amount, 2)
            END
        ELSE p.pay_amount
    END
)) * 100), 0)::bigint`

const domesticIPPaidOrderFilter = `
p.order_type = 'balance' AND p.paid_at IS NOT NULL AND p.pay_amount > 0
AND COALESCE(NULLIF(UPPER(BTRIM(p.provider_snapshot->>'currency')), ''), 'CNY') = 'CNY'`

const domesticIPPaidQuery = `SELECT ` + domesticIPNetPaidCentsExpression + `
FROM payment_orders p WHERE p.user_id = $1 AND ` + domesticIPPaidOrderFilter

const domesticIPEntriesQuery = `SELECT id, host(ip), created_at, updated_at
FROM user_ip_grants WHERE user_id = $1 ORDER BY id`

const domesticIPQuotaQuery = `SELECT COUNT(*) FROM user_ip_grant_events
WHERE user_id = $1 AND local_day = ($2::timestamptz AT TIME ZONE 'Asia/Shanghai')::date AND actor_type = 'user'`

const domesticIPLockUserQuery = `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL AND status = 'active' FOR UPDATE`

func (r *domesticIPGrantRepository) Snapshot(ctx context.Context, userID int64) (_ *service.DomesticIPGrantSnapshot, err error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL AND status = 'active'`, userID).Scan(&id); err != nil {
		return nil, domesticIPUserError(err)
	}
	now, err := domesticIPDatabaseTime(ctx, tx)
	if err != nil {
		return nil, err
	}
	snapshot := &service.DomesticIPGrantSnapshot{}
	if err = tx.QueryRowContext(ctx, domesticIPPaidQuery, userID).Scan(&snapshot.PaidCents); err != nil {
		return nil, err
	}
	if snapshot.Entries, err = domesticIPReadEntries(ctx, tx, userID); err != nil {
		return nil, err
	}
	if err = tx.QueryRowContext(ctx, domesticIPQuotaQuery, userID, now).Scan(&snapshot.ChangesUsed); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (r *domesticIPGrantRepository) Mutate(ctx context.Context, change service.DomesticIPGrantChange) (_ *service.DomesticIPGrantMutation, err error) {
	if change.Operation != "add" && change.Operation != "replace" && change.Operation != "delete" {
		return nil, infraerrors.BadRequest("DOMESTIC_IP_OPERATION_INVALID", "不支持的操作")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var userID int64
	// Every writer for a user shares this lock. Read quotas only after waiting,
	// under READ COMMITTED, so a concurrent fifth change cannot be missed.
	if err = tx.QueryRowContext(ctx, domesticIPLockUserQuery, change.UserID).Scan(&userID); err != nil {
		return nil, domesticIPUserError(err)
	}
	now, err := domesticIPDatabaseTime(ctx, tx)
	if err != nil {
		return nil, err
	}
	entries, err := domesticIPReadEntries(ctx, tx, change.UserID)
	if err != nil {
		return nil, err
	}
	var target *service.DomesticIPGrantEntry
	for i := range entries {
		if entries[i].ID == change.EntryID {
			target = &entries[i]
		}
	}
	if change.Operation != "add" && target == nil {
		return nil, service.ErrDomesticIPGrantNotFound
	}
	for _, entry := range entries {
		if change.Operation != "delete" && entry.IP == change.SourceIP {
			if change.Operation == "add" || entry.ID == change.EntryID {
				// Retrying an already applied add/replace neither sends another
				// notice nor consumes another change, even after the daily cap.
				if err = tx.Commit(); err != nil {
					return nil, err
				}
				return &service.DomesticIPGrantMutation{Changed: false}, nil
			}
			return nil, infraerrors.Conflict("DOMESTIC_IP_ALREADY_PRESENT", "当前 IP 已在你的名单中，无需替换")
		}
	}
	if change.Operation != "delete" {
		var paidCents int64
		if err = tx.QueryRowContext(ctx, domesticIPPaidQuery, change.UserID).Scan(&paidCents); err != nil {
			return nil, err
		}
		if paidCents < service.DomesticIPGrantMinimumPaidCents {
			return nil, service.DomesticIPGrantEligibilityError(paidCents)
		}
	}
	if change.Operation == "add" && len(entries) >= service.DomesticIPGrantMaxIPs {
		return nil, service.ErrDomesticIPGrantMaxIPs
	}
	var used int
	if err = tx.QueryRowContext(ctx, domesticIPQuotaQuery, change.UserID, now).Scan(&used); err != nil {
		return nil, err
	}
	if used >= service.DomesticIPGrantDailyLimit {
		return nil, service.ErrDomesticIPGrantDailyLimit
	}
	result := &service.DomesticIPGrantMutation{Changed: true, Operation: change.Operation, NotificationStatus: "pending"}
	var oldIP, newIP any
	if target != nil {
		result.OldIP = target.IP
		oldIP = target.IP
	}
	if change.Operation != "delete" {
		result.NewIP = change.SourceIP
		newIP = change.SourceIP
	}
	if err = tx.QueryRowContext(ctx, `INSERT INTO user_ip_grant_events
    (user_id, actor_type, operation, old_ip, new_ip, source_ip, created_at, local_day)
    VALUES ($1, 'user', $2, $3::inet, $4::inet, $5::inet, $6, ($6::timestamptz AT TIME ZONE 'Asia/Shanghai')::date)
    RETURNING id`, change.UserID, change.Operation, oldIP, newIP, change.SourceIP, now).Scan(&result.EventID); err != nil {
		return nil, err
	}
	var changed sql.Result
	switch change.Operation {
	case "add":
		changed, err = tx.ExecContext(ctx, `INSERT INTO user_ip_grants
            (user_id, ip, added_from_ip, created_at, updated_at, revision) VALUES ($1, $2::inet, $2::inet, $3, $3, $4)`,
			change.UserID, change.SourceIP, now, result.EventID)
	case "replace":
		changed, err = tx.ExecContext(ctx, `UPDATE user_ip_grants SET ip = $1::inet,
            added_from_ip = $1::inet, updated_at = $2, revision = $3 WHERE id = $4 AND user_id = $5`,
			change.SourceIP, now, result.EventID, change.EntryID, change.UserID)
	case "delete":
		changed, err = tx.ExecContext(ctx, `DELETE FROM user_ip_grants WHERE id = $1 AND user_id = $2`, change.EntryID, change.UserID)
	}
	if err != nil {
		return nil, err
	}
	rows, err := changed.RowsAffected()
	if err != nil || rows != 1 {
		return nil, fmt.Errorf("domestic IP mutation affected unexpected row count: %d", rows)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *domesticIPGrantRepository) HasActiveGrant(ctx context.Context, ip string) (bool, error) {
	// A shared IP stays allowed while any active owner still qualifies. This
	// reads live payment state, so a successful refund needs no cache invalidation.
	const query = `SELECT EXISTS (
        SELECT 1 FROM user_ip_grants g JOIN users u ON u.id = g.user_id
        WHERE g.ip = $1::inet AND u.deleted_at IS NULL AND u.status = 'active'
        AND (SELECT ` + domesticIPNetPaidCentsExpression + `
             FROM payment_orders p WHERE p.user_id = g.user_id AND ` + domesticIPPaidOrderFilter + `) >= $2
    )`
	var allowed bool
	err := r.db.QueryRowContext(ctx, query, ip, service.DomesticIPGrantMinimumPaidCents).Scan(&allowed)
	return allowed, err
}

func (r *domesticIPGrantRepository) MarkNotification(ctx context.Context, eventID int64, status string) error {
	if status != "sent" && status != "failed" && status != "skipped" {
		return fmt.Errorf("invalid domestic IP notification status")
	}
	_, err := r.db.ExecContext(ctx, `UPDATE user_ip_grant_events SET notification_status = $1,
        notification_attempted_at = clock_timestamp() WHERE id = $2 AND notification_status = 'pending'`, status, eventID)
	return err
}

func domesticIPReadEntries(ctx context.Context, tx *sql.Tx, userID int64) ([]service.DomesticIPGrantEntry, error) {
	rows, err := tx.QueryContext(ctx, domesticIPEntriesQuery, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]service.DomesticIPGrantEntry, 0, service.DomesticIPGrantMaxIPs)
	for rows.Next() {
		var entry service.DomesticIPGrantEntry
		if err = rows.Scan(&entry.ID, &entry.IP, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func domesticIPDatabaseTime(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var now time.Time
	// Unlike transaction_timestamp, this is taken after a potentially long row
	// lock wait and therefore cannot charge a new-day change to yesterday.
	err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

func domesticIPUserError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return infraerrors.NotFound("USER_NOT_FOUND", "用户不存在或已停用")
	}
	return err
}
