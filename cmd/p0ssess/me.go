package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"p0ssess/internal/config"
	"p0ssess/internal/notices"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/proc"
)

// pageState is what a running page server writes so the next /p0ssess-me reuses it.
type pageState struct {
	PID   int       `json:"pid"`
	URL   string    `json:"url"`
	Until time.Time `json:"until"`
}

func pageStateFile() string { return filepath.Join(paths.Default().Cache, "me.json") }

// meBrief prints the few-line summary /p0ssess:p0ssess-me shows, and makes sure the local page runs.
func meBrief(args []string) error {
	fs := flag.NewFlagSet("me", flag.ContinueOnError)
	fs.Bool("brief", true, "")
	noPage := fs.Bool("no-page", false, "do not start the local page")
	if err := fs.Parse(args); err != nil {
		return err
	}
	eng, err := local.Load(paths.Default())
	if err != nil {
		return err
	}
	st, err := eng.Status(local.Scope{})
	if err != nil {
		return err
	}
	url := ""
	if !*noPage {
		if url, err = ensurePage(); err != nil {
			url = "not started (" + err.Error() + ")"
		}
	}
	fmt.Print(brief(st, eng, url, setupGaps()))
	fmt.Print(outcomesNotice(eng))
	return nil
}

// outcomesNotice says once, the first time the summary runs with outcome records on, that they are sent
// and what one looks like (D13).
func outcomesNotice(eng *local.Engine) string {
	mark := filepath.Join(eng.Dirs.State, "outcomes-notice")
	if !eng.OutcomesEnabled() {
		return ""
	}
	if _, err := os.Stat(mark); err == nil {
		return ""
	}
	if os.MkdirAll(eng.Dirs.State, 0o700) != nil || os.WriteFile(mark, nil, 0o600) != nil {
		return ""
	}
	return "  records when a Claude Code session ends, p0ssess sends anonymous outcome records; the page shows each one first. To stop: \"turn off outcome records\"\n" +
		`          one record: {"v":1,"kind":"image.generate","model":"google/gemini-3.4-flash-image","route":"direct","stakes":2,"outcome":"accepted","cost":"<1c","week":"2026-W46"}` + "\n"
}

// brief renders the summary as a small card: spend against the envelope with meters, what this month
// saved, the last run, the limits, then the page and how to change things below the card.
func brief(st *local.Status, eng *local.Engine, url string, gaps []string) string {
	b, sp := st.Budget, st.Spend
	running := "nothing running"
	if n := len(st.Running); n > 0 {
		var ms []string
		for _, j := range st.Running {
			ms = append(ms, short(j.Model))
		}
		running = fmt.Sprintf("● %d running: %s", n, strings.Join(ms, ", "))
	}

	var rows []string
	head := "ᗣ  p0ssess " + version
	rows = append(rows, head+strings.Repeat(" ", max(2, cardWidth-4-runes(head)-runes(running)))+running, "")

	dayOf, dayFrac := "no daily limit", 0.0
	if st.DailyLimit > 0 {
		dayOf, dayFrac = "of "+money(st.DailyLimit), sp.Today/st.DailyLimit
		if b.DailyUSD == 0 {
			dayOf += " paced"
		}
	}
	rows = append(rows, meterRow("today", sp.Today, dayFrac, dayOf))
	monthFrac, monthOf := 0.0, "no monthly cap"
	if b.MonthlyUSD > 0 {
		monthFrac, monthOf = (sp.Month+sp.Reserved)/b.MonthlyUSD, "of "+money(b.MonthlyUSD)
		if st.NearBudget {
			monthOf += "  near the cap"
		}
	}
	rows = append(rows, meterRow("month", sp.Month+sp.Reserved, monthFrac, monthOf))

	saved, runs, last := monthReceipts(eng.Dirs.NoticesFile(), time.Now())
	if runs > 0 {
		line := fmt.Sprintf("%d this month", runs)
		if saved > 0 {
			line += " · ~" + money(saved) + " saved"
		}
		rows = append(rows, "runs    "+line)
	}
	if last != nil {
		rows = append(rows, fmt.Sprintf("last    %s · %s · %s", short(last.Model), smallMoney(last.CostUSD), ago(time.Since(last.TS))))
	}
	rows = append(rows, "")

	limits := []string{"asks above " + money(b.PerTaskUSD) + " a task", b.Privacy.Mode}
	if b.Ask.FirstUse {
		limits = append(limits, "asks on a model's first run")
	}
	if b.Ask.CodeEdits {
		limits = append(limits, "asks before edits")
	}
	rows = append(rows, "limits  "+strings.Join(limits, " · "))
	if us := eng.Usage(); us.RateLimits != nil && st.Lambda.Usage.Known {
		var ws []string
		if w := us.RateLimits.FiveHour; w != nil && w.UsedPercentage != nil {
			ws = append(ws, fmt.Sprintf("5-hour %.0f%%", *w.UsedPercentage))
		}
		if w := us.RateLimits.SevenDay; w != nil && w.UsedPercentage != nil {
			ws = append(ws, fmt.Sprintf("weekly %.0f%%", *w.UsedPercentage))
		}
		if len(ws) > 0 {
			rows = append(rows, "Claude  "+strings.Join(ws, " · "))
		}
	}

	var out strings.Builder
	out.WriteString("╭" + strings.Repeat("─", cardWidth-2) + "╮\n")
	for _, r := range rows {
		r = clip(r, cardWidth-4)
		out.WriteString("│ " + r + strings.Repeat(" ", cardWidth-4-runes(r)) + " │\n")
	}
	out.WriteString("╰" + strings.Repeat("─", cardWidth-2) + "╯\n")
	if url != "" {
		fmt.Fprintf(&out, "  page    %s\n", url)
	}
	fmt.Fprintf(&out, "  change  tell Claude: \"cap OpenRouter at $5\", \"always ask before grok\"\n")
	if len(gaps) > 0 {
		fmt.Fprintf(&out, "  setup   %s\n", strings.Join(gaps, " · "))
	}
	return out.String()
}

