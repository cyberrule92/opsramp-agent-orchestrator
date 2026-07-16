-- OpsRamp-managed agent inventory, synced from the OpsRamp Resources API.

CREATE TABLE IF NOT EXISTS opsramp_agents (
    resource_id     TEXT PRIMARY KEY,
    name            TEXT NOT NULL DEFAULT '',
    host_name       TEXT NOT NULL DEFAULT '',
    ip_address      TEXT NOT NULL DEFAULT '',
    resource_type   TEXT NOT NULL DEFAULT '',
    agent_installed BOOLEAN NOT NULL DEFAULT false,
    agent_version   TEXT NOT NULL DEFAULT '',
    agent_status    TEXT NOT NULL DEFAULT '',
    client_id       TEXT NOT NULL DEFAULT '',
    raw             JSONB NOT NULL DEFAULT '{}',
    first_seen      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_synced     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_opsramp_agents_status ON opsramp_agents (agent_status);
CREATE INDEX IF NOT EXISTS idx_opsramp_agents_synced ON opsramp_agents (last_synced DESC);
