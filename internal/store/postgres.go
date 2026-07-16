package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

// Postgres implements Store on top of a pgx connection pool.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres connects to the database (with retry) and runs migrations.
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 10

	var pool *pgxpool.Pool
	// Retry to tolerate Postgres still starting up in Compose.
	for attempt := 1; attempt <= 30; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				break
			}
			pool.Close()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	p := &Postgres{pool: pool}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return p, nil
}

func (p *Postgres) migrate(ctx context.Context) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names) // apply in lexical order (0001_, 0002_, ...)
	for _, name := range names {
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err := p.pool.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return nil
}

// Close releases the connection pool.
func (p *Postgres) Close() { p.pool.Close() }

// --- Agents ---

func (p *Postgres) TouchAgent(ctx context.Context, uid string) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO agents (instance_uid, status, first_seen, last_seen)
		VALUES ($1, 'connected', now(), now())
		ON CONFLICT (instance_uid)
		DO UPDATE SET status = 'connected', last_seen = now()`, uid)
	return err
}

func (p *Postgres) SetAgentStatus(ctx context.Context, uid, status string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE agents SET status = $2, last_seen = now() WHERE instance_uid = $1`, uid, status)
	return err
}

func (p *Postgres) SaveAgentDescription(ctx context.Context, uid string, d AgentDescription) error {
	ident, _ := json.Marshal(nonNil(d.IdentifyingAttrs))
	nonident, _ := json.Marshal(nonNil(d.NonIdentifyingAttrs))
	_, err := p.pool.Exec(ctx, `
		UPDATE agents SET
			service_name = $2, service_version = $3, hostname = $4, os_type = $5,
			capabilities = $6, identifying_attrs = $7, non_identifying_attrs = $8,
			last_seen = now()
		WHERE instance_uid = $1`,
		uid, d.ServiceName, d.ServiceVersion, d.Hostname, d.OSType,
		int64(d.Capabilities), ident, nonident)
	return err
}

func (p *Postgres) SaveAgentHealth(ctx context.Context, uid string, h AgentHealth) error {
	_, err := p.pool.Exec(ctx, `
		UPDATE agents SET
			health_healthy = $2, health_status = $3, health_last_error = $4,
			health_start_time = $5, health_status_time = $6, last_seen = now()
		WHERE instance_uid = $1`,
		uid, h.Healthy, h.Status, h.LastError, nanoToTime(h.StartNano), nanoToTime(h.StatusNano))
	return err
}

func (p *Postgres) SaveAgentEffectiveConfig(ctx context.Context, uid, cfg, hash string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE agents SET effective_config = $2, effective_config_hash = $3, last_seen = now() WHERE instance_uid = $1`,
		uid, cfg, hash)
	return err
}

func (p *Postgres) SaveAgentRemoteConfigStatus(ctx context.Context, uid string, status int32, errMsg, lastHashHex string) error {
	_, err := p.pool.Exec(ctx, `
		UPDATE agents SET remote_config_status = $2, remote_config_error = $3,
			last_remote_config_hash = $4, last_seen = now()
		WHERE instance_uid = $1`, uid, status, errMsg, lastHashHex)
	return err
}

func (p *Postgres) SaveAgentPackageStatuses(ctx context.Context, uid string, statuses map[string]any) error {
	b, _ := json.Marshal(nonNilAny(statuses))
	_, err := p.pool.Exec(ctx,
		`UPDATE agents SET package_statuses = $2, last_seen = now() WHERE instance_uid = $1`, uid, b)
	return err
}

func (p *Postgres) SetAgentGroup(ctx context.Context, uid string, group *string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE agents SET assigned_group = $2 WHERE instance_uid = $1`, uid, group)
	return err
}

const agentCols = `instance_uid, status, service_name, service_version, hostname, os_type,
	identifying_attrs, non_identifying_attrs, capabilities, effective_config, effective_config_hash,
	remote_config_status, remote_config_error, last_remote_config_hash,
	health_healthy, health_status, health_last_error, health_start_time, health_status_time,
	package_statuses, assigned_group, first_seen, last_seen`

func scanAgent(row pgx.Row) (*model.Agent, error) {
	var a model.Agent
	var ident, nonident, pkgStatuses []byte
	var caps int64
	if err := row.Scan(
		&a.InstanceUID, &a.Status, &a.ServiceName, &a.ServiceVersion, &a.Hostname, &a.OSType,
		&ident, &nonident, &caps, &a.EffectiveConfig, &a.EffectiveConfigHash,
		&a.RemoteConfigStatus, &a.RemoteConfigError, &a.LastRemoteConfigHash,
		&a.HealthHealthy, &a.HealthStatus, &a.HealthLastError, &a.HealthStartTime, &a.HealthStatusTime,
		&pkgStatuses, &a.AssignedGroup, &a.FirstSeen, &a.LastSeen,
	); err != nil {
		return nil, err
	}
	a.Capabilities = uint64(caps)
	_ = json.Unmarshal(ident, &a.IdentifyingAttrs)
	_ = json.Unmarshal(nonident, &a.NonIdentifyingAttrs)
	_ = json.Unmarshal(pkgStatuses, &a.PackageStatuses)
	return &a, nil
}

