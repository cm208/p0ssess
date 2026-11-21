package jail

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestOpenCodeConfigDeclaresModels: a run's model is declared under providers.openrouter.models, the
// rule OpenCode 2.x uses to run an id its built-in catalog lacks, with the cap's output limit.
func TestOpenCodeConfigDeclaresModels(t *testing.T) {
	b, err := OpenCodeConfig(Model{ID: "google/gemini-4-carbon", Name: "Gemini 4 Carbon", Context: 1048576, Output: 40000, InPerM: 2, OutPerM: 10})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Providers map[string]struct {
			Models map[string]struct {
				Name  string `json:"name"`
				Limit struct {
					Context int `json:"context"`
					Output  int `json:"output"`
				} `json:"limit"`
				Cost struct {
					Input  float64 `json:"input"`
					Output float64 `json:"output"`
				} `json:"cost"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	m, ok := cfg.Providers["openrouter"].Models["google/gemini-4-carbon"]
	if !ok || m.Name != "Gemini 4 Carbon" || m.Limit.Output != 40000 || m.Limit.Context != 1048576 || m.Cost.Output != 10 {
		t.Fatalf("declared = %+v (%v)", m, ok)
	}
	plain, _ := OpenCodeConfig()
	if strings.Contains(string(plain), `"models"`) {
		t.Error("no models, no declaration")
	}
}
