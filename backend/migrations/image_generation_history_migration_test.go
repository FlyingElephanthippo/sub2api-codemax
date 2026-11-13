package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageGenerationHistoryMigration(t *testing.T) {
	content, err := FS.ReadFile("243_image_generation_history.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS image_generation_history")
	require.Contains(t, sql, "user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE")
	require.Contains(t, sql, "task_id VARCHAR(128) UNIQUE")
	require.Contains(t, sql, "api_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL")
	require.Contains(t, sql, "content BYTEA")
	require.Contains(t, sql, "normalized_params JSONB NOT NULL DEFAULT '{}'::jsonb")
	require.Contains(t, sql, "reference_snapshot JSONB NOT NULL DEFAULT '[]'::jsonb")
	require.Contains(t, sql, "archived_at TIMESTAMPTZ")
	require.Contains(t, sql,
		"CREATE INDEX IF NOT EXISTS idx_image_generation_history_user_created ON image_generation_history (user_id, created_at DESC)")
	require.Contains(t, sql,
		"CREATE INDEX IF NOT EXISTS idx_image_generation_history_created_at ON image_generation_history (created_at)")
}
