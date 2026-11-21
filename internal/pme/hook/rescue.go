package hook

import (
	"fmt"
	"math"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/budget"
	"p0ssess/internal/history"
	"p0ssess/internal/notices"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/rescue"
	"p0ssess/internal/usage"
)

// criticalIDs are the catalog use cases whose work is stakes 4: while rescue is on the user is asked
// once per task before p0ssess takes them.
var criticalIDs = []string{"security-critical-code", "prod-db-migration", "iac-firewall-change"}

func isCritical(id string) bool {
	for _, c := range criticalIDs {
		if c == id {
			return true
		}
	}
	return false
}

// routable reports whether a prompt looks like work p0ssess could take: media, bulk, or a catalog use
// case the engine does not keep on Claude. A prompt that keeps itself on Claude is not.
func routable(p string) bool {
	if keepRe.MatchString(p) {
		return false
	}
	if len(p) >= BulkChars || bulkRe.MatchString(p) || videoRe.MatchString(p) || videoAskRe.MatchString(p) ||
		audioRe.MatchString(p) || audioAskRe.MatchString(p) || genRe.MatchString(p) {
		return true
	}
	for _, w := range matchWork(p) {
		if !w.keep {
			return true
		}
	}
	return false
}

// envelopeRoom reports whether the spending envelope can still take paid runs: the monthly cap and the
// daily limit are not used up.
func envelopeRoom(d paths.Dirs, b budget.Budget, now time.Time) bool {
	es, _ := history.Read(d.HistoryFile())
	sp := history.NewLedger(es).Spend(now)
	if b.MonthlyUSD > 0 && sp.Month+sp.Reserved >= b.MonthlyUSD-1e-9 {
		return false
	}
	if dl := b.DailyLimit(sp.MonthBeforeToday, now); !math.IsInf(dl, 1) && dl > 0 && sp.Today+sp.Reserved >= dl-1e-9 {
		return false
	}
	return true
}

// rescueTick moves rescue's state for this prompt and returns the line to tell the user, or "".
func rescueTick(d paths.Dirs, prompt string, now time.Time) string {
	st, err := usage.Load(d.UsageFile())
	if err != nil || st.RateLimits == nil {
		return ""
	}
	b, err := budget.ForInstall(d)
	if err != nil {
		return ""
	}
	return rescue.Tick(rescue.Input{
		D: d, Now: now, Usage: st, Pro: account.Current(d).Pro(), Enabled: b.Rescue.Enabled, Ready: keyReady(),
		Threshold: b.Rescue.Level(), Room: envelopeRoom(d, b, now), Routable: routable(prompt),
		Runs: func(since time.Time) int {
			es, _ := history.Read(d.HistoryFile())
			n := 0
			for _, e := range es {
				if e.Type == history.TypeRun && e.TS.After(since) {
					n++
				}
			}
			return n
		},
		SavedEach: func() float64 { return avgSaved(d) },
	})
}

// avgSaved is the average saving of the user's past receipts that saved something, or 0.
func avgSaved(d paths.Dirs) float64 {
	var sum float64
	n := 0
	for _, r := range notices.Recent(d.NoticesFile(), 200) {
		if r.Receipt != "" && r.SavedUSD > 0 {
			sum += r.SavedUSD
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// rescueHint is the hint while rescue is on: routable work goes to the p0ssess agent now; critical
// work is offered once per task, with the model and cost.
func rescueHint(p string) string {
	for _, w := range matchWork(p) {
		if isCritical(w.id) {
			return fmt.Sprintf(`p0ssess rescue is on (Claude's limit is near), and this is %s: critical work (stakes 4). `+
				`Ask the user once, for this task only: call recommend with use case %s and stakes 4, then say in one line which model would do it and what it would cost, and ask whether p0ssess should take it. `+
				`On a yes, hand it to the p0ssess agent (Agent tool, subagent_type "p0ssess:p0ssess") with "stakes 4"; on a no, or if no route comes back, do it yourself.`, w.label, w.id)
		}
	}
	for _, w := range matchWork(p) {
		if w.keep {
			continue
		}
		edit := ""
		if w.kind == "code" || w.kind == "agent" {
			edit = ` ("edit mode" when files should change)`
		}
		return fmt.Sprintf(`p0ssess rescue is on (Claude's limit is near): this is %s, which p0ssess takes now inside the user's limits. `+
			`Hand it to the p0ssess agent now (Agent tool, subagent_type "p0ssess:p0ssess"): one sentence with the request and absolute paths, plus "use case %s"%s. `+
			`Do not do it yourself, ask first, or price it. If the user said to keep it on Claude, or the request is about this conversation, ignore this line. `+
			`If p0ssess finds no model that fits or returns an error, do the work yourself and say so in one line. Write nothing while it runs; when it returns, give its answer and its receipt line.`, w.label, w.id, edit)
	}
	return ""
}
