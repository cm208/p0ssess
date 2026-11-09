// Package history is the local delegation ledger: offers, choices, costs and
// outcomes, one JSON object per line. It never stores prompt content.
package history

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"p0ssess/internal/rules"
)

const (
	TypeOffer   = "offer"
	TypeOutcome = "outcome"
	// TypeRun is one finished delegated call with its measured cost: the
	// relay's session cost or the direct path's OpenRouter plus upstream
	// (bring-your-own-key) charge. Spend is the sum of these.
	TypeRun = "run"

	Accepted   = "accepted"   // the delegated result was used
	Redo       = "redo"       // Claude had to redo the work
	Overridden = "overridden" // Claude or the user picked another route
	Declined   = "declined"   // the user said no to the offer
)

type OfferOption struct {
	Model   string  `json:"model"`
	Route   string  `json:"route"`
	Total   float64 `json:"total_usd"`
	Cash    float64 `json:"cash_usd"`
	PFail   float64 `json:"p_fail"`
	Approve bool    `json:"needs_approval,omitempty"`
	// ApprovalID is the standing approval that covered this option.
	ApprovalID string `json:"approval_id,omitempty"`
}

type Entry struct {
	TS      time.Time `json:"ts"`
	Type    string    `json:"type"`
	OfferID string    `json:"offer_id,omitempty"`
	UseCase string    `json:"use_case,omitempty"`
	Kind    string    `json:"kind,omitempty"`
	Family  string    `json:"family,omitempty"`
	Stakes  int       `json:"stakes,omitempty"`
	Project string    `json:"project,omitempty"`
	Session string    `json:"session,omitempty"`

	// Offers.
	Options []OfferOption `json:"options,omitempty"`
	Lambda  float64       `json:"lambda,omitempty"`
	Preset  string        `json:"preset,omitempty"`

	// Outcomes.
	Model      string  `json:"model,omitempty"`
	Chosen     string  `json:"chosen,omitempty"`
	Outcome    string  `json:"outcome,omitempty"`
	CashUSD    float64 `json:"cash_usd,omitempty"`
	ApprovalID string  `json:"approval_id,omitempty"`
	Reason     string  `json:"reason,omitempty"`

	// Runs, and the outcome that names one.
	RunID       string  `json:"run_id,omitempty"`
	Route       string  `json:"route,omitempty"`
	UpstreamUSD float64 `json:"upstream_usd,omitempty"`
	Status      string  `json:"status,omitempty"`
}

var mu sync.Mutex

func NewOfferID() string {
	var b [6]byte
	rand.Read(b[:])
	return "o-" + hex.EncodeToString(b[:])
}

// MaxReason bounds the free-text reason so prompt text cannot creep in.
const MaxReason = 160

func Append(path string, e Entry) error {
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if e.Type == TypeOutcome {
		switch e.Outcome {
		case Accepted, Redo, Overridden, Declined:
		default:
			return fmt.Errorf("outcome must be accepted, redo, overridden or declined, not %q", e.Outcome)
		}
		if e.Model == "" {
			return errors.New("outcome needs the model")
		}
	}
	if len(e.Reason) > MaxReason {
		e.Reason = e.Reason[:MaxReason]
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// If a previous write was torn (a crash left a line with no trailing newline), start a fresh line
	// first, so this entry is not merged onto the partial one and lost as a single malformed line.
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, st.Size()-1); err == nil && last[0] != '\n' {
			b = append([]byte{'\n'}, b...)
		}
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// Read loads the ledger, skipping malformed lines.
func Read(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var e Entry
			// A line longer than bufio's default is returned across several ReadBytes calls with no '\n';
			// each chunk fails to parse and is skipped, so one corrupt run never aborts the whole scan
			// (which would make spend read low and the cap fail open).
			if json.Unmarshal(line, &e) == nil && e.Type != "" {
				out = append(out, e)
			}
		}
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, err
		}
	}
}

