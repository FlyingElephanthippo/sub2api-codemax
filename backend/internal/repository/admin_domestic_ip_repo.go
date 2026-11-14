package repository

import (
	"context"
	"database/sql"
	"errors"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type adminDomesticIPRepository struct{ db *sql.DB }

func NewAdminDomesticIPRepository(db *sql.DB) service.AdminDomesticIPRepository {
	return &adminDomesticIPRepository{db: db}
}

func (r *adminDomesticIPRepository) List(ctx context.Context, page, pageSize int, search string) ([]service.AdminDomesticIPEntry, int64, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	const filter = ` WHERE $1 = '' OR strpos(host(ip), $1) > 0 OR strpos(lower(note), lower($1)) > 0`
	var total int64
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM admin_domestic_ip_grants`+filter, search).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, host(ip), note, created_at, updated_at FROM admin_domestic_ip_grants`+filter+` ORDER BY id DESC LIMIT $2 OFFSET $3`, search, pageSize, int64(page-1)*int64(pageSize))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []service.AdminDomesticIPEntry{}
	for rows.Next() {
		var item service.AdminDomesticIPEntry
		if err = rows.Scan(&item.ID, &item.IP, &item.Note, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func lockAdminDomesticIPMutation(ctx context.Context, tx *sql.Tx, actor service.AdminDomesticIPActor) error {
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 AND role = 'admin' AND status = 'active' AND deleted_at IS NULL FOR SHARE`, actor.UserID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return infraerrors.Forbidden("ADMIN_DOMESTIC_IP_ACTOR_REQUIRED", "需要有效的管理员身份")
		}
		return err
	}
	// Low-volume operator writes share a lock, making add/delete retries and audit events atomic.
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(1685024112, 252)`)
	return err
}

func insertAdminDomesticIPEvent(ctx context.Context, tx *sql.Tx, operation string, item *service.AdminDomesticIPEntry, actor service.AdminDomesticIPActor) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO admin_domestic_ip_grant_events
        (grant_id, ip, operation, note, actor_user_id, auth_method, source_ip, request_id)
        VALUES ($1, $2::inet, $3, $4, $5, $6, NULLIF($7, '')::inet, $8)`,
		item.ID, item.IP, operation, item.Note, actor.UserID, actor.AuthMethod, actor.SourceIP, actor.RequestID)
	return err
}

func (r *adminDomesticIPRepository) Add(ctx context.Context, ip, note string, actor service.AdminDomesticIPActor) (*service.AdminDomesticIPEntry, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockAdminDomesticIPMutation(ctx, tx, actor); err != nil {
		return nil, err
	}
	var item service.AdminDomesticIPEntry
	err = tx.QueryRowContext(ctx, `INSERT INTO admin_domestic_ip_grants (ip, note, created_by)
        VALUES ($1::inet, $2, $3) ON CONFLICT (ip) DO NOTHING
        RETURNING id, host(ip), note, created_at, updated_at`, ip, note, actor.UserID).
		Scan(&item.ID, &item.IP, &item.Note, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id, host(ip), note, created_at, updated_at FROM admin_domestic_ip_grants WHERE ip = $1::inet`, ip).
			Scan(&item.ID, &item.IP, &item.Note, &item.CreatedAt, &item.UpdatedAt)
	} else if err == nil {
		err = insertAdminDomesticIPEvent(ctx, tx, "add", &item, actor)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *adminDomesticIPRepository) Delete(ctx context.Context, id int64, actor service.AdminDomesticIPActor) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockAdminDomesticIPMutation(ctx, tx, actor); err != nil {
		return err
	}
	var item service.AdminDomesticIPEntry
	err = tx.QueryRowContext(ctx, `DELETE FROM admin_domestic_ip_grants WHERE id = $1 RETURNING id, host(ip), note, created_at, updated_at`, id).
		Scan(&item.ID, &item.IP, &item.Note, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if err = insertAdminDomesticIPEvent(ctx, tx, "delete", &item, actor); err != nil {
		return err
	}
	return tx.Commit()
}
