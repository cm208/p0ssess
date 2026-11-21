package hook

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/tour"
)

// The tests here must not see the environment of a Claude Code session they may run inside.
func TestMain(m *testing.M) {
	for _, k := range []string{"CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_PID"} {
		os.Unsetenv(k)
	}
	os.Exit(m.Run())
}

// The prompt hook starts the tour in the first interactive session of a new install, once, and never
// in print mode.
func TestPromptHookStartsTour(t *testing.T) {
	d, proj := dirs(t)
	raw, _ := json.Marshal(Input{SessionID: "s1", Cwd: proj, Prompt: "hi there"})
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "sdk-cli")
	if out := Run(raw, d, time.Now()); strings.Contains(out, "tour") {
		t.Fatalf("print mode: %q", out)
	}
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	if out := Run(raw, d, time.Now()); !strings.Contains(out, `skill "p0ssess:tour"`) {
		t.Fatalf("first run: %q", out)
	}
	if out := Run(raw, d, time.Now()); strings.Contains(out, "tour") {
		t.Fatalf("repeated: %q", out)
	}
	if st, _ := tour.Load(d); st.State != tour.Started {
		t.Errorf("%+v", st)
	}
}

// A private directory defers the tour.
func TestPromptHookTourPrivate(t *testing.T) {
	d, proj := dirs(t)
	os.WriteFile(proj+"/.p0ssess-private", nil, 0o600)
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	raw, _ := json.Marshal(Input{SessionID: "s1", Cwd: proj, Prompt: "hi"})
	if out := Run(raw, d, time.Now()); out != "" {
		t.Fatalf("%q", out)
	}
	if _, ok := tour.Load(d); ok {
		t.Error("used up in a private directory")
	}
}
