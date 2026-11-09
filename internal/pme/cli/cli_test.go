package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, IO{In: strings.NewReader(stdin), Out: &out, Err: &errb})
	return code, out.String(), errb.String()
}

func TestCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	proj := filepath.Join(home, "proj")
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)

	if code, out, _ := run(t, "", "help"); code != 0 || !strings.Contains(out, "recommend") {
		t.Fatal("help")
	}
	code, out, errs := run(t, "", "engine", "recommend", "--use-case", "video-understand", "--cwd", proj, "--record")
	if code != 0 || !strings.Contains(out, "video-understand (#75") || !strings.Contains(out, "pick: handing this to") || !strings.Contains(out, "offer: o-") {
		t.Fatalf("recommend: %d %s %s", code, out, errs)
	}
	code, out, _ = run(t, "", "recommend", "--text", "transcribe the call recording", "--in", "audio", "--audio-seconds", "900", "--cwd", proj, "--json")
	if code != 0 || !strings.Contains(out, `"id": "audio-transcribe"`) || !strings.Contains(out, `"audio_seconds": 900`) {
		t.Fatalf("profile recommend: %s", out)
	}
	if code, out, _ := run(t, "", "set", "per_task_usd", "0.05"); code != 0 || !strings.Contains(out, "per_task_usd = 0.05") {
		t.Errorf("set: %s", out)
	}
	if code, _, _ := run(t, "", "set", "nope", "1"); code != 1 {
		t.Error("unknown setting should fail")
	}
	code, out, _ = run(t, "", "rules", "add", `{"kind":"only","models":["google/gemini-3.8-flash"],"kinds":["media"],"scope":"project"}`, "--cwd", proj, "--dry-run")
	if code != 0 || !strings.Contains(out, "would save: only use gemini-3.8-flash for media work (project)") {
		t.Errorf("dry run: %s", out)
	}
	if code, out, _ := run(t, "", "rules", "list", "--cwd", proj); code != 0 || !strings.Contains(out, "no rules") {
		t.Errorf("rules list: %s", out)
	}
	if code, out, _ := run(t, "", "outcome", "--model", "google/gemini-3.8-flash", "--outcome", "accepted", "--cash", "0.04", "--use-case", "video-understand"); code != 0 || !strings.Contains(out, "recorded accepted") {
		t.Errorf("outcome: %s", out)
	}
	if code, out, _ := run(t, "", "status", "--cwd", proj); code != 0 || !strings.Contains(out, "spent $0.04 today") {
		t.Errorf("status: %s", out)
	}
	if code, out, _ := run(t, `{"session_id":"s","cwd":"`+proj+`","prompt":"what does bug.mp4 show?"}`, "hook"); code != 0 || !strings.Contains(out, "video in this prompt") {
		t.Errorf("hook: %s", out)
	}
	t.Setenv("NO_COLOR", "1")
	if code, out, _ := run(t, `{"session_id":"s","model":{"id":"claude-sonnet-5-5"}}`, "statusline"); code != 0 || strings.TrimSpace(out) != "ᗣ p0ssess  $0.04 today" {
		t.Errorf("statusline: %q", out)
	}
	if code, out, _ := run(t, "", "usecases"); code != 0 || strings.Count(out, "\n") != 104 {
		t.Errorf("usecases lines = %d", strings.Count(out, "\n"))
	}
	if code, _, _ := run(t, "", "bogus"); code != 2 {
		t.Error("unknown command exit code")
	}
}
