package savings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"p0ssess/internal/notices"
	"p0ssess/internal/pme/paths"
)

// The monthly report is the one thing a Pro or Team install uploads about savings: last month's totals,
// so the server can email the summary. It holds figures only, never a prompt, a file, a path or task
// text; a model id and a kind of work are the only labels.

// ReportV is the report's version on the wire.
const ReportV = 1

// MaxLabels is how many models and kinds a report lists.
const MaxLabels = 8

// Label is one model or kind in a report.
type Label struct {
	Name     string  `json:"name"`
	Runs     int     `json:"runs"`
	SavedUSD float64 `json:"saved_usd"`
}

// Monthly is the request body of POST /v1/savings/month, field for field.
type Monthly struct {
	V                 int     `json:"v"`
	Month             string  `json:"month"`
	Runs              int     `json:"runs"`
	PricedRuns        int     `json:"priced_runs"`
	MeasuredRuns      int     `json:"measured_runs"`
	CostUSD           float64 `json:"cost_usd"`
	ClaudeUSD         float64 `json:"claude_usd"`
	SavedUSD          float64 `json:"saved_usd"`
	ClaudeCheaperRuns int     `json:"claude_cheaper_runs"`
	RescueRuns        int     `json:"rescue_runs"`
	RescueSavedUSD    float64 `json:"rescue_saved_usd"`
	TokensKept        int64   `json:"tokens_kept"`
	Models            []Label `json:"models"`
	Kinds             []Label `json:"kinds"`
}

// MonthlyOf builds the report for a month's roll-up. CostUSD is the cost of the priced runs, so it sits
// beside ClaudeUSD and SavedUSD on the same footing.
func MonthlyOf(p Period) Monthly {
	m := Monthly{V: ReportV, Month: p.Label, Runs: p.Runs, PricedRuns: p.Priced, MeasuredRuns: p.Measured,
		CostUSD: round(p.PricedCostUSD), ClaudeUSD: round(p.ClaudeUSD), SavedUSD: round(p.SavedUSD), ClaudeCheaperRuns: p.ClaudeCheaper,
		RescueRuns: p.Rescue.Runs, RescueSavedUSD: round(p.Rescue.SavedUSD), TokensKept: p.TokensKept,
		Models: labels(p.ByModel), Kinds: labels(p.ByKind)}
	return m
}

func labels(rs []Row) []Label {
	out := []Label{}
	for i, r := range rs {
		if i == MaxLabels {
			break
		}
		out = append(out, Label{Name: r.Key, Runs: r.Runs, SavedUSD: round(r.SavedUSD)})
	}
	return out
}

func round(v float64) float64 {
	if v < 0 {
		return -round(-v)
	}
	return float64(int64(v*1e6+0.5)) / 1e6
}

// Body is the report exactly as it is sent.
func (m Monthly) Body() string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(m)
	return string(bytes.TrimRight(buf.Bytes(), "\n"))
}

func sentFile(d paths.Dirs) string { return filepath.Join(d.State, "savings-report.json") }

type sentState struct {
	Sent string `json:"sent"` // the last month sent or skipped for having nothing to say
}

func readSent(d paths.Dirs) string {
	b, err := os.ReadFile(sentFile(d))
	if err != nil {
		return ""
	}
	var s sentState
	_ = json.Unmarshal(b, &s)
	return s.Sent
}

func markSent(d paths.Dirs, month string) {
	b, _ := json.Marshal(sentState{Sent: month})
	_ = paths.WriteFileAtomic(sentFile(d), append(b, '\n'), 0o600)
}

// Last is the month before now's.
func Last(now time.Time) string {
	loc := now.Location()
	n := now.In(loc)
	return Month(time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 0, -1), loc)
}

// Pending is the report still to go: last month's, unless it was sent or had no runs.
func Pending(d paths.Dirs, recs []notices.Record, now time.Time) (Monthly, bool) {
	last := Last(now)
	if readSent(d) >= last {
		return Monthly{}, false
	}
	p := Roll(recs, last, now.Location())
	if p.Runs == 0 {
		return Monthly{}, false
	}
	return MonthlyOf(p), true
}

// Poster sends a body and returns the HTTP status.
type Poster func(ctx context.Context, body []byte) (int, error)

// Send uploads last month's report once, when the install is linked to Pro or Team and has not turned
// the report off. A report the server refuses outright (400, 403, 413) is not retried; anything else is
// tried again next time. It returns whether a report went.
func Send(ctx context.Context, d paths.Dirs, recs []notices.Record, enabled, pro, linked bool, post Poster, now time.Time) (bool, error) {
	if !enabled || !pro || !linked {
		return false, nil
	}
	last := Last(now)
	if readSent(d) >= last {
		return false, nil
	}
	m, ok := Pending(d, recs, now)
	if !ok {
		markSent(d, last)
		return false, nil
	}
	status, err := post(ctx, []byte(m.Body()))
	switch {
	case status >= 200 && status < 300:
		markSent(d, last)
		return true, nil
	case status == 400 || status == 403 || status == 413:
		markSent(d, last)
		return false, nil
	}
	if err == nil {
		err = fmt.Errorf("savings report: server answered %d", status)
	}
	return false, err
}
