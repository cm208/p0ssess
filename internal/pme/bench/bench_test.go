package bench

import (
	"math"
	"testing"

	"p0ssess"
)

func load(t *testing.T) *Set {
	t.Helper()
	s, err := Load(p0ssess.Data)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAgenticOrdering(t *testing.T) {
	s := load(t)
	order := []string{"anthropic/claude-opus-5.5", "anthropic/claude-sonnet-5.5", "openai/gpt-6.1-sol",
		"deepseek/deepseek-v4.1-flash-1017", "z-ai/glm-5.3-flash"}
	prev := -1.0
	for _, m := range order {
		e := s.For("agentic_coding", m, false, -1)
		if e.PFail <= prev {
			t.Errorf("%s p_fail %.3f not above previous %.3f", m, e.PFail, prev)
		}
		prev = e.PFail
	}
	if e := s.For("agentic_coding", "anthropic/claude-opus-5.5", false, -1); math.Abs(e.PFail-0.12) > 1e-9 {
		t.Errorf("best model should sit at f_best, got %.3f", e.PFail)
	}
	if e := s.For("agentic_coding", "z-ai/glm-5.3-flash", false, -1); math.Abs(e.PFail-0.60) > 1e-9 {
		t.Errorf("negative IPS should sit at f_worst, got %.3f", e.PFail)
	}
}

func TestTable(t *testing.T) {
	s := load(t)
	cases := []struct {
		family, model string
		lo, hi        float64
	}{
		{"agentic_coding", "openai/gpt-6.1-sol", 0.28, 0.33},
		{"agentic_coding", "deepseek/deepseek-v4.1-flash-1017", 0.52, 0.56},
		{"coding", "google/gemini-4-argon", 0.06, 0.06},
		{"coding", "anthropic/claude-haiku-5.5", 0.16, 0.19},
		{"chat", "z-ai/glm-5.3-flash", 0.12, 0.15},
		{"webdev", "deepseek/deepseek-v4.1-flash-1017", 0.34, 0.38},
		{"math", "openai/gpt-6.1-sol", 0.08, 0.12},
		{"science", "deepseek/deepseek-v4.1-flash-1017", 0.06, 0.12},
		{"ocr", "google/gemini-3.8-flash", 0.06, 0.06},
		{"image_gen", "google/gemini-3.1-flash-image", 0.20, 0.35},
	}
	for _, tc := range cases {
		e := s.For(tc.family, tc.model, false, -1)
		if e.PFail < tc.lo-1e-9 || e.PFail > tc.hi+1e-9 {
			t.Errorf("%s/%s p_fail %.3f outside [%.2f, %.2f] (%s)", tc.family, tc.model, e.PFail, tc.lo, tc.hi, e.Evidence)
		}
		if e.Boards == 0 {
			t.Errorf("%s/%s should have evidence", tc.family, tc.model)
		}
	}
}

func TestAliasAndNoEvidence(t *testing.T) {
	s := load(t)
	// The coding board has deepseek-v4.1-flash, not the 1017 snapshot.
	e := s.For("coding", "deepseek/deepseek-v4.1-flash-1017", false, -1)
	if e.Boards == 0 {
		t.Fatal("dated snapshot should borrow evidence")
	}
	direct := s.For("coding", "deepseek/deepseek-v4.1-flash", false, -1)
	if e.PFail != direct.PFail || e.Sigma <= direct.Sigma {
		t.Errorf("alias should match mean and widen sigma: %+v vs %+v", e, direct)
	}
	none := s.For("coding", "newco/unknown", false, -1)
	if none.Boards != 0 || none.Sigma != SigmaNoEvid || none.PFail < 0.3 {
		t.Errorf("no-evidence prior should be wide and pessimistic: %+v", none)
	}
	proxy := s.For("coding", "anthropic/claude-haiku-5.5", true, -1)
	if proxy.Sigma <= s.For("coding", "anthropic/claude-haiku-5.5", false, -1).Sigma {
		t.Error("proxy should widen sigma")
	}
}

func TestPriorFamilies(t *testing.T) {
	s := load(t)
	rec := s.For("transcription", "openai/whisper-large-v3-turbo", false, 0)
	alt := s.For("transcription", "nvidia/parakeet-tdt-0.6b-v3", false, 1)
	other := s.For("transcription", "newco/asr", false, -1)
	if !(rec.PFail < alt.PFail && alt.PFail < other.PFail) {
		t.Errorf("catalog prior ordering wrong: %v %v %v", rec.PFail, alt.PFail, other.PFail)
	}
	if other.Sigma <= rec.Sigma {
		t.Error("unlisted model should be wider")
	}
}

func TestBase(t *testing.T) {
	for in, want := range map[string]string{
		"deepseek/deepseek-v4.1-flash-1017": "deepseek/deepseek-v4.1-flash",
		"z-ai/glm-5.3-flash:batch":          "z-ai/glm-5.3-flash",
		"anthropic/claude-sonnet-5.5":       "anthropic/claude-sonnet-5.5",
	} {
		if got := Base(in); got != want {
			t.Errorf("Base(%s) = %s", in, got)
		}
	}
}