const cardWidth = 64

func meterRow(label string, v, frac float64, of string) string {
	return fmt.Sprintf("%-8s%-7s  %s  %s", label, money(v), bar(frac, 16), of)
}

func runes(s string) int { return utf8.RuneCountInString(s) }

func clip(s string, n int) string {
	if runes(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// monthReceipts sums this month's receipts from the notices file: the saving, the number of runs and
// the newest one.
func monthReceipts(path string, now time.Time) (saved float64, runs int, last *notices.Record) {
	for _, r := range notices.Recent(path, 0) {
		if r.Receipt == "" || r.TS.Year() != now.Year() || r.TS.Month() != now.Month() {
			continue
		}
		if last == nil {
			rec := r
			last = &rec
		}
		runs++
		saved += r.SavedUSD
	}
	return
}

func smallMoney(v float64) string {
	if v > 0 && v < 0.01 {
		return "<$0.01"
	}
	return money(v)
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

func bar(frac float64, n int) string {
	full := int(frac*float64(n) + 0.5)
	if frac > 0 && full == 0 {
		full = 1
	}
	full = min(max(full, 0), n)
	return strings.Repeat("▰", full) + strings.Repeat("▱", n-full)
}

func money(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("$%.0f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

func short(id string) string {
	if _, after, ok := strings.Cut(id, "/"); ok {
		return after
	}
	return id
}

// setupGaps lists what is missing for p0ssess to run quietly, each with the command that fixes it.
func setupGaps() []string {
	var gaps []string
	if cfg, err := config.Load(); err == nil {
		if st, err := os.Stat(cfg.OpenRouterKeyFile); err != nil || st.Size() == 0 {
			gaps = append(gaps, "no OpenRouter key (put it in "+cfg.OpenRouterKeyFile+", mode 0600)")
		}
	}
	o, err := readObject(settingsPath())
	if err != nil {
		return gaps
	}
	perms, _ := o.object("permissions")
	var allow []string
	if v, ok := perms.get("allow"); ok {
		json.Unmarshal(v, &allow)
	}
	missing := 0
	for _, t := range AllowTools {
		if !contains(allow, toolPrefix+t) && !contains(allow, strings.TrimSuffix(toolPrefix, "__")) {
			missing++
		}
	}
	if missing > 0 {
		gaps = append(gaps, fmt.Sprintf("%d tools still prompt (p0ssess setup allow)", missing))
	}
	if sl, _ := o.get("statusLine"); !strings.Contains(string(sl), "p0ssess") {
		gaps = append(gaps, "no status line segment (p0ssess setup statusline)")
	}
	return gaps
}

// ensurePage returns the URL of the running local page, starting one in the background when none is.
func ensurePage() (string, error) {
	f := pageStateFile()
	if b, err := os.ReadFile(f); err == nil {
		var ps pageState
		if json.Unmarshal(b, &ps) == nil && ps.Until.After(time.Now().Add(5*time.Minute)) && reachable(ps.URL) {
			return ps.URL, nil
		}
	}
	os.Remove(f)
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(exe, "me", "--for", "2h", "--state", f)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := proc.StartDetached(cmd); err != nil {
		return "", err
	}
	cmd.Process.Release()
	for i := 0; i < 100; i++ {
		time.Sleep(50 * time.Millisecond)
		if b, err := os.ReadFile(f); err == nil {
			var ps pageState
			if json.Unmarshal(b, &ps) == nil && ps.URL != "" {
				return ps.URL, nil
			}
		}
	}
	return "", fmt.Errorf("the page did not start")
}

func reachable(url string) bool {
	c := &http.Client{Timeout: time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
