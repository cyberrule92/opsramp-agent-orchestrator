package opampserver

import (
	"testing"

	"github.com/opsramp/opsramp-agent-orchestrator/internal/model"
)

func TestConfigMapHashIsOrderIndependent(t *testing.T) {
	a := map[string]model.ConfigFile{
		"a.yaml": {Body: "x", ContentType: "text/yaml"},
		"b.yaml": {Body: "y", ContentType: "text/yaml"},
	}
	// Same content, different insertion order (Go maps are unordered anyway, but
	// the hash must not depend on iteration order).
	b := map[string]model.ConfigFile{
		"b.yaml": {Body: "y", ContentType: "text/yaml"},
		"a.yaml": {Body: "x", ContentType: "text/yaml"},
	}
	if ConfigMapHashHex(a) != ConfigMapHashHex(b) {
		t.Fatalf("hash should be order-independent")
	}
}

func TestConfigMapHashChangesWithContent(t *testing.T) {
	base := map[string]model.ConfigFile{"a.yaml": {Body: "x", ContentType: "text/yaml"}}
	changedBody := map[string]model.ConfigFile{"a.yaml": {Body: "z", ContentType: "text/yaml"}}
	changedType := map[string]model.ConfigFile{"a.yaml": {Body: "x", ContentType: "application/json"}}

	if ConfigMapHashHex(base) == ConfigMapHashHex(changedBody) {
		t.Errorf("hash must change when body changes")
	}
	if ConfigMapHashHex(base) == ConfigMapHashHex(changedType) {
		t.Errorf("hash must change when content type changes")
	}
}

func TestSelectorMatches(t *testing.T) {
	attrs := map[string]string{"env": "prod", "region": "us-east-1"}
	cases := []struct {
		name     string
		selector map[string]string
		want     bool
	}{
		{"empty selector matches", map[string]string{}, true},
		{"single key match", map[string]string{"env": "prod"}, true},
		{"multi key match", map[string]string{"env": "prod", "region": "us-east-1"}, true},
		{"value mismatch", map[string]string{"env": "dev"}, false},
		{"missing key", map[string]string{"tier": "gold"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := selectorMatches(c.selector, attrs); got != c.want {
				t.Errorf("selectorMatches(%v) = %v, want %v", c.selector, got, c.want)
			}
		})
	}
}