// Ledger is a loaded history with lookups.
type Ledger struct {
	Entries []Entry
	offers  map[string]*Entry
}

func NewLedger(es []Entry) *Ledger {
	l := &Ledger{Entries: es, offers: map[string]*Entry{}}
	for i := range es {
		if es[i].Type == TypeOffer && es[i].OfferID != "" {
			l.offers[es[i].OfferID] = &es[i]
		}
	}
	return l
}

func (l *Ledger) Offer(id string) *Entry { return l.offers[id] }

func (l *Ledger) outcomes() []Entry {
	var out []Entry
	for _, e := range l.Entries {
		if e.Type == TypeOutcome {
			out = append(out, e)
		}
	}
	return out
}

// Spend sums the measured cost of delegated runs. Outcomes do not add to it:
// their cash is the run's, filled in when the outcome names or matches a run.
type Spend struct {
	Today            float64 `json:"today_usd"`
	Month            float64 `json:"month_usd"`
	MonthBeforeToday float64 `json:"month_before_today_usd"`
	Runs             int     `json:"runs_this_month"`
}

func (l *Ledger) Spend(now time.Time) Spend {
	var s Spend
	y, m, d := now.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	month := time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
	for _, e := range l.Entries {
		if e.Type != TypeRun || e.TS.Before(month) {
			continue
		}
		s.Runs++
		s.Month += e.CashUSD
		if !e.TS.Before(day) {
			s.Today += e.CashUSD
		} else {
			s.MonthBeforeToday += e.CashUSD
		}
	}
	return s
}

// Runs returns run entries, newest first.
func (l *Ledger) Runs(n int) []Entry {
	var out []Entry
	for i := len(l.Entries) - 1; i >= 0 && (n <= 0 || len(out) < n); i-- {
		if l.Entries[i].Type == TypeRun {
			out = append(out, l.Entries[i])
		}
	}
	return out
}

// RunCost sums the runs with this id. A relay run id is "<session>/<turn>",
// so a bare session id sums every turn of that session.
func (l *Ledger) RunCost(id string) (float64, bool) {
	var sum float64
	found := false
	for _, e := range l.Entries {
		if e.Type == TypeRun && (e.RunID == id || strings.HasPrefix(e.RunID, id+"/")) {
			sum += e.CashUSD
			found = true
		}
	}
	return sum, found
}

// MatchRun finds the run an outcome is about when Claude did not name it: the
// newest run for the offer, else the newest run of the model since since that
// no outcome has claimed yet.
func (l *Ledger) MatchRun(offerID, model string, since time.Time) (Entry, bool) {
	claimed := map[string]bool{}
	for _, e := range l.Entries {
		if e.Type == TypeOutcome && e.RunID != "" {
			claimed[e.RunID] = true
		}
	}
	for _, match := range []func(Entry) bool{
		func(e Entry) bool { return offerID != "" && e.OfferID == offerID },
		func(e Entry) bool { return model != "" && Short(e.Model) == Short(model) && !e.TS.Before(since) },
	} {
		for i := len(l.Entries) - 1; i >= 0; i-- {
			e := l.Entries[i]
			session, _, _ := strings.Cut(e.RunID, "/")
			if e.Type == TypeRun && e.RunID != "" && !claimed[e.RunID] && !claimed[session] && match(e) {
				return e, true
			}
		}
	}
	return Entry{}, false
}

// UsedModels lists models that have run at least once (accepted or redo).
func (l *Ledger) UsedModels() map[string]bool {
	out := map[string]bool{}
	for _, e := range l.outcomes() {
		if e.Outcome == Accepted || e.Outcome == Redo {
			out[e.Model] = true
		}
	}
	return out
}

