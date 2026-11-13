-- 生图历史：用户在生图页生成/上传的图片归档。
-- 记录本身持有图片二进制（Postgres bytea），不依赖对象存储；行按 created_at 到期，
-- 到期后由清理任务删除，前端据 capabilities.retention_days 展示保留期。
-- archived_at 是"用户在任务完成前点了删除"的标记：此时任务仍在跑，图片一旦落库
-- 就立刻删除，只留一条墓碑记录避免重复归档。

CREATE TABLE IF NOT EXISTS image_generation_history (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    task_id VARCHAR(128) UNIQUE,
    api_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    model VARCHAR(128) NOT NULL,
    provider VARCHAR(64) NOT NULL,
    prompt TEXT NOT NULL DEFAULT '',
    normalized_params JSONB NOT NULL DEFAULT '{}'::jsonb,
    reference_snapshot JSONB NOT NULL DEFAULT '[]'::jsonb,
    content BYTEA,
    content_type VARCHAR(128) NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL DEFAULT 0,
    content_url TEXT NOT NULL DEFAULT '',
    storage_object_id VARCHAR(128) NOT NULL DEFAULT '',
    library_asset_id VARCHAR(128) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'completed',
    error TEXT NOT NULL DEFAULT '',
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 历史列表按用户倒序分页；created_at 同时是保留期清理的扫描列。
CREATE INDEX IF NOT EXISTS idx_image_generation_history_user_created
    ON image_generation_history (user_id, created_at DESC);

-- 清理任务按到期时间扫描。
CREATE INDEX IF NOT EXISTS idx_image_generation_history_created_at
    ON image_generation_history (created_at);

COMMENT ON TABLE image_generation_history IS
    'User image generation history; rows hold the image bytes and expire after the configured retention period';
COMMENT ON COLUMN image_generation_history.task_id IS
    'Async image task id when the row was archived from a completed task; NULL for direct uploads';
COMMENT ON COLUMN image_generation_history.archived_at IS
    'Set when the user deleted a still-running task; the row becomes a tombstone so the task cannot re-archive';
