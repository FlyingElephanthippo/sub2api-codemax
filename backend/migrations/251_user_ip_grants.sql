-- User-owned network grants are separate from the operator's manual allowlist.
CREATE TABLE IF NOT EXISTS user_ip_grants (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    ip INET NOT NULL,
    added_from_ip INET NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revision BIGINT NOT NULL DEFAULT 0,
    UNIQUE (user_id, ip),
    CHECK (masklen(ip) = CASE family(ip) WHEN 4 THEN 32 ELSE 128 END),
    CHECK (masklen(added_from_ip) = CASE family(added_from_ip) WHEN 4 THEN 32 ELSE 128 END)
);

CREATE INDEX IF NOT EXISTS idx_user_ip_grants_ip ON user_ip_grants (ip);

-- Audit and quota accounting commit in the same transaction as the grant change.
-- There is deliberately no cascading delete: historical attribution is retained.
CREATE TABLE IF NOT EXISTS user_ip_grant_events (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    actor_type TEXT NOT NULL DEFAULT 'user' CHECK (actor_type IN ('user', 'system')),
    operation TEXT NOT NULL CHECK (operation IN ('add', 'replace', 'delete')),
    old_ip INET,
    new_ip INET,
    source_ip INET NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    local_day DATE NOT NULL,
    notification_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (notification_status IN ('pending', 'sent', 'failed', 'skipped')),
    notification_attempted_at TIMESTAMPTZ,
    CHECK ((operation = 'add' AND old_ip IS NULL AND new_ip IS NOT NULL)
        OR (operation = 'replace' AND old_ip IS NOT NULL AND new_ip IS NOT NULL)
        OR (operation = 'delete' AND old_ip IS NOT NULL AND new_ip IS NULL))
);

CREATE INDEX IF NOT EXISTS idx_user_ip_grant_events_quota
    ON user_ip_grant_events (user_id, local_day) WHERE actor_type = 'user';

COMMENT ON TABLE user_ip_grants IS '用户自助国内 IP 放行登记；实时净实付资格决定生效，不改变人工名单';
COMMENT ON TABLE user_ip_grant_events IS '自助 IP 变更事务审计与北京时间自然日限额；通知状态单独更新';