// ApprovalUse counts runs under each standing approval since its period began.
func (l *Ledger) ApprovalUse(rs []rules.Rule, now time.Time) map[string]rules.Use {
	start := map[string]time.Time{}
	for _, r := range rs {
		if r.Kind == rules.KindApprove {
			start[r.ID] = r.PeriodStart(now)
		}
	}
	out := map[string]rules.Use{}
	for _, e := range l.outcomes() {
		s, ok := start[e.ApprovalID]
		if !ok || e.TS.Before(s) || (e.Outcome != Accepted && e.Outcome != Redo) {
			continue
		}
		u := out[e.ApprovalID]
		u.Count++
		u.Total += e.CashUSD
		out[e.ApprovalID] = u
	}
	return out
}

// Counts are outcomes for one model on one slice of work.
type Counts struct {
	Accepted   int `json:"accepted"`
	Redo       int `json:"redo"`
	Declined   int `json:"declined"`
	Overridden int `json:"overridden"`
}

func (c Counts) Runs() int   { return c.Accepted + c.Redo }
func (c Counts) Offers() int { return c.Accepted + c.Redo + c.Declined + c.Overridden }

func (c *Counts) add(o string, w int) {
	switch o {
	case Accepted:
		c.Accepted += w
	case Redo:
		c.Redo += w
	case Declined:
		c.Declined += w
	case Overridden:
		c.Overridden += w
	}
}

// ModelStats is what the scorer needs from history for one model and task.
type ModelStats struct {
	UseCase Counts `json:"use_case"`
	Family  Counts `json:"family"`
}

// Stats returns outcomes for model on a use case and on its whole family.
func (l *Ledger) Stats(model, useCase, family string) ModelStats {
	var s ModelStats
	for _, e := range l.outcomes() {
		if e.Model != model {
			continue
		}
		if e.UseCase == useCase {
			s.UseCase.add(e.Outcome, 1)
		}
		if family != "" && e.Family == family {
			s.Family.add(e.Outcome, 1)
		}
	}
	return s
}

