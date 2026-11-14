-- Operator grants have no owner or payment requirement. Creator fields are audit metadata.
CREATE TABLE admin_domestic_ip_grants (
    id BIGSERIAL PRIMARY KEY,
    ip INET NOT NULL UNIQUE,
    note VARCHAR(500) NOT NULL DEFAULT '',
    created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT admin_domestic_ip_single_host CHECK (
        (family(ip) = 4 AND masklen(ip) = 32) OR (family(ip) = 6 AND masklen(ip) = 128)
    )
);

-- No FK to the live grant or actor: removal must not erase the historical action.
CREATE TABLE admin_domestic_ip_grant_events (
    id BIGSERIAL PRIMARY KEY,
    grant_id BIGINT NOT NULL,
    ip INET NOT NULL,
    operation VARCHAR(16) NOT NULL CHECK (operation IN ('add', 'delete')),
    note VARCHAR(500) NOT NULL DEFAULT '',
    actor_user_id BIGINT NOT NULL,
    auth_method VARCHAR(32) NOT NULL CHECK (auth_method IN ('jwt', 'admin_api_key')),
    source_ip INET,
    request_id VARCHAR(128) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX admin_domestic_ip_events_grant_id_idx ON admin_domestic_ip_grant_events(grant_id, id DESC);
