// Package usage stores Claude Code's rate-limit readings, as the status line
// wrapper sees them, and projects them into limit pressure for the engine.
// It never touches Claude credentials; the only input is the status line JSON
// Claude Code already hands to status line scripts.
package usage

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"sort"
	"time"

	"p0ssess/internal/pme/paths"
)

type Window struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *int64   `json:"resets_at"`
}

type RateLimits struct {
	FiveHour *Window `json:"five_hour,omitempty"`
	SevenDay *Window `json:"seven_day,omitempty"`
}

type Session struct {
	ID      string    `json:"id"`
	Cwd     string    `json:"cwd"`
	Project string    `json:"project"`
	Model   string    `json:"model"`
	Seen    time.Time `json:"seen"`
}

// State is the content of usage.json.
type State struct {
	Updated     time.Time   `json:"updated"`
	RateLimits  *RateLimits `json:"rate_limits,omitempty"`
	ReadingAt   time.Time   `json:"rate_limits_at,omitempty"`
	Subscriber  bool        `json:"plan_seen_subscription,omitempty"`
	LeadModel   string      `json:"lead_model,omitempty"`
	Sessions    []Session   `json:"sessions,omitempty"`
	SessionCost float64     `json:"session_cost_usd,omitempty"`
}

func Load(path string) (State, error) {
	var s State
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, err
	}
	return s, nil
}

func Save(path string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(path, b, 0o600)
}

// Update applies fn to the stored state and saves it, holding a lock across the read and the write so
// a concurrent update (another session's status line or prompt hook) is not lost. An unreadable file is
// left alone.
func Update(path string, fn func(*State)) error {
	return paths.WithLock(path, func() error {
		s, err := Load(path)
		if err != nil {
			return err
		}
		fn(&s)
		return Save(path, s)
	})
}

// StatusInput is the subset of Claude Code's status line JSON p0ssess reads.
type StatusInput struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Model     struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
	Cost struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
	} `json:"cost"`
	RateLimits *RateLimits `json:"rate_limits"`
}

// Record folds one status line reading into the state.
func (s *State) Record(in StatusInput, now time.Time) {
	s.Updated = now.UTC()
	if in.RateLimits != nil && (in.RateLimits.FiveHour != nil || in.RateLimits.SevenDay != nil) {
		s.RateLimits = in.RateLimits
		s.ReadingAt = now.UTC()
		s.Subscriber = true
	}
	if in.Model.ID != "" {
		s.LeadModel = in.Model.ID
	}
	s.SessionCost = in.Cost.TotalCostUSD
	if in.SessionID == "" {
		return
	}
	cwd := in.Workspace.CurrentDir
	if cwd == "" {
		cwd = in.Cwd
	}
	proj := in.Workspace.ProjectDir
	if proj == "" {
		proj = paths.ProjectRoot(cwd)
	}
	s.TouchSession(Session{ID: in.SessionID, Cwd: cwd, Project: proj, Model: in.Model.ID, Seen: now.UTC()})
}

// TouchSession records that a session is active, keeping the 20 most recent.
func (s *State) TouchSession(se Session) {
	out := []Session{se}
	for _, x := range s.Sessions {
		if x.ID != se.ID {
			out = append(out, x)
		}
	}
	if len(out) > 20 {
		out = out[:20]
	}
	s.Sessions = out
}

// SessionFor guesses the active session for a directory: the most recently
// seen session for the same directory or project.
func (s State) SessionFor(dir string) (Session, bool) {
	ss := append([]Session(nil), s.Sessions...)
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].Seen.After(ss[j].Seen) })
	root := paths.ProjectRoot(dir)
	for _, x := range ss {
		if dir != "" && (x.Cwd == dir || (root != "" && x.Project == root)) {
			return x, true
		}
	}
	// No fallback to the newest session elsewhere: its session-scoped rules would apply to an unrelated project.
	return Session{}, false
}

// Pressure is projected end-of-window Claude usage as a fraction of the limit.
type Pressure struct {
	Known     bool    `json:"known"`
	Projected float64 `json:"projected"` // û, the worse window
	Window    string  `json:"window,omitempty"`
	Used      float64 `json:"used,omitempty"` // that window's used fraction
	FiveHour  float64 `json:"five_hour_projected,omitempty"`
	SevenDay  float64 `json:"seven_day_projected,omitempty"`
	Note      string  `json:"note"`
	AgeSec    int64   `json:"reading_age_seconds,omitempty"`
}

const (
	FiveHourLen = 5 * time.Hour
	SevenDayLen = 7 * 24 * time.Hour
)

// Project turns the latest reading into pressure. eMin bounds how much an
// early-window reading is extrapolated (û = u / max(elapsed, eMin)). A reading
// older than stale, or none at all, gives Known = false.
func (s State) Project(now time.Time, eMin float64, stale time.Duration) Pressure {
	if s.RateLimits == nil || s.ReadingAt.IsZero() {
		return Pressure{Note: "no Claude usage reading yet"}
	}
	age := now.Sub(s.ReadingAt)
	if stale > 0 && age > stale {
		return Pressure{Note: "Claude usage reading is " + age.Round(time.Minute).String() + " old", AgeSec: int64(age.Seconds())}
	}
	p := Pressure{Known: true, AgeSec: int64(age.Seconds())}
	eval := func(w *Window, length time.Duration) (float64, float64, bool) {
		if w == nil || w.UsedPercentage == nil || w.ResetsAt == nil {
			return 0, 0, false
		}
		reset := time.Unix(*w.ResetsAt, 0)
		u := *w.UsedPercentage / 100
		if !reset.After(now) {
			return 0, 0, true // the window has reset since the reading
		}
		elapsed := 1 - reset.Sub(s.ReadingAt).Seconds()/length.Seconds()
		elapsed = math.Max(0, math.Min(1, elapsed))
		return u / math.Max(elapsed, eMin), u, true
	}
	any := false
	if proj, u, ok := eval(s.RateLimits.FiveHour, FiveHourLen); ok {
		any = true
		p.FiveHour = proj
		if proj > p.Projected {
			p.Projected, p.Window, p.Used = proj, "5-hour", u
		}
	}
	if proj, u, ok := eval(s.RateLimits.SevenDay, SevenDayLen); ok {
		any = true
		p.SevenDay = proj
		if proj > p.Projected {
			p.Projected, p.Window, p.Used = proj, "weekly", u
		}
	}
	if !any {
		return Pressure{Note: "reading has no usable window"}
	}
	if p.Window == "" {
		p.Note = "windows have reset"
	} else {
		p.Note = p.Window + " window " + pct(p.Used) + " used, projected " + pct(p.Projected)
	}
	return p
}

func pct(f float64) string {
	return itoa(int(math.Round(f*100))) + "%"
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
