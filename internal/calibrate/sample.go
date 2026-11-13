// Package calibrate measures models on samples with known answers, on the user's own OpenRouter key,
// and Claude doing the same samples alone, and turns both into the savings report. Only per-kind,
// per-model counts leave the machine (POST /v1/calibration); sample files, prompts and answers never do.
package calibrate

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// ManifestName is the answer key in a samples folder.
const ManifestName = "answers.toml"

// Checks a sample's answer can be graded with.
const (
	CheckExact    = "exact"    // the answer equals the key, ignoring case, spacing and surrounding punctuation
	CheckContains = "contains" // the key appears in the answer, normalized the same way
	CheckNumber   = "number"   // the first number in the answer is within tolerance of the key
	CheckChoice   = "choice"   // the answer is one of choices and equals the key
	CheckWER      = "wer"      // word error rate of the whole reply against the key is at most max_wer
)

// Sample is one task with a known answer.
type Sample struct {
	ID      string `toml:"id" json:"id"`
	UseCase string `toml:"use_case" json:"use_case"`
	// File is a path relative to the manifest; empty for text-only samples.
	File   string `toml:"file" json:"file,omitempty"`
	Prompt string `toml:"prompt" json:"-"`
	// Output is "text" (default) or "transcript" for speech-to-text samples.
	Output    string   `toml:"output" json:"output,omitempty"`
	Answer    string   `toml:"answer" json:"-"`
	Check     string   `toml:"check" json:"check"`
	Tolerance float64  `toml:"tolerance" json:"-"`
	MaxWER    float64  `toml:"max_wer" json:"-"`
	Choices   []string `toml:"choices" json:"-"`
	Source    string   `toml:"source" json:"source,omitempty"`
	License   string   `toml:"license" json:"license,omitempty"`

	// Path is File resolved against the manifest's folder (set by the loader).
	Path    string `toml:"-" json:"-"`
	Builtin bool   `toml:"-" json:"builtin"`
}

type manifest struct {
	Sample []Sample `toml:"sample"`
}

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// licenses a built-in sample may carry.
var builtinLicenses = map[string]bool{"CC0-1.0": true, "public-domain": true}

// ParseManifest reads an answers.toml from fsys at dir. Built-in samples must name a source and a
// public-domain or CC0 license.
func ParseManifest(fsys fs.FS, dir string, builtin bool) ([]Sample, error) {
	b, err := fs.ReadFile(fsys, path.Join(dir, ManifestName))
	if err != nil {
		return nil, err
	}
	var m manifest
	md, err := toml.Decode(string(b), &m)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestName, err)
	}
	if un := md.Undecoded(); len(un) > 0 {
		return nil, fmt.Errorf("%s: unknown key %s", ManifestName, un[0])
	}
	seen := map[string]bool{}
	for i := range m.Sample {
		s := &m.Sample[i]
		s.Builtin = builtin
		if s.ID == "" && s.File != "" {
			s.ID = strings.ToLower(strings.TrimSuffix(path.Base(s.File), path.Ext(s.File)))
		}
		if err := s.validate(); err != nil {
			return nil, fmt.Errorf("%s: sample %d (%s): %w", ManifestName, i+1, s.ID, err)
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("%s: sample id %q is used twice", ManifestName, s.ID)
		}
		seen[s.ID] = true
		if builtin && (s.Source == "" || !builtinLicenses[s.License]) {
			return nil, fmt.Errorf("%s: built-in sample %s needs a source and a CC0-1.0 or public-domain license", ManifestName, s.ID)
		}
		if s.File != "" {
			clean := path.Clean(s.File)
			if path.IsAbs(clean) || strings.HasPrefix(clean, "../") || clean == ".." {
				return nil, fmt.Errorf("%s: sample %s: file must be inside the samples folder", ManifestName, s.ID)
			}
			s.Path = path.Join(dir, clean)
			if _, err := fs.Stat(fsys, s.Path); err != nil {
				return nil, fmt.Errorf("%s: sample %s: %w", ManifestName, s.ID, err)
			}
		}
	}
	return m.Sample, nil
}

