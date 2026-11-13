package calibrate

import (
	"sort"
	"strings"
	"time"
)

// ReportInput is what BuildReport reads: everything measured on this machine.
type ReportInput struct {
	Pro       bool
	Now       time.Time
	Results   *Results
	Baselines *Baselines
	// Picked is the model p0ssess routes each use case to now.
	Picked map[string]string
	// Task is the catalog's short description of a use case.
	Task func(useCase string) string
}

type acc struct {
	runs, correct  int
	claude, router float64
	seconds        float64
	share          float64
	shareRuns      int
	priced         bool
}

func (a *acc) side(source string) *Side {
	if a == nil || a.runs == 0 {
		return nil
	}
	s := &Side{Runs: a.runs, Correct: a.correct, ClaudeUSD: a.claude, OpenRouterUSD: a.router,
		MeanSeconds: a.seconds / float64(a.runs), Source: source}
	if a.correct > 0 && a.priced {
		v := (a.claude + a.router) / float64(a.correct)
		s.CostPerCorrectUSD = &v
	}
	if a.correct > 0 && a.shareRuns == a.runs {
		v := a.share / float64(a.correct)
		s.WeeklySharePerCorrect = &v
	}
	return s
}

// BuildReport turns the measured runs into the savings report. A kind appears once anything was
// measured for it; a side with no runs stays nil.
func BuildReport(in ReportInput) Report {
	r := Report{Pro: in.Pro, GeneratedAt: in.Now.UTC(), Kinds: []KindRow{}, Models: []ModelRow{}, Prices: PriceTable}
	if in.Results == nil {
		in.Results = &Results{}
	}
	if in.Baselines == nil {
		in.Baselines = &Baselines{}
	}
	r.Calibrated = in.Results.Last()
	alone, with := map[string]*acc{}, map[string]*acc{}
	anyLower := false
	for _, b := range in.Baselines.Runs {
		if !b.Graded() {
			continue
		}
		if b.At.After(r.Baselined) {
			r.Baselined = b.At
		}
		m := alone
		if b.Mode == ModeWith {
			m = with
		}
		a := m[b.UseCase]
		if a == nil {
			a = &acc{priced: true}
			m[b.UseCase] = a
		}
		a.runs++
		if b.Correct {
			a.correct++
		}
		a.seconds += b.Seconds
		a.router += b.OpenRouterUSD
		if b.ClaudeUSD != nil {
			a.claude += *b.ClaudeUSD
		} else {
			a.priced = false
		}
		if b.WeeklyShare != nil {
			a.share += *b.WeeklyShare
			a.shareRuns++
			anyLower = anyLower || b.ShareLowerBound
		}
	}

	models := map[[2]string]*acc{}
	for _, x := range in.Results.List {
		if !x.Graded() {
			continue
		}
		k := [2]string{x.UseCase, x.Model}
		a := models[k]
		if a == nil {
			a = &acc{priced: true}
			models[k] = a
		}
		a.runs++
		if x.Correct {
			a.correct++
		}
		a.router += x.CostUSD
		a.seconds += x.Seconds
	}
	keys := make([][2]string, 0, len(models))
	for k := range models {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		s := models[k].side("calibration")
		r.Models = append(r.Models, ModelRow{UseCase: k[0], Model: k[1], Runs: s.Runs, Correct: s.Correct,
			CostPerCorrectUSD: s.CostPerCorrectUSD, MeanSeconds: s.MeanSeconds, Picked: in.Picked[k[0]] == k[1]})
	}

	kinds := map[string]bool{}
	for k := range alone {
		kinds[k] = true
	}
	for k := range with {
		kinds[k] = true
	}
	for _, k := range keys {
		kinds[k[0]] = true
	}
	ids := make([]string, 0, len(kinds))
	for k := range kinds {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	fromCalibration := false
	for _, uc := range ids {
		row := KindRow{UseCase: uc, Alone: alone[uc].side("baseline"), Model: in.Picked[uc]}
		if in.Task != nil {
			row.Task = in.Task(uc)
		}
		if w := with[uc]; w != nil {
			row.With = w.side("baseline")
		} else if m := models[[2]string{uc, in.Picked[uc]}]; m != nil {
			row.With = m.side("calibration")
			fromCalibration = true
		}
		r.Kinds = append(r.Kinds, row)
	}

	t := Totals{}
	shareOK := true
	var shareSaved float64
	for _, k := range r.Kinds {
		if k.Alone == nil || k.With == nil || k.Alone.CostPerCorrectUSD == nil || k.With.CostPerCorrectUSD == nil {
			continue
		}
		t.Kinds++
		t.AloneUSD += *k.Alone.CostPerCorrectUSD
		t.WithUSD += *k.With.CostPerCorrectUSD
		t.AloneSeconds += k.Alone.MeanSeconds
		t.WithSeconds += k.With.MeanSeconds
		switch {
		case k.Alone.WeeklySharePerCorrect == nil:
			shareOK = false
		case k.With.WeeklySharePerCorrect != nil:
			shareSaved += *k.Alone.WeeklySharePerCorrect - *k.With.WeeklySharePerCorrect
		case k.With.Source == "calibration":
			// The routed model's own call uses none of Claude's limit; the handoff was not measured.
			shareSaved += *k.Alone.WeeklySharePerCorrect
		default:
			shareOK = false
		}
	}
	if t.Kinds > 0 {
		if shareOK {
			t.WeeklyShareSaved = &shareSaved
		}
		r.Totals = &t
	}

	r.Notes = append(r.Notes,
		"Every number is a measured run on this machine: p0ssess's models on your OpenRouter key, Claude in headless Claude Code runs. Nothing is estimated.",
		"Claude's dollars are its tokens at API prices; cache writes are counted at the input price, which understates Claude slightly.",
		"Cost per correct answer is everything a side spent on a kind divided by its correct answers. Time is the average from start to answer.")
	claudes := map[string]bool{}
	for _, b := range in.Baselines.Runs {
		if b.Graded() && b.Model != "" {
			claudes[b.Model] = true
		}
	}
	if len(claudes) > 0 {
		names := make([]string, 0, len(claudes))
		for m := range claudes {
			names = append(names, m)
		}
		sort.Strings(names)
		r.Notes = append(r.Notes, "Claude ran as "+strings.Join(names, ", ")+" in Claude Code.")
	}
	if r.Baselined.IsZero() {
		r.Notes = append(r.Notes, "Claude alone has not been measured on this machine yet.")
	} else {
		r.Notes = append(r.Notes, "The weekly-limit share comes from Claude Code's own usage meter read during each run; other Claude sessions running at the same time would count too.")
	}
	if anyLower {
		r.Notes = append(r.Notes, "The first run of a batch had no reading before it, so its share leaves out its first request.")
	}
	if fromCalibration {
		r.Notes = append(r.Notes, "Where Claude with p0ssess was not run end to end, that side is the routed model's own calls from calibration; Claude's handoff to p0ssess is not in those numbers.")
	}
	return r
}
