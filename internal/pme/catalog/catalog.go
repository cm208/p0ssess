// Package catalog loads catalog/usecases.yaml and the engine's reading of
// each entry (evidence family, modalities, default work size), and maps a
// task onto use cases.
package catalog

import (
	_ "embed"
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Cost struct {
	Min *float64 `yaml:"min" json:"min"`
	Max *float64 `yaml:"max" json:"max"`
}

type Verify struct {
	Level  string `yaml:"level" json:"level"`
	Method string `yaml:"method" json:"method"`
}

type UseCase struct {
	N            int      `yaml:"n" json:"n"`
	ID           string   `yaml:"id" json:"id"`
	Origin       string   `yaml:"origin" json:"-"`
	Task         string   `yaml:"task" json:"task"`
	Inputs       []string `yaml:"inputs" json:"inputs"`
	Outputs      []string `yaml:"outputs" json:"outputs"`
	Stakes       any      `yaml:"stakes" json:"-"`
	StakesRange  []int    `yaml:"stakes_range" json:"stakes_range,omitempty"`
	StakesReason string   `yaml:"stakes_reason" json:"-"`
	QualityBar   string   `yaml:"quality_bar" json:"quality_bar"`
	Path         string   `yaml:"path" json:"path"`
	LocalTool    string   `yaml:"local_tool" json:"local_tool,omitempty"`
	Engine       string   `yaml:"engine" json:"engine,omitempty"`
	Recommended  *string  `yaml:"recommended" json:"recommended"`
	Ensemble     []string `yaml:"ensemble" json:"ensemble,omitempty"`
	Alternatives []string `yaml:"alternatives" json:"alternatives"`
	EstCost      *Cost    `yaml:"est_cost_usd" json:"est_cost_usd"`
	Budget       string   `yaml:"budget_sensitivity" json:"-"`
	Verify       Verify   `yaml:"verify" json:"verify"`
	RoutingNote  string   `yaml:"stakes_routing_change" json:"-"`
	Related      []string `yaml:"related" json:"-"`
	Notes        []string `yaml:"notes" json:"-"`

	// Filled from the engine map.
	Family string   `yaml:"-" json:"family"`
	Proxy  bool     `yaml:"-" json:"-"`
	In     []string `yaml:"-" json:"-"`
	Out    []string `yaml:"-" json:"-"`
	Work   Work     `yaml:"-" json:"-"`
}

// Work is the size of one run.
type Work struct {
	InTokens  int     `json:"in_tokens,omitempty"`
	OutTokens int     `json:"out_tokens,omitempty"`
	Turns     int     `json:"turns,omitempty"`
	Items     int     `json:"items,omitempty"`
	AudioSec  float64 `json:"audio_seconds,omitempty"`
	VideoSec  float64 `json:"video_seconds,omitempty"`
	Images    int     `json:"images,omitempty"`
	Pages     int     `json:"pages,omitempty"`
	Chars     int     `json:"chars,omitempty"`
	GenSec    float64 `json:"gen_seconds,omitempty"`
}

// Sizes are catalog/README.md's size profiles (input, output tokens).
var Sizes = map[string][2]int{
	"T": {1000, 50},
	"S": {5000, 1000},
	"M": {30000, 3000},
	"R": {30000, 6000},
	"L": {200000, 4000},
}

type mapEntry struct {
	F      string   `yaml:"f"`
	Size   string   `yaml:"size"`
	InTok  *int     `yaml:"in_tok"`
	OutTok *int     `yaml:"out_tok"`
	Turns  int      `yaml:"turns"`
	Items  int      `yaml:"items"`
	Audio  float64  `yaml:"audio_s"`
	Video  float64  `yaml:"video_s"`
	Images int      `yaml:"images"`
	Pages  int      `yaml:"pages"`
	Chars  int      `yaml:"chars"`
	GenS   float64  `yaml:"gen_s"`
	In     []string `yaml:"in"`
	Out    []string `yaml:"out"`
	Proxy  bool     `yaml:"proxy"`
}

//go:embed engine_map.yaml
var engineMap []byte

type Catalog struct {
	Version  string
	AsOf     string
	UseCases []*UseCase
	byID     map[string]*UseCase
}

type file struct {
	Version  string     `yaml:"version"`
	AsOf     string     `yaml:"as_of"`
	UseCases []*UseCase `yaml:"use_cases"`
}

func Load(fsys fs.FS) (*Catalog, error) {
	b, err := fs.ReadFile(fsys, "catalog/usecases.yaml")
	if err != nil {
		return nil, err
	}
	return Parse(b, engineMap)
}

func Parse(catalogYAML, mapYAML []byte) (*Catalog, error) {
	var f file
	if err := yaml.Unmarshal(catalogYAML, &f); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	var m map[string]mapEntry
	if err := yaml.Unmarshal(mapYAML, &m); err != nil {
		return nil, fmt.Errorf("engine map: %w", err)
	}
	c := &Catalog{Version: f.Version, AsOf: f.AsOf, UseCases: f.UseCases, byID: map[string]*UseCase{}}
	for _, u := range f.UseCases {
		e, ok := m[u.ID]
		if !ok {
			return nil, fmt.Errorf("engine map: no entry for use case %q", u.ID)
		}
		u.Family, u.Proxy = e.F, e.Proxy
		u.In, u.Out = e.In, e.Out
		if len(u.In) == 0 {
			u.In = []string{"text"}
		}
		if len(u.Out) == 0 {
			u.Out = []string{"text"}
		}
		w := Work{Turns: e.Turns, Items: e.Items, AudioSec: e.Audio, VideoSec: e.Video,
			Images: e.Images, Pages: e.Pages, Chars: e.Chars, GenSec: e.GenS}
		if s, ok := Sizes[e.Size]; ok {
			w.InTokens, w.OutTokens = s[0], s[1]
		}
		if e.InTok != nil {
			w.InTokens = *e.InTok
		}
		if e.OutTok != nil {
			w.OutTokens = *e.OutTok
		}
		u.Work = w
		c.byID[u.ID] = u
	}
	for id := range m {
		if c.byID[id] == nil {
			return nil, fmt.Errorf("engine map: %q is not in the catalog", id)
		}
	}
	return c, nil
}

func (c *Catalog) Get(id string) *UseCase { return c.byID[id] }

// DefaultStakes is the catalog's first-listed level; 0 for "inherit".
func (u *UseCase) DefaultStakes() int {
	switch v := u.Stakes.(type) {
	case int:
		return v
	}
	return 0
}

// StakesBounds returns the level range the use case covers.
func (u *UseCase) StakesBounds() (int, int) {
	if len(u.StakesRange) == 2 {
		return u.StakesRange[0], u.StakesRange[1]
	}
	s := u.DefaultStakes()
	if s == 0 {
		return 1, 4
	}
	return s, s
}

// Candidates lists the recommended model, the ensemble and the alternatives.
func (u *UseCase) Candidates() []string {
	var out []string
	if u.Recommended != nil {
		out = append(out, *u.Recommended)
	}
	out = append(out, u.Ensemble...)
	out = append(out, u.Alternatives...)
	return out
}

// CatalogRank is 0 for the recommended model, 1 for ensemble or alternatives,
// and -1 when the catalog does not name the model.
func (u *UseCase) CatalogRank(model string) int {
	if u.Recommended != nil && *u.Recommended == model {
		return 0
	}
	for _, m := range append(append([]string{}, u.Ensemble...), u.Alternatives...) {
		if m == model {
			return 1
		}
	}
	return -1
}

// Kinds group use cases for rules, approvals and tendencies.
const (
	KindMedia    = "media"    // reads audio, video, images or documents
	KindGenerate = "generate" // makes images, audio, speech or video
	KindAgent    = "agent"    // multi-step work in a sandboxed session
	KindReview   = "review"   // reviews, critiques and judgments
	KindCode     = "code"     // one-shot code work
	KindText     = "text"     // everything else
)

var reviewWords = []string{"review", "judge", "audit", "redteam", "critique", "check", "consensus", "sycophancy", "hypothesis", "edge-cases", "gate"}

func (u *UseCase) Kind() string {
	for _, o := range u.Out {
		switch o {
		case "image", "video", "speech", "audio":
			return KindGenerate
		}
	}
	for _, i := range u.In {
		switch i {
		case "audio", "video", "image", "file":
			return KindMedia
		}
	}
	if u.Path == "agent" {
		return KindAgent
	}
	for _, w := range reviewWords {
		if strings.Contains(u.ID, w) {
			return KindReview
		}
	}
	switch u.Family {
	case "coding", "agentic_coding", "webdev":
		return KindCode
	}
	return KindText
}

// KindMatches reports whether a rule's kind covers a use case kind. "media"
// covers generation too; "all" or "" covers everything.
func KindMatches(rule, kind string) bool {
	switch rule {
	case "", "all", "*":
		return true
	case KindMedia:
		return kind == KindMedia || kind == KindGenerate
	}
	return rule == kind
}

var editOutputs = []string{"diff", "patch", "change", "skeleton", "test files", "doc comments", "migrated file",
	"harness", "migration", "config", "html", "css", "js", "accepted tests"}

// EditsCode reports whether the job's result changes the user's files.
func (u *UseCase) EditsCode() bool {
	for _, o := range u.Outputs {
		lo := strings.ToLower(o)
		for _, w := range editOutputs {
			if lo == w || strings.Contains(lo, w) {
				return true
			}
		}
	}
	return false
}

// Profile describes a task when no use-case id is given, and overrides the
// default work size when one is.
type Profile struct {
	Text       string   `json:"text,omitempty"`
	Inputs     []string `json:"inputs,omitempty"`
	Outputs    []string `json:"outputs,omitempty"`
	Path       string   `json:"path,omitempty"`
	Size       string   `json:"size,omitempty"`
	Stakes     int      `json:"stakes,omitempty"`
	InTokens   int      `json:"in_tokens,omitempty"`
	OutTokens  int      `json:"out_tokens,omitempty"`
	Bytes      int      `json:"bytes,omitempty"`
	Turns      int      `json:"turns,omitempty"`
	Items      int      `json:"items,omitempty"`
	AudioSec   float64  `json:"audio_seconds,omitempty"`
	VideoSec   float64  `json:"video_seconds,omitempty"`
	Images     int      `json:"images,omitempty"`
	Pages      int      `json:"pages,omitempty"`
	Chars      int      `json:"chars,omitempty"`
	GenSec     float64  `json:"gen_seconds,omitempty"`
	NeedsTools bool     `json:"needs_tools,omitempty"`
}

// NormalizeModality maps loose names onto the engine's modality words.
func NormalizeModality(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "pdf", "file", "files", "document", "doc", "documents":
		return "file"
	case "img", "images", "picture", "photo", "screenshot", "png", "jpg":
		return "image"
	case "sound", "voice", "mp3", "wav", "microphone audio", "speech-in":
		return "audio"
	case "clip", "movie", "mp4", "mov", "url":
		if s == "url" {
			return "text"
		}
		return "video"
	case "tts", "narration", "speech":
		return "speech"
	case "transcript", "transcription", "subtitles", "srt":
		return "transcript"
	case "embedding", "embeddings", "vectors":
		return "embeddings"
	case "code", "repo", "diff", "json", "sql", "markdown":
		return "text"
	case "music", "sfx":
		return "audio"
	}
	return s
}

