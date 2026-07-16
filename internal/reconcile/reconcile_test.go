package reconcile

import (
	"context"
	"log/slog"
	"testing"

	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"
)

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"17.1.0-1", "17.1.0-2", -1},
		{"17.1.0-2", "17.1.0-1", 1},
		{"17.1.0-1", "17.1.0-1", 0},
		{"17.2.0", "17.10.0", -1}, // numeric, not lexical
		{"18.0.0", "17.9.9", 1},
		{"", "1.0.0", -1},
		{"1.0.0", "", 1},
	}
	for _, c := range cases {
		if got := compareVersion(c.a, c.b); got != c.want {
			t.Errorf("compareVersion(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

type fakeStore struct{ agents []model.OpsRampAgent }

func (f fakeStore) ListOpsRampAgents(context.Context) ([]model.OpsRampAgent, error) {
	return f.agents, nil
}

func TestEvaluateRecommendations(t *testing.T) {
	e := New(fakeStore{agents: []model.OpsRampAgent{
		{IPAddress: "10.0.0.1", AgentVersion: "17.2.0", AgentStatus: "active"},
		{IPAddress: "10.0.0.2", AgentVersion: "17.1.0", AgentStatus: "UP"},        // outdated
		{IPAddress: "10.0.0.3", AgentVersion: "17.2.0", AgentStatus: "disconnected"}, // down
		{IPAddress: "", AgentVersion: "17.1.0", AgentStatus: "active"},            // outdated but no IP -> excluded from target
	}}, slog.Default())

	rep, err := e.Evaluate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.LatestVersion != "17.2.0" {
		t.Errorf("latest=%q want 17.2.0", rep.LatestVersion)
	}
	if rep.Down != 1 {
		t.Errorf("down=%d want 1", rep.Down)
	}
	if rep.Outdated != 2 {
		t.Errorf("outdated=%d want 2", rep.Outdated)
	}
	var repair, upgrade *Recommendation
	for i := range rep.Recommendations {
		switch rep.Recommendations[i].Kind {
		case "repair":
			repair = &rep.Recommendations[i]
		case "upgrade":
			upgrade = &rep.Recommendations[i]
		}
	}
	if repair == nil || repair.TargetSpec != "10.0.0.3" {
		t.Errorf("repair recommendation wrong: %+v", repair)
	}
	// Only the outdated host WITH an IP is targetable.
	if upgrade == nil || upgrade.TargetSpec != "10.0.0.2" {
		t.Errorf("upgrade recommendation wrong: %+v", upgrade)
	}
}
