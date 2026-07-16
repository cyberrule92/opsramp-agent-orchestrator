package opampserver

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"
)

// ErrNotConnected is returned when a push targets an agent with no live
// (WebSocket) connection. Such agents receive updates on their next poll.
var ErrNotConnected = errors.New("agent not connected via websocket")

// ConnectedUIDs returns the instance UIDs of agents with a live connection.
func (m *Manager) ConnectedUIDs() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]bool, len(m.byUID))
	for uid := range m.byUID {
		out[uid] = true
	}
	return out
}

func (m *Manager) connFor(uid string) (types.Connection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.byUID[uid]
	return c, ok
}

// PushToAgent builds the current desired offer and sends it to a connected
// agent over its WebSocket connection. HTTP-transport agents (no live conn)
// return ErrNotConnected and will reconcile on their next poll.
func (m *Manager) PushToAgent(ctx context.Context, uid string) error {
	conn, ok := m.connFor(uid)
	if !ok {
		return ErrNotConnected
	}
	agent, err := m.store.GetAgent(ctx, uid)
	if err != nil {
		return err
	}
	agent.ResolvedGroup = m.resolveGroup(ctx, agent)

	uidBytes, err := hex.DecodeString(uid)
	if err != nil {
		return err
	}
	offer := m.buildOfferForPush(ctx, agent, uidBytes)
	if err := conn.Send(ctx, offer); err != nil {
		return err
	}
	m.log.Slog().Info("pushed offer to agent", "uid", uid, "group", agent.ResolvedGroup)
	return nil
}

// PushGroup pushes the current desired offer to every connected agent that
// resolves to the given group. Returns the number of agents pushed.
func (m *Manager) PushGroup(ctx context.Context, group string) int {
	agents, err := m.store.ListAgents(ctx)
	if err != nil {
		m.log.Slog().Error("push group: list agents", "group", group, "err", err)
		return 0
	}
	connected := m.ConnectedUIDs()
	n := 0
	for i := range agents {
		a := &agents[i]
		if !connected[a.InstanceUID] {
			continue
		}
		a.ResolvedGroup = m.resolveGroup(ctx, a)
		if a.ResolvedGroup != group {
			continue
		}
		if err := m.PushToAgent(ctx, a.InstanceUID); err != nil && !errors.Is(err, ErrNotConnected) {
			m.log.Slog().Error("push to agent", "uid", a.InstanceUID, "err", err)
			continue
		}
		n++
	}
	return n
}

// buildOfferForPush builds an unconditional offer (config + packages) from the
// agent's resolved group. The agent dedups by hash, applying only real changes.
func (m *Manager) buildOfferForPush(ctx context.Context, agent *model.Agent, uidBytes []byte) *protobufs.ServerToAgent {
	resp := &protobufs.ServerToAgent{InstanceUid: uidBytes}
	group := agent.ResolvedGroup

	if hasCapability(agent.Capabilities, uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig)) {
		if cv, err := m.store.GetCurrentConfig(ctx, group); err == nil && cv != nil && len(cv.Files) > 0 {
			desiredHash, _ := hex.DecodeString(cv.Hash)
			resp.RemoteConfig = &protobufs.AgentRemoteConfig{
				Config:     modelFilesToConfigMap(cv.Files),
				ConfigHash: desiredHash,
			}
		}
	}

	if hasCapability(agent.Capabilities, uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages)) {
		if pkgs, err := m.store.ListGroupPackages(ctx, group); err == nil && len(pkgs) > 0 {
			avail, _ := m.buildPackagesAvailable(pkgs)
			resp.PackagesAvailable = avail
		}
	}
	return resp
}