func (p *Postgres) GetAgent(ctx context.Context, uid string) (*model.Agent, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+agentCols+` FROM agents WHERE instance_uid = $1`, uid)
	a, err := scanAgent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (p *Postgres) ListAgents(ctx context.Context) ([]model.Agent, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+agentCols+` FROM agents ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// --- Groups & configs ---

func scanGroup(row pgx.Row) (*model.Group, error) {
	var g model.Group
	var sel []byte
	if err := row.Scan(&g.Name, &g.Description, &sel, &g.CurrentVersion, &g.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(sel, &g.Selector)
	return &g, nil
}

func (p *Postgres) ListGroups(ctx context.Context) ([]model.Group, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT name, description, selector, current_version, updated_at FROM groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

func (p *Postgres) GetGroup(ctx context.Context, name string) (*model.Group, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT name, description, selector, current_version, updated_at FROM groups WHERE name = $1`, name)
	g, err := scanGroup(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return g, err
}

func (p *Postgres) UpsertGroup(ctx context.Context, g model.Group) error {
	sel, _ := json.Marshal(nonNil(g.Selector))
	_, err := p.pool.Exec(ctx, `
		INSERT INTO groups (name, description, selector, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (name) DO UPDATE SET description = $2, selector = $3, updated_at = now()`,
		g.Name, g.Description, sel)
	return err
}

func (p *Postgres) DeleteGroup(ctx context.Context, name string) error {
	if name == "default" {
		return errors.New("cannot delete the default group")
	}
	ct, err := p.pool.Exec(ctx, `DELETE FROM groups WHERE name = $1`, name)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) CreateConfigVersion(ctx context.Context, group string, files map[string]model.ConfigFile, hash, note, createdBy string) (*model.ConfigVersion, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var next int
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM config_versions WHERE group_name = $1`, group).Scan(&next)
	if err != nil {
		return nil, err
	}
	filesJSON, _ := json.Marshal(files)
	cv := &model.ConfigVersion{GroupName: group, Version: next, Files: files, Hash: hash, Note: note, CreatedBy: createdBy}
	err = tx.QueryRow(ctx, `
		INSERT INTO config_versions (group_name, version, files, hash, note, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
		group, next, filesJSON, hash, note, createdBy).Scan(&cv.ID, &cv.CreatedAt)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx,
		`UPDATE groups SET current_version = $2, updated_at = now() WHERE name = $1`, group, next); err != nil {
		return nil, err
	}
	return cv, tx.Commit(ctx)
}

func (p *Postgres) SetCurrentConfigVersion(ctx context.Context, group string, version int) error {
	var exists bool
	err := p.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM config_versions WHERE group_name = $1 AND version = $2)`,
		group, version).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	_, err = p.pool.Exec(ctx,
		`UPDATE groups SET current_version = $2, updated_at = now() WHERE name = $1`, group, version)
	return err
}

func scanConfigVersion(row pgx.Row) (*model.ConfigVersion, error) {
	var cv model.ConfigVersion
	var files []byte
	if err := row.Scan(&cv.ID, &cv.GroupName, &cv.Version, &files, &cv.Hash, &cv.Note, &cv.CreatedBy, &cv.CreatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(files, &cv.Files)
	return &cv, nil
}

const cvCols = `id, group_name, version, files, hash, note, created_by, created_at`

func (p *Postgres) GetCurrentConfig(ctx context.Context, group string) (*model.ConfigVersion, error) {
	row := p.pool.QueryRow(ctx, `
		SELECT `+cvCols+` FROM config_versions
		WHERE group_name = $1 AND version = (SELECT current_version FROM groups WHERE name = $1)`, group)
	cv, err := scanConfigVersion(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return cv, err
}

func (p *Postgres) ListConfigVersions(ctx context.Context, group string) ([]model.ConfigVersion, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT `+cvCols+` FROM config_versions WHERE group_name = $1 ORDER BY version DESC`, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ConfigVersion
	for rows.Next() {
		cv, err := scanConfigVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *cv)
	}
	return out, rows.Err()
}

// --- Packages ---

