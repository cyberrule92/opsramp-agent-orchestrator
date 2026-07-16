// Package opampserver implements the OpAMP management server: it accepts agent
// connections, persists their reported state, and reconciles desired config and
// packages back to them.
package opampserver

import (
	"context"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"

	opampserver "github.com/open-telemetry/opamp-go/server"
	"github.com/open-telemetry/opamp-go/server/types"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/config"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/logger"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/store"
)

// Manager owns the OpAMP server and the live connection registry.
type Manager struct {
	store store.Store
	log   *logger.Logger
	cfg   *config.Config
	srv   opampserver.OpAMPServer

	mu     sync.RWMutex
	byUID  map[string]types.Connection
	byConn map[types.Connection]string
}

// New creates a Manager backed by the given store.
func New(st store.Store, log *logger.Logger, cfg *config.Config) *Manager {
	return &Manager{
		store:  st,
		log:    log,
		cfg:    cfg,
		byUID:  make(map[string]types.Connection),
		byConn: make(map[types.Connection]string),
	}
}

// Start binds and starts the OpAMP server.
func (m *Manager) Start() error {
	m.srv = opampserver.New(m.log)
	settings := opampserver.StartSettings{
		Settings: opampserver.Settings{
			Callbacks:         m.callbacks(),
			EnableCompression: true,
		},
		ListenEndpoint: m.cfg.OpAMPListen,
		ListenPath:     m.cfg.OpAMPPath,
	}
	return m.srv.Start(settings)
}

// Stop shuts the OpAMP server down.
func (m *Manager) Stop(ctx context.Context) error {
	if m.srv == nil {
		return nil
	}
	return m.srv.Stop(ctx)
}

func (m *Manager) callbacks() types.Callbacks {
	return types.Callbacks{
		OnConnecting: func(r *http.Request) types.ConnectionResponse {
			if m.cfg.AuthToken != "" && !checkBearer(r, m.cfg.AuthToken) {
				return types.ConnectionResponse{Accept: false, HTTPStatusCode: http.StatusUnauthorized}
			}
			return types.ConnectionResponse{
				Accept: true,
				ConnectionCallbacks: types.ConnectionCallbacks{
					OnConnected:       m.onConnected,
					OnMessage:         m.onMessage,
					OnConnectionClose: m.onConnectionClose,
				},
			}
		},
	}
}

func (m *Manager) onConnected(_ context.Context, _ types.Connection) {
	// The instance UID is unknown until the first message; nothing to do here.
}

func (m *Manager) onConnectionClose(conn types.Connection) {
	m.mu.Lock()
	uid, ok := m.byConn[conn]
	if ok {
		delete(m.byConn, conn)
		// Only clear the byUID entry if it still points at this connection.
		if cur, exists := m.byUID[uid]; exists && cur == conn {
			delete(m.byUID, uid)
		}
	}
	m.mu.Unlock()

	if ok {
		ctx := context.Background()
		if err := m.store.SetAgentStatus(ctx, uid, "disconnected"); err != nil {
			m.log.Slog().Error("set disconnected", "uid", uid, "err", err)
		}
		_ = m.store.AddEvent(ctx, uid, "disconnected", "OpAMP connection closed")
		m.log.Slog().Info("agent disconnected", "uid", uid)
	}
}

// registerConn associates a connection with an instance UID, returning true if
// this is a newly observed connection for that UID.
func (m *Manager) registerConn(uid string, conn types.Connection) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, existed := m.byUID[uid]
	m.byUID[uid] = conn
	m.byConn[conn] = uid
	return !existed || prev != conn
}

func checkBearer(r *http.Request, token string) bool {
	h := r.Header.Get("Authorization")
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")) == token
}

// instanceUIDHex returns a stable string key for an instance UID.
func instanceUIDHex(b []byte) string { return hex.EncodeToString(b) }
