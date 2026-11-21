// Package statusline wraps the user's Claude Code status line. It saves the
// rate-limit reading Claude Code passes on stdin, runs the user's own status
// line command with the same input, and appends the p0ssess segment:
// today's spend against the daily limit and running jobs, e.g.
// "p0ssess $0.42/$1 · 1 running".
package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"p0ssess/internal/account"
	"p0ssess/internal/budget"
	"p0ssess/internal/cloudsandbox"
	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table"
	"p0ssess/internal/proc"
	"p0ssess/internal/rescue"
	"p0ssess/internal/team"
	"p0ssess/internal/usage"
)

// Settings is the [statusline] table of engine.toml.
type Settings struct {
	// Command is the user's original status line command, run through sh -c.
	Command string `toml:"command" json:"command"`
	// Mark is the ghost glyph; empty hides it.
	Mark string `toml:"mark" json:"mark"`
	// Width is used when Claude Code does not set COLUMNS. 0 means 120.
	Width int  `toml:"width" json:"width"`
	Color bool `toml:"color" json:"color"`
	// TimeoutMS bounds the user's command.
	TimeoutMS int `toml:"timeout_ms" json:"timeout_ms"`
}

func DefaultSettings() Settings {
	return Settings{Mark: "ᗣ", Color: true, TimeoutMS: 2000}
}

func LoadSettings(path string) Settings {
	var f struct {
		Statusline Settings `toml:"statusline"`
	}
	f.Statusline = DefaultSettings()
	if b, err := os.ReadFile(path); err == nil {
		if _, err := toml.Decode(string(b), &f); err != nil {
			return DefaultSettings()
		}
	}
	if v := os.Getenv("P0SSESS_STATUSLINE_COMMAND"); v != "" {
		f.Statusline.Command = v
	}
	if os.Getenv("NO_COLOR") != "" {
		f.Statusline.Color = false
	}
	return f.Statusline
}

// Colors, in 256-color codes. Amber means near or over the daily limit; red only when paid runs are
// stopped (the monthly cap or the team budget is used up). Never blinking.
const (
	colorIdle    = "\x1b[38;5;244m"
	colorOffline = "\x1b[38;5;240m"
	colorRunning = "\x1b[38;5;250m"
	colorAmber   = "\x1b[38;5;214m"
	colorRed     = "\x1b[38;5;203m"
	colorViolet  = "\x1b[38;5;99m"
	reset        = "\x1b[0m"
)

// State is what the segment shows.
type State struct {
	SpentToday float64
	// DailyLimit is today's limit; 0 or less means none.
	DailyLimit float64
	// SpentMonth counts open reservations too.
	SpentMonth float64
	MonthlyCap float64
	// Team is set for a Team seat whose team has a shared budget.
	Team      bool
	TeamSpent float64
	TeamCap   float64
	Running   []usage.Job
	// Offline is true while the last call to the engine service failed: the mark dims a little.
	Offline bool
	// Stale is the older p0ssess version this session's server still runs, or "".
	Stale string
	// Rescue is set while usage-limit rescue is on.
	Rescue bool
	// Queued is the place of a job waiting for a cloud sandbox slot; IsQueued says whether one waits.
	Queued   cloudsandbox.QueueInfo
	IsQueued bool
}

// Level is how the segment is colored.
type Level int

const (
	Calm    Level = iota
	Near          // near or over the daily limit, or near a cap
	Stopped       // the monthly cap or the team budget stops paid runs
)

func (s State) monthStopped() bool { return s.MonthlyCap > 0 && s.SpentMonth >= s.MonthlyCap-1e-9 }
func (s State) teamStopped() bool  { return s.Team && s.TeamCap > 0 && s.TeamSpent >= s.TeamCap-1e-9 }

func (s State) Level() Level {
	switch {
	case s.monthStopped() || s.teamStopped():
		return Stopped
	case s.DailyLimit > 0 && s.SpentToday >= 0.8*s.DailyLimit,
		s.MonthlyCap > 0 && s.SpentMonth >= 0.8*s.MonthlyCap,
		s.Team && s.TeamCap > 0 && s.TeamSpent >= 0.8*s.TeamCap:
		return Near
	}
	return Calm
}