func (s *Sample) validate() error {
	if !idRE.MatchString(s.ID) {
		return errors.New("id must be 1-64 lowercase letters, digits, dots, dashes or underscores")
	}
	if s.UseCase == "" {
		return errors.New("use_case is required")
	}
	if strings.TrimSpace(s.Prompt) == "" && s.Output != "transcript" {
		return errors.New("prompt is required")
	}
	switch s.Output {
	case "", "text", "transcript":
	default:
		return fmt.Errorf("output must be text or transcript, not %q", s.Output)
	}
	if strings.TrimSpace(s.Answer) == "" {
		return errors.New("answer is required")
	}
	if s.Check == "" {
		s.Check = CheckExact
	}
	switch s.Check {
	case CheckExact, CheckContains:
	case CheckNumber:
		if _, ok := firstNumber(s.Answer); !ok {
			return errors.New("check number needs a numeric answer")
		}
		if s.Tolerance < 0 {
			return errors.New("tolerance must be 0 or more")
		}
	case CheckChoice:
		if len(s.Choices) < 2 {
			return errors.New("check choice needs at least two choices")
		}
		ok := false
		for _, c := range s.Choices {
			ok = ok || norm(c) == norm(s.Answer)
		}
		if !ok {
			return errors.New("the answer must be one of the choices")
		}
	case CheckWER:
		if s.MaxWER <= 0 || s.MaxWER >= 1 {
			return errors.New("check wer needs max_wer between 0 and 1")
		}
	default:
		return fmt.Errorf("check must be exact, contains, number, choice or wer, not %q", s.Check)
	}
	return nil
}

// AnswerLine is what every prompt asks the model to end with, so a reply can be graded.
const AnswerLine = "ANSWER:"

// Instruction is appended to every sample's prompt, for p0ssess's models and for Claude alike.
const Instruction = "\n\nEnd your reply with one line that starts with \"ANSWER:\" followed by only the answer."

// PromptFor is the prompt sent for a sample. Transcript samples go to speech-to-text models and get
// no instruction.
func (s Sample) PromptFor() string {
	if s.Output == "transcript" {
		return s.Prompt
	}
	return strings.TrimSpace(s.Prompt) + Instruction
}

var answerRE = regexp.MustCompile(`(?im)^\s*\**\s*ANSWER\s*\**\s*:\s*\**\s*(.+?)\s*\**\s*$`)

// Extract returns the graded part of a reply: the last ANSWER: line, else the whole reply.
func Extract(reply string) string {
	ms := answerRE.FindAllStringSubmatch(reply, -1)
	if len(ms) == 0 {
		return strings.TrimSpace(reply)
	}
	return strings.TrimSpace(ms[len(ms)-1][1])
}

// Grade reports whether a reply answers the sample correctly.
func (s Sample) Grade(reply string) bool {
	switch s.Check {
	case CheckWER:
		return WER(s.Answer, Extract(reply)) <= s.MaxWER+1e-9
	case CheckNumber:
		got, ok := firstNumber(Extract(reply))
		want, _ := firstNumber(s.Answer)
		return ok && math.Abs(got-want) <= s.Tolerance+1e-9
	case CheckContains:
		return strings.Contains(" "+norm(Extract(reply))+" ", " "+norm(s.Answer)+" ")
	case CheckChoice:
		got := norm(Extract(reply))
		return got == norm(s.Answer)
	default:
		return norm(Extract(reply)) == norm(s.Answer)
	}
}

var (
	numRE   = regexp.MustCompile(`-?\d[\d,]*(?:\.\d+)?|-?\.\d+`)
	spaceRE = regexp.MustCompile(`\s+`)
)

func firstNumber(s string) (float64, bool) {
	m := numRE.FindString(s)
	if m == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", ""), 64)
	return v, err == nil
}

// norm lowercases, collapses spaces and trims surrounding quotes, brackets and punctuation.
func norm(s string) string {
	s = strings.ToLower(spaceRE.ReplaceAllString(strings.TrimSpace(s), " "))
	return strings.Trim(s, " \"'`.,;:!?()[]{}*_")
}

// words splits text into lowercase words without punctuation, for WER.
func words(s string) []string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '\'', r > 127:
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Fields(strings.ReplaceAll(b.String(), "'", ""))
}

// WER is the word error rate of hyp against ref: word edits over the reference's length.
func WER(ref, hyp string) float64 {
	r, h := words(ref), words(hyp)
	if len(r) == 0 {
		if len(h) == 0 {
			return 0
		}
		return 1
	}
	prev := make([]int, len(h)+1)
	cur := make([]int, len(h)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(r); i++ {
		cur[0] = i
		for j := 1; j <= len(h); j++ {
			sub := prev[j-1]
			if r[i-1] != h[j-1] {
				sub++
			}
			cur[j] = min(sub, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return float64(prev[len(h)]) / float64(len(r))
}
