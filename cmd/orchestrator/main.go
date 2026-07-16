// Command orchestrator runs the OpAMP management server (agent-facing) and the
// admin REST API + dashboard (operator-facing) against a Postgres backend.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/opsramp/opsramp-agent-orchestrator/internal/api"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/config"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/deploy"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/logger"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/opampserver"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/opsramp"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/reconcile"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	log := logger.New(cfg.LogLevel)
	slog := log.Slog()
	slog.Info("starting orchestrator",
		"opamp", cfg.OpAMPListen+cfg.OpAMPPath, "admin", cfg.AdminListen, "default_group", cfg.DefaultGroup)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Store (connects with retry + runs migrations).
	st, err := store.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("store init failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	slog.Info("database ready")

	// OpAMP server.
	mgr := opampserver.New(st, log, cfg)
	if err := mgr.Start(); err != nil {
		slog.Error("opamp server start failed", "err", err)
		os.Exit(1)
	}
	slog.Info("opamp server listening", "endpoint", cfg.OpAMPListen, "path", cfg.OpAMPPath)

	// OpsRamp connector: manages/monitors OpsRamp-managed agents via the OpsRamp
	// REST API. Config is persisted and editable from the UI; env vars seed the
	// initial config on first boot when the DB has none.
	connector := opsramp.NewConnector(st, slog)
	if err := connector.Start(ctx, model.OpsRampSettings{
		BaseURL:             cfg.OpsRamp.BaseURL,
		TenantID:            cfg.OpsRamp.TenantID,
		ClientKey:           cfg.OpsRamp.ClientKey,
		ClientSecret:        cfg.OpsRamp.ClientSecret,
		PollIntervalSeconds: int(cfg.OpsRamp.PollInterval.Seconds()),
	}); err != nil {
		slog.Error("opsramp connector start", "err", err)
	}

	// Bulk agent deployment over SSH (installs the OpsRamp agent on remote VMs).
	deployMgr, err := deploy.NewManager(st, connector,
		filepath.Join(cfg.DeployStateDir, "known_hosts"), cfg.DeployConcurrency, slog)
	if err != nil {
		slog.Error("deploy manager init", "err", err)
		os.Exit(1)
	}

	// Fleet reconciliation: continuously evaluate inventory for down/outdated
	// agents and surface remediation recommendations to operators.
	reconciler := reconcile.New(st, slog)
	go reconciler.Run(ctx, cfg.OpsRamp.PollInterval)

	// Admin API + UI.
	adminSrv := &http.Server{
		Addr:              cfg.AdminListen,
		Handler:           api.NewServer(st, mgr, cfg, log, connector, deployMgr, reconciler).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("admin server failed", "err", err)
			stop()
		}
	}()
	slog.Info("admin api listening", "endpoint", cfg.AdminListen)

	<-ctx.Done()
	slog.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := adminSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("admin shutdown", "err", err)
	}
	if err := mgr.Stop(shutdownCtx); err != nil {
		slog.Error("opamp shutdown", "err", err)
	}
	slog.Info("stopped cleanly")
}
