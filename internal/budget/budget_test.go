package budget

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaultsAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "budget.toml")
	b, err := Load(path)
	if err != nil || b.MonthlyUSD != 10 || b.PerTaskUSD != 0.02 || b.Privacy.Mode != "no-training" {
		t.Fatalf("defaults: %+v %v", b, err)
	}
	os.WriteFile(path, []byte("monthly_usd = 25\n[privacy]\nmode = \"zdr\"\n"), 0o600)
	b, err = Load(path)
	if err != nil || b.MonthlyUSD != 25 || b.PerTaskUSD != 0.02 || b.Privacy.Mode != "zdr" {
		t.Fatalf("partial file: %+v %v", b, err)
	}
	b.Models.Deny = []string{"x-ai"}
	if err := Save(path, b); err != nil {
		t.Fatal(err)
	}
	back, _ := Load(path)
	if back.Models.Deny[0] != "x-ai" || back.MonthlyUSD != 25 {
		t.Errorf("round trip: %+v", back)
	}
	os.WriteFile(path, []byte("plan = \"monthly\"\n"), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("bad plan should fail")
	}
}

func TestDailyLimit(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) // 10 days left including today
	cases := []struct {
		daily, spent, want float64
	}{
		{0, 0, 1.0},
		{0, 5, 0.5},
		{0, 12, 0},
		{2, 5, 2},
		{-1, 5, math.Inf(1)},
	}
	for _, tc := range cases {
		b := Default()
		b.DailyUSD = tc.daily
		if got := b.DailyLimit(tc.spent, now); math.Abs(got-tc.want) > 1e-9 && !(math.IsInf(got, 1) && math.IsInf(tc.want, 1)) {
			t.Errorf("daily=%v spent=%v: got %v want %v", tc.daily, tc.spent, got, tc.want)
		}
	}
}

func TestModelAllowed(t *testing.T) {
	b := Default()
	b.Models.Deny = []string{"deepseek"}
	if ok, _ := b.ModelAllowed("deepseek/deepseek-v4.1-flash", nil); ok {
		t.Error("author deny should match")
	}
	if ok, _ := b.ModelAllowed("z-ai/glm-5.3-flash", nil); !ok {
		t.Error("other author allowed")
	}
	b.Models.Allow = []string{"google/*"}
	if ok, _ := b.ModelAllowed("z-ai/glm-5.3-flash", nil); ok {
		t.Error("allow list should restrict")
	}
	if ok, _ := b.ModelAllowed("google/gemini-3.8-flash", nil); !ok {
		t.Error("glob allow")
	}
	b = Default()
	b.Providers.Deny = []string{"Alibaba"}
	if ok, _ := b.ModelAllowed("alibaba/wan-3.0", []string{"Alibaba"}); ok {
		t.Error("only provider denied")
	}
}