// Work merges a profile's explicit sizes over the use case's default.
func (u *UseCase) WorkFor(p *Profile) Work {
	w := u.Work
	if p == nil {
		return w
	}
	if s, ok := Sizes[strings.ToUpper(p.Size)]; ok {
		w.InTokens, w.OutTokens = s[0], s[1]
	}
	if p.Bytes > 0 {
		w.InTokens = p.Bytes / 4
	}
	set := func(dst *int, v int) {
		if v > 0 {
			*dst = v
		}
	}
	setf := func(dst *float64, v float64) {
		if v > 0 {
			*dst = v
		}
	}
	set(&w.InTokens, p.InTokens)
	set(&w.OutTokens, p.OutTokens)
	set(&w.Turns, p.Turns)
	set(&w.Items, p.Items)
	set(&w.Images, p.Images)
	set(&w.Pages, p.Pages)
	set(&w.Chars, p.Chars)
	setf(&w.AudioSec, p.AudioSec)
	setf(&w.VideoSec, p.VideoSec)
	setf(&w.GenSec, p.GenSec)
	return w
}

// Match is one classification result.
type Match struct {
	ID         string  `json:"id"`
	Confidence float64 `json:"confidence"`
	Why        string  `json:"why"`
}

var stop = map[string]bool{"the": true, "and": true, "for": true, "with": true, "this": true, "that": true,
	"from": true, "into": true, "are": true, "all": true, "can": true, "you": true, "please": true, "our": true,
	"one": true, "use": true, "make": true, "about": true, "what": true, "how": true, "its": true, "per": true}

