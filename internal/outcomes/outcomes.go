// Package outcomes turns the local delegation ledger into anonymous weekly
// outcome records and sends them to the server once per ISO week.
//
// A record carries eight coarse fields and nothing else from the ledger: no
// session, project, run id, prompt, reason or exact cost.
package outcomes

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
)

const (
	// MaxRecords is the most records one batch holds.
	MaxRecords = 2000
	// MaxWeeks is how many weeks back a record may be.
	MaxWeeks = 8
)

// Record is one anonymous outcome record, exactly as it goes on the wire.
type Record struct {
	V       int    `json:"v"`
	Kind    string `json:"kind"`
	Model   string `json:"model"`
	Route   string `json:"route"`
	Stakes  int    `json:"stakes"`
	Outcome string `json:"outcome"`
	Cost    string `json:"cost"`
	Week    string `json:"week"`
}

var (
	kindRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	modelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}/[a-z0-9][a-z0-9._:-]{0,79}$`)
)

// KindOf writes a catalog use-case id as a record kind: the first hyphen
// becomes a dot. It returns "" when there is no use case.
func KindOf(useCase string) string {
	return strings.Replace(useCase, "-", ".", 1)
}

// Bucket names the range a run's cash cost falls in.
func Bucket(usd float64) string {
	switch {
	case !(usd > 0):
		return "0"
	case usd < 0.01:
		return "<1c"
	case usd < 0.05:
		return "1-5c"
	case usd < 0.25:
		return "5-25c"
	case usd <= 1:
		return "25c-1"
	}
	return ">1"
}

// Week is the ISO week of t in UTC, such as "2026-W46".
func Week(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

func validRoute(r string) bool {
	switch r {
	case "direct", "agent", "claude-subagent", "engine":
		return true
	}
	return false
}

// Build derives the records for finished weeks from ledger entries. A
// delegated run is accepted unless an outcome naming it says otherwise; a
// declined or overridden outcome with no run is recorded on its own. Only
// weeks before the one containing the given time, at most MaxWeeks back and
// after afterWeek, are included, in ascending week order.
func Build(entries []history.Entry, now time.Time, afterWeek string) []Record {
	cur := Week(now)
	earliest := Week(now.AddDate(0, 0, -7*MaxWeeks))
	inWindow := func(w string) bool { return w < cur && w >= earliest && w > afterWeek }

	l := history.NewLedger(entries)
	runIDs := map[string]bool{}
	byRun := map[string]history.Entry{}
	bySession := map[string]history.Entry{}
	for _, e := range entries {
		switch e.Type {
		case history.TypeRun:
			if e.RunID != "" {
				runIDs[e.RunID] = true
			}
		case history.TypeOutcome:
			if e.RunID != "" {
				// A later outcome replaces an earlier one for the same run.
				byRun[e.RunID] = e
				bySession[session(e.RunID)] = e
			}
		}
	}

	var out []Record
	add := func(ts time.Time, useCase string, stakes int, model, route, outcome string, cost float64) {
		week := Week(ts)
		kind := KindOf(useCase)
		if ts.IsZero() || !inWindow(week) || !kindRE.MatchString(kind) || stakes < 1 || stakes > 4 ||
			!modelRE.MatchString(model) || !validRoute(route) {
			return
		}
		out = append(out, Record{V: 1, Kind: kind, Model: model, Route: route, Stakes: stakes,
			Outcome: outcome, Cost: Bucket(cost), Week: week})
	}

	for _, e := range entries {
		switch e.Type {
		case history.TypeRun:
			off := l.Offer(e.OfferID)
			outcome, explicit := history.Accepted, false
			if o, ok := byRun[e.RunID]; ok && e.RunID != "" {
				outcome, explicit = o.Outcome, true
			} else if o, ok := bySession[session(e.RunID)]; ok && e.RunID != "" {
				outcome, explicit = o.Outcome, true
			}
			if !explicit && e.Status != "done" && e.Status != "" {
				continue
			}
			useCase, stakes := e.UseCase, e.Stakes
			if off != nil {
				useCase, stakes = cmp.Or(useCase, off.UseCase), cmp.Or(stakes, off.Stakes)
			}
			add(e.TS, useCase, stakes, e.Model, cmp.Or(e.Route, optionRoute(off, e.Model, "")), outcome, l.Cost(e))
		case history.TypeOutcome:
			if e.Outcome != history.Declined && e.Outcome != history.Overridden {
				continue
			}
			if e.RunID != "" && (runIDs[e.RunID] || runsInSession(runIDs, session(e.RunID))) {
				continue
			}
			off := l.Offer(e.OfferID)
			useCase, stakes := e.UseCase, e.Stakes
			if off != nil {
				useCase, stakes = cmp.Or(useCase, off.UseCase), cmp.Or(stakes, off.Stakes)
			}
			add(e.TS, useCase, stakes, e.Model, cmp.Or(e.Route, optionRoute(off, e.Model, e.Chosen)), e.Outcome, e.CashUSD)
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Week < out[j].Week })
	if len(out) > MaxRecords {
		out = out[len(out)-MaxRecords:]
	}
	return out
}

func session(runID string) string {
	s, _, _ := strings.Cut(runID, "/")
	return s
}

// runsInSession reports whether an outcome naming a bare relay session
// covers a run, as the ledger does.
func runsInSession(runIDs map[string]bool, sess string) bool {
	for id := range runIDs {
		if session(id) == sess {
			return true
		}
	}
	return false
}

// optionRoute finds the route an offer gave a model, trying model first and
// then the option the user chose.
func optionRoute(off *history.Entry, model, chosen string) string {
	if off == nil {
		return ""
	}
	for _, want := range []string{model, chosen} {
		for _, o := range off.Options {
			if want != "" && o.Model == want {
				return o.Route
			}
		}
	}
	return ""
}

// state is what the client remembers between weekly sends.
type state struct {
	LastSentWeek string `json:"last_sent_week"`
	LastTryWeek  string `json:"last_try_week"`
}

func stateFile(d paths.Dirs) string { return filepath.Join(d.State, "outcomes.json") }

func readState(d paths.Dirs) state {
	var st state
	b, err := os.ReadFile(stateFile(d))
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func writeState(d paths.Dirs, st state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(stateFile(d), append(b, '\n'), 0o600)
}

// Pending is the batch the next send would carry.
func Pending(d paths.Dirs, now time.Time) ([]Record, error) {
	es, err := history.Read(d.HistoryFile())
	if err != nil {
		return nil, err
	}
	return Build(es, now, readState(d).LastSentWeek), nil
}

// marshal encodes v without HTML escaping, so "<1c" stays readable.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Lines renders each record as one compact JSON line.
func Lines(recs []Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		b, err := marshal(r)
		if err != nil {
			continue
		}
		out = append(out, string(b))
	}
	return out
}

// Poster sends a request body to the server's outcomes endpoint and returns
// the HTTP status.
type Poster func(ctx context.Context, body []byte) (status int, err error)

// MaybeSend sends the pending batch at most once per ISO week. Nothing is
// read or written when enabled is false. An accepted batch, or one the
// server refuses as invalid or too large, moves last_sent_week forward and
// is never resent; a rate limit, server error or network failure leaves
// the state alone so the next start tries again. It returns how many records
// were sent.
func MaybeSend(ctx context.Context, d paths.Dirs, enabled bool, post Poster, now time.Time) (sent int, err error) {
	if !enabled {
		return 0, nil
	}
	week := Week(now)
	st := readState(d)
	if st.LastTryWeek == week {
		return 0, nil
	}
	recs, err := Pending(d, now)
	if err != nil {
		return 0, err
	}
	if len(recs) == 0 {
		st.LastTryWeek = week
		return 0, writeState(d, st)
	}
	body, err := marshal(struct {
		V       int      `json:"v"`
		Records []Record `json:"records"`
	}{1, recs})
	if err != nil {
		return 0, err
	}
	status, err := post(ctx, body)
	if err != nil {
		return 0, err
	}
	newest := recs[len(recs)-1].Week
	switch {
	case status >= 200 && status < 300:
		sent = len(recs)
	case status == 400 || status == 413:
		// The server refused the batch whole; it is dropped.
	default:
		return 0, errors.New("outcomes: server answered " + fmt.Sprint(status))
	}
	st.LastSentWeek, st.LastTryWeek = newest, week
	return sent, writeState(d, st)
}
