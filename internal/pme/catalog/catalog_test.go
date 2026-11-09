package catalog

import (
	"testing"

	"p0ssess"
)

func load(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load(p0ssess.Data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoadAndMap(t *testing.T) {
	c := load(t)
	if len(c.UseCases) != 104 {
		t.Fatalf("want 104 use cases, got %d", len(c.UseCases))
	}
	for _, u := range c.UseCases {
		if u.Family == "" {
			t.Errorf("%s has no family", u.ID)
		}
		w := u.Work
		if w.InTokens == 0 && w.Turns == 0 && w.AudioSec == 0 && w.VideoSec == 0 && w.Images == 0 &&
			w.Pages == 0 && w.Chars == 0 && w.GenSec == 0 {
			t.Errorf("%s has no default work size", u.ID)
		}
	}
	if c.Get("limit-fallback-coding").DefaultStakes() != 0 {
		t.Error("inherit should read as 0")
	}
	lo, hi := c.Get("sql-query-write").StakesBounds()
	if lo != 2 || hi != 4 {
		t.Errorf("sql bounds %d-%d", lo, hi)
	}
}

func TestKindsAndEdits(t *testing.T) {
	c := load(t)
	cases := []struct {
		id, kind string
		edits    bool
	}{
		{"video-understand", KindMedia, false},
		{"image-generate", KindGenerate, false},
		{"code-file-edits", KindAgent, true},
		{"code-review-diff", KindReview, false},
		{"sql-query-write", KindCode, false},
		{"summarize-long-doc", KindText, false},
		{"devops-config", KindCode, true},
		{"security-critical-code", KindCode, true},
	}
	for _, tc := range cases {
		u := c.Get(tc.id)
		if got := u.Kind(); got != tc.kind {
			t.Errorf("%s kind = %s, want %s", tc.id, got, tc.kind)
		}
		if got := u.EditsCode(); got != tc.edits {
			t.Errorf("%s edits = %v", tc.id, got)
		}
	}
	if !KindMatches("media", KindGenerate) || KindMatches("code", KindAgent) || !KindMatches("", KindText) {
		t.Error("KindMatches wrong")
	}
}

func TestClassify(t *testing.T) {
	c := load(t)
	cases := []struct {
		name string
		p    Profile
		want string
	}{
		{"video summary", Profile{Text: "summarize this lecture video", Inputs: []string{"video"}}, "video-understand"},
		{"transcribe", Profile{Text: "transcribe the interview", Inputs: []string{"audio"}, Outputs: []string{"transcript"}}, "audio-transcribe"},
		{"image gen", Profile{Text: "generate an image of a lighthouse", Outputs: []string{"image"}}, "image-generate"},
		{"log", Profile{Text: "digest these server logs and find errors"}, "log-digest"},
		{"sql", Profile{Text: "write a SQL query for monthly totals"}, "sql-query-write"},
		{"translate", Profile{Text: "translate the README into Spanish"}, "translate-text"},
		{"review", Profile{Text: "review this diff for bugs"}, "code-review-diff"},
	}
	for _, tc := range cases {
		got := c.Classify(tc.p, 3)
		if len(got) == 0 || got[0].ID != tc.want {
			t.Errorf("%s: got %+v, want %s first", tc.name, got, tc.want)
		}
	}
}

func TestWorkFor(t *testing.T) {
	c := load(t)
	u := c.Get("video-understand")
	w := u.WorkFor(&Profile{VideoSec: 3600})
	if w.VideoSec != 3600 || w.OutTokens != u.Work.OutTokens {
		t.Errorf("WorkFor = %+v", w)
	}
	w = c.Get("log-digest").WorkFor(&Profile{Bytes: 400000})
	if w.InTokens != 100000 {
		t.Errorf("bytes→tokens = %d", w.InTokens)
	}
	if NormalizeModality("PDF") != "file" || NormalizeModality("mp4") != "video" {
		t.Error("NormalizeModality")
	}
}