func (p *Postgres) UpsertPackage(ctx context.Context, pkg model.Package, content []byte) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO packages (name, type, version, content, content_hash, signature, size, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (name) DO UPDATE SET
			type = $2, version = $3, content = $4, content_hash = $5, signature = $6, size = $7, updated_at = now()`,
		pkg.Name, pkg.Type, pkg.Version, content, pkg.ContentHash, pkg.Signature, int64(len(content)))
	return err
}

func scanPackage(row pgx.Row) (*model.Package, error) {
	var pkg model.Package
	if err := row.Scan(&pkg.Name, &pkg.Type, &pkg.Version, &pkg.ContentHash, &pkg.Signature, &pkg.Size, &pkg.UpdatedAt); err != nil {
		return nil, err
	}
	return &pkg, nil
}

const pkgCols = `name, type, version, content_hash, signature, size, updated_at`

func (p *Postgres) GetPackage(ctx context.Context, name string) (*model.Package, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+pkgCols+` FROM packages WHERE name = $1`, name)
	pkg, err := scanPackage(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return pkg, err
}

func (p *Postgres) GetPackageContent(ctx context.Context, name string) ([]byte, error) {
	var content []byte
	err := p.pool.QueryRow(ctx, `SELECT content FROM packages WHERE name = $1`, name).Scan(&content)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return content, err
}

func (p *Postgres) ListPackages(ctx context.Context) ([]model.Package, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+pkgCols+` FROM packages ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Package
	for rows.Next() {
		pkg, err := scanPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *pkg)
	}
	return out, rows.Err()
}

func (p *Postgres) DeletePackage(ctx context.Context, name string) error {
	ct, err := p.pool.Exec(ctx, `DELETE FROM packages WHERE name = $1`, name)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) AssignPackage(ctx context.Context, group, pkg string) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO group_packages (group_name, package_name) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, group, pkg)
	return err
}

func (p *Postgres) UnassignPackage(ctx context.Context, group, pkg string) error {
	_, err := p.pool.Exec(ctx,
		`DELETE FROM group_packages WHERE group_name = $1 AND package_name = $2`, group, pkg)
	return err
}

func (p *Postgres) ListGroupPackages(ctx context.Context, group string) ([]model.Package, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT p.name, p.type, p.version, p.content_hash, p.signature, p.size, p.updated_at
		FROM packages p JOIN group_packages gp ON gp.package_name = p.name
		WHERE gp.group_name = $1 ORDER BY p.name`, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Package
	for rows.Next() {
		pkg, err := scanPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *pkg)
	}
	return out, rows.Err()
}

// --- Events ---

func (p *Postgres) AddEvent(ctx context.Context, uid, kind, detail string) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO agent_events (instance_uid, kind, detail) VALUES ($1, $2, $3)`, uid, kind, detail)
	return err
}

func (p *Postgres) ListEvents(ctx context.Context, limit int) ([]model.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := p.pool.Query(ctx,
		`SELECT id, instance_uid, kind, detail, ts FROM agent_events ORDER BY ts DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		if err := rows.Scan(&e.ID, &e.InstanceUID, &e.Kind, &e.Detail, &e.TS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- OpsRamp agent inventory ---

func (p *Postgres) UpsertOpsRampAgent(ctx context.Context, a model.OpsRampAgent) error {
	raw, _ := json.Marshal(nonNilAny(a.Raw))
	_, err := p.pool.Exec(ctx, `
		INSERT INTO opsramp_agents
			(resource_id, name, host_name, ip_address, resource_type, agent_installed,
			 agent_version, agent_status, client_id, raw, first_seen, last_synced)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, now(), now())
		ON CONFLICT (resource_id) DO UPDATE SET
			name = $2, host_name = $3, ip_address = $4, resource_type = $5, agent_installed = $6,
			agent_version = $7, agent_status = $8, client_id = $9, raw = $10, last_synced = now()`,
		a.ResourceID, a.Name, a.HostName, a.IPAddress, a.ResourceType, a.AgentInstalled,
		a.AgentVersion, a.AgentStatus, a.ClientID, raw)
	return err
}

func (p *Postgres) ListOpsRampAgents(ctx context.Context) ([]model.OpsRampAgent, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT resource_id, name, host_name, ip_address, resource_type, agent_installed,
		       agent_version, agent_status, client_id, first_seen, last_synced
		FROM opsramp_agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.OpsRampAgent
	for rows.Next() {
		var a model.OpsRampAgent
		if err := rows.Scan(&a.ResourceID, &a.Name, &a.HostName, &a.IPAddress, &a.ResourceType,
			&a.AgentInstalled, &a.AgentVersion, &a.AgentStatus, &a.ClientID, &a.FirstSeen, &a.LastSynced); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (p *Postgres) CountOpsRampAgents(ctx context.Context) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `SELECT COUNT(*) FROM opsramp_agents`).Scan(&n)
	return n, err
}

func (p *Postgres) GetOpsRampSettings(ctx context.Context) (*model.OpsRampSettings, error) {
	var s model.OpsRampSettings
	err := p.pool.QueryRow(ctx, `
		SELECT base_url, tenant_id, client_key, client_secret, poll_interval_seconds, enabled, updated_at
		FROM opsramp_config WHERE id = 1`).Scan(
		&s.BaseURL, &s.TenantID, &s.ClientKey, &s.ClientSecret, &s.PollIntervalSeconds, &s.Enabled, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (p *Postgres) SaveOpsRampSettings(ctx context.Context, s model.OpsRampSettings) error {
	if s.PollIntervalSeconds <= 0 {
		s.PollIntervalSeconds = 60
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO opsramp_config
			(id, base_url, tenant_id, client_key, client_secret, poll_interval_seconds, enabled, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (id) DO UPDATE SET
			base_url = $1, tenant_id = $2, client_key = $3, client_secret = $4,
			poll_interval_seconds = $5, enabled = $6, updated_at = now()`,
		s.BaseURL, s.TenantID, s.ClientKey, s.ClientSecret, s.PollIntervalSeconds, s.Enabled)
	return err
}

