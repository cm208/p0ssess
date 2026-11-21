package me

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"p0ssess/internal/budget"
	"p0ssess/internal/cloudsandbox"
	"p0ssess/internal/pme/paths"
)

func TestSandboxView(t *testing.T) {
	root := t.TempDir()
	d := paths.Dirs{Config: filepath.Join(root, "c"), State: filepath.Join(root, "s"), Cache: filepath.Join(root, "k")}
	for _, p := range []string{d.Config, d.State, d.Cache} {
		os.MkdirAll(p, 0o700)
	}
	calls := 0
	sandboxUsage = func(context.Context, paths.Dirs, time.Time) (cloudsandbox.Usage, error) {
		calls++
		return cloudsandbox.Usage{Month: "2026-11", UsedS: 3600, LeftS: 68400, AllowedS: 72000}, nil
	}
	now := time.Now()
	free := sandboxOf(context.Background(), d, budget.Sandbox{}, now)
	if free.Pro || free.Hours || calls != 0 || free.Where != "auto" || free.WaitMin != 10 {
		t.Fatalf("free: %+v calls %d", free, calls)
	}
	os.MkdirAll(filepath.Join(d.Config, "secrets"), 0o700)
	os.WriteFile(filepath.Join(d.Config, "secrets", "p0ssess_token"), []byte("p0d_x\n"), 0o600)
	os.WriteFile(filepath.Join(d.State, "account.json"), []byte(`{"plan":"pro","status":"active"}`), 0o600)
	cloudsandbox.SaveQueue(d, cloudsandbox.QueueInfo{Position: 2, ETA: 4 * time.Minute, HasETA: true}, now, now)
	pro := sandboxOf(context.Background(), d, budget.Sandbox{Where: "cloud", WaitMin: 20}, now)
	if !pro.Pro || !pro.Hours || pro.UsedS != 3600 || pro.Line != "1h used, 19h left this month" ||
		!pro.Queued || pro.Queue != "queued 2nd, ~4 min" || pro.Where != "cloud" || pro.WaitMin != 20 {
		t.Fatalf("pro: %+v", pro)
	}
	// A failed reading with nothing cached leaves the hours out rather than inventing them.
	sandboxUsage = func(context.Context, paths.Dirs, time.Time) (cloudsandbox.Usage, error) {
		return cloudsandbox.Usage{}, errors.New("offline")
	}
	if v := sandboxOf(context.Background(), d, budget.Sandbox{}, now); v.Hours {
		t.Errorf("offline: %+v", v)
	}
}
