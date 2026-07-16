-- Runtime-editable OpsRamp connector configuration (single row, id = 1).

CREATE TABLE IF NOT EXISTS opsramp_config (
    id                    INT PRIMARY KEY DEFAULT 1,
    base_url              TEXT NOT NULL DEFAULT '',
    tenant_id             TEXT NOT NULL DEFAULT '',
    client_key            TEXT NOT NULL DEFAULT '',
    client_secret         TEXT NOT NULL DEFAULT '',
    poll_interval_seconds INT NOT NULL DEFAULT 60,
    enabled               BOOLEAN NOT NULL DEFAULT false,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT opsramp_config_singleton CHECK (id = 1)
);
