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

	"p0ssess/internal/config"
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
	return "records  when a Claude Code session ends, p0ssess sends anonymous outcome records; the page shows each one first. To stop: \"turn off outcome records\"\n" +
		`         one record: {"v":1,"kind":"image.generate","model":"google/gemini-3.4-flash-image","route":"direct","stakes":2,"outcome":"accepted","cost":"<1c","week":"2026-W46"}` + "\n"
}

// brief renders the summary: spend against the cap, what runs, the limits, and how to change them.
func brief(st *local.Status, eng *local.Engine, url string, gaps []string) string {
	b, sp := st.Budget, st.Spend
	var out strings.Builder
	frac := 0.0
	if b.MonthlyUSD > 0 {
		frac = (sp.Month + sp.Reserved) / b.MonthlyUSD
	}
	running := "nothing running"
	if n := len(st.Running); n > 0 {
		var ms []string
		for _, j := range st.Running {
			ms = append(ms, short(j.Model))
		}
		running = fmt.Sprintf("%d running: %s", n, strings.Join(ms, ", "))
	}
	near := ""
	if st.NearBudget {
		near = "  near the cap"
	}
	fmt.Fprintf(&out, "ᗣ p0ssess %s   today %s   month %s of %s %s%s   %s\n",
		version, money(sp.Today), money(sp.Month+sp.Reserved), money(b.MonthlyUSD), bar(frac, 10), near, running)

	daily := "no daily limit"
	switch {
	case b.DailyUSD > 0:
		daily = money(st.DailyLimit) + " a day"
	case b.DailyUSD == 0:
		daily = money(st.DailyLimit) + " today (paced)"
	}
	limits := []string{"asks above " + money(b.PerTaskUSD) + " a task", daily, b.Privacy.Mode}
	if b.Ask.FirstUse {
		limits = append(limits, "asks on a model's first run")
	}
	if b.Ask.CodeEdits {
		limits = append(limits, "asks before edits")
	}
	if us := eng.Usage(); us.RateLimits != nil && st.Lambda.Usage.Known {
		var ws []string
		if w := us.RateLimits.FiveHour; w != nil && w.UsedPercentage != nil {
			ws = append(ws, fmt.Sprintf("5-hour %.0f%%", *w.UsedPercentage))
		}
		if w := us.RateLimits.SevenDay; w != nil && w.UsedPercentage != nil {
			ws = append(ws, fmt.Sprintf("weekly %.0f%%", *w.UsedPercentage))
		}
		if len(ws) > 0 {
			limits = append(limits, "Claude "+strings.Join(ws, ", "))
		}
	}
	fmt.Fprintf(&out, "limits   %s\n", strings.Join(limits, " · "))
	change := `tell Claude ("cap OpenRouter at $5", "always ask before grok")`
	if url != "" {
		change += " · page " + url
	}
	fmt.Fprintf(&out, "change   %s\n", change)
	if len(gaps) > 0 {
		fmt.Fprintf(&out, "setup    %s\n", strings.Join(gaps, " · "))
	}
	return out.String()
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