// --- Deployment jobs ---

func (p *Postgres) CreateDeployJob(ctx context.Context, job model.DeployJob, hosts []string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	action := job.Action
	if action == "" {
		action = "install"
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO deploy_jobs (id, action, status, target_spec, ssh_user, port, use_sudo, integration_id, total, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		job.ID, action, job.Status, job.TargetSpec, job.SSHUser, job.Port, job.UseSudo, job.IntegrationID, job.Total, job.CreatedBy)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if _, err = tx.Exec(ctx,
			`INSERT INTO deploy_host_results (job_id, host, status) VALUES ($1, $2, 'pending')
			 ON CONFLICT (job_id, host) DO NOTHING`, job.ID, h); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (p *Postgres) SetDeployJobStatus(ctx context.Context, id, status string, succeeded, failed int, finished bool) error {
	if finished {
		_, err := p.pool.Exec(ctx,
			`UPDATE deploy_jobs SET status=$2, succeeded=$3, failed=$4, finished_at=now() WHERE id=$1`,
			id, status, succeeded, failed)
		return err
	}
	_, err := p.pool.Exec(ctx,
		`UPDATE deploy_jobs SET status=$2, succeeded=$3, failed=$4 WHERE id=$1`, id, status, succeeded, failed)
	return err
}

func (p *Postgres) UpsertDeployHostResult(ctx context.Context, r model.DeployHostResult) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO deploy_host_results (job_id, host, status, exit_code, output, error, duration_ms, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7, now())
		ON CONFLICT (job_id, host) DO UPDATE SET
			status=$3, exit_code=$4, output=$5, error=$6, duration_ms=$7, updated_at=now()`,
		r.JobID, r.Host, r.Status, r.ExitCode, r.Output, r.Error, r.DurationMs)
	return err
}

func scanDeployJob(row pgx.Row) (*model.DeployJob, error) {
	var j model.DeployJob
	if err := row.Scan(&j.ID, &j.Action, &j.Status, &j.TargetSpec, &j.SSHUser, &j.Port, &j.UseSudo,
		&j.IntegrationID, &j.Total, &j.Succeeded, &j.Failed, &j.CreatedBy, &j.CreatedAt, &j.FinishedAt); err != nil {
		return nil, err
	}
	return &j, nil
}

const deployJobCols = `id, action, status, target_spec, ssh_user, port, use_sudo, integration_id,
	total, succeeded, failed, created_by, created_at, finished_at`

func (p *Postgres) ListDeployJobs(ctx context.Context, limit int) ([]model.DeployJob, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := p.pool.Query(ctx,
		`SELECT `+deployJobCols+` FROM deploy_jobs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.DeployJob
	for rows.Next() {
		j, err := scanDeployJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func (p *Postgres) GetDeployJob(ctx context.Context, id string) (*model.DeployJob, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+deployJobCols+` FROM deploy_jobs WHERE id = $1`, id)
	j, err := scanDeployJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, `
		SELECT job_id, host, status, exit_code, output, error, duration_ms, updated_at
		FROM deploy_host_results WHERE job_id = $1 ORDER BY host`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r model.DeployHostResult
		if err := rows.Scan(&r.JobID, &r.Host, &r.Status, &r.ExitCode, &r.Output, &r.Error, &r.DurationMs, &r.UpdatedAt); err != nil {
			return nil, err
		}
		j.Hosts = append(j.Hosts, r)
	}
	return j, rows.Err()
}

// --- helpers ---

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func nonNilAny(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func nanoToTime(nano uint64) *time.Time {
	if nano == 0 {
		return nil
	}
	t := time.Unix(0, int64(nano)).UTC()
	return &t
}
