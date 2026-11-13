package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var imageGenerationHistoryTestColumns = []string{
	"id", "user_id", "task_id", "api_key_id", "model", "provider", "prompt",
	"normalized_params", "reference_snapshot", "content_type", "size_bytes", "content_url",
	"storage_object_id", "library_asset_id", "status", "error", "archived", "created_at", "has_content",
}

func TestImageGenerationHistoryListPreservesStoredContentAvailability(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM image_generation_history WHERE user_id = $1 AND archived_at IS NULL")).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	// The list must report stored content without selecting the image bytes.
	mock.ExpectQuery(`(?s)SELECT .*COALESCE\(octet_length\(content\), 0\) > 0.*FROM image_generation_history.*WHERE user_id = \$1 AND archived_at IS NULL.*LIMIT \$2 OFFSET \$3`).
		WithArgs(int64(7), 20, 0).
		WillReturnRows(sqlmock.NewRows(imageGenerationHistoryTestColumns).
			AddRow(11, 7, "", nil, "gpt-image-2", "openai", "a cat", []byte(`{}`), []byte(`[]`),
				"image/png", 128, "", "", "", "completed", "", false, time.Now(), true).
			AddRow(12, 7, "", nil, "gpt-image-2", "openai", "a dog", []byte(`{}`), []byte(`[]`),
				"image/png", 128, "", "", "", "completed", "", false, time.Now(), false))

	records, total, err := NewImageGenerationRepository(db).ListByUser(context.Background(), 7, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Len(t, records, 2)
	require.Empty(t, records[0].Content, "the list should not load image bytes")
	require.True(t, service.ImageGenerationHistoryItemFromRecord(&records[0]).ContentAvailable)
	require.False(t, service.ImageGenerationHistoryItemFromRecord(&records[1]).ContentAvailable,
		"stale size metadata must not make missing content appear available")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImageGenerationHistoryArchiveAndReadContent(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	image := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	contentRows := func() *sqlmock.Rows {
		columns := append(append([]string(nil), imageGenerationHistoryTestColumns...), "content")
		return sqlmock.NewRows(columns).AddRow(11, 7, "", nil, "gpt-image-2", "openai", "a cat",
			[]byte(`{}`), []byte(`[]`), "image/png", len(image), "", "", "", "completed", "", false, time.Now(), true, image)
	}
	mock.ExpectQuery(`(?s)INSERT INTO image_generation_history.*RETURNING.*`).
		WithArgs(int64(7), "", nil, "gpt-image-2", "openai", "a cat", []byte(`{}`), []byte(`[]`),
			image, "image/png", len(image), "", "", "", "completed", "").WillReturnRows(contentRows())
	readQuery := `(?s)SELECT .* FROM image_generation_history WHERE id = \$1 AND user_id = \$2`
	mock.ExpectQuery(readQuery).WithArgs(int64(11), int64(7)).WillReturnRows(contentRows())
	mock.ExpectQuery(readQuery).WithArgs(int64(11), int64(8)).
		WillReturnRows(sqlmock.NewRows(append(append([]string(nil), imageGenerationHistoryTestColumns...), "content")))

	repo := NewImageGenerationRepository(db)
	record, created, err := repo.Create(context.Background(), 7, &service.ImageGenerationHistoryCreate{
		Model: "gpt-image-2", Provider: "openai", Prompt: "a cat", Content: image, ContentType: "image/png",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.True(t, record.ContentAvailable())
	require.Equal(t, image, record.Content)

	svc := &service.ImageGenerationService{Repo: repo, Config: &config.Config{ImageHistory: config.ImageHistoryConfig{Enabled: true}}}
	restored, err := svc.GetHistoryContent(context.Background(), 11, 7)
	require.NoError(t, err)
	require.Equal(t, image, restored.Content)
	_, err = svc.GetHistoryContent(context.Background(), 11, 8)
	require.ErrorIs(t, err, service.ErrImageGenerationHistoryNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
