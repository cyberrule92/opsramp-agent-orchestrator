package store

import "testing"

func TestPGTextStripsBytesPostgresRejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"clean text is untouched", "installer exited with code 1", "installer exited with code 1"},
		{"nul characters are removed", "ok\x00\x00done", "okdone"},
		{"invalid utf-8 is replaced", "out\xffput", "out�put"},
		{"rune split by truncation is replaced", "caf\xc3", "caf�"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pgText(tt.in); got != tt.want {
				t.Errorf("pgText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
