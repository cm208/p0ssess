// Package bench turns the clean benchmark files into a failure-probability
// prior per model and task family.
//
// Each family names one or more boards. On a board a model's score becomes a
// relative quality q in [0,1] against the best model on that board:
//
//   - Bradley-Terry ratings (LMArena): q = 10^((r-best)/400), the model's
//     Bradley-Terry strength relative to the best model's.
//   - IPS scores (LMArena agent boards): q = clamp(s / best, 0, 1); a score at or
//     below zero means no measured lift and gives q = 0.
//   - Accuracies (Epoch, OpenRouter evals, p0ssess's own checks): q = acc / best.
//
// Then p_fail = f_best + (f_worst - f_best) * (1 - q), where f_best is the
// family's failure rate for the best model and f_worst the rate at q = 0.
// Both are engine presets, not measurements; see docs/ENGINE.md.
package bench

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"regexp"
	"sort"
	"strings"
)

type Record struct {
	Source       string   `json:"source"`
	AsOf         string   `json:"as_of"`
	Subset       string   `json:"subset"`
	Category     string   `json:"category"`
	Benchmark    string   `json:"benchmark"`
	OpenRouterID string   `json:"openrouter_id"`
	Variant      any      `json:"variant"`
	Metric       string   `json:"metric"`
	Value        *float64 `json:"value"`
	CILower      *float64 `json:"ci_lower"`
	CIUpper      *float64 `json:"ci_upper"`
	Stderr       *float64 `json:"stderr"`
	BinomialSE   *float64 `json:"binomial_se_computed"`
	Families     []string `json:"task_families"`
}

// Board identifies one leaderboard slice.
type Board struct {
	Source    string // lmarena, epoch, openrouter
	Subset    string // LMArena subset
	Category  string // LMArena category
	Benchmark string // Epoch or OpenRouter benchmark name
	Label     string
}

func (b Board) key() string { return b.Source + "|" + b.Subset + "|" + b.Category + "|" + b.Benchmark }

type Family struct {
	ID     string
	Boards []Board
	FBest  float64 // p_fail of the best model on the family's boards
	FWorst float64 // p_fail at q = 0
	// Prior is used when no model has evidence for the family (media, speech,
	// embeddings). Zero means the family has boards.
	Prior float64
	Note  string
}

func lm(subset, category, label string) Board {
	return Board{Source: "lmarena", Subset: subset, Category: category, Label: label}
}

