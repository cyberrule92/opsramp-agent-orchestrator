-- Bulk agent deployment jobs. SSH credentials are never stored here.

CREATE TABLE IF NOT EXISTS deploy_jobs (
    id             TEXT PRIMARY KEY,
    status         TEXT NOT NULL DEFAULT 'pending',
    target_spec    TEXT NOT NULL DEFAULT '',
    ssh_user       TEXT NOT NULL DEFAULT '',
    port           INT NOT NULL DEFAULT 22,
    use_sudo       BOOLEAN NOT NULL DEFAULT false,
    integration_id TEXT NOT NULL DEFAULT '',
    total          INT NOT NULL DEFAULT 0,
    succeeded      INT NOT NULL DEFAULT 0,
    failed         INT NOT NULL DEFAULT 0,
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS deploy_host_results (
    job_id      TEXT NOT NULL REFERENCES deploy_jobs(id) ON DELETE CASCADE,
    host        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    exit_code   INT NOT NULL DEFAULT 0,
    output      TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, host)
);

CREATE INDEX IF NOT EXISTS idx_deploy_jobs_created ON deploy_jobs (created_at DESC);
