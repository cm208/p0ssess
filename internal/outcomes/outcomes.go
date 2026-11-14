// Package outcomes turns the local delegation ledger into anonymous outcome
// records and sends the ones not yet sent when a Claude Code session ends
// (and, for any that missed a session end, at the next plugin start).
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
	"strconv"
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

// HoldMax is how long a run held for its outcome waits. A run not rated by then is never recorded.
const HoldMax = 30 * 24 * time.Hour

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

// The checks the server makes (DecodeBatch); a record that fails one is never built, since one bad
// record refuses the whole batch.
var (
	kindRE  = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9-]+)?$`)
	modelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}/[a-z0-9][a-z0-9._:-]{0,79}$`)
)

const (
	maxKind  = 64
	maxModel = 100
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

// Build derives the records for ledger entries newer than after. A delegated
// run is accepted unless an outcome naming it says otherwise; a declined or
// overridden outcome with no run is recorded on its own. A run held for its
// outcome (AwaitOutcome) is left out until an outcome names it, and is then
// due as of that first outcome, so it goes out with the next send even when
// earlier sends moved past the run; one not rated within HoldMax is never
// recorded. Only entries from the current ISO week or at most MaxWeeks
// before it are included, in ascending week order. Calibration results
// (route "calibrate") are tests graded by p0ssess, not outcomes, and are
// never recorded.
func Build(entries []history.Entry, now time.Time, after time.Time) []Record {
	cur := Week(now)
	earliest := Week(now.AddDate(0, 0, -7*MaxWeeks))

	// NewLedger also gives 1.0.0's declined and overridden outcomes, written without a run id, the run
	// of their offer.
	l := history.NewLedger(entries)
	entries = l.Entries
	runIDs := map[string]bool{}
	reserves := map[string]history.Entry{}
	// latest is the newest outcome naming a run id or a bare relay session; first is when the first
	// one was recorded.
	latest := map[string]history.Entry{}
	first := map[string]time.Time{}
	for _, e := range entries {
		switch e.Type {
		case history.TypeRun:
			if e.RunID != "" {
				runIDs[e.RunID] = true
			}
		case history.TypeReserve:
			if e.RunID != "" {
				reserves[e.RunID] = e
			}
		case history.TypeOutcome:
			if e.RunID != "" && e.Route != history.RouteCalibrate {
				latest[e.RunID] = e
				if _, ok := first[e.RunID]; !ok {
					first[e.RunID] = e.TS
				}
			}
		}
	}
	// outcomeFor finds the outcome for a run: one naming the run itself, else one naming its whole
	// relay session.
	outcomeFor := func(id string) (history.Entry, time.Time, bool) {
		if id == "" {
			return history.Entry{}, time.Time{}, false
		}
		if o, ok := latest[id]; ok {
			return o, first[id], true
		}
		if s := session(id); s != id {
			if o, ok := latest[s]; ok {
				return o, first[s], true
			}
		}
		return history.Entry{}, time.Time{}, false
	}

	var out []Record
	// add records one entry. ts is when it happened (its week); due is when it became sendable.
	add := func(ts, due time.Time, useCase string, stakes int, model, route, outcome string, cost float64) {
		week := Week(ts)
		kind := KindOf(useCase)
		if ts.IsZero() || !due.After(after) || week > cur || week < earliest || route == history.RouteCalibrate ||
			len(kind) > maxKind || !kindRE.MatchString(kind) || stakes < 1 || stakes > 4 ||
			len(model) > maxModel || !modelRE.MatchString(model) || !validRoute(route) {
			return
		}
		out = append(out, Record{V: 1, Kind: kind, Model: model, Route: route, Stakes: stakes,
			Outcome: outcome, Cost: Bucket(cost), Week: week})
	}

	for _, e := range entries {
		switch e.Type {
		case history.TypeRun:
			off := l.Offer(e.OfferID)
			outcome := history.Accepted
			o, firstTS, explicit := outcomeFor(e.RunID)
			if explicit {
				outcome = o.Outcome
			}
			if !explicit && e.Status != "done" && e.Status != "" {
				continue
			}
			due := e.TS
			if e.AwaitOutcome {
				if !explicit || firstTS.Sub(e.TS) > HoldMax {
					continue
				}
				if firstTS.After(due) {
					due = firstTS
				}
			}
			useCase, stakes := e.UseCase, e.Stakes
			if off != nil {
				useCase, stakes = cmp.Or(useCase, off.UseCase), cmp.Or(stakes, off.Stakes)
			}
			// Runs before 1.0.1 carry no stakes; their reservation has them.
			if r, ok := reserves[e.Reserve]; ok && e.Reserve != "" {
				useCase, stakes = cmp.Or(useCase, r.UseCase), cmp.Or(stakes, r.Stakes)
			}
			add(e.TS, due, useCase, stakes, e.Model, cmp.Or(e.Route, optionRoute(off, e.Model, "")), outcome, l.Cost(e))
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
			add(e.TS, e.TS, useCase, stakes, e.Model, cmp.Or(e.Route, optionRoute(off, e.Model, e.Chosen)), e.Outcome, e.CashUSD)
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

// state is what the client remembers between sends: the newest ledger entry
// already covered by a send, and the last refusal. It stays on this machine.
type state struct {
	SentThrough time.Time `json:"sent_through"`
	Refused     *refusal  `json:"refused,omitempty"`
}

// refusal is a batch the server refused whole and the client kept.
type refusal struct {
	At      time.Time `json:"at"`
	Status  int       `json:"status"`
	Message string    `json:"message,omitempty"`
	Records int       `json:"records"`
}

// MaxBody is the most bytes the server takes in one batch.
const MaxBody = 256 << 10

// maxSetAside bounds how many records one send sets aside before it gives up and keeps the batch.
const maxSetAside = 20

func refusedFile(d paths.Dirs) string { return filepath.Join(d.State, "outcomes-refused.jsonl") }

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
	return Build(es, now, readState(d).SentThrough), nil
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

// newest is the latest timestamp in the ledger.
func newest(es []history.Entry) time.Time {
	var t time.Time
	for _, e := range es {
		if e.TS.After(t) {
			t = e.TS
		}
	}
	return t
}

// Send sends whatever is pending. Nothing is read or written when enabled is
// false, and nothing is sent when nothing is pending. Only an accepted batch
// moves the sent mark forward. When the server refuses a batch as invalid and
// names the record (400 "record N: ..."), that record is set aside in
// outcomes-refused.jsonl in the state directory and the rest is sent again at
// once (refusals do not count against the upload limits). Any other refusal
// (400 without a record, 413, other 4xx) keeps the batch, notes the refusal
// in the state file and returns an error, as a rate limit, server error or
// network failure does: the next session end or plugin start tries again. A
// batch larger than MaxBody drops its oldest records first. A lock on the
// state keeps two processes from sending the same records. It returns how
// many records were sent.
func Send(ctx context.Context, d paths.Dirs, enabled bool, post Poster, now time.Time) (sent int, err error) {
	if !enabled {
		return 0, nil
	}
	if err := os.MkdirAll(d.State, 0o700); err != nil {
		return 0, err
	}
	unlock, err := paths.LockFile(stateFile(d) + ".lock")
	if err != nil {
		return 0, err
	}
	defer unlock()

	es, err := history.Read(d.HistoryFile())
	if err != nil {
		return 0, err
	}
	st := readState(d)
	recs := Build(es, now, st.SentThrough)
	if len(recs) == 0 {
		return 0, nil
	}
	recs = fit(recs)
	for setAside := 0; ; {
		body, err := batch(recs)
		if err != nil {
			return 0, err
		}
		status, perr := post(ctx, body)
		switch {
		case status >= 200 && status < 300:
			st.SentThrough, st.Refused = newest(es), nil
			return len(recs), writeState(d, st)
		case status == 400 && setAside < maxSetAside:
			if i, ok := refusedIndex(perr); ok && i < len(recs) {
				if err := setAsideRecord(d, recs[i], status, perr, now); err != nil {
					return 0, err
				}
				setAside++
				recs = append(recs[:i:i], recs[i+1:]...)
				if len(recs) == 0 {
					st.SentThrough, st.Refused = newest(es), nil
					return 0, writeState(d, st)
				}
				continue
			}
		}
		if status == 0 && perr != nil {
			return 0, perr
		}
		if status >= 400 && status < 500 && status != 429 {
			msg := ""
			if perr != nil {
				msg = perr.Error()
			}
			st.Refused = &refusal{At: now.UTC(), Status: status, Message: msg, Records: len(recs)}
			if err := writeState(d, st); err != nil {
				return 0, err
			}
			return 0, fmt.Errorf("outcomes: server refused the batch (%s); it is kept for the next send", strings.TrimSpace(fmt.Sprint(status, " ", msg)))
		}
		return 0, errors.New("outcomes: server answered " + fmt.Sprint(status))
	}
}

func batch(recs []Record) ([]byte, error) {
	return marshal(struct {
		V       int      `json:"v"`
		Records []Record `json:"records"`
	}{1, recs})
}

// fit drops the oldest records until the batch fits in MaxBody.
func fit(recs []Record) []Record {
	for len(recs) > 0 {
		b, err := batch(recs)
		if err != nil || len(b) <= MaxBody {
			return recs
		}
		over := (len(b)-MaxBody)*len(recs)/len(b) + 1
		recs = recs[min(over, len(recs)):]
	}
	return recs
}

var recordIndexRE = regexp.MustCompile(`(?i)\brecord (\d+)\b`)

// refusedIndex reads the index of the refused record from the server's message.
func refusedIndex(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	m := recordIndexRE.FindStringSubmatch(err.Error())
	if m == nil {
		return 0, false
	}
	i, convErr := strconv.Atoi(m[1])
	return i, convErr == nil
}

// setAsideRecord appends a refused record, with the server's reason, to outcomes-refused.jsonl.
func setAsideRecord(d paths.Dirs, r Record, status int, reason error, now time.Time) error {
	line := struct {
		At     time.Time `json:"at"`
		Status int       `json:"status"`
		Reason string    `json:"reason,omitempty"`
		Record Record    `json:"record"`
	}{At: now.UTC(), Status: status, Record: r}
	if reason != nil {
		line.Reason = reason.Error()
	}
	b, err := marshal(line)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(refusedFile(d), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}