// Families is the engine's task-family map. Keep it in step with docs/ENGINE.md.
var Families = map[string]Family{
	"agentic_coding":  {Boards: []Board{lm("agent", "overall", "LMArena agent")}, FBest: 0.12, FWorst: 0.60},
	"coding":          {Boards: []Board{lm("text_style_control", "coding", "LMArena coding")}, FBest: 0.06, FWorst: 0.45},
	"webdev":          {Boards: []Board{lm("webdev", "webdev", "LMArena webdev")}, FBest: 0.10, FWorst: 0.55},
	"chat":            {Boards: []Board{lm("text_style_control", "overall", "LMArena text")}, FBest: 0.05, FWorst: 0.40},
	"instruction":     {Boards: []Board{lm("text_style_control", "instruction_following", "LMArena instruction following")}, FBest: 0.05, FWorst: 0.40},
	"creative":        {Boards: []Board{lm("text_style_control", "creative_writing", "LMArena creative writing")}, FBest: 0.08, FWorst: 0.45},
	"multilingual":    {Boards: []Board{lm("text_style_control", "non_english", "LMArena non-English")}, FBest: 0.06, FWorst: 0.45},
	"long_context":    {Boards: []Board{lm("text_style_control", "longer_query", "LMArena longer query")}, FBest: 0.06, FWorst: 0.45},
	"hard_reasoning":  {Boards: []Board{lm("text_style_control", "hard_prompts", "LMArena hard prompts")}, FBest: 0.08, FWorst: 0.50},
	"factual":         {Boards: []Board{lm("text_factuality", "overall", "LMArena factuality")}, FBest: 0.06, FWorst: 0.45},
	"business":        {Boards: []Board{lm("text_style_control", "industry_business_and_management_and_financial_operations", "LMArena business and finance")}, FBest: 0.08, FWorst: 0.50},
	"legal":           {Boards: []Board{lm("text_style_control", "industry_legal_and_government", "LMArena legal")}, FBest: 0.08, FWorst: 0.50},
	"science_writing": {Boards: []Board{lm("text_style_control", "industry_life_and_physical_and_social_science", "LMArena science")}, FBest: 0.06, FWorst: 0.45},
	"writing":         {Boards: []Board{lm("text_style_control", "industry_writing_and_literature_and_language", "LMArena writing")}, FBest: 0.06, FWorst: 0.45},
	"math": {Boards: []Board{
		{Source: "epoch", Benchmark: "FrontierMath Tiers 1-3 v2 (private)", Label: "Epoch FrontierMath 1-3"},
		lm("text_style_control", "math", "LMArena math"),
	}, FBest: 0.08, FWorst: 0.55},
	"science": {Boards: []Board{
		{Source: "epoch", Benchmark: "GPQA Diamond", Label: "Epoch GPQA Diamond"},
		{Source: "openrouter", Benchmark: "gpqa_diamond", Label: "OpenRouter GPQA"},
	}, FBest: 0.06, FWorst: 0.45},
	"tool_use": {Boards: []Board{{Source: "openrouter", Benchmark: "tau_bench_verified_airline", Label: "OpenRouter tau-bench airline"}},
		FBest: 0.10, FWorst: 0.50},
	"search":              {Boards: []Board{lm("search_style_control", "overall", "LMArena search")}, FBest: 0.08, FWorst: 0.45},
	"ocr":                 {Boards: []Board{lm("vision_style_control", "ocr", "LMArena OCR")}, FBest: 0.06, FWorst: 0.45},
	"vision":              {Boards: []Board{lm("vision_style_control", "overall", "LMArena vision")}, FBest: 0.06, FWorst: 0.45},
	"document":            {Boards: []Board{lm("document_style_control", "overall", "LMArena document"), lm("text_style_control", "longer_query", "LMArena longer query")}, FBest: 0.08, FWorst: 0.45},
	"image_gen":           {Boards: []Board{lm("text_to_image", "overall", "LMArena text-to-image")}, FBest: 0.15, FWorst: 0.55},
	"image_edit":          {Boards: []Board{lm("image_edit", "overall", "LMArena image edit")}, FBest: 0.15, FWorst: 0.55},
	"video_gen":           {Boards: []Board{lm("text_to_video", "overall", "LMArena text-to-video"), lm("image_to_video", "overall", "LMArena image-to-video")}, FBest: 0.20, FWorst: 0.60},
	"transcription":       {Prior: 0.10, Note: "no clean benchmark for speech-to-text"},
	"tts":                 {Prior: 0.12, Note: "no clean benchmark for speech synthesis"},
	"music":               {Prior: 0.25, Note: "no clean benchmark for music generation"},
	"audio_understanding": {Prior: 0.15, Note: "no clean benchmark for audio understanding"},
	// p0ssess's own measurement (registry/benchmarks/p0ssess_evals.json, docs/MEDIA.md). f_best keeps the
	// best model at the old family prior; f_worst is the highest any family uses.
	"video_understanding": {Boards: []Board{{Source: "p0ssess", Benchmark: "video_describe_synthetic_v1", Label: "p0ssess video check"}},
		FBest: 0.15, FWorst: 0.60},
	"embeddings": {Prior: 0.05, Note: "no clean benchmark for embeddings"},
	"rerank":     {Prior: 0.05, Note: "no clean benchmark for reranking"},
	"safety":     {Prior: 0.15, Note: "no clean benchmark for safety classification"},
	"local":      {Prior: 0.08, Note: "local tool"},
}

func init() {
	for id, f := range Families {
		f.ID = id
		Families[id] = f
	}
}

// Uncertainty presets (absolute p_fail).
const (
	SigmaMap       = 0.05 // the board-to-p_fail mapping itself
	SigmaProxy     = 0.08 // the family only approximates the job
	SigmaAlias     = 0.03 // evidence from another dated snapshot of the model
	SigmaNoEvid    = 0.20 // no evidence on an evidenced family
	SigmaPrior     = 0.15 // family without any clean benchmark
	NoEvidPosition = 0.75 // where an unevidenced model sits between f_best and f_worst
)

type Set struct {
	boards map[string][]Record // board key -> records
	best   map[string]float64
}

func Load(fsys fs.FS) (*Set, error) {
	s := &Set{boards: map[string][]Record{}, best: map[string]float64{}}
	for _, f := range []struct{ path, src string }{
		{"registry/benchmarks/lmarena.json", "lmarena"},
		{"registry/benchmarks/epoch_ai.json", "epoch"},
		{"registry/benchmarks/openrouter_evals.json", "openrouter"},
		{"registry/benchmarks/aider_polyglot.json", "aider"},
		{"registry/benchmarks/p0ssess_evals.json", "p0ssess"},
	} {
		b, err := fs.ReadFile(fsys, f.path)
		if err != nil {
			return nil, err
		}
		var file struct {
			Records []Record `json:"records"`
		}
		if err := json.Unmarshal(b, &file); err != nil {
			return nil, fmt.Errorf("%s: %w", f.path, err)
		}
		for _, r := range file.Records {
			if r.Value == nil || r.OpenRouterID == "" {
				continue
			}
			k := Board{Source: f.src, Subset: r.Subset, Category: r.Category, Benchmark: r.Benchmark}.key()
			if f.src == "lmarena" {
				k = Board{Source: f.src, Subset: r.Subset, Category: r.Category}.key()
			} else {
				k = Board{Source: f.src, Benchmark: r.Benchmark}.key()
			}
			s.boards[k] = append(s.boards[k], r)
		}
	}
	for k, rs := range s.boards {
		best := math.Inf(-1)
		for _, r := range rs {
			best = math.Max(best, *r.Value)
		}
		s.best[k] = best
	}
	return s, nil
}

var dated = regexp.MustCompile(`-\d{4}$`)