func tokens(s string) []string {
	f := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	out := f[:0]
	for _, w := range f {
		if len(w) >= 3 && !stop[w] {
			out = append(out, stem(w))
		}
	}
	return out
}

func stem(w string) string {
	for _, suf := range []string{"ing", "ion", "es", "s", "ed"} {
		if len(w) > len(suf)+3 && strings.HasSuffix(w, suf) {
			return strings.TrimSuffix(w, suf)
		}
	}
	return w
}

// Classify ranks use cases for a profile: required modalities first, then
// word overlap with the task text, the path hint and the stakes range. It is
// a fallback; Claude should pass an explicit use-case id when it knows one.
func (c *Catalog) Classify(p Profile, n int) []Match {
	if n <= 0 {
		n = 3
	}
	ins := normAll(p.Inputs)
	outs := normAll(p.Outputs)
	words := tokens(p.Text)
	type scored struct {
		u   *UseCase
		s   float64
		why []string
	}
	var all []scored
	for _, u := range c.UseCases {
		s, why := 0.0, []string{}
		uin, uout := set(u.In), set(u.Out)
		mismatch := false
		for _, m := range ins {
			if m == "text" {
				continue
			}
			if uin[m] {
				s += 3
				why = append(why, m+" input")
			} else {
				mismatch = true
			}
		}
		for _, m := range outs {
			if m == "text" {
				continue
			}
			if uout[m] {
				s += 3
				why = append(why, m+" output")
			} else {
				mismatch = true
			}
		}
		if mismatch {
			s -= 4
		}
		// Media use cases need media; do not match them on words alone.
		if len(ins) > 0 || len(outs) > 0 {
			for m := range uin {
				if m != "text" && !contains(ins, m) {
					s -= 2
				}
			}
		}
		if len(outs) > 0 {
			for m := range uout {
				if m != "text" && !contains(outs, m) {
					s -= 2
				}
			}
		} else if len(ins) > 0 {
			for m := range uout {
				if m != "text" && m != "transcript" {
					s -= 2
				}
			}
		}
		if len(words) > 0 {
			idw := tokens(strings.ReplaceAll(u.ID, "-", " "))
			body := tokens(u.Task + " " + u.QualityBar + " " + strings.Join(u.Inputs, " ") + " " + strings.Join(u.Outputs, " "))
			hit := 0.0
			for _, w := range words {
				if contains(idw, w) {
					hit += 2
				} else if contains(body, w) {
					hit += 1
				}
			}
			if hit > 0 {
				s += 4 * hit / math.Sqrt(float64(len(words))*float64(len(idw)+len(body)/2+1))
				why = append(why, "task words")
			}
		}
		if p.Path != "" && p.Path == u.Path {
			s += 0.5
		}
		if p.Stakes > 0 {
			lo, hi := u.StakesBounds()
			if p.Stakes < lo || p.Stakes > hi {
				s -= 0.75
			}
		}
		all = append(all, scored{u, s, why})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].s != all[j].s {
			return all[i].s > all[j].s
		}
		return all[i].u.N < all[j].u.N
	})
	var total float64
	top := all
	if len(top) > n {
		top = top[:n]
	}
	for _, x := range top {
		total += math.Max(x.s, 0)
	}
	out := make([]Match, 0, len(top))
	for _, x := range top {
		conf := 0.0
		if total > 0 {
			conf = math.Max(x.s, 0) / total
		}
		// Weak overall signal lowers confidence across the board.
		conf *= math.Min(1, math.Max(x.s, 0)/4)
		out = append(out, Match{ID: x.u.ID, Confidence: math.Round(conf*100) / 100, Why: strings.Join(x.why, ", ")})
	}
	return out
}

func normAll(in []string) []string {
	var out []string
	for _, s := range in {
		if n := NormalizeModality(s); n != "" && !contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

func set(list []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range list {
		m[s] = true
	}
	return m
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
