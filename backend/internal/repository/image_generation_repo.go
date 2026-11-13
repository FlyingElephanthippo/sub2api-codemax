package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type imageGenerationRepository struct {
	sql sqlExecutor
}

var _ service.ImageGenerationRepository = (*imageGenerationRepository)(nil)

// NewImageGenerationRepository 创建生图历史仓储。
func NewImageGenerationRepository(sqlDB *sql.DB) service.ImageGenerationRepository {
	return &imageGenerationRepository{sql: sqlDB}
}

// 列表与单条查询共用列清单；content 只在需要字节时追加，避免列表把图片载入内存。
const imageGenerationHistoryColumns = `
	id, user_id, COALESCE(task_id, ''), api_key_id, model, provider, prompt,
	normalized_params, reference_snapshot, content_type, size_bytes, content_url,
	storage_object_id, library_asset_id, status, error, archived_at IS NOT NULL, created_at,
	COALESCE(octet_length(content), 0) > 0`

// historyScanDest 返回与 imageGenerationHistoryColumns 对齐的扫描目标。
// 传 withContent 时末尾追加 content 列。
func historyScanDest(rec *service.ImageGenerationHistoryRecord, withContent bool) ([]any, *sql.NullInt64, *[]byte, *[]byte, *[]byte, *time.Time) {
	var (
		apiKeyID  sql.NullInt64
		params    []byte
		snapshot  []byte
		content   []byte
		createdAt time.Time
	)
	dest := []any{
		&rec.ID, &rec.UserID, &rec.TaskID, &apiKeyID, &rec.Model, &rec.Provider, &rec.Prompt,
		&params, &snapshot, &rec.ContentType, &rec.SizeBytes, &rec.ContentURL,
		&rec.StorageObjectID, &rec.LibraryAssetID, &rec.Status, &rec.Error, &rec.Archived, &createdAt,
		&rec.StoredContentAvailable,
	}
	if withContent {
		dest = append(dest, &content)
	}
	return dest, &apiKeyID, &params, &snapshot, &content, &createdAt
}

func applyHistoryScan(
	rec *service.ImageGenerationHistoryRecord,
	apiKeyID *sql.NullInt64,
	params, snapshot, content *[]byte,
	createdAt *time.Time,
) {
	if apiKeyID.Valid {
		rec.APIKeyID = &apiKeyID.Int64
	}
	rec.NormalizedParams = json.RawMessage(*params)
	rec.ReferenceSnapshot = json.RawMessage(*snapshot)
	if content != nil {
		rec.Content = *content
	}
	rec.CreatedAt = *createdAt
}

// Create 插入一条历史；task_id 冲突时返回已有记录，让重复归档保持幂等。
func (r *imageGenerationRepository) Create(ctx context.Context, userID int64, in *service.ImageGenerationHistoryCreate) (*service.ImageGenerationHistoryRecord, bool, error) {
	params := normalizeJSONObject(in.NormalizedParams)
	snapshot := normalizeJSONArray(in.ReferenceSnapshot)
	status := in.Status
	if status == "" {
		status = "completed"
	}

	const insert = `
		INSERT INTO image_generation_history (
			user_id, task_id, api_key_id, model, provider, prompt, normalized_params,
			reference_snapshot, content, content_type, size_bytes, content_url,
			storage_object_id, library_asset_id, status, error
		) VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (task_id) DO NOTHING
		RETURNING` + imageGenerationHistoryColumns + `, content`

	var rec service.ImageGenerationHistoryRecord
	dest, apiKeyID, rawParams, rawSnap, content, createdAt := historyScanDest(&rec, true)
	err := scanSingleRow(ctx, r.sql, insert,
		[]any{
			userID, in.TaskID, in.APIKeyID, in.Model, in.Provider, in.Prompt, params,
			snapshot, in.Content, in.ContentType, len(in.Content), in.ContentURL,
			in.StorageObjectID, in.LibraryAssetID, status, in.Error,
		},
		dest...,
	)
	if err == nil {
		applyHistoryScan(&rec, apiKeyID, rawParams, rawSnap, content, createdAt)
		return &rec, true, nil
	}
	if err != sql.ErrNoRows {
		return nil, false, translatePersistenceError(err, nil, nil)
	}

	// ON CONFLICT DO NOTHING 吞掉插入后没有返回行：该 task_id 已被归档过。
	if in.TaskID == "" {
		return nil, false, service.ErrImageGenerationHistoryInvalid.WithCause(err)
	}
	existing, getErr := r.GetByTaskID(ctx, in.TaskID)
	if getErr != nil {
		return nil, false, getErr
	}
	return existing, false, nil
}

