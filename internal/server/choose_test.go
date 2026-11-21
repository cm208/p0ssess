package server

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"p0ssess/internal/relay"
)

// A run that names no model asks the routing engine, as direct does.
func TestRunChoosesAModel(t *testing.T) {
	_, eng, proj := directEnv(t, "monthly_usd = 5\nper_task_usd = 1\ndaily_usd = -1\n")
	o := NewObserver(eng).(*observer)

	// Edit mode defaults to stakes 3; the use case comes from the task text.
	c, err := o.Choose(relay.ChooseRequest{Task: "implement a new flag for the export command", Mode: "edit", Cwd: proj})
	if err != nil {
		t.Fatal(err)
	}
	if c.UseCase != "code-implement-feature" || c.Stakes != 3 || c.Model != "openai/gpt-6.1-sol" || !strings.Contains(c.Why, "code-implement-feature") {
		t.Fatalf("%+v", c)
	}
	// The next options inside the limits come along, for a retry on another model.
	for _, a := range c.Alternatives {
		if a == c.Model {
			t.Errorf("the pick is its own alternative: %v", c.Alternatives)
		}
	}
	// Read mode defaults to stakes 2, and a use case from the caller wins over the text.
	c, err = o.Choose(relay.ChooseRequest{Task: "implement a new flag", Mode: "read", UseCase: "code-implement-feature", Cwd: proj})
	if err != nil || c.Stakes != 2 || c.Model != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("%+v %v", c, err)
	}
	// Stakes from the caller are kept.
	if c, err = o.Choose(relay.ChooseRequest{Task: "x", Mode: "read", UseCase: "code-implement-feature", Stakes: 3, Cwd: proj}); err != nil || c.Stakes != 3 {
		t.Fatalf("%+v %v", c, err)
	}

	// Nothing fits: the reason says what was considered, never a bare "model must be".
	_, err = o.Choose(relay.ChooseRequest{Task: "what does this function do?", Mode: "read", Cwd: proj})
	if err == nil || !strings.Contains(err.Error(), "no model fits") || !strings.Contains(err.Error(), "quick-code-question") || strings.Contains(err.Error(), "model must be") {
		t.Fatalf("err %v", err)
	}
}

// With no Claude lead the pick is the best direct route even where Claude would keep the job.
func TestRunChoosesWithNoLead(t *testing.T) {
	_, eng, proj := directEnv(t, "monthly_usd = 5\nper_task_usd = 1\ndaily_usd = -1\n")
	o := NewObserver(eng).(*observer)
	if _, err := o.Choose(relay.ChooseRequest{Task: "x", Mode: "read", UseCase: "qa-factual", Cwd: proj}); err == nil {
		t.Fatal("with a lead Claude keeps qa-factual")
	}
	os.WriteFile(eng.Dirs.EngineFile(), []byte("lead = \"none\"\n"), 0o600)
	c, err := o.Choose(relay.ChooseRequest{Task: "x", Mode: "read", UseCase: "qa-factual", Cwd: proj})
	if err != nil || c.Model != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("%+v %v", c, err)
	}
}

// Through the MCP tool: no model is no longer a refusal to read.
func TestRunToolWithNoModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	t.Setenv("P0SSESS_OPENROUTER_KEY_FILE", filepath.Join(home, "key"))
	os.WriteFile(filepath.Join(home, "key"), []byte("placeholder\n"), 0o600)
	t.Setenv("P0SSESS_REGISTRY", "")
	plain := t.TempDir()
	p := start(t, Options{Version: "test", Log: io.Discard, Agent: func() error { return nil }})
	text, isErr := p.call(t, 1, "run", map[string]any{"task": "summarize the changelog", "cwd": plain})
	if !isErr || strings.Contains(text, "model must be") {
		t.Fatalf("%v %s", isErr, text)
	}
}
