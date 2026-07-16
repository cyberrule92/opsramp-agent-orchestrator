// Command demo-agent is a minimal OpAMP-managed monitoring agent. It connects
// to the orchestrator, reports identity/health/effective-config, applies remote
// config offers, and participates in package sync. It exists to exercise the
// orchestrator end-to-end; it does not collect real telemetry.
package main

import (
	"context"
	"encoding/hex"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/opsramp/opamp-orchestrator/internal/logger"
)

func main() {
	log := logger.New(env("LOG_LEVEL", "info"))
	slog := log.Slog()

	serverURL := env("OPAMP_SERVER_URL", "ws://localhost:4320/v1/opamp")
	stateDir := resolveStateDir(env("AGENT_STATE_DIR", "/var/lib/opamp-agent"))
	serviceName := env("AGENT_SERVICE_NAME", "demo-monitoring-agent")
	serviceVersion := env("AGENT_SERVICE_VERSION", "1.0.0")

	uid, err := loadOrCreateInstanceUID(stateDir)
	if err != nil {
		slog.Error("instance uid", "err", err)
		os.Exit(1)
	}
	slog.Info("demo agent starting", "uid", hex.EncodeToString(uid[:]), "server", serverURL, "state_dir", stateDir)

	pkgProvider, err := newFilePackagesProvider(stateDir)
	if err != nil {
		slog.Error("package provider", "err", err)
		os.Exit(1)
	}

	ag := &agent{
		log:      log,
		stateDir: stateDir,
		effective: map[string]*protobufs.AgentConfigFile{},
	}
	ag.loadPersistedConfig()

	opampClient := client.NewWebSocket(log)
	ag.client = opampClient

	capabilities := protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus |
		protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig |
		protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsPackageStatuses |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsHeartbeat

	heartbeat := 20 * time.Second
	settings := types.StartSettings{
		OpAMPServerURL: serverURL,
		InstanceUid:    uid,
		// Capabilities is set here (rather than SetCapabilities) because a
		// package-capable agent needs the PackagesStateProvider present when
		// capabilities are validated, which only happens once Start applies the
		// settings. The library logs a deprecation notice for this field.
		Capabilities:          capabilities,
		PackagesStateProvider: pkgProvider,
		HeartbeatInterval:     &heartbeat,
		Header:                authHeader(),
		Callbacks: types.Callbacks{
			OnConnect:          func(_ context.Context) { slog.Info("connected to orchestrator") },
			OnConnectFailed:    func(_ context.Context, e error) { slog.Warn("connect failed", "err", e) },
			OnError:            func(_ context.Context, e *protobufs.ServerErrorResponse) { slog.Warn("server error", "msg", e.GetErrorMessage()) },
			OnMessage:          ag.onMessage,
			GetEffectiveConfig: ag.getEffectiveConfig,
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Initial agent state must be set before Start() when the corresponding
	// capabilities are advertised.
	if err := opampClient.SetAgentDescription(buildDescription(serviceName, serviceVersion, uid)); err != nil {
		slog.Error("set description", "err", err)
		os.Exit(1)
	}
	start := uint64(time.Now().UnixNano())
	if err := opampClient.SetHealth(&protobufs.ComponentHealth{
		Healthy: true, Status: "running", StartTimeUnixNano: start, StatusTimeUnixNano: start,
	}); err != nil {
		slog.Error("set health", "err", err)
		os.Exit(1)
	}

	if err := opampClient.Start(ctx, settings); err != nil {
		slog.Error("client start", "err", err)
		os.Exit(1)
	}

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = opampClient.Stop(shutdownCtx)
}

// agent holds mutable state shared between callbacks.
type agent struct {
	log      *logger.Logger
	client   client.OpAMPClient
	stateDir string

	mu        sync.Mutex
	effective map[string]*protobufs.AgentConfigFile
}

func (a *agent) onMessage(ctx context.Context, msg *types.MessageData) {
	if msg.RemoteConfig != nil {
		a.applyRemoteConfig(ctx, msg.RemoteConfig)
	}
	if msg.PackagesAvailable != nil && msg.PackageSyncer != nil {
		a.log.Slog().Info("packages offered", "count", len(msg.PackagesAvailable.GetPackages()))
		go func() {
			if err := msg.PackageSyncer.Sync(context.Background()); err != nil {
				a.log.Slog().Warn("package sync failed", "err", err)
			}
		}()
	}
}

func (a *agent) applyRemoteConfig(ctx context.Context, rc *protobufs.AgentRemoteConfig) {
	slog := a.log.Slog()
	status := &protobufs.RemoteConfigStatus{LastRemoteConfigHash: rc.GetConfigHash()}

	newEffective := map[string]*protobufs.AgentConfigFile{}
	if rc.Config != nil {
		for name, f := range rc.Config.GetConfigMap() {
			newEffective[name] = f
			if err := a.writeConfigFile(name, f.GetBody()); err != nil {
				slog.Error("write config file", "file", name, "err", err)
				status.Status = protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED
				status.ErrorMessage = err.Error()
				_ = a.client.SetRemoteConfigStatus(status)
				return
			}
		}
	}

	a.mu.Lock()
	a.effective = newEffective
	a.mu.Unlock()

	status.Status = protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED
	if err := a.client.SetRemoteConfigStatus(status); err != nil {
		slog.Error("set remote config status", "err", err)
	}
	if err := a.client.UpdateEffectiveConfig(ctx); err != nil {
		slog.Error("update effective config", "err", err)
	}
	slog.Info("applied remote config", "files", len(newEffective), "hash", hex.EncodeToString(rc.GetConfigHash()))
}

func (a *agent) getEffectiveConfig(_ context.Context) (*protobufs.EffectiveConfig, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cm := &protobufs.AgentConfigMap{ConfigMap: make(map[string]*protobufs.AgentConfigFile, len(a.effective))}
	for name, f := range a.effective {
		cm.ConfigMap[name] = f
	}
	return &protobufs.EffectiveConfig{ConfigMap: cm}, nil
}

func (a *agent) writeConfigFile(name string, body []byte) error {
	dir := filepath.Join(a.stateDir, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Flatten any path separators in the config key to a safe filename.
	safe := strings.ReplaceAll(name, string(os.PathSeparator), "_")
	if safe == "" {
		safe = "config.yaml"
	}
	return os.WriteFile(filepath.Join(dir, safe), body, 0o644)
}

func (a *agent) loadPersistedConfig() {
	dir := filepath.Join(a.stateDir, "config")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil {
			a.effective[e.Name()] = &protobufs.AgentConfigFile{Body: body, ContentType: "text/yaml"}
		}
	}
}
