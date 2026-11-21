// Package rescue is usage-limit rescue. When Claude's 5-hour or weekly window nears its limit, a Pro
// install lets p0ssess take routable work inside the spending envelope until the window resets; a Free
// install is told once per window that rescue exists and what it would have taken. This package holds
// the detection and the enter/exit state; the prompt hook and the status line read it.
package rescue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"p0ssess/internal/funnel"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/usage"
)

// MaxPeriods is how many finished rescue periods are kept for the page.
const MaxPeriods = 20

// Reading is a Claude window that is near its limit.
type Reading struct {
	Window    string
	Used      float64
	Projected float64
	ResetsAt  time.Time
}

// Detect finds the window nearest its limit: used at or past threshold, or at least half used and
// projected to run out before it resets. A window that has reset since the reading never counts.
func Detect(st usage.State, now time.Time, threshold float64) (Reading, bool) {
	if st.RateLimits == nil || st.ReadingAt.IsZero() {
		return Reading{}, false
	}
	var best Reading
	found := false
	check := func(name string, w *usage.Window, length time.Duration) {
		if w == nil || w.UsedPercentage == nil || w.ResetsAt == nil {
			return
		}
		reset := time.Unix(*w.ResetsAt, 0)
		if !reset.After(now) {
			return
		}
		used := *w.UsedPercentage / 100
		elapsed := 1 - reset.Sub(st.ReadingAt).Seconds()/length.Seconds()
		elapsed = max(0.15, min(1, elapsed))
		proj := used / elapsed
		if used >= threshold || (used >= 0.5 && proj >= 1) {
			if !found || used > best.Used {
				best, found = Reading{Window: name, Used: used, Projected: proj, ResetsAt: reset}, true
			}
		}
	}
	check("5-hour", st.RateLimits.FiveHour, usage.FiveHourLen)
	check("weekly", st.RateLimits.SevenDay, usage.SevenDayLen)
	return best, found
}

// Period is one stretch of rescue.
type Period struct {
	Window string    `json:"window"`
	Start  time.Time `json:"start"`
	Until  time.Time `json:"until"`
	End    time.Time `json:"end,omitempty"`
	Used   float64   `json:"used"`
	// UsedNoted is set once the first routed run of the period was counted.
	UsedNoted bool `json:"used_noted,omitempty"`
}

// free is the Free-install bookkeeping for one window: the routable tasks seen and whether the
// one-line offer was said.
type free struct {
	Key     string `json:"key"`
	Seen    int    `json:"seen"`
	Offered bool   `json:"offered,omitempty"`
}

// State is rescue.json.
type State struct {
	Active *Period  `json:"active,omitempty"`
	Done   []Period `json:"done,omitempty"`
	Free   *free    `json:"free,omitempty"`
}

func file(d paths.Dirs) string { return filepath.Join(d.State, "rescue.json") }

