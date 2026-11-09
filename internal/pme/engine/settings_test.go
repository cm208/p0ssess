package engine

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"p0ssess/internal/pme/score"
	"p0ssess/internal/pme/statusline"
)

// TestConcurrentSettingsAreAllKept changes different settings at once; each change rewrites the whole
// file, so without the lock some would be lost.
func TestConcurrentSettingsAreAllKept(t *testing.T) {
	e, _ := setup(t, "")
	set := map[string]string{"monthly_usd": "42", "per_task_usd": "0.07", "daily_usd": "3", "plan": "api",
		"usage_credits": "true", "models.deny": "x-ai", "providers.deny": "Acme", "privacy.allow_safety_retention": "true",
		"lambda.preset": "eager", "statusline.width": "77", "statusline.mark": "*"}
	var wg sync.WaitGroup
	for k, v := range set {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.SetSetting(k, v); err != nil {
				t.Error(k, err)
			}
		}()
	}
	wg.Wait()
	b, err := e.Budget()
	if err != nil {
		t.Fatal(err)
	}
	if b.MonthlyUSD != 42 || b.PerTaskUSD != 0.07 || b.DailyUSD != 3 || b.Plan != "api" || !b.UsageCredits ||
		len(b.Models.Deny) != 1 || len(b.Providers.Deny) != 1 || !b.Privacy.AllowSafetyRetention {
		t.Errorf("budget lost a concurrent change: %+v", b)
	}
	cfg, err := score.LoadConfig(e.Dirs.EngineFile())
	if err != nil {
		t.Fatal(err)
	}
	st := statusline.LoadSettings(e.Dirs.EngineFile())
	if cfg.Lambda.Preset != "eager" || st.Width != 77 || st.Mark != "*" {
		t.Errorf("engine.toml lost a concurrent change: preset %q, width %d, mark %q", cfg.Lambda.Preset, st.Width, st.Mark)
	}
}

func TestShellInternetSetting(t *testing.T) {
	e, _ := setup(t, "")
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(dir, link)
	real, _ := filepath.EvalSymlinks(dir)
	msg, err := e.SetSetting("sandbox.shell_internet.add", link)
	if err != nil || !strings.Contains(msg, "Shell internet is now on for: "+real) {
		t.Fatalf("add: %q %v", msg, err)
	}
	e.SetSetting("sandbox.shell_internet.add", dir)
	b, _ := e.Budget()
	if len(b.Sandbox.ShellInternet) != 1 || !b.Sandbox.ShellInternetFor(filepath.Join(real, "x")) {
		t.Errorf("list %v", b.Sandbox.ShellInternet)
	}
	for _, bad := range []string{"relative/dir", "/", filepath.Join(dir, "missing")} {
		if _, err := e.SetSetting("sandbox.shell_internet.add", bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if msg, err := e.SetSetting("sandbox.shell_internet.remove", dir); err != nil || !strings.Contains(msg, "on for: none") {
		t.Errorf("remove: %q %v", msg, err)
	}
	b, _ = e.Budget()
	if len(b.Sandbox.ShellInternet) != 0 {
		t.Errorf("still on: %v", b.Sandbox.ShellInternet)
	}
}