// Segment renders the p0ssess part for a given width budget (0: no limit), longest variant that fits.
func Segment(s State, st Settings, room int) string {
	running := len(s.Running)
	var main []string
	switch {
	case s.teamStopped():
		main = []string{"team budget " + money(s.TeamSpent) + "/" + limit(s.TeamCap) + " · paid runs stopped", "team budget used up", "stopped"}
	case s.monthStopped():
		main = []string{"month cap " + money(s.SpentMonth) + "/" + limit(s.MonthlyCap) + " · paid runs stopped", "month cap reached", "stopped"}
	case s.DailyLimit > 0:
		main = []string{money(s.SpentToday) + "/" + limit(s.DailyLimit)}
	default:
		main = []string{money(s.SpentToday) + " today", money(s.SpentToday)}
	}
	var extras [][]string
	if running > 0 {
		n := fmt.Sprintf("%d running", running)
		extras = append(extras, []string{n + " (" + modelNames(s.Running) + ")", n, ""})
	}
	if s.Team && s.TeamCap > 0 && !s.teamStopped() {
		extras = append(extras, []string{"team " + money(s.TeamSpent) + "/" + limit(s.TeamCap), ""})
	}
	if s.IsQueued {
		q := s.Queued
		v := []string{q.QueuedText()}
		if q.HasETA {
			q.HasETA = false
			v = append(v, q.QueuedText())
		}
		extras = append(extras, append(v, "queued"))
	}
	if s.Rescue {
		extras = append(extras, []string{"rescue"})
	}
	if s.Stale != "" {
		extras = append(extras, []string{"restart to update", "restart"})
	}
	prefix := "p0ssess"
	if st.Mark != "" {
		prefix = st.Mark + " " + prefix
	}
	// Variants shrink the extras first (most detail first), then the main text.
	pick := make([]int, len(extras))
	build := func(m string) (string, []string) {
		parts := []string{m}
		var ex []string
		for i, e := range extras {
			if v := e[pick[i]]; v != "" {
				parts = append(parts, v)
				ex = append(ex, v)
			}
		}
		return strings.Join(parts, " · "), ex
	}
	fits := func(t string) bool { return room <= 0 || utf8.RuneCountInString(prefix+" "+t) <= room }
	text, mi := "", 0
	for {
		t, _ := build(main[mi])
		if fits(t) {
			text = t
			break
		}
		// Shrink the extra that still has the most detail, the first one on a tie.
		best := -1
		for i := range extras {
			if pick[i] < len(extras[i])-1 && (best < 0 || pick[i] < pick[best]) {
				best = i
			}
		}
		if best >= 0 {
			pick[best]++
			continue
		}
		if mi < len(main)-1 {
			mi++
			continue
		}
		text = t
		break
	}
	if !st.Color {
		return prefix + " " + text
	}
	textColor, markColor := colorIdle, colorIdle
	if running > 0 {
		textColor, markColor = colorRunning, colorRunning
	}
	if s.Offline {
		markColor = colorOffline
	}
	switch s.Level() {
	case Near:
		textColor, markColor = colorAmber, colorAmber
	case Stopped:
		textColor, markColor = colorRed, colorRed
	}
	mark := ""
	if st.Mark != "" {
		mark = markColor + st.Mark + reset + " "
	}
	body := "p0ssess " + text
	if s.Rescue && s.Level() != Stopped {
		if i := strings.LastIndex(body, " · rescue"); i >= 0 {
			j := i + len(" · rescue")
			body = body[:i+len(" · ")] + reset + colorViolet + "rescue" + reset + textColor + body[j:]
		}
	}
	if s.Stale != "" && textColor != colorAmber {
		// The restart marker is amber whatever the rest says.
		for _, v := range []string{" · restart to update", " · restart"} {
			if i := strings.LastIndex(body, v); i >= 0 {
				return mark + textColor + body[:i] + reset + colorAmber + v + reset
			}
		}
	}
	return mark + textColor + body + reset
}

// money is spend: $0.42, $0, <$0.01, $12.
func money(v float64) string {
	switch {
	case v <= 0:
		return "$0"
	case v < 0.005:
		return "<$0.01"
	case v >= 100:
		return "$" + strconv.Itoa(int(v+0.5))
	}
	return fmt.Sprintf("$%.2f", v)
}

// limit is a limit: $1, $10, $0.50.
func limit(v float64) string {
	if v >= 100 || v == float64(int(v)) {
		return "$" + strconv.Itoa(int(v+0.5))
	}
	return fmt.Sprintf("$%.2f", v)
}