// Base strips :batch and a trailing -MMDD snapshot tag, so dated snapshots of
// one model share evidence.
func Base(id string) string {
	id = strings.TrimSuffix(id, ":batch")
	return dated.ReplaceAllString(id, "")
}

// Estimate is a failure-probability prior for one model on one family.
type Estimate struct {
	PFail    float64 `json:"p_fail"`
	Sigma    float64 `json:"sigma"`
	Evidence string  `json:"evidence"`
	Boards   int     `json:"boards"`
	// Strength is the prior's weight in pseudo-observations when history is
	// mixed in.
	Strength float64 `json:"strength"`
}

// For returns the estimate for model on family. catalogRank is 0 when the
// catalog recommends the model for the use case, 1 when it lists it as an
// alternative and -1 otherwise; it only matters for families with no boards.
func (s *Set) For(familyID, model string, proxy bool, catalogRank int) Estimate {
	fam, ok := Families[familyID]
	if !ok {
		fam = Family{ID: familyID, Prior: 0.30, Note: "unknown family"}
	}
	if len(fam.Boards) == 0 {
		p, sig := fam.Prior, SigmaPrior
		why := fam.Note + "; catalog prior"
		switch catalogRank {
		case 1:
			p += 0.03
		case -1:
			p += 0.10
			sig = SigmaNoEvid
			why = fam.Note + "; not named by the catalog"
		}
		return Estimate{PFail: clamp01(p), Sigma: sig, Evidence: why, Strength: 3}
	}
	var qSum, wSum, sqSum float64
	var labels []string
	alias := false
	for _, b := range fam.Boards {
		q, sq, line, viaAlias, ok := s.quality(b, model)
		if !ok {
			continue
		}
		qSum += q
		sqSum += sq
		wSum++
		labels = append(labels, line)
		alias = alias || viaAlias
	}
	spread := fam.FWorst - fam.FBest
	if wSum == 0 {
		return Estimate{
			PFail:    clamp01(fam.FBest + NoEvidPosition*spread),
			Sigma:    SigmaNoEvid,
			Evidence: "no clean evidence on " + boardNames(fam) + "; pessimistic prior",
			Strength: 2,
		}
	}
	q := qSum / wSum
	sq := sqSum / wSum
	sig2 := math.Pow(sq*spread, 2) + SigmaMap*SigmaMap
	if proxy {
		sig2 += SigmaProxy * SigmaProxy
	}
	if alias {
		sig2 += SigmaAlias * SigmaAlias
	}
	ev := strings.Join(labels, "; ")
	if proxy {
		ev += " (proxy)"
	}
	return Estimate{
		PFail:    clamp01(fam.FBest + spread*(1-q)),
		Sigma:    math.Sqrt(sig2),
		Evidence: ev,
		Boards:   int(wSum),
		Strength: 8,
	}
}

func boardNames(f Family) string {
	var n []string
	for _, b := range f.Boards {
		n = append(n, b.Label)
	}
	return strings.Join(n, ", ")
}

// quality finds the model's best variant on a board and returns q, its
// standard error, and a one-line description.
func (s *Set) quality(b Board, model string) (q, sq float64, line string, alias, ok bool) {
	k := b.key()
	rs := s.boards[k]
	pick := func(match func(string) bool) *Record {
		var best *Record
		for i := range rs {
			r := &rs[i]
			if match(r.OpenRouterID) && (best == nil || *r.Value > *best.Value) {
				best = r
			}
		}
		return best
	}
	r := pick(func(id string) bool { return id == model })
	if r == nil {
		base := Base(model)
		r = pick(func(id string) bool { return Base(id) == base })
		alias = r != nil
	}
	if r == nil {
		return 0, 0, "", false, false
	}
	top := s.best[k]
	v := *r.Value
	lo := v
	switch {
	case r.CILower != nil:
		lo = *r.CILower
	case r.Stderr != nil:
		lo = v - 1.96**r.Stderr
	case r.BinomialSE != nil:
		lo = v - 1.96**r.BinomialSE
	}
	qf := func(x float64) float64 {
		switch r.Metric {
		case "bradley_terry_rating":
			return math.Pow(10, (x-top)/400)
		case "ips_score":
			if top <= 0 {
				return 0
			}
			return clamp01(x / top)
		default:
			if top <= 0 {
				return 0
			}
			return clamp01(x / top)
		}
	}
	q = math.Min(1, qf(v))
	sq = math.Max(0, (q-math.Min(1, qf(lo)))/1.96)
	line = fmt.Sprintf("%s %s vs best %s", b.Label, fmtScore(r.Metric, v), fmtScore(r.Metric, top))
	if alias {
		line += " (" + r.OpenRouterID + ")"
	}
	return q, sq, line, alias, true
}

func fmtScore(metric string, v float64) string {
	if metric == "bradley_terry_rating" {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.3f", v)
}

func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// Models lists every model id with at least one record.
func (s *Set) Models() []string {
	seen := map[string]bool{}
	for _, rs := range s.boards {
		for _, r := range rs {
			seen[r.OpenRouterID] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
