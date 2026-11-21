package savings

import (
	"sort"
	"strings"
	"time"

	"p0ssess/internal/notices"
)

// Totals are the sums of a set of receipts. Saving figures count only runs that were priced against
// Claude; Runs counts them all.
type Totals struct {
	Runs int `json:"runs"`
	// Priced runs have token counts, so a Claude price: Measured ones against OpenRouter's reported cost,
	// Estimated ones against a cost priced from tokens. Legacy are receipts from before 1.2 with only a
	// saving recorded; they count as Estimated.
	Priced    int `json:"priced"`
	Measured  int `json:"measured"`
	Estimated int `json:"estimated"`
	Unpriced  int `json:"unpriced"`
	// CostUSD is what every run cost; PricedCostUSD only the priced runs, the figure ClaudeUSD is compared to.
	CostUSD       float64 `json:"cost_usd"`
	PricedCostUSD float64 `json:"priced_cost_usd"`
	ClaudeUSD     float64 `json:"claude_usd"`
	// SavedUSD is ClaudeUSD - PricedCostUSD: negative when Claude would have been cheaper overall.
	SavedUSD float64 `json:"saved_usd"`
	// ClaudeCheaper counts priced runs where Claude would have cost less.
	ClaudeCheaper int `json:"claude_cheaper"`
	// TokensKept are the tokens other models handled that Claude's windows did not see.
	TokensKept int64 `json:"tokens_kept"`
}

func (t *Totals) add(r notices.Record) {
	t.Runs++
	t.CostUSD += r.CostUSD
	if r.Tok != nil {
		t.TokensKept += int64(r.Tok.Prompt + r.Tok.Completion + r.Tok.Reasoning + r.Tok.CacheRead + r.Tok.CacheWrite)
	}
	claude, saved, basis := figures(r)
	switch basis {
	case Unpriced:
		t.Unpriced++
		return
	case Measured:
		t.Measured++
	default:
		t.Estimated++
	}
	t.Priced++
	t.PricedCostUSD += r.CostUSD
	t.ClaudeUSD += claude
	t.SavedUSD += saved
	if saved < 0 {
		t.ClaudeCheaper++
	}
}

// figures reads a record's Claude price, saving and basis, including the receipts of older versions that
// only kept a positive saving.
func figures(r notices.Record) (claude, saved float64, basis string) {
	switch r.Basis {
	case Measured, Estimated:
		return r.ClaudeUSD, r.SavedUSD, r.Basis
	case Unpriced:
		return 0, 0, Unpriced
	}
	if r.SavedUSD > 0 {
		return r.CostUSD + r.SavedUSD, r.SavedUSD, Estimated
	}
	return 0, 0, Unpriced
}

// Row is a total with its label.
type Row struct {
	Key string `json:"key"`
	Totals
}

// Period is one stretch of time rolled up: a month, or everything.
type Period struct {
	Label string `json:"label"` // 2026-11, or "all"
	Totals
	ByModel []Row  `json:"by_model"`
	ByKind  []Row  `json:"by_kind"`
	Rescue  Totals `json:"rescue"`
	Normal  Totals `json:"normal"`
}

// Month is a month key such as "2026-11" in loc.
func Month(t time.Time, loc *time.Location) string { return t.In(loc).Format("2006-01") }

// ShortModel drops the provider from a model id.
func ShortModel(id string) string {
	if _, after, ok := strings.Cut(id, "/"); ok {
		return after
	}
	return id
}

// Roll sums the receipts of one month ("" for all of them).
func Roll(recs []notices.Record, month string, loc *time.Location) Period {
	label := month
	if label == "" {
		label = "all"
	}
	p := Period{Label: label}
	models, kinds := map[string]*Totals{}, map[string]*Totals{}
	for _, r := range recs {
		if r.Receipt == "" || month != "" && Month(r.TS, loc) != month {
			continue
		}
		p.Totals.add(r)
		bucket(models, ShortModel(r.Model), r)
		kind := r.Kind
		if kind == "" {
			kind = "other"
		}
		bucket(kinds, kind, r)
		if r.Rescue {
			p.Rescue.add(r)
		} else {
			p.Normal.add(r)
		}
	}
	p.ByModel, p.ByKind = rows(models), rows(kinds)
	return p
}

func bucket(m map[string]*Totals, key string, r notices.Record) {
	if key == "" {
		key = "unknown"
	}
	t := m[key]
	if t == nil {
		t = &Totals{}
		m[key] = t
	}
	t.add(r)
}

func rows(m map[string]*Totals) []Row {
	out := make([]Row, 0, len(m))
	for k, t := range m {
		out = append(out, Row{Key: k, Totals: *t})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SavedUSD != out[j].SavedUSD {
			return out[i].SavedUSD > out[j].SavedUSD
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Report is what the page and the terminal card show.
type Report struct {
	This   Period   `json:"this"`
	Last   Period   `json:"last"`
	All    Period   `json:"all"`
	Months []string `json:"months"` // months with receipts, newest first
}

// Build rolls up this month, last month and everything as of now.
func Build(recs []notices.Record, now time.Time) Report {
	loc := now.Location()
	this := Month(now, loc)
	last := Month(time.Date(now.In(loc).Year(), now.In(loc).Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 0, -1), loc)
	seen := map[string]bool{}
	for _, r := range recs {
		if r.Receipt != "" {
			seen[Month(r.TS, loc)] = true
		}
	}
	var months []string
	for m := range seen {
		months = append(months, m)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(months)))
	if months == nil {
		months = []string{}
	}
	return Report{This: Roll(recs, this, loc), Last: Roll(recs, last, loc), All: Roll(recs, "", loc), Months: months}
}

// Headline words a period's saving honestly: what was saved, that nothing was, or that Claude would have
// been cheaper.
type Headline struct {
	Kind   string  // "saved", "none" or "worse"
	Amount float64 // dollars, always >= 0
}

// HeadlineOf classifies a period's net saving. Under half a cent is "none", never a claim.
func HeadlineOf(t Totals) Headline {
	switch {
	case t.Priced == 0:
		return Headline{Kind: "none"}
	case t.SavedUSD >= 0.005:
		return Headline{Kind: "saved", Amount: t.SavedUSD}
	case t.SavedUSD <= -0.005:
		return Headline{Kind: "worse", Amount: -t.SavedUSD}
	}
	return Headline{Kind: "none"}
}

// SharePct is the saving as a share of Claude's price for the same work, 0 when nothing was saved.
func SharePct(t Totals) int {
	if t.ClaudeUSD <= 0 || t.SavedUSD <= 0 {
		return 0
	}
	return int(t.SavedUSD/t.ClaudeUSD*100 + 0.5)
}
