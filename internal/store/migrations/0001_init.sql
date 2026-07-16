-- Orchestrator schema: groups, versioned configs, packages, agents, events.

CREATE TABLE IF NOT EXISTS groups (
    name            TEXT PRIMARY KEY,
    description     TEXT NOT NULL DEFAULT '',
    selector        JSONB NOT NULL DEFAULT '{}',
    current_version INT NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS config_versions (
    id         BIGSERIAL PRIMARY KEY,
    group_name TEXT NOT NULL REFERENCES groups(name) ON DELETE CASCADE,
    version    INT NOT NULL,
    files      JSONB NOT NULL DEFAULT '{}',
    hash       TEXT NOT NULL,
    note       TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (group_name, version)
);

CREATE TABLE IF NOT EXISTS packages (
    name         TEXT PRIMARY KEY,
    type         INT NOT NULL DEFAULT 0,
    version      TEXT NOT NULL DEFAULT '',
    content      BYTEA NOT NULL DEFAULT '\x',
    content_hash TEXT NOT NULL DEFAULT '',
    signature    TEXT NOT NULL DEFAULT '',
    size         BIGINT NOT NULL DEFAULT 0,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS group_packages (
    group_name   TEXT NOT NULL REFERENCES groups(name) ON DELETE CASCADE,
    package_name TEXT NOT NULL REFERENCES packages(name) ON DELETE CASCADE,
    PRIMARY KEY (group_name, package_name)
);

CREATE TABLE IF NOT EXISTS agents (
    instance_uid            TEXT PRIMARY KEY,
    status                  TEXT NOT NULL DEFAULT 'disconnected',
    service_name            TEXT NOT NULL DEFAULT '',
    service_version         TEXT NOT NULL DEFAULT '',
    hostname                TEXT NOT NULL DEFAULT '',
    os_type                 TEXT NOT NULL DEFAULT '',
    identifying_attrs       JSONB NOT NULL DEFAULT '{}',
    non_identifying_attrs   JSONB NOT NULL DEFAULT '{}',
    capabilities            BIGINT NOT NULL DEFAULT 0,
    effective_config        TEXT NOT NULL DEFAULT '',
    effective_config_hash   TEXT NOT NULL DEFAULT '',
    remote_config_status    INT NOT NULL DEFAULT 0,
    remote_config_error     TEXT NOT NULL DEFAULT '',
    last_remote_config_hash TEXT NOT NULL DEFAULT '',
    health_healthy          BOOLEAN NOT NULL DEFAULT false,
    health_status           TEXT NOT NULL DEFAULT '',
    health_last_error       TEXT NOT NULL DEFAULT '',
    health_start_time       TIMESTAMPTZ,
    health_status_time      TIMESTAMPTZ,
    package_statuses        JSONB NOT NULL DEFAULT '{}',
    assigned_group          TEXT REFERENCES groups(name) ON DELETE SET NULL,
    first_seen              TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS agent_events (
    id           BIGSERIAL PRIMARY KEY,
    instance_uid TEXT NOT NULL,
    kind         TEXT NOT NULL,
    detail       TEXT NOT NULL DEFAULT '',
    ts           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_agent_events_uid ON agent_events (instance_uid, ts DESC);
CREATE INDEX IF NOT EXISTS idx_agent_events_ts ON agent_events (ts DESC);
CREATE INDEX IF NOT EXISTS idx_agents_group ON agents (assigned_group);
CREATE INDEX IF NOT EXISTS idx_agents_status ON agents (status);

INSERT INTO groups (name, description)
VALUES ('default', 'Default group for all agents')
ON CONFLICT (name) DO NOTHING;
