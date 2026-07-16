package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// resolveStateDir returns dir if writable, else falls back to ./agent-state.
func resolveStateDir(dir string) string {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fallback := filepath.Join(".", "agent-state")
		_ = os.MkdirAll(fallback, 0o755)
		return fallback
	}
	return dir
}

// loadOrCreateInstanceUID reads a stable 16-byte instance UID from disk,
// generating and persisting one on first run so the agent keeps its identity
// across restarts.
func loadOrCreateInstanceUID(stateDir string) (types.InstanceUid, error) {
	var uid types.InstanceUid
	path := filepath.Join(stateDir, "instance.uid")
	if data, err := os.ReadFile(path); err == nil {
		if raw, err := hex.DecodeString(strings.TrimSpace(string(data))); err == nil && len(raw) == 16 {
			copy(uid[:], raw)
			return uid, nil
		}
	}
	if _, err := rand.Read(uid[:]); err != nil {
		return uid, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(uid[:])), 0o644); err != nil {
		return uid, err
	}
	return uid, nil
}

func authHeader() http.Header {
	tok := os.Getenv("OPAMP_AUTH_TOKEN")
	if tok == "" {
		return nil
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok)
	return h
}

// buildDescription assembles the AgentDescription reported to the orchestrator.
// Extra labels come from AGENT_LABELS (e.g. "env=prod,region=us-east").
func buildDescription(serviceName, serviceVersion string, uid types.InstanceUid) *protobufs.AgentDescription {
	hostname, _ := os.Hostname()

	ident := []*protobufs.KeyValue{
		stringKV("service.name", serviceName),
		stringKV("service.instance.id", hex.EncodeToString(uid[:])),
	}
	nonident := []*protobufs.KeyValue{
		stringKV("service.version", serviceVersion),
		stringKV("host.name", hostname),
		stringKV("os.type", runtime.GOOS),
		stringKV("host.arch", runtime.GOARCH),
	}
	for k, v := range parseLabels(os.Getenv("AGENT_LABELS")) {
		nonident = append(nonident, stringKV(k, v))
	}
	return &protobufs.AgentDescription{
		IdentifyingAttributes:    ident,
		NonIdentifyingAttributes: nonident,
	}
}

func parseLabels(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		if k, v, ok := strings.Cut(pair, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func stringKV(key, val string) *protobufs.KeyValue {
	return &protobufs.KeyValue{
		Key:   key,
		Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: val}},
	}
}
