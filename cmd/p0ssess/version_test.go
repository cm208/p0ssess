package main

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// The launcher, the plugin manifest and the binary must agree on the version: the launcher caches
// binaries by it and injects it at build time.
func TestVersionsAgree(t *testing.T) {
	b, err := os.ReadFile("../../bin/p0ssess")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^version=(\S+)$`).FindSubmatch(b)
	if m == nil || string(m[1]) != version {
		t.Errorf("launcher version %q, binary %q", m, version)
	}
	var p struct {
		Version string `json:"version"`
	}
	b, _ = os.ReadFile("../../.claude-plugin/plugin.json")
	if err := json.Unmarshal(b, &p); err != nil || p.Version != version {
		t.Errorf("plugin.json version %q, binary %q", p.Version, version)
	}
}
