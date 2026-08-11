// Package api exposes the operator-facing admin REST API and dashboard UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/opsramp/opsramp-agent-orchestrator/internal/config"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/deploy"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/logger"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/opampserver"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/opsramp"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/reconcile"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/store"
)

// Server wires the admin API to the store and OpAMP manager.
type Server struct {
	store     store.Store
	mgr       *opampserver.Manager
	cfg       *config.Config
	log       *logger.Logger
	opsramp   *opsramp.Connector // always set; may be disabled until configured
	deploy    *deploy.Manager
	reconcile *reconcile.Engine
}

// NewServer constructs the admin API server.
func NewServer(st store.Store, mgr *opampserver.Manager, cfg *config.Config, log *logger.Logger, connector *opsramp.Connector, deployMgr *deploy.Manager, recon *reconcile.Engine) *Server {
	return &Server{store: st, mgr: mgr, cfg: cfg, log: log, opsramp: connector, deploy: deployMgr, reconcile: recon}
}

// Handler returns the fully-wired HTTP handler (API + UI).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Liveness/readiness.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.handleReady)

	// Agents.
	mux.HandleFunc("GET /api/v1/agents", s.handleListAgents)
	mux.HandleFunc("GET /api/v1/agents/{uid}", s.handleGetAgent)
	mux.HandleFunc("PUT /api/v1/agents/{uid}/group", s.handleSetAgentGroup)
	mux.HandleFunc("POST /api/v1/agents/{uid}/push", s.handlePushAgent)
	mux.HandleFunc("GET /api/v1/events", s.handleListEvents)

	// Groups.
	mux.HandleFunc("GET /api/v1/groups", s.handleListGroups)
	mux.HandleFunc("GET /api/v1/groups/{name}", s.handleGetGroup)
	mux.HandleFunc("PUT /api/v1/groups/{name}", s.handleUpsertGroup)
	mux.HandleFunc("DELETE /api/v1/groups/{name}", s.handleDeleteGroup)
	mux.HandleFunc("GET /api/v1/groups/{name}/config", s.handleGetGroupConfig)
	mux.HandleFunc("GET /api/v1/groups/{name}/config/versions", s.handleListConfigVersions)
	mux.HandleFunc("POST /api/v1/groups/{name}/config", s.handleCreateConfig)
	mux.HandleFunc("POST /api/v1/groups/{name}/config/rollback", s.handleRollbackConfig)
	mux.HandleFunc("GET /api/v1/groups/{name}/packages", s.handleListGroupPackages)
	mux.HandleFunc("POST /api/v1/groups/{name}/packages", s.handleAssignPackage)
	mux.HandleFunc("DELETE /api/v1/groups/{name}/packages/{pkg}", s.handleUnassignPackage)

	// Packages.
	mux.HandleFunc("GET /api/v1/packages", s.handleListPackages)
	mux.HandleFunc("GET /api/v1/packages/{name}", s.handleGetPackage)
	mux.HandleFunc("POST /api/v1/packages", s.handleUpsertPackage)
	mux.HandleFunc("DELETE /api/v1/packages/{name}", s.handleDeletePackage)
	mux.HandleFunc("GET /api/v1/packages/{name}/content", s.handlePackageContent)

	// OpsRamp connector.
	mux.HandleFunc("GET /api/v1/opsramp/config", s.handleOpsRampGetConfig)
	mux.HandleFunc("PUT /api/v1/opsramp/config", s.handleOpsRampSetConfig)
	mux.HandleFunc("GET /api/v1/opsramp/status", s.handleOpsRampStatus)
	mux.HandleFunc("POST /api/v1/opsramp/test", s.handleOpsRampTest)
	mux.HandleFunc("POST /api/v1/opsramp/token", s.handleOpsRampToken)
	mux.HandleFunc("GET /api/v1/opsramp/agents", s.handleOpsRampAgents)
	mux.HandleFunc("POST /api/v1/opsramp/sync", s.handleOpsRampSync)
	mux.HandleFunc("GET /api/v1/opsramp/agents/{platform}/info", s.handleOpsRampAgentInfo)
	mux.HandleFunc("POST /api/v1/opsramp/updates", s.handleOpsRampUpdates)
	mux.HandleFunc("POST /api/v1/opsramp/policies/{policyId}/devices", s.handleOpsRampAssignPolicy)
	mux.HandleFunc("POST /api/v1/opsramp/profiles/{profileId}/devices", s.handleOpsRampAssignProfile)
	mux.HandleFunc("GET /api/v1/opsramp/agents/{platform}/download/{pkg}", s.handleOpsRampDownload)

	// Bulk agent deployment over SSH (install / preflight / repair / upgrade / uninstall).
	mux.HandleFunc("POST /api/v1/deploy", s.handleDeployStart)
	mux.HandleFunc("GET /api/v1/deploy/jobs", s.handleDeployList)
	mux.HandleFunc("GET /api/v1/deploy/jobs/{id}", s.handleDeployGet)

	// Fleet reconciliation (drift + remediation recommendations).
	mux.HandleFunc("GET /api/v1/reconcile", s.handleReconcile)

	// UI.
	mux.HandleFunc("GET /", s.handleUI)

	return s.recover(s.authMiddleware(mux))
}

// authMiddleware requires a bearer token for mutating requests when AdminToken
// is configured. GET requests (including package content fetched by agents)
// remain open.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken != "" && r.Method != http.MethodGet && r.Method != http.MethodHead {
			tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if tok != s.cfg.AdminToken {
				writeErr(w, http.StatusUnauthorized, "missing or invalid admin token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Slog().Error("panic in handler", "path", r.URL.Path, "err", rec)
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if _, err := s.store.ListGroups(ctx); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 32<<20)) // 32 MiB cap
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// mapStoreErr converts store errors into HTTP responses; returns true if handled.
func mapStoreErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return true
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
	return true
}
