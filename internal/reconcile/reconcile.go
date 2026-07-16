// Package reconcile continuously compares the OpsRamp agent inventory against a
// healthy target state and proposes remediation: repairing agents that are down
// and upgrading agents behind the newest version in the fleet. It only ever
// produces recommendations — applying them requires an operator to supply SSH
// credentials, which are never stored.
package reconcile

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"
)

// Store is the inventory read surface the engine needs.
type Store interface {
	ListOpsRampAgents(ctx context.Context) ([]model.OpsRampAgent, error)
}

// HostRef is one agent referenced by a recommendation.
type HostRef struct {
	ResourceID   string `json:"resource_id"`
	Name         string `json:"name"`
	HostName     string `json:"host_name"`
	IPAddress    string `json:"ip_address"`
	AgentVersion string `json:"agent_version"`
	AgentStatus  string `json:"agent_status"`
}

// Recommendation is a proposed remediation over a set of hosts.
type Recommendation struct {
	Kind       string    `json:"kind"`        // repair | upgrade
	Action     string    `json:"action"`      // deploy action to apply
	Reason     string    `json:"reason"`      // human-readable rationale
	TargetSpec string    `json:"target_spec"` // comma-joined IPs, ready to prefill a deploy
	Hosts      []HostRef `json:"hosts"`
}

// VersionCount is one bar of the fleet version histogram.
type VersionCount struct {
	Version string `json:"version"`
	Count   int    `json:"count"`
}

// Report is the current reconciliation state of the fleet.
type Report struct {
	Total           int              `json:"total"`
	Active          int              `json:"active"`
	Down            int              `json:"down"`
	Unknown         int              `json:"unknown"`
	LatestVersion   string           `json:"latest_version"`
	Outdated        int              `json:"outdated"`
	Versions        []VersionCount   `json:"versions"`
	Recommendations []Recommendation `json:"recommendations"`
	GeneratedAt     time.Time        `json:"generated_at"`
}

// Engine evaluates inventory into a Report.
type Engine struct {
	store Store
	log   *slog.Logger
}

// New builds an Engine.
func New(store Store, log *slog.Logger) *Engine { return &Engine{store: store, log: log} }

var (
	downRE = regexp.MustCompile(`(?i)down|inactive|offline|error|suspend|dead|stopp?ed|disconnect`)
	upRE   = regexp.MustCompile(`(?i)active|up|online|running|connect`)
)

// Evaluate computes the current reconciliation report from inventory.
func (e *Engine) Evaluate(ctx context.Context) (*Report, error) {
	agents, err := e.store.ListOpsRampAgents(ctx)
	if err != nil {
		return nil, err
	}
	rep := &Report{GeneratedAt: time.Now().UTC()}
	rep.Total = len(agents)

	// Version histogram + latest.
	counts := map[string]int{}
	for _, a := range agents {
		v := strings.TrimSpace(a.AgentVersion)
		if v == "" {
			v = "unknown"
		}
		counts[v]++
		if v != "unknown" && compareVersion(v, rep.LatestVersion) > 0 {
			rep.LatestVersion = v
		}
	}
	for v, n := range counts {
		rep.Versions = append(rep.Versions, VersionCount{Version: v, Count: n})
	}
	sortVersionsDesc(rep.Versions)

	var down, outdated []HostRef
	for _, a := range agents {
		switch classify(a.AgentStatus) {
		case "down":
			rep.Down++
			if a.IPAddress != "" {
				down = append(down, ref(a))
			}
		case "up":
			rep.Active++
		default:
			rep.Unknown++
		}
		v := strings.TrimSpace(a.AgentVersion)
		if rep.LatestVersion != "" && v != "" && compareVersion(v, rep.LatestVersion) < 0 {
			rep.Outdated++
			if a.IPAddress != "" {
				outdated = append(outdated, ref(a))
			}
		}
	}

	if len(down) > 0 {
		rep.Recommendations = append(rep.Recommendations, Recommendation{
			Kind: "repair", Action: "repair",
			Reason:     "Agent reporting down/inactive — re-run the installer to restore it.",
			TargetSpec: joinIPs(down), Hosts: down,
		})
	}
	if len(outdated) > 0 {
		rep.Recommendations = append(rep.Recommendations, Recommendation{
			Kind: "upgrade", Action: "upgrade",
			Reason:     "Agent behind the newest version in the fleet (" + rep.LatestVersion + ").",
			TargetSpec: joinIPs(outdated), Hosts: outdated,
		})
	}
	return rep, nil
}

// Run evaluates on an interval, logging drift so the engine is visibly
// continuous. The UI reads Evaluate on demand for the live view.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	if interval < 30*time.Second {
		interval = 60 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rep, err := e.Evaluate(ctx)
			if err != nil {
				e.log.Warn("reconcile evaluate failed", "err", err)
				continue
			}
			if rep.Down > 0 || rep.Outdated > 0 {
				e.log.Info("reconcile drift", "down", rep.Down, "outdated", rep.Outdated, "latest", rep.LatestVersion)
			}
		}
	}
}

func classify(status string) string {
	s := strings.TrimSpace(status)
	if s == "" {
		return "unknown"
	}
	// down patterns win over up patterns (e.g. "disconnected").
	if downRE.MatchString(s) {
		return "down"
	}
	if upRE.MatchString(s) {
		return "up"
	}
	return "unknown"
}

func ref(a model.OpsRampAgent) HostRef {
	return HostRef{
		ResourceID: a.ResourceID, Name: a.Name, HostName: a.HostName,
		IPAddress: a.IPAddress, AgentVersion: a.AgentVersion, AgentStatus: a.AgentStatus,
	}
}

func joinIPs(hs []HostRef) string {
	ips := make([]string, 0, len(hs))
	for _, h := range hs {
		ips = append(ips, h.IPAddress)
	}
	return strings.Join(ips, ",")
}

var digitsRE = regexp.MustCompile(`\d+`)

// compareVersion compares dotted/dashed numeric versions field by field,
// e.g. "17.1.0-1" vs "17.1.0-2". Non-numeric separators are ignored. Returns
// -1, 0, or 1. An empty version sorts lowest.
func compareVersion(a, b string) int {
	if a == b {
		return 0
	}
	fa, fb := digitsRE.FindAllString(a, -1), digitsRE.FindAllString(b, -1)
	for i := 0; i < len(fa) || i < len(fb); i++ {
		var x, y int
		if i < len(fa) {
			x = atoi(fa[i])
		}
		if i < len(fb) {
			y = atoi(fb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
		if n > 1_000_000_000 { // guard against absurd version fields
			break
		}
	}
	return n
}

func sortVersionsDesc(v []VersionCount) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && compareVersion(v[j-1].Version, v[j].Version) < 0; j-- {
			v[j-1], v[j] = v[j], v[j-1]
		}
	}
}
