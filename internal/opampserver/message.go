package opampserver

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

// onMessage is the heart of the server: it persists everything the agent
// reported, then returns an offer (remote config / packages) reconciled against
// the agent's group.
func (m *Manager) onMessage(ctx context.Context, conn types.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	uid := instanceUIDHex(msg.InstanceUid)
	if uid == "" {
		return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
	}

	isNew := m.registerConn(uid, conn)
	if err := m.store.TouchAgent(ctx, uid); err != nil {
		m.log.Slog().Error("touch agent", "uid", uid, "err", err)
	}
	if isNew {
		_ = m.store.AddEvent(ctx, uid, "connected", "OpAMP connection established")
		m.log.Slog().Info("agent connected", "uid", uid)
	}

	m.persistReportedState(ctx, uid, msg)

	// Load the freshly-persisted agent and resolve its group for reconciliation.
	agent, err := m.store.GetAgent(ctx, uid)
	if err != nil {
		m.log.Slog().Error("load agent", "uid", uid, "err", err)
		return &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
	}
	agent.ResolvedGroup = m.resolveGroup(ctx, agent)

	return m.buildServerToAgent(ctx, agent, msg)
}

// persistReportedState writes each optional section of the AgentToServer message.
func (m *Manager) persistReportedState(ctx context.Context, uid string, msg *protobufs.AgentToServer) {
	if d := msg.AgentDescription; d != nil {
		ident := attrsToMap(d.IdentifyingAttributes)
		nonident := attrsToMap(d.NonIdentifyingAttributes)
		desc := storeDescription(ident, nonident, msg.Capabilities)
		if err := m.store.SaveAgentDescription(ctx, uid, desc); err != nil {
			m.log.Slog().Error("save description", "uid", uid, "err", err)
		}
	}

	if h := msg.Health; h != nil {
		err := m.store.SaveAgentHealth(ctx, uid, healthFromProto(h))
		if err != nil {
			m.log.Slog().Error("save health", "uid", uid, "err", err)
		}
	}

	if ec := msg.EffectiveConfig; ec != nil {
		cfgStr := effectiveConfigToString(ec)
		if err := m.store.SaveAgentEffectiveConfig(ctx, uid, cfgStr, effectiveConfigHash(ec)); err != nil {
			m.log.Slog().Error("save effective config", "uid", uid, "err", err)
		}
	}

	if rc := msg.RemoteConfigStatus; rc != nil {
		lastHash := hex.EncodeToString(rc.GetLastRemoteConfigHash())
		if err := m.store.SaveAgentRemoteConfigStatus(ctx, uid, int32(rc.Status), rc.ErrorMessage, lastHash); err != nil {
			m.log.Slog().Error("save remote config status", "uid", uid, "err", err)
		}
		m.recordRemoteConfigEvent(ctx, uid, rc)
	}

	if ps := msg.PackageStatuses; ps != nil {
		if err := m.store.SaveAgentPackageStatuses(ctx, uid, packageStatusesToMap(ps)); err != nil {
			m.log.Slog().Error("save package statuses", "uid", uid, "err", err)
		}
	}
}

func (m *Manager) recordRemoteConfigEvent(ctx context.Context, uid string, rc *protobufs.RemoteConfigStatus) {
	switch rc.Status {
	case protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED:
		_ = m.store.AddEvent(ctx, uid, "config-applied",
			fmt.Sprintf("hash=%s", hex.EncodeToString(rc.GetLastRemoteConfigHash())))
	case protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED:
		_ = m.store.AddEvent(ctx, uid, "config-failed", rc.ErrorMessage)
	}
}