// Recent returns the last n outcomes, newest first.
func (l *Ledger) Recent(n int) []Entry {
	out := l.outcomes()
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.After(out[j].TS) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Tendency is one learned pattern.
type Tendency struct {
	Model  string `json:"model"`
	Kind   string `json:"kind"`
	Counts Counts `json:"counts"`
	Text   string `json:"text"`
	// Favored is true when the user reliably accepts this route.
	Favored bool `json:"favored"`
	// Disfavored is true when the user mostly declines or overrides it.
	Disfavored bool `json:"disfavored"`
}

// MinTendency is the number of outcomes before a pattern is reported.
const MinTendency = 3

func (l *Ledger) Tendencies() []Tendency {
	type key struct{ model, kind string }
	agg := map[key]*Counts{}
	for _, e := range l.outcomes() {
		k := key{e.Model, e.Kind}
		if agg[k] == nil {
			agg[k] = &Counts{}
		}
		agg[k].add(e.Outcome, 1)
	}
	var out []Tendency
	for k, c := range agg {
		n := c.Offers()
		if n < MinTendency {
			continue
		}
		t := Tendency{Model: k.model, Kind: k.kind, Counts: *c}
		name, kind := Short(k.model), kindWord(k.kind)
		switch {
		case float64(c.Accepted)/float64(n) >= 0.8:
			t.Favored = true
			t.Text = fmt.Sprintf("accepts %s for %s %d/%d", name, kind, c.Accepted, n)
		case float64(c.Declined+c.Overridden)/float64(n) >= 0.6:
			t.Disfavored = true
			t.Text = fmt.Sprintf("declines %s for %s %d/%d", name, kind, c.Declined+c.Overridden, n)
		case c.Runs() >= MinTendency && float64(c.Redo)/float64(c.Runs()) >= 0.3:
			t.Text = fmt.Sprintf("redoes %s's %s %d/%d", name, kind, c.Redo, c.Runs())
		default:
			t.Text = fmt.Sprintf("mixed on %s for %s (%d accepted, %d redone, %d declined)", name, kind, c.Accepted, c.Redo, c.Declined+c.Overridden)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Counts.Offers() != out[j].Counts.Offers() {
			return out[i].Counts.Offers() > out[j].Counts.Offers()
		}
		return out[i].Text < out[j].Text
	})
	return out
}

func kindWord(k string) string {
	switch k {
	case "":
		return "work"
	case "code":
		return "code"
	case "media":
		return "media"
	case "generate":
		return "generation"
	case "agent":
		return "agent work"
	case "review":
		return "reviews"
	}
	return k
}

// Short drops the author prefix from a model id.
func Short(id string) string {
	if _, after, ok := strings.Cut(id, "/"); ok {
		return after
	}
	return id
}

// Suggestion is a rule the engine proposes. It is never saved on its own.
type Suggestion struct {
	Rule rules.Rule `json:"rule"`
	Text string     `json:"text"`
}

// MinSuggest is how many matching outcomes a suggestion needs.
const MinSuggest = 5

// Suggest proposes standing approvals for work the user keeps approving above
// the per-task limit, and forbid rules for models the user keeps declining.
func (l *Ledger) Suggest(active []rules.Rule, perTask float64) []Suggestion {
	type agg struct {
		costs []float64
		kind  string
	}
	approved := map[string]*agg{}
	for _, e := range l.outcomes() {
		if e.Outcome != Accepted || e.ApprovalID != "" || e.CashUSD <= perTask || e.UseCase == "" {
			continue
		}
		a := approved[e.UseCase]
		if a == nil {
			a = &agg{kind: e.Kind}
			approved[e.UseCase] = a
		}
		a.costs = append(a.costs, e.CashUSD)
	}
	var out []Suggestion
	ids := make([]string, 0, len(approved))
	for id := range approved {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a := approved[id]
		if len(a.costs) < MinSuggest || coveredByApproval(active, id, a.kind) {
			continue
		}
		sort.Float64s(a.costs)
		p90 := a.costs[int(math.Ceil(0.9*float64(len(a.costs))))-1]
		limit := ceilCents(p90 * 1.25)
		mean := 0.0
		for _, c := range a.costs {
			mean += c
		}
		mean /= float64(len(a.costs))
		r := rules.Rule{Kind: rules.KindApprove, UseCases: []string{id}, MaxPerRunUSD: limit, Scope: rules.ScopeGlobal}
		out = append(out, Suggestion{Rule: r, Text: fmt.Sprintf("you've approved %d %s runs at about $%.2f; make that a standing approval up to $%.2f each?", len(a.costs), id, mean, limit)})
	}
	for _, t := range l.Tendencies() {
		if !t.Disfavored || t.Counts.Declined+t.Counts.Overridden < MinSuggest-1 || forbidden(active, t.Model, t.Kind) {
			continue
		}
		r := rules.Rule{Kind: rules.KindForbid, Target: rules.TargetModel, Models: []string{t.Model}, Scope: rules.ScopeGlobal}
		if t.Kind != "" {
			r.Kinds = []string{t.Kind}
		}
		out = append(out, Suggestion{Rule: r, Text: fmt.Sprintf("you turned down %s for %s %d of %d times; stop offering it for %s?",
			Short(t.Model), kindWord(t.Kind), t.Counts.Declined+t.Counts.Overridden, t.Counts.Offers(), kindWord(t.Kind))})
	}
	return out
}

func ceilCents(x float64) float64 {
	c := float64(int64(x*100+0.999999)) / 100
	if c < 0.01 {
		c = 0.01
	}
	return c
}

func coveredByApproval(rs []rules.Rule, useCase, kind string) bool {
	for _, r := range rs {
		if r.Kind == rules.KindApprove && r.Covers(useCase, kind) {
			return true
		}
	}
	return false
}

func forbidden(rs []rules.Rule, model, kind string) bool {
	for _, r := range rs {
		if r.Kind == rules.KindForbid && r.Target == rules.TargetModel {
			for _, m := range r.Models {
				if m == model && (len(r.Kinds) == 0 || contains(r.Kinds, kind)) {
					return true
				}
			}
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
