package deploy

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"

	"strings"
	"testing"
)

func TestExpandTargets(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want []string
	}{
		{"single", "10.0.0.5", []string{"10.0.0.5"}},
		{"list comma", "10.0.0.1, 10.0.0.2", []string{"10.0.0.1", "10.0.0.2"}},
		{"list newline", "10.0.0.1\n10.0.0.2", []string{"10.0.0.1", "10.0.0.2"}},
		{"hostname", "web1.example.com", []string{"web1.example.com"}},
		{"range full", "10.0.0.1-10.0.0.3", []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}},
		{"range short", "10.0.0.5-7", []string{"10.0.0.5", "10.0.0.6", "10.0.0.7"}},
		{"cidr /30", "10.0.0.0/30", []string{"10.0.0.1", "10.0.0.2"}}, // network+broadcast dropped
		{"cidr /32", "10.0.0.9/32", []string{"10.0.0.9"}},
		{"dedup", "10.0.0.1, 10.0.0.1", []string{"10.0.0.1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ExpandTargets(c.spec)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestExpandTargetsErrors(t *testing.T) {
	for _, spec := range []string{"", "   ", "10.0.0.5-10.0.0.1", "not/a/cidr"} {
		if _, err := ExpandTargets(spec); err == nil {
			t.Errorf("expected error for %q", spec)
		}
	}
}

func TestBuildInstallCommand(t *testing.T) {
	cmd := BuildInstallCommand(true, InstallParams{
		APIHost: "host.api.opsramp.com", Key: "K", Secret: "S", IntegrationID: "INTG-1", EnableLogMgmt: true,
	})
	for _, want := range []string{
		"sudo sh /tmp/opsramp-deployAgent.sh",
		// Without -i silent the installer blocks on an interactive prompt.
		"-i silent",
		"-K 'K'", "-S 'S'", "-s 'host.api.opsramp.com'", "-F 'INTG-1'", "-L true",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q:\n%s", want, cmd)
		}
	}

	// No sudo, no integration id, no log mgmt.
	cmd2 := BuildInstallCommand(false, InstallParams{APIHost: "h", Key: "K", Secret: "S"})
	if strings.HasPrefix(cmd2, "sudo") {
		t.Errorf("did not expect sudo: %s", cmd2)
	}
	if strings.Contains(cmd2, "-F") || strings.Contains(cmd2, "-L") {
		t.Errorf("unexpected optional flags: %s", cmd2)
	}
}

func TestShellQuoteEscaping(t *testing.T) {
	got := shellQuote("a'b")
	if got != `'a'\''b'` {
		t.Errorf("shellQuote escaping wrong: %s", got)
	}
}

func TestAuthFromRequiresCredential(t *testing.T) {
	if _, err := authFrom("", "", ""); err == nil {
		t.Error("expected error when no password or key provided")
	}
	if _, err := authFrom("", "", "x"); err != nil {
		t.Errorf("password auth should succeed: %v", err)
	}
}

func TestBuildUninstallCommandOverride(t *testing.T) {
	// A custom command is used verbatim (with sudo when requested).
	got := buildUninstallCommand(true, "rm -rf /opt/opsramp")
	if got != "sudo rm -rf /opt/opsramp" {
		t.Errorf("custom uninstall command wrong: %q", got)
	}
	// The default path uses OpsRamp's own package-removal commands.
	def := buildUninstallCommand(false, "")
	for _, want := range []string{"dpkg -P opsramp-agent", "rpm -e opsramp-agent", "/opt/opsramp/agent"} {
		if !strings.Contains(def, want) {
			t.Errorf("default uninstall missing %q: %q", want, def)
		}
	}
}

func TestBuildProbeCommandReachability(t *testing.T) {
	with := buildProbeCommand("host.api.opsramp.com")
	if !strings.Contains(with, "host.api.opsramp.com") || !strings.Contains(with, "API=") {
		t.Errorf("probe should test API reachability: %q", with)
	}
	if !strings.Contains(with, "SUDO=") || !strings.Contains(with, "AGENT=") {
		t.Errorf("probe should report sudo + agent presence: %q", with)
	}
	without := buildProbeCommand("")
	if !strings.Contains(without, "API=skip") {
		t.Errorf("probe without apiHost should skip reachability: %q", without)
	}
}

// stubStore records host results and can be made to reject ones carrying
// captured output, the way Postgres rejects text it cannot store.
type stubStore struct {
	mu           sync.Mutex
	results      []model.DeployHostResult
	rejectOutput bool
}

func (s *stubStore) CreateDeployJob(context.Context, model.DeployJob, []string) error { return nil }

func (s *stubStore) SetDeployJobStatus(context.Context, string, string, int, int, bool) error {
	return nil
}

func (s *stubStore) UpsertDeployHostResult(_ context.Context, r model.DeployHostResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rejectOutput && r.Output != "" {
		return errors.New(`invalid byte sequence for encoding "UTF8"`)
	}
	s.results = append(s.results, r)
	return nil
}

func (s *stubStore) last() model.DeployHostResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.results[len(s.results)-1]
}

// A host result the store rejects must not leave the host looking like it is
// still running: the verdict is re-recorded without the captured output.
func TestRunRecordsOutcomeWhenStoreRejectsOutput(t *testing.T) {
	store := &stubStore{rejectOutput: true}
	m := &Manager{store: store, log: slog.New(slog.DiscardHandler), concurrency: 1}

	m.run("job-1", []string{"10.0.0.1"}, func(context.Context, string) HostOutcome {
		return HostOutcome{Host: "10.0.0.1", Output: "binary\x00output", Err: "installer exited with code 1"}
	})

	got := store.last()
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Output != "" {
		t.Errorf("fallback should drop the output, got %q", got.Output)
	}
	if !strings.Contains(got.Error, "installer exited with code 1") {
		t.Errorf("original error lost: %q", got.Error)
	}
}
