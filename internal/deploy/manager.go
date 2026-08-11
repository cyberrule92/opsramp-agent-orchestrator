package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"
)

// Store is the persistence the deploy Manager needs.
type Store interface {
	CreateDeployJob(ctx context.Context, job model.DeployJob, hosts []string) error
	SetDeployJobStatus(ctx context.Context, id, status string, succeeded, failed int, finished bool) error
	UpsertDeployHostResult(ctx context.Context, r model.DeployHostResult) error
}

// OpsRampSource supplies the installer script and credentials from the connector.
type OpsRampSource interface {
	// DeployScript returns the OpsRamp deployAgent.sh contents.
	DeployScript(ctx context.Context) ([]byte, error)
	// InstallParams returns the API host (no scheme), key and secret; enabled is
	// false when the OpsRamp connector is not configured.
	InstallParams() (apiHost, key, secret string, enabled bool)
	// DeregisterByHost removes the OpsRamp resource matching host (by IP or
	// hostname) from the tenant. found is false when no resource matched.
	DeregisterByHost(ctx context.Context, host string) (found bool, err error)
	// EnsureToken refreshes the OpsRamp access token when it is missing or
	// expired.
	EnsureToken(ctx context.Context) error
}

// Valid deploy actions.
const (
	ActionInstall   = "install"
	ActionPreflight = "preflight"
	ActionRepair    = "repair"
	ActionUpgrade   = "upgrade"
	ActionUninstall = "uninstall"
)

// installActions are the actions that run the OpsRamp installer.
func isInstallAction(a string) bool {
	return a == ActionInstall || a == ActionRepair || a == ActionUpgrade
}

// Manager runs bulk agent-install jobs over SSH.
type Manager struct {
	store       Store
	src         OpsRampSource
	runner      *Runner
	log         *slog.Logger
	concurrency int
}

// NewManager builds a deploy Manager. knownHostsPath persists TOFU host keys.
func NewManager(store Store, src OpsRampSource, knownHostsPath string, concurrency int, log *slog.Logger) (*Manager, error) {
	runner, err := NewRunner(knownHostsPath)
	if err != nil {
		return nil, err
	}
	if concurrency <= 0 {
		concurrency = 10
	}
	return &Manager{store: store, src: src, runner: runner, log: log, concurrency: concurrency}, nil
}

// StartRequest describes a deployment.
type StartRequest struct {
	Action        string // install (default) | preflight | repair | upgrade | uninstall
	TargetSpec    string
	Creds         Credentials
	IntegrationID string
	EnableLogMgmt bool
	CreatedBy     string

	// AgentKey/AgentSecret are the installer's -K/-S (the agent integration's
	// access/security keys). These are distinct from the connector's REST API
	// OAuth credentials; when blank the connector's are used as a fallback.
	AgentKey    string
	AgentSecret string

	// UninstallCommand overrides the built-in uninstall detection (uninstall only).
	UninstallCommand string
	// Deregister, on a successful uninstall, also removes the resource from OpsRamp.
	Deregister bool
}

