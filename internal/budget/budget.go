// Package budget reads and writes budget.toml: OpenRouter spending limits,
// the Claude plan, privacy, and model and provider allow and deny lists.
package budget

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
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

// Sandbox holds the sandbox opt-ins. It lives only in the user's own budget.toml: project rules cannot
// set it.
type Sandbox struct {
	// ShellInternet lists project directories whose sandboxed shell commands may reach the public internet
	// through the jail's egress proxy. Off for every project not listed.
	ShellInternet []string `toml:"shell_internet" json:"shell_internet"`
	// Where picks where agent jobs run: "auto" (default: here when rootless podman works, the cloud
	// sandbox for Pro and Team otherwise), "local" or "cloud". Empty means auto.
	Where string `toml:"where,omitempty" json:"where,omitempty"`
	// WaitMin is how many minutes a job waits in the cloud sandbox queue before giving up; 0 means the
	// default, DefaultWaitMin.
	WaitMin int `toml:"wait_min,omitempty" json:"wait_min,omitempty"`
}

// DefaultWaitMin is how long a job waits for a cloud sandbox slot.
const DefaultWaitMin = 10

// MaxWaitMin bounds the wait setting.
const MaxWaitMin = 120

// QueueWait is the wait limit in effect.
func (s Sandbox) QueueWait() time.Duration {
	if s.WaitMin <= 0 {
		return DefaultWaitMin * time.Minute
	}
	return time.Duration(s.WaitMin) * time.Minute
}

