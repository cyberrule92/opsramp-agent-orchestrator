package opampserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/opsramp/opamp-orchestrator/internal/model"
)

// ResolveGroup is the exported form of resolveGroup for use by the admin API.
func (m *Manager) ResolveGroup(ctx context.Context, a *model.Agent) string {
	return m.resolveGroup(ctx, a)
}

// resolveGroup determines which group governs an agent. Explicit assignment
// wins; otherwise the first group (by name) whose selector matches the agent's
// attributes; otherwise the configured default group.
func (m *Manager) resolveGroup(ctx context.Context, a *model.Agent) string {
	if a.AssignedGroup != nil && *a.AssignedGroup != "" {
		return *a.AssignedGroup
	}
	groups, err := m.store.ListGroups(ctx)
	if err != nil {
		return m.cfg.DefaultGroup
	}
	// Merge identifying + non-identifying attributes for selector matching.
	attrs := map[string]string{}
	for k, v := range a.IdentifyingAttrs {
		attrs[k] = v
	}
	for k, v := range a.NonIdentifyingAttrs {
		attrs[k] = v
	}
	// Deterministic order; skip the default group as a selector target.
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	for _, g := range groups {
		if g.Name == m.cfg.DefaultGroup || len(g.Selector) == 0 {
			continue
		}
		if selectorMatches(g.Selector, attrs) {
			return g.Name
		}
	}
	return m.cfg.DefaultGroup
}

func selectorMatches(selector, attrs map[string]string) bool {
	for k, want := range selector {
		if attrs[k] != want {
			return false
		}
	}
	return true
}

// buildServerToAgent constructs the response/offer for an agent: remote config
// and available packages, included only when they differ from what the agent
// last reported (hash-based reconciliation).
func (m *Manager) buildServerToAgent(ctx context.Context, agent *model.Agent, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	resp := &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
	group := agent.ResolvedGroup

	// --- Remote config offer ---
	if hasCapability(agent.Capabilities, uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig)) {
		if cv, err := m.store.GetCurrentConfig(ctx, group); err == nil && cv != nil && len(cv.Files) > 0 {
			desiredHash, _ := hex.DecodeString(cv.Hash)
			reported := msg.GetRemoteConfigStatus().GetLastRemoteConfigHash()
			if !bytes.Equal(desiredHash, reported) {
				resp.RemoteConfig = &protobufs.AgentRemoteConfig{
					Config:     modelFilesToConfigMap(cv.Files),
					ConfigHash: desiredHash,
				}
			}
		}
	}

	// --- Packages offer ---
	if hasCapability(agent.Capabilities, uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages)) {
		pkgs, err := m.store.ListGroupPackages(ctx, group)
		if err == nil && len(pkgs) > 0 {
			avail, allHash := m.buildPackagesAvailable(pkgs)
			reported := msg.GetPackageStatuses().GetServerProvidedAllPackagesHash()
			if !bytes.Equal(allHash, reported) {
				resp.PackagesAvailable = avail
			}
		}
	}

	return resp
}

func (m *Manager) buildPackagesAvailable(pkgs []model.Package) (*protobufs.PackagesAvailable, []byte) {
	pa := &protobufs.PackagesAvailable{Packages: make(map[string]*protobufs.PackageAvailable, len(pkgs))}

	// all-packages hash: canonical over sorted (name, hash) pairs.
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })
	allHasher := sha256.New()

	for _, p := range pkgs {
		contentHash, _ := hex.DecodeString(p.ContentHash)
		downloadURL := m.cfg.PublicBaseURL + "/api/v1/packages/" + p.Name + "/content"
		pa.Packages[p.Name] = &protobufs.PackageAvailable{
			Type:    protobufs.PackageType(p.Type),
			Version: p.Version,
			File: &protobufs.DownloadableFile{
				DownloadUrl: downloadURL,
				ContentHash: contentHash,
			},
			Hash: contentHash,
		}
		allHasher.Write([]byte(p.Name))
		allHasher.Write([]byte{0})
		allHasher.Write(contentHash)
	}
	allHash := allHasher.Sum(nil)
	pa.AllPackagesHash = allHash
	return pa, allHash
}

func hasCapability(caps, bit uint64) bool { return caps&bit != 0 }
