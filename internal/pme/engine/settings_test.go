package engine

import (
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
