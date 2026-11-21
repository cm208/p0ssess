package me

import (
	"context"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/budget"
	"p0ssess/internal/cloudsandbox"
	"p0ssess/internal/pme/paths"
)

// sandboxView is the page's cloud sandbox section: where agent jobs run, this month's hours, and the
// place in line while a job waits.
type sandboxView struct {
	Pro      bool   `json:"pro"`
	Where    string `json:"where"`
	WaitMin  int    `json:"wait_minutes"`
	Hours    bool   `json:"hours"` // used_s, left_s and allowance_s are filled in
	UsedS    int64  `json:"used_s"`
	LeftS    int64  `json:"left_s"`
	AllowedS int64  `json:"allowance_s"`
	Line     string `json:"line,omitempty"`
	Queued   bool   `json:"queued"`
	Queue    string `json:"queue,omitempty"`
}

// sandboxUsage reads this month's hours; tests replace it.
var sandboxUsage = func(ctx context.Context, d paths.Dirs, now time.Time) (cloudsandbox.Usage, error) {
	return cloudsandbox.CurrentUsage(ctx, d, account.Token(d), now)
}

func sandboxOf(ctx context.Context, d paths.Dirs, b budget.Sandbox, now time.Time) sandboxView {
	v := sandboxView{Pro: account.Current(d).Pro(), Where: b.Where, WaitMin: int(b.QueueWait() / time.Minute)}
	if v.Where == "" {
		v.Where = "auto"
	}
	if q, _, ok := cloudsandbox.Queued(d, now); ok {
		v.Queued, v.Queue = true, q.QueuedText()
	}
	if !v.Pro {
		return v
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if u, err := sandboxUsage(cctx, d, now); err == nil || u.Month != "" {
		v.Hours, v.UsedS, v.LeftS, v.AllowedS, v.Line = true, u.UsedS, u.LeftS, u.AllowedS, u.UsageLine()
	}
	return v
}

// SandboxLine is the terminal summary's cloud sandbox row: this month's hours, or the place in line.
// Empty for a Free install and when the hours cannot be read.
func SandboxLine(ctx context.Context, d paths.Dirs, b budget.Sandbox, now time.Time) string {
	v := sandboxOf(ctx, d, b, now)
	switch {
	case !v.Pro:
		return ""
	case v.Queued:
		return v.Queue
	case v.Hours:
		return v.Line
	}
	return ""
}
