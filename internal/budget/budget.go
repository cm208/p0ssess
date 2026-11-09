// Package budget reads and writes budget.toml: OpenRouter spending limits,
// the Claude plan, privacy, and model and provider allow and deny lists.
package budget

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"p0ssess/internal/pme/paths"
)

type List struct {
	Allow []string `toml:"allow" json:"allow"`
	Deny  []string `toml:"deny" json:"deny"`
}

type Privacy struct {
	// Mode is "no-training" (default: every call sends
	// provider.data_collection = "deny") or "zdr" (zero-retention endpoints only).
	Mode string `toml:"mode" json:"mode"`
	// AllowSafetyRetention lets "zdr" mode use endpoints that keep requests
	// only for abuse checks.
	AllowSafetyRetention bool `toml:"allow_safety_retention" json:"allow_safety_retention"`
}

type Budget struct {
	MonthlyUSD float64 `toml:"monthly_usd" json:"monthly_usd"`
	PerTaskUSD float64 `toml:"per_task_usd" json:"per_task_usd"`
	// DailyUSD is the daily soft limit. 0 paces it: (cap - spent before today)
	// / days left in the month. Negative turns it off.
	DailyUSD   float64 `toml:"daily_usd" json:"daily_usd"`
	SaveClaude bool    `toml:"save_claude" json:"save_claude"`
	// Plan is "auto", "api" or "subscription".
	Plan         string  `toml:"plan" json:"plan"`
	UsageCredits bool    `toml:"usage_credits" json:"usage_credits"`
	Models       List    `toml:"models" json:"models"`
	Providers    List    `toml:"providers" json:"providers"`
	Privacy      Privacy `toml:"privacy" json:"privacy"`
	// DailyCapUSD is a lower daily limit set by project rules; 0 means none. It is never saved.
	DailyCapUSD float64 `toml:"-" json:"daily_cap_usd,omitempty"`
}

func Default() Budget {
	return Budget{MonthlyUSD: 10, PerTaskUSD: 0.02, DailyUSD: 0, Plan: "auto", Privacy: Privacy{Mode: "no-training"}}
}

// Load reads path, filling defaults for anything missing.
func Load(path string) (Budget, error) {
	b := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	if _, err := toml.Decode(string(data), &b); err != nil {
		return Default(), fmt.Errorf("%s: %w", path, err)
	}
	return b, b.Validate()
}

func (b Budget) Validate() error {
	for name, v := range map[string]float64{"monthly_usd": b.MonthlyUSD, "per_task_usd": b.PerTaskUSD, "daily_usd": b.DailyUSD} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%s must be a finite number", name)
		}
	}
	switch {
	case b.MonthlyUSD < 0:
		return errors.New("monthly_usd must be 0 or more")
	case b.PerTaskUSD < 0:
		return errors.New("per_task_usd must be 0 or more")
	}
	switch b.Plan {
	case "auto", "api", "subscription":
	default:
		return fmt.Errorf("plan must be auto, api or subscription, not %q", b.Plan)
	}
	switch b.Privacy.Mode {
	case "no-training", "zdr":
	default:
		return fmt.Errorf("privacy.mode must be no-training or zdr, not %q", b.Privacy.Mode)
	}
	return nil
}

const header = "# p0ssess budget. OpenRouter cash only; Claude usage is weighed separately.\n# See docs/ENGINE.md for every field.\n\n"

func Save(path string, b Budget) error {
	if err := b.Validate(); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString(header)
	if err := toml.NewEncoder(&buf).Encode(b); err != nil {
		return err
	}
	return paths.WriteFileAtomic(path, buf.Bytes(), 0o600)
}

// DailyLimit is the soft limit for today. spentBeforeToday is this month's
// spend up to the start of today.
func (b Budget) DailyLimit(spentBeforeToday float64, now time.Time) float64 {
	limit := b.dailyLimit(spentBeforeToday, now)
	if b.DailyCapUSD > 0 {
		limit = math.Min(limit, b.DailyCapUSD)
	}
	return limit
}

func (b Budget) dailyLimit(spentBeforeToday float64, now time.Time) float64 {
	if b.DailyUSD > 0 {
		return b.DailyUSD
	}
	if b.DailyUSD < 0 {
		return math.Inf(1)
	}
	y, m, d := now.Date()
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, now.Location()).Day()
	left := last - d + 1
	return math.Max(0, b.MonthlyUSD-spentBeforeToday) / float64(left)
}

// ModelAllowed applies the allow and deny lists. Entries match a full model
// id, an author prefix ("deepseek" or "deepseek/"), or a provider name.
func (b Budget) ModelAllowed(id string, providers []string) (bool, string) {
	if matchAny(b.Models.Deny, id) {
		return false, "on your model deny list"
	}
	if len(b.Models.Allow) > 0 && !matchAny(b.Models.Allow, id) {
		return false, "not on your model allow list"
	}
	for _, p := range providers {
		if matchName(b.Providers.Deny, p) {
			if len(providers) == 1 {
				return false, "its only provider is on your deny list"
			}
		}
	}
	if len(b.Providers.Allow) > 0 && len(providers) > 0 {
		ok := false
		for _, p := range providers {
			if matchName(b.Providers.Allow, p) {
				ok = true
			}
		}
		if !ok {
			return false, "no provider on your allow list"
		}
	}
	return true, ""
}

func matchAny(list []string, id string) bool {
	author, _, _ := strings.Cut(id, "/")
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == id || strings.TrimSuffix(e, "/") == author || (strings.HasSuffix(e, "*") && strings.HasPrefix(id, strings.TrimSuffix(e, "*"))) {
			return true
		}
	}
	return false
}

func matchName(list []string, name string) bool {
	for _, e := range list {
		if strings.EqualFold(strings.TrimSpace(e), name) {
			return true
		}
	}
	return false
}