// Load reads the state; a missing or unreadable file is an empty one.
func Load(d paths.Dirs) State {
	var s State
	if b, err := os.ReadFile(file(d)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// Active returns the running rescue period, if one is and its window has not ended.
func Active(d paths.Dirs, now time.Time) (Period, bool) {
	s := Load(d)
	if s.Active == nil || !s.Active.Until.After(now) {
		return Period{}, false
	}
	return *s.Active, true
}

// Input is what one hook call knows.
type Input struct {
	D     paths.Dirs
	Now   time.Time
	Usage usage.State
	// Pro, Enabled and Ready: the account is Pro, rescue is on in budget.toml, and an OpenRouter key exists.
	Pro, Enabled, Ready bool
	Threshold           float64
	// Room says whether the envelope can still take paid runs.
	Room bool
	// Routable says whether this prompt looks like work p0ssess could take.
	Routable bool
	// Runs counts p0ssess runs since a time; SavedEach is the average saving of a past receipt. Both
	// are asked only when needed.
	Runs      func(since time.Time) int
	SavedEach func() float64
}

// Tick moves the state for one prompt and returns the one line to say to the user, or "".
func Tick(in Input) string {
	var say string
	var events []string
	_ = os.MkdirAll(in.D.State, 0o700)
	_ = paths.WithLock(file(in.D), func() error {
		s := Load(in.D)
		before, _ := json.Marshal(s)
		say = step(&s, in, &events)
		if after, _ := json.Marshal(s); string(before) != string(after) {
			if b, err := json.Marshal(s); err == nil {
				_ = paths.WriteFileAtomic(file(in.D), append(b, '\n'), 0o600)
			}
		}
		return nil
	})
	// Counted after the lock: the usage counts keep their file in the same directory, and the lock is per directory.
	for _, ev := range events {
		funnel.Note(in.D, ev, in.Now)
	}
	return say
}

func step(s *State, in Input, events *[]string) string {
	rd, near := Detect(in.Usage, in.Now, in.Threshold)
	if s.Active != nil {
		p := s.Active
		reset := !p.Until.After(in.Now)
		switch {
		case reset:
			return endActive(s, in.Now, "Claude's "+p.Window+" window has reset: p0ssess handed routable work back to Claude.")
		case !near:
			// Still inside the window but no longer near: the threshold was raised.
			return endActive(s, in.Now, "")
		case !in.Pro || !in.Enabled:
			return endActive(s, in.Now, "")
		case !in.Room:
			return endActive(s, in.Now, "p0ssess rescue is paused: your p0ssess limit is used up, so work stays on Claude until it frees up.")
		}
		if !p.UsedNoted && in.Runs != nil && in.Runs(p.Start) > 0 {
			p.UsedNoted = true
			*events = append(*events, funnel.RescueUsed)
		}
		return ""
	}
	if !near || !in.Enabled {
		return ""
	}
	if in.Pro {
		if !in.Ready || !in.Room {
			return ""
		}
		s.Active = &Period{Window: rd.Window, Start: in.Now.UTC(), Until: rd.ResetsAt.UTC(), Used: rd.Used}
		*events = append(*events, funnel.RescueOffered)
		return fmt.Sprintf("Claude's %s window is at %d%%: p0ssess is taking routable work until it resets at %s.",
			rd.Window, pct(rd.Used), clock(rd.ResetsAt.In(in.Now.Location()), rd.Window))
	}
	key := fmt.Sprintf("%s@%d", rd.Window, rd.ResetsAt.Unix())
	if s.Free == nil || s.Free.Key != key {
		s.Free = &free{Key: key}
	}
	if in.Routable {
		s.Free.Seen++
	}
	if s.Free.Offered || s.Free.Seen == 0 {
		return ""
	}
	s.Free.Offered = true
	*events = append(*events, funnel.RescueOffered)
	n := "1 routable task"
	if s.Free.Seen > 1 {
		n = fmt.Sprintf("%d routable tasks", s.Free.Seen)
	}
	est := "an estimate"
	if in.SavedEach != nil {
		if v := in.SavedEach(); v > 0 {
			est = fmt.Sprintf("an estimate: about $%.2f saved, from your past receipts", v*float64(s.Free.Seen))
		}
	}
	return fmt.Sprintf("Claude's %s window is at %d%%. Rescue is a Pro feature: p0ssess would take routable work until it resets at %s. "+
		"This window it would have taken %s so far (%s). /p0ssess:pro has the plans.",
		rd.Window, pct(rd.Used), clock(rd.ResetsAt.In(in.Now.Location()), rd.Window), n, est)
}

func endActive(s *State, now time.Time, say string) string {
	p := *s.Active
	p.End = now.UTC()
	s.Done = append(s.Done, p)
	if len(s.Done) > MaxPeriods {
		s.Done = s.Done[len(s.Done)-MaxPeriods:]
	}
	s.Active = nil
	return say
}

func pct(f float64) int { return int(f*100 + 0.5) }

// clock is a reset time as people say it: "3:40 PM", with the weekday for the weekly window.
func clock(t time.Time, window string) string {
	if window == "weekly" {
		return t.Format("Mon 3:04 PM")
	}
	return t.Format("3:04 PM")
}
