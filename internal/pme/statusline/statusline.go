// Package statusline wraps the user's Claude Code status line. It saves the
// rate-limit reading Claude Code passes on stdin, runs the user's own status
// line command with the same input, and appends a quiet p0ssess segment.
package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"p0ssess/internal/budget"
	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
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

// Colors, in 256-color codes. Never red, never blinking.
const (
	colorIdle    = "\x1b[38;5;244m"
	colorRunning = "\x1b[38;5;250m"
	colorAmber   = "\x1b[38;5;214m"
	reset        = "\x1b[0m"
)

// State is what the segment shows.
type State struct {
	SpentToday float64
	SpentMonth float64
	MonthlyCap float64
	Running    []usage.Job
}

// Segment renders the p0ssess part for a given width budget. It returns ""
// when idle with nothing spent today.
func Segment(s State, st Settings, room int) string {
	near := s.MonthlyCap > 0 && s.SpentMonth >= 0.8*s.MonthlyCap
	running := len(s.Running)
	if running == 0 && s.SpentToday <= 0 && !near {
		return ""
	}
	var variants []string
	switch {
	case near:
		variants = []string{fmt.Sprintf("%s of %s this month", money(s.SpentMonth), money(s.MonthlyCap)), money(s.SpentMonth)}
		if running > 0 {
			variants = append([]string{fmt.Sprintf("%d running  %s of %s this month", running, money(s.SpentMonth), money(s.MonthlyCap))}, variants...)
		}
	case running > 0:
		models := modelNames(s.Running)
		variants = []string{
			fmt.Sprintf("%d running  %s today  %s", running, money(s.SpentToday), models),
			fmt.Sprintf("%d running  %s today", running, money(s.SpentToday)),
			fmt.Sprintf("%d running  %s", running, money(s.SpentToday)),
			fmt.Sprintf("%d running", running),
		}
	default:
		variants = []string{money(s.SpentToday) + " today", money(s.SpentToday)}
	}
	prefix := "p0ssess"
	if st.Mark != "" {
		prefix = st.Mark + " " + prefix
	}
	text := variants[len(variants)-1]
	for _, v := range variants {
		if room <= 0 || utf8.RuneCountInString(prefix+"  "+v) <= room {
			text = v
			break
		}
	}
	if !st.Color {
		return prefix + "  " + text
	}
	textColor, markColor := colorIdle, colorIdle
	if running > 0 {
		textColor, markColor = colorRunning, colorRunning
	}
	if near {
		markColor = colorAmber
	}
	mark := ""
	if st.Mark != "" {
		mark = markColor + st.Mark + reset + " "
	}
	return mark + textColor + "p0ssess  " + text + reset
}

func money(v float64) string {
	if v > 0 && v < 0.005 {
		return "<$0.01"
	}
	if v >= 100 || v == float64(int(v)) && v >= 1 {
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
	seg := Segment(gather(d, now), st, room)
	if seg == "" {
		return strings.TrimRight(user, "\n")
	}
	if last == "" {
		lines[len(lines)-1] = seg
	} else {
		lines[len(lines)-1] = last + "  " + seg
	}
	return strings.Join(lines, "\n")
}

func gather(d paths.Dirs, now time.Time) State {
	b, _ := budget.Load(d.BudgetFile())
	es, _ := history.Read(d.HistoryFile())
	sp := history.NewLedger(es).Spend(now)
	return State{SpentToday: sp.Today, SpentMonth: sp.Month, MonthlyCap: b.MonthlyUSD, Running: usage.Running(d.RunningDir(), now)}
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
	cmd := exec.CommandContext(ctx, "sh", "-c", st.Command)
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