// StartJob validates the request, records the job, and runs it in the
// background. The returned job reflects the initial (running) state.
func (m *Manager) StartJob(ctx context.Context, req StartRequest) (*model.DeployJob, error) {
	action := req.Action
	if action == "" {
		action = ActionInstall
	}
	switch action {
	case ActionInstall, ActionPreflight, ActionRepair, ActionUpgrade, ActionUninstall:
	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}

	hosts, err := ExpandTargets(req.TargetSpec)
	if err != nil {
		return nil, err
	}
	if req.Creds.User == "" {
		return nil, fmt.Errorf("ssh user is required")
	}
	if req.Creds.Password == "" && strings.TrimSpace(req.Creds.PrivateKey) == "" {
		return nil, fmt.Errorf("an SSH password or private key is required")
	}

	// The API host is used both for the installer and for preflight reachability.
	apiHost, key, secret, enabled := m.src.InstallParams()

	// Renew the OpsRamp token up front. A job that fans out over many hosts is
	// slow and disruptive to fail halfway through, and an expired token is the
	// common cause: it is rejected with a 407 that reads as an install failure.
	if enabled {
		if err := m.src.EnsureToken(ctx); err != nil {
			return nil, err
		}
	}

	// perHost is the action-specific operation applied to each target.
	var perHost func(ctx context.Context, host string) HostOutcome
	if isInstallAction(action) {
		if !enabled {
			return nil, fmt.Errorf("OpsRamp connector is not configured; configure it first")
		}
		script, err := m.src.DeployScript(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetch deploy script: %w", err)
		}
		// Prefer operator-supplied agent keys; fall back to the connector's.
		if req.AgentKey != "" {
			key = req.AgentKey
		}
		if req.AgentSecret != "" {
			secret = req.AgentSecret
		}
		params := InstallParams{
			APIHost: apiHost, Key: key, Secret: secret, Script: script,
			IntegrationID: req.IntegrationID, EnableLogMgmt: req.EnableLogMgmt,
		}
		perHost = func(ctx context.Context, host string) HostOutcome {
			return m.runner.InstallOnHost(ctx, host, req.Creds, params)
		}
	} else if action == ActionPreflight {
		perHost = func(ctx context.Context, host string) HostOutcome {
			return m.runner.ProbeHost(ctx, host, req.Creds, apiHost)
		}
	} else { // uninstall
		perHost = func(ctx context.Context, host string) HostOutcome {
			out := m.runner.UninstallOnHost(ctx, host, req.Creds, req.UninstallCommand)
			if out.OK && req.Deregister {
				if found, derr := m.src.DeregisterByHost(ctx, host); derr != nil {
					out.Output += "\n[deregister] error: " + derr.Error()
				} else if found {
					out.Output += "\n[deregister] resource removed from OpsRamp"
				} else {
					out.Output += "\n[deregister] no matching OpsRamp resource"
				}
			}
			return out
		}
	}

	job := model.DeployJob{
		ID:            newID(),
		Action:        action,
		Status:        "running",
		TargetSpec:    req.TargetSpec,
		SSHUser:       req.Creds.User,
		Port:          req.Creds.Port,
		UseSudo:       req.Creds.UseSudo,
		IntegrationID: req.IntegrationID,
		Total:         len(hosts),
		CreatedBy:     req.CreatedBy,
	}
	if err := m.store.CreateDeployJob(ctx, job, hosts); err != nil {
		return nil, err
	}

	// Credentials are captured by the closure only; never persisted.
	go m.run(job.ID, hosts, perHost)

	m.log.Info("deploy job started", "id", job.ID, "action", action, "hosts", len(hosts), "user", req.Creds.User)
	return &job, nil
}

func (m *Manager) run(jobID string, hosts []string, perHost func(context.Context, string) HostOutcome) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	sem := make(chan struct{}, m.concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded, failed := 0, 0

	for _, host := range hosts {
		wg.Add(1)
		sem <- struct{}{}
		go func(host string) {
			defer wg.Done()
			defer func() { <-sem }()

			_ = m.store.UpsertDeployHostResult(ctx, model.DeployHostResult{
				JobID: jobID, Host: host, Status: "running"})

			outcome := perHost(ctx, host)

			status := "failed"
			if outcome.OK {
				status = "success"
			}
			_ = m.store.UpsertDeployHostResult(ctx, model.DeployHostResult{
				JobID: jobID, Host: host, Status: status, ExitCode: outcome.ExitCode,
				Output: outcome.Output, Error: outcome.Err, DurationMs: outcome.Duration.Milliseconds(),
			})

			mu.Lock()
			if outcome.OK {
				succeeded++
			} else {
				failed++
			}
			s, f := succeeded, failed
			mu.Unlock()

			// Periodically reflect progress on the job row.
			_ = m.store.SetDeployJobStatus(ctx, jobID, "running", s, f, false)
		}(host)
	}
	wg.Wait()

	final := "succeeded"
	switch {
	case succeeded == 0:
		final = "failed"
	case failed > 0:
		final = "partial"
	}
	if err := m.store.SetDeployJobStatus(ctx, jobID, final, succeeded, failed, true); err != nil {
		m.log.Warn("finalize deploy job", "id", jobID, "err", err)
	}
	m.log.Info("deploy job finished", "id", jobID, "status", final, "succeeded", succeeded, "failed", failed)
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("dep-%d-%s", time.Now().Unix(), hex.EncodeToString(b[:]))
}