func (r *imageGenerationRepository) GetByTaskID(ctx context.Context, taskID string) (*service.ImageGenerationHistoryRecord, error) {
	var rec service.ImageGenerationHistoryRecord
	dest, apiKeyID, rawParams, rawSnap, _, createdAt := historyScanDest(&rec, false)
	err := scanSingleRow(ctx, r.sql,
		`SELECT`+imageGenerationHistoryColumns+` FROM image_generation_history WHERE task_id = $1`,
		[]any{taskID}, dest...)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrImageGenerationHistoryNotFound, nil)
	}
	applyHistoryScan(&rec, apiKeyID, rawParams, rawSnap, nil, createdAt)
	return &rec, nil
}

// ListByUser 按创建时间倒序返回未归档的历史（不含图片字节）。
func (r *imageGenerationRepository) ListByUser(ctx context.Context, userID int64, params pagination.PaginationParams) ([]service.ImageGenerationHistoryRecord, int64, error) {
	var total int64
	if err := scanSingleRow(ctx, r.sql,
		`SELECT COUNT(*) FROM image_generation_history WHERE user_id = $1 AND archived_at IS NULL`,
		[]any{userID}, &total); err != nil {
		return nil, 0, err
	}

	rows, err := r.sql.QueryContext(ctx,
		`SELECT`+imageGenerationHistoryColumns+`
		 FROM image_generation_history
		 WHERE user_id = $1 AND archived_at IS NULL
		 ORDER BY created_at DESC, id DESC
		 LIMIT $2 OFFSET $3`,
		userID, params.Limit(), params.Offset(),
	)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.ImageGenerationHistoryRecord, 0, params.Limit())
	for rows.Next() {
		var rec service.ImageGenerationHistoryRecord
		dest, apiKeyID, rawParams, rawSnap, _, createdAt := historyScanDest(&rec, false)
		if scanErr := rows.Scan(dest...); scanErr != nil {
			return nil, 0, scanErr
		}
		applyHistoryScan(&rec, apiKeyID, rawParams, rawSnap, nil, createdAt)
		items = append(items, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Get 读取单条（含图片字节），并限定归属用户。
func (r *imageGenerationRepository) Get(ctx context.Context, id, userID int64) (*service.ImageGenerationHistoryRecord, error) {
	var rec service.ImageGenerationHistoryRecord
	dest, apiKeyID, rawParams, rawSnap, content, createdAt := historyScanDest(&rec, true)
	err := scanSingleRow(ctx, r.sql,
		`SELECT`+imageGenerationHistoryColumns+`, content
		 FROM image_generation_history
		 WHERE id = $1 AND user_id = $2`,
		[]any{id, userID}, dest...)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrImageGenerationHistoryNotFound, nil)
	}
	applyHistoryScan(&rec, apiKeyID, rawParams, rawSnap, content, createdAt)
	return &rec, nil
}

// Delete 删除单条，并限定归属用户。
func (r *imageGenerationRepository) Delete(ctx context.Context, id, userID int64) error {
	res, err := r.sql.ExecContext(ctx,
		`DELETE FROM image_generation_history WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrImageGenerationHistoryNotFound
	}
	return nil
}

// MarkArchived 把任务标记为墓碑：清空内容并置 archived_at，列表不再返回，
// 同时保留 task_id，使任务完成后的再次归档变成幂等空操作。
func (r *imageGenerationRepository) MarkArchived(ctx context.Context, taskID string, userID int64) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE image_generation_history
		SET archived_at = NOW(), content = NULL, content_url = '', size_bytes = 0
		WHERE task_id = $1 AND user_id = $2`, taskID, userID)
	return err
}

// DeleteExpired 删除超过保留期的历史，返回删除条数。
func (r *imageGenerationRepository) DeleteExpired(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	res, err := r.sql.ExecContext(ctx, `
		DELETE FROM image_generation_history
		WHERE id IN (
			SELECT id FROM image_generation_history
			WHERE created_at < $1
			ORDER BY created_at
			LIMIT $2
		)`, cutoff, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func normalizeJSONObject(raw json.RawMessage) []byte {
	if len(raw) == 0 || !json.Valid(raw) {
		return []byte("{}")
	}
	return raw
}

func normalizeJSONArray(raw json.RawMessage) []byte {
	if len(raw) == 0 || !json.Valid(raw) {
		return []byte("[]")
	}
	return raw
}