// ShellInternetFor reports whether repo (a canonical path) is, or is inside, a listed project.
func (s Sandbox) ShellInternetFor(repo string) bool {
	if repo == "" || !filepath.IsAbs(repo) {
		return false
	}
	repo = filepath.Clean(repo)
	for _, p := range s.ShellInternet {
		p = filepath.Clean(p)
		if !filepath.IsAbs(p) || p == "/" {
			continue
		}
		if repo == p || strings.HasPrefix(repo, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Ask holds opt-in reasons to ask before a call that is within every limit. Both are off by default:
// a call whose estimate is under the per-task limit runs without asking.
type Ask struct {
	// FirstUse asks before the first run on a model the ledger has never seen run.
	FirstUse bool `toml:"first_use" json:"first_use"`
	// CodeEdits asks before an agent run in edit mode. The diff still comes back for review either way.
	CodeEdits bool `toml:"code_edits" json:"code_edits"`
}

// Outcomes holds whether anonymous outcome records are sent when a Claude Code session ends. On unless turned off.
type Outcomes struct {
	Send bool `toml:"send" json:"send"`
}

// Funnel holds whether anonymous usage counts are sent (opt-in; off unless the user says yes).
type Funnel struct {
	Send bool `toml:"send" json:"send"`
}

// Report holds whether a linked Pro or Team install sends last month's savings figures so the server can
// email the monthly summary. On by default for those plans; Free installs never send it.
type Report struct {
	Send bool `toml:"send" json:"send"`
}

// Rescue holds usage-limit rescue (Pro): when Claude's 5-hour or weekly window is near its limit,
// p0ssess takes routable work inside the envelope. On by default.
type Rescue struct {
	Enabled bool `toml:"enabled" json:"enabled"`
	// Threshold is the share of a window used (0.5 to 0.99) from which rescue starts; 0 means the default.
	Threshold float64 `toml:"threshold" json:"threshold"`
}

// DefaultRescueThreshold is 85% of a window used.
const DefaultRescueThreshold = 0.85

// Level is the threshold in effect.
func (r Rescue) Level() float64 {
	if r.Threshold <= 0 {
		return DefaultRescueThreshold
	}
	return r.Threshold
}

type Budget struct {
	MonthlyUSD float64 `toml:"monthly_usd" json:"monthly_usd"`
	PerTaskUSD float64 `toml:"per_task_usd" json:"per_task_usd"`
	// DailyUSD is the daily soft limit. 0 paces it: (cap - spent before today)
	// / days left in the month. Negative turns it off.
	DailyUSD   float64 `toml:"daily_usd" json:"daily_usd"`
	SaveClaude bool    `toml:"save_claude" json:"save_claude"`
	// Plan is "auto", "api" or "subscription".
	Plan         string   `toml:"plan" json:"plan"`
	UsageCredits bool     `toml:"usage_credits" json:"usage_credits"`
	Models       List     `toml:"models" json:"models"`
	Providers    List     `toml:"providers" json:"providers"`
	Privacy      Privacy  `toml:"privacy" json:"privacy"`
	Sandbox      Sandbox  `toml:"sandbox" json:"sandbox"`
	Ask          Ask      `toml:"ask" json:"ask"`
	Outcomes     Outcomes `toml:"outcomes" json:"outcomes"`
	Funnel       Funnel   `toml:"funnel" json:"funnel"`
	Rescue       Rescue   `toml:"rescue" json:"rescue"`
	Report       Report   `toml:"report" json:"report"`
	// DailyCapUSD is a lower daily limit set by project rules; 0 means none. It is never saved.
	DailyCapUSD float64 `toml:"-" json:"daily_cap_usd,omitempty"`
}

// Default is a new Free install's envelope: $1 a day, $10 a month, and up to $0.25 a task without asking.
func Default() Budget {
	b := legacy()
	b.DailyUSD = 1
	b.PerTaskUSD = 0.25
	return b
}

// legacy is what an install from before 1.1 ran under: a daily limit paced from the monthly cap.
func legacy() Budget {
	return Budget{MonthlyUSD: 10, PerTaskUSD: 0.02, DailyUSD: 0, Plan: "auto", Privacy: Privacy{Mode: "no-training"}, Outcomes: Outcomes{Send: true},
		Rescue: Rescue{Enabled: true, Threshold: DefaultRescueThreshold}, Report: Report{Send: true}}
}

// newInstallMarker is created in the state directory the first time a new install's limits are read,
// so the Free defaults stay after its first run writes the ledger; an install from before 1.1 has a
// ledger and no marker.
func newInstallMarker(d paths.Dirs) string { return filepath.Join(d.State, "envelope-defaults") }

// NewInstall reports whether this install started under 1.1's defaults.
func NewInstall(d paths.Dirs) bool {
	_, err := os.Stat(newInstallMarker(d))
	return err == nil
}

// ForInstall reads the install's budget.toml: a new install gets Default, and keeps it after its first
// run; an install that ran work before 1.1 keeps the defaults it ran under.
func ForInstall(d paths.Dirs) (Budget, error) {
	_, herr := os.Stat(d.HistoryFile())
	_, merr := os.Stat(newInstallMarker(d))
	if herr != nil && merr != nil {
		if _, err := os.Stat(d.BudgetFile()); errors.Is(err, os.ErrNotExist) {
			paths.WriteFileAtomic(newInstallMarker(d), []byte("1.1\n"), 0o600)
			merr = nil
		}
	}
	return LoadFor(d.BudgetFile(), herr == nil && merr != nil)
}

// Load reads path, filling defaults for anything missing.
func Load(path string) (Budget, error) { return LoadFor(path, false) }

// LoadFor reads path. A field the file leaves out takes the pre-1.1 default when the file exists or
// existing is set (an install that has run work), so an upgrade never changes a user's limits; only a
// new install gets Default.
func LoadFor(path string, existing bool) (Budget, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if existing {
			return legacy(), nil
		}
		return Default(), nil
	}
	b := legacy()
	if err != nil {
		return b, err
	}
	if _, err := toml.Decode(string(data), &b); err != nil {
		return legacy(), fmt.Errorf("%s: %w", path, err)
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
	if t := b.Rescue.Threshold; t != 0 && (math.IsNaN(t) || t < 0.5 || t > 0.99) {
		return errors.New("rescue.threshold must be between 50% and 99%")
	}
	switch b.Sandbox.Where {
	case "", "auto", "local", "cloud":
	default:
		return fmt.Errorf("sandbox.where must be auto, local or cloud, not %q", b.Sandbox.Where)
	}
	if b.Sandbox.WaitMin < 0 || b.Sandbox.WaitMin > MaxWaitMin {
		return fmt.Errorf("sandbox.wait_minutes must be between 1 and %d", MaxWaitMin)
	}
	for _, p := range b.Sandbox.ShellInternet {
		if !filepath.IsAbs(p) || filepath.Clean(p) == "/" {
			return fmt.Errorf("sandbox.shell_internet: %q must be an absolute project path, not /", p)
		}
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
