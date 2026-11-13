package service

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

var (
	ErrImageGenerationHistoryNotFound = infraerrors.New(http.StatusNotFound, "IMAGE_GENERATION_HISTORY_NOT_FOUND", "image generation history not found")
	ErrImageGenerationHistoryNoFile   = infraerrors.New(http.StatusNotFound, "IMAGE_GENERATION_HISTORY_NO_CONTENT", "image generation history has no stored content")
	ErrImageGenerationHistoryInvalid  = infraerrors.New(http.StatusBadRequest, "IMAGE_GENERATION_HISTORY_INVALID", "invalid image generation history payload")
	ErrImageGenerationHistoryUploadIncomplete = infraerrors.New(http.StatusBadRequest, "IMAGE_GENERATION_HISTORY_UPLOAD_INCOMPLETE", "image history upload was interrupted or could not be read; please retry")
	ErrImageGenerationHistoryUploadInvalid = infraerrors.New(http.StatusBadRequest, "IMAGE_GENERATION_HISTORY_UPLOAD_INVALID", "image history upload must contain valid multipart form data")
	ErrImageGenerationHistoryTooLarge = infraerrors.New(http.StatusRequestEntityTooLarge, "IMAGE_GENERATION_HISTORY_TOO_LARGE", "image history upload exceeds the allowed size")
	ErrImageGenerationHistoryKeyInvalid = infraerrors.New(http.StatusBadRequest, "IMAGE_GENERATION_HISTORY_KEY_INVALID", "invalid API key ID for image history")
)

// ImageGenerationHistoryRecord 是一条生图历史。Content 只在内联存储时有值；
// 走对象存储时由 ContentURL 承载，两者互斥。
type ImageGenerationHistoryRecord struct {
	ID                int64           `json:"id"`
	UserID            int64           `json:"user_id"`
	TaskID            string          `json:"task_id,omitempty"`
	APIKeyID          *int64          `json:"api_key_id,omitempty"`
	Model             string          `json:"model"`
	Provider          string          `json:"provider"`
	Prompt            string          `json:"prompt"`
	NormalizedParams  json.RawMessage `json:"normalized_params,omitempty"`
	ReferenceSnapshot json.RawMessage `json:"reference_snapshot,omitempty"`
	Content           []byte          `json:"-"`
	ContentType       string          `json:"content_type"`
	SizeBytes         int64           `json:"size_bytes"`
	ContentURL        string          `json:"content_url,omitempty"`
	StorageObjectID   string          `json:"storage_object_id,omitempty"`
	LibraryAssetID    string          `json:"library_asset_id,omitempty"`
	Status            string          `json:"status"`
	Error             string          `json:"error,omitempty"`
	Archived          bool            `json:"-"`
	CreatedAt         time.Time       `json:"created_at"`

	// 列表不载入图片字节，由仓储单独查询图片是否存在。
	StoredContentAvailable bool `json:"-"`
}

// ContentAvailable 表示这条记录当前能否取回图片：要么内联字节还在，要么有外链。
func (r *ImageGenerationHistoryRecord) ContentAvailable() bool {
	if r == nil {
		return false
	}
	if r.Archived {
		return false
	}
	return r.StoredContentAvailable || len(r.Content) > 0 || r.ContentURL != ""
}

// ImageGenerationHistoryCreate 是归档一条记录的入参。
type ImageGenerationHistoryCreate struct {
	TaskID            string
	APIKeyID          *int64
	Model             string
	Provider          string
	Prompt            string
	NormalizedParams  json.RawMessage
	ReferenceSnapshot json.RawMessage
	Content           []byte
	ContentType       string
	ContentURL        string
	StorageObjectID   string
	LibraryAssetID    string
	Status            string
	Error             string
}

// ImageGenerationRepository 持久化生图历史。
type ImageGenerationRepository interface {
	// Create 插入一条记录。task_id 已存在时返回已有记录（幂等归档）。
	Create(ctx context.Context, userID int64, in *ImageGenerationHistoryCreate) (*ImageGenerationHistoryRecord, bool, error)
	// ListByUser 按创建时间倒序分页。archived 行不出现在列表中。
	ListByUser(ctx context.Context, userID int64, params pagination.PaginationParams) ([]ImageGenerationHistoryRecord, int64, error)
	// Get 读取单条，并限定归属用户。
	Get(ctx context.Context, id, userID int64) (*ImageGenerationHistoryRecord, error)
	// Delete 删除一条，并限定归属用户。
	Delete(ctx context.Context, id, userID int64) error
	// MarkArchived 把仍在生成中的任务标记为墓碑：图片落库时立即删除内容，只留记录。
	MarkArchived(ctx context.Context, taskID string, userID int64) error
	// DeleteExpired 删除超过保留期的记录，返回删除条数。
	DeleteExpired(ctx context.Context, cutoff time.Time, limit int) (int64, error)
}
