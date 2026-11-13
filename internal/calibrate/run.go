package calibrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// PerKind is how many of a kind's routes are measured: the models p0ssess would pick first.
const PerKind = 3

// Call is one sample sent to one model.
type Call struct {
	UseCase string
	Stakes  int
	Model   string
	Prompt  string
	// File is a path on disk, or "" for a text-only sample.
	File       string
	Transcript bool
	DryRun     bool
}

// Reply is what a call came back with.
type Reply struct {
	Text            string
	CostUSD         float64
	Elapsed         time.Duration
	EstimateUSD     float64
	EstimateUnknown bool
}

// Runner sends calls; the plugin's runner goes through every one of the user's limits and records the
// spend in the ledger under its own route.
type Runner interface {
	Run(ctx context.Context, c Call) (Reply, error)
}

// Router names the models p0ssess would pick for a use case, best first, after the user's rules and
// lists. Claude is never among them.
type Router func(ctx context.Context, useCase string) ([]string, error)

// Item is one planned call.
type Item struct {
	Sample      Sample  `json:"sample"`
	Model       string  `json:"model"`
	EstimateUSD float64 `json:"estimate_usd"`
}

// Plan is a calibration run before anything is spent.
type Plan struct {
	Items       []Item   `json:"items"`
	EstimateUSD float64  `json:"estimate_usd"`
	Kinds       []string `json:"kinds"`
	// Skipped says what was left out and why (no route, no price).
	Skipped []string `json:"skipped,omitempty"`
}

// Key names a sample in the results: built-in and user samples may share an id.
func (s Sample) Key() string {
	if s.Builtin {
		return "b:" + s.ID
	}
	return "u:" + s.ID
}

// Stakes is the lowest stakes a calibration call of this use case runs at, capped at 3.
type StakesFunc func(useCase string) int

// NewPlan lists the calls a run would make and prices each one with a dry run. With onlyNew, pairs
// that already have a result are left out, so a rerun measures only models that newly entered the
// routes (D37).
func NewPlan(ctx context.Context, samples []Sample, route Router, r Runner, stakes StakesFunc, have *Results, onlyNew bool) (Plan, error) {
	var p Plan
	byKind := map[string][]Sample{}
	for _, s := range samples {
		byKind[s.UseCase] = append(byKind[s.UseCase], s)
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, uc := range kinds {
		models, err := route(ctx, uc)
		if err != nil {
			p.Skipped = append(p.Skipped, fmt.Sprintf("%s: %v", uc, err))
			continue
		}
		if len(models) > PerKind {
			models = models[:PerKind]
		}
		if len(models) == 0 {
			p.Skipped = append(p.Skipped, uc+": no model fits under your rules")
			continue
		}
		added := false
		for _, s := range byKind[uc] {
			for _, m := range models {
				if onlyNew && have != nil && have.Has(s.Key(), m) {
					continue
				}
				rep, err := r.Run(ctx, callFor(s, m, stakes, true))
				switch {
				case err != nil:
					p.Skipped = append(p.Skipped, fmt.Sprintf("%s on %s: %v", s.ID, short(m), err))
					continue
				case rep.EstimateUnknown:
					p.Skipped = append(p.Skipped, fmt.Sprintf("%s on %s: no price is known", s.ID, short(m)))
					continue
				}
				p.Items = append(p.Items, Item{Sample: s, Model: m, EstimateUSD: rep.EstimateUSD})
				p.EstimateUSD += rep.EstimateUSD
				added = true
			}
		}
		if added {
			p.Kinds = append(p.Kinds, uc)
		}
	}
	return p, nil
}

func callFor(s Sample, model string, stakes StakesFunc, dry bool) Call {
	st := 2
	if stakes != nil {
		st = stakes(s.UseCase)
	}
	return Call{UseCase: s.UseCase, Stakes: st, Model: model, Prompt: s.PromptFor(), File: s.Path,
		Transcript: s.Output == "transcript", DryRun: dry}
}

// Workers is how many calls run at once.
const Workers = 4

// ErrCeiling is returned when the run stopped because the next call could pass the approved ceiling.
var ErrCeiling = errors.New("stopped at the approved ceiling")

// Execute runs a plan and grades each reply. It never starts a call whose estimate, with what is spent
// and reserved so far, could pass maxUSD. A call that fails before a reply comes back is recorded with
// its error and counts in no score.
func Execute(ctx context.Context, p Plan, r Runner, stakes StakesFunc, maxUSD float64, now func() time.Time, progress func(Result)) ([]Result, float64, error) {
	var (
		mu       sync.Mutex
		spent    float64
		reserved float64
		out      []Result
		stopped  bool
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, Workers)
	for _, it := range p.Items {
		mu.Lock()
		if spent+reserved+it.EstimateUSD > maxUSD+1e-9 {
			stopped = true
			mu.Unlock()
			break
		}
		reserved += it.EstimateUSD
		mu.Unlock()
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(it Item) {
			defer func() { <-sem; wg.Done() }()
			rep, err := r.Run(ctx, callFor(it.Sample, it.Model, stakes, false))
			res := Result{Sample: it.Sample.Key(), UseCase: it.Sample.UseCase, Model: it.Model, User: !it.Sample.Builtin,
				CostUSD: rep.CostUSD, Seconds: rep.Elapsed.Seconds(), At: now().UTC()}
			if err != nil {
				res.Error = oneLine(err.Error())
			} else {
				res.Correct = it.Sample.Grade(rep.Text)
			}
			mu.Lock()
			reserved -= it.EstimateUSD
			spent += rep.CostUSD
			out = append(out, res)
			mu.Unlock()
			if progress != nil {
				progress(res)
			}
		}(it)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool {
		if out[i].UseCase != out[j].UseCase {
			return out[i].UseCase < out[j].UseCase
		}
		if out[i].Sample != out[j].Sample {
			return out[i].Sample < out[j].Sample
		}
		return out[i].Model < out[j].Model
	})
	if stopped {
		return out, spent, ErrCeiling
	}
	return out, spent, ctx.Err()
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func short(id string) string {
	if _, s, ok := strings.Cut(id, "/"); ok {
		return s
	}
	return id
}
