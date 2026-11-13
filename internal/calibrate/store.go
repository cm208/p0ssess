package calibrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"p0ssess/internal/pme/paths"
)

// Result is one graded call. It stays on this machine; only Scores leave it.
type Result struct {
	Sample  string  `json:"sample"`
	UseCase string  `json:"use_case"`
	Model   string  `json:"model"`
	User    bool    `json:"user,omitempty"`
	Correct bool    `json:"correct"`
	CostUSD float64 `json:"cost_usd"`
	// CostBasis is how the call's cost was arrived at (reported, byok-upstream, tokens or reserved).
	CostBasis string    `json:"cost_basis,omitempty"`
	Seconds   float64   `json:"seconds"`
	At        time.Time `json:"at"`
	// Error is set when no reply came back; such a result counts in no score.
	Error string `json:"error,omitempty"`
}

// Graded reports whether the result counts.
func (r Result) Graded() bool { return r.Error == "" }

// Results is the latest result per sample and model.
type Results struct {
	V    int      `json:"v"`
	List []Result `json:"results"`
}

// Has reports whether a sample and model already have a graded result.
func (rs *Results) Has(sample, model string) bool {
	for _, r := range rs.List {
		if r.Sample == sample && r.Model == model && r.Graded() {
			return true
		}
	}
	return false
}

// Merge replaces older results for the same sample and model. A failed call does not replace a graded one.
func (rs *Results) Merge(add []Result) {
	idx := map[[2]string]int{}
	for i, r := range rs.List {
		idx[[2]string{r.Sample, r.Model}] = i
	}
	for _, r := range add {
		k := [2]string{r.Sample, r.Model}
		if i, ok := idx[k]; ok {
			if !r.Graded() && rs.List[i].Graded() {
				continue
			}
			rs.List[i] = r
			continue
		}
		idx[k] = len(rs.List)
		rs.List = append(rs.List, r)
	}
}

// Last is when the newest result was recorded.
func (rs *Results) Last() time.Time {
	var t time.Time
	for _, r := range rs.List {
		if r.At.After(t) {
			t = r.At
		}
	}
	return t
}

// Score is what the server gets: graded runs and correct answers per use case and model.
type Score struct {
	UseCase string `json:"use_case"`
	Model   string `json:"model"`
	N       int    `json:"n"`
	Correct int    `json:"correct"`
}

// Scores counts the graded results per use case and model, built-in and user samples together.
func (rs *Results) Scores() []Score {
	m := map[[2]string]*Score{}
	for _, r := range rs.List {
		if !r.Graded() {
			continue
		}
		k := [2]string{r.UseCase, r.Model}
		s := m[k]
		if s == nil {
			s = &Score{UseCase: r.UseCase, Model: r.Model}
			m[k] = s
		}
		s.N++
		if r.Correct {
			s.Correct++
		}
	}
	out := make([]Score, 0, len(m))
	for _, s := range m {
		s.N = min(s.N, 100)
		s.Correct = min(s.Correct, s.N)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UseCase != out[j].UseCase {
			return out[i].UseCase < out[j].UseCase
		}
		return out[i].Model < out[j].Model
	})
	if len(out) > 500 {
		out = out[:500]
	}
	return out
}

// Meta is the calibration bookkeeping.
type Meta struct {
	// Offered is set once the offer after Pro turned on was shown.
	Offered bool `json:"offered,omitempty"`
	// Pending means the scores changed and the server has not taken them yet.
	Pending bool `json:"pending,omitempty"`
}

// Dir is where calibration keeps its files.
func Dir(d paths.Dirs) string { return filepath.Join(d.State, "calibration") }

// UserDir is the default folder for the user's own samples (answers.toml and the files it names).
func UserDir(d paths.Dirs) string { return filepath.Join(d.Config, "calibration") }

func load(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func save(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(path, b, 0o600)
}

func LoadResults(d paths.Dirs) (*Results, error) {
	rs := &Results{V: 1}
	err := load(filepath.Join(Dir(d), "results.json"), rs)
	return rs, err
}

func SaveResults(d paths.Dirs, rs *Results) error {
	rs.V = 1
	return save(filepath.Join(Dir(d), "results.json"), rs)
}

func LoadMeta(d paths.Dirs) Meta {
	var m Meta
	load(filepath.Join(Dir(d), "meta.json"), &m)
	return m
}

func SaveMeta(d paths.Dirs, m Meta) error { return save(filepath.Join(Dir(d), "meta.json"), m) }

// Baselines is every Claude baseline run, the latest per sample, mode and model.
type Baselines struct {
	V    int           `json:"v"`
	Runs []BaselineRun `json:"runs"`
}

func LoadBaselines(d paths.Dirs) (*Baselines, error) {
	b := &Baselines{V: 1}
	err := load(filepath.Join(Dir(d), "baselines.json"), b)
	return b, err
}

func SaveBaselines(d paths.Dirs, b *Baselines) error {
	b.V = 1
	return save(filepath.Join(Dir(d), "baselines.json"), b)
}

// Merge replaces an older run of the same sample, mode and model.
func (b *Baselines) Merge(add []BaselineRun) {
	for _, r := range add {
		replaced := false
		for i, o := range b.Runs {
			if o.Sample == r.Sample && o.Mode == r.Mode && o.Model == r.Model {
				b.Runs[i], replaced = r, true
				break
			}
		}
		if !replaced {
			b.Runs = append(b.Runs, r)
		}
	}
}

// Forget removes every local calibration file.
func Forget(d paths.Dirs) error {
	var first error
	for _, f := range []string{"results.json", "baselines.json", "meta.json"} {
		if err := os.Remove(filepath.Join(Dir(d), f)); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}

// Upload sends the whole set of scores through put (POST /v1/calibration); with no scores it asks the
// server to forget them (put with nil). Only the counts go: no sample, prompt, reply, cost or time.
func Upload(ctx context.Context, put func(context.Context, []byte) error, rs *Results) error {
	sc := rs.Scores()
	if len(sc) == 0 {
		return put(ctx, nil)
	}
	b, err := json.Marshal(struct {
		V      int     `json:"v"`
		Scores []Score `json:"scores"`
	}{1, sc})
	if err != nil {
		return err
	}
	return put(ctx, b)
}
