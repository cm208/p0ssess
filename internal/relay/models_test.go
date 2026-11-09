package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"p0ssess/internal/config"
)

func writeRegistry(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "models.json")
	doc := `{"meta":{"as_of":"2026-11-08"},"models":[
	  {"id":"deepseek/deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash"},
	  {"id":"deepseek/deepseek-r2","name":"DeepSeek R2"},
	  {"id":"google/gemini-3.8-flash","name":"Gemini 3.8 Flash"}
	]}`
	if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveModelID(t *testing.T) {
	t.Setenv("P0SSESS_REGISTRY", writeRegistry(t))
	e := NewEngine(config.Config{}, "test", os.Stderr)

	// Exact ids pass through untouched, registry or not.
	for _, id := range []string{"deepseek/deepseek-v4.1-flash", "openrouter/google/gemini-3.8-flash", "x/y#free", "vendor/brand-new-model"} {
		got, err := e.ResolveModelID(id)
		if err != nil || got != id {
			t.Errorf("exact %q -> %q, %v", id, got, err)
		}
	}
	// A unique fuzzy name resolves.
	if got, err := e.ResolveModelID("gemini"); err != nil || got != "google/gemini-3.8-flash" {
		t.Errorf("gemini -> %q, %v", got, err)
	}
	// An ambiguous name returns the candidates instead of guessing.
	_, err := e.ResolveModelID("deepseek")
	if err == nil || !strings.Contains(err.Error(), "deepseek/deepseek-v4.1-flash") || !strings.Contains(err.Error(), "deepseek/deepseek-r2") {
		t.Errorf("ambiguous deepseek should list candidates, got %v", err)
	}
	// An unknown name is rejected.
	if _, err := e.ResolveModelID("llama"); err == nil {
		t.Error("unknown name should error")
	}
}
