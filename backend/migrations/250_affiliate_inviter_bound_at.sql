-- Keep binding time separate from profile updates so late payment retries cannot
-- issue rebates for payments made before an invitation code was supplemented.
ALTER TABLE user_affiliates
    ADD COLUMN IF NOT EXISTS inviter_bound_at TIMESTAMPTZ;

-- Preserve the effective history of existing invitation relationships.
UPDATE user_affiliates
SET inviter_bound_at = created_at
WHERE inviter_id IS NOT NULL AND inviter_bound_at IS NULL;

COMMENT ON COLUMN user_affiliates.inviter_bound_at IS '邀请关系绑定时间；历史已绑定用户回填档案创建时间';