func modelNames(js []usage.Job) string {
	seen := map[string]bool{}
	var names []string
	for _, j := range js {
		n := history.Short(j.Model)
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	return strings.Join(names, ", ")
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// VisibleLen counts runes after stripping ANSI escapes and OSC 8 links.
func VisibleLen(s string) int { return utf8.RuneCountInString(ansi.ReplaceAllString(s, "")) }

// Run handles one status line refresh. It never fails loudly: a broken
// p0ssess setup must not blank the user's status line.
func Run(input []byte, d paths.Dirs, now time.Time) string {
	st := LoadSettings(d.EngineFile())
	var in usage.StatusInput
	if json.Unmarshal(input, &in) == nil {
		usage.Update(d.UsageFile(), func(s *usage.State) { s.Record(in, now) })
	}
	user := runUser(st, input)
	width := st.Width
	if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && c > 0 {
		width = c
	}
	if width <= 0 {
		width = 120
	}
	lines := strings.Split(strings.TrimRight(user, "\n"), "\n")
	last := lines[len(lines)-1]
	room := width - VisibleLen(last) - 2
	if last == "" {
		room = width
	}
	seg := Segment(Gather(d, in.SessionID, now), st, room)
	if last == "" {
		lines[len(lines)-1] = seg
	} else {
		lines[len(lines)-1] = last + "  " + seg
	}
	return strings.Join(lines, "\n")
}

// cached is the part of the state that needs the ledger, kept between refreshes in the cache dir.
type cached struct {
	Key        string    `json:"key"`
	At         time.Time `json:"at"`
	SpentToday float64   `json:"spent_today"`
	SpentMonth float64   `json:"spent_month"`
	DailyLimit float64   `json:"daily_limit"`
	MonthlyCap float64   `json:"monthly_cap"`
	Team       bool      `json:"team,omitempty"`
	TeamSpent  float64   `json:"team_spent,omitempty"`
	TeamCap    float64   `json:"team_cap,omitempty"`
}

// cacheTTL bounds how long a reading stands when no file changed (open reservations expire with time).
const cacheTTL = 30 * time.Second

func cacheFile(d paths.Dirs) string { return filepath.Join(d.Cache, "statusline.json") }

// inputsKey changes whenever a file the spend reading depends on changes, or the day does.
func inputsKey(d paths.Dirs, now time.Time) string {
	var b strings.Builder
	b.WriteString(now.Format("2006-01-02"))
	for _, p := range append([]string{d.HistoryFile(), d.BudgetFile(), team.PolicyFile(d)}, account.Files(d)...) {
		if st, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "|%d.%d", st.Size(), st.ModTime().UnixNano())
		} else {
			b.WriteString("|-")
		}
	}
	return b.String()
}

// Gather reads the state. The ledger part comes from the cache while no input file changed; running
// jobs, the offline marker and the session's stale marker are read every time. No network.
func Gather(d paths.Dirs, session string, now time.Time) State {
	key := inputsKey(d, now)
	var c cached
	if b, err := os.ReadFile(cacheFile(d)); err != nil || json.Unmarshal(b, &c) != nil || c.Key != key ||
		now.Sub(c.At) > cacheTTL || now.Before(c.At) {
		c = read(d, now)
		c.Key, c.At = key, now
		if b, err := json.Marshal(c); err == nil {
			paths.WriteFileAtomic(cacheFile(d), b, 0o600)
		}
	}
	_, err := os.Stat(d.OfflineMarker())
	q, _, queued := cloudsandbox.Queued(d, now)
	return State{Queued: q, IsQueued: queued, SpentToday: c.SpentToday, DailyLimit: c.DailyLimit, SpentMonth: c.SpentMonth, MonthlyCap: c.MonthlyCap,
		Team: c.Team, TeamSpent: c.TeamSpent, TeamCap: c.TeamCap, Running: usage.Running(d.RunningDir(), now),
		Offline: err == nil, Stale: staleMark(d, session), Rescue: rescueOn(d, now)}
}

func read(d paths.Dirs, now time.Time) cached {
	b, _ := budget.ForInstall(d)
	es, _ := history.Read(d.HistoryFile())
	l := history.NewLedger(es)
	sp := l.Spend(now)
	c := cached{SpentToday: sp.Today + sp.Reserved, SpentMonth: sp.Month + sp.Reserved, MonthlyCap: b.MonthlyUSD}
	if dl := b.DailyLimit(sp.MonthBeforeToday, now); !math.IsInf(dl, 1) {
		c.DailyLimit = dl
	}
	if pol := team.Load(d, table.PinnedKeys()); pol != nil {
		if pol.MonthlyShareUSD != nil && *pol.MonthlyShareUSD < c.MonthlyCap {
			c.MonthlyCap = *pol.MonthlyShareUSD
		}
		if pol.TeamMonthlyUSD != nil {
			c.Team, c.TeamCap = true, *pol.TeamMonthlyUSD
			c.TeamSpent = pol.TeamSpent(now, l.SpendSince) + sp.Reserved
		}
	}
	return c
}

func rescueOn(d paths.Dirs, now time.Time) bool {
	_, on := rescue.Active(d, now)
	return on
}

// staleMark is the version named by the marker the prompt hook leaves when this session's server is
// older than the installed plugin.
func staleMark(d paths.Dirs, session string) string {
	if session == "" {
		return ""
	}
	ents, err := os.ReadDir(filepath.Dir(d.SessionRulesFile(session)))
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if v, ok := strings.CutPrefix(e.Name(), "stale-"); ok && v != "" {
			return v
		}
	}
	return ""
}

func runUser(st Settings, input []byte) string {
	if strings.TrimSpace(st.Command) == "" {
		return ""
	}
	timeout := time.Duration(st.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	name, args := proc.Shell(st.Command)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = append(os.Environ(), "P0SSESS_WRAPPED=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil && out.Len() == 0 {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ""
		}
	}
	return out.String()
}
