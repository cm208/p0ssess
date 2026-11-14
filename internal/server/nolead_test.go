package server

import (
	"os"
	"strings"
	"testing"

	"p0ssess/internal/direct"
)

// With no Claude lead the direct tool picks a model for jobs the Claude-lead routes keep on Claude.
func TestDirectPicksWithNoLead(t *testing.T) {
	_, eng, proj := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.5\ndaily_usd = -1\n")
	c := direct.New("test-key")
	for _, uc := range []string{"qa-factual", "invoice-receipt-extract"} {
		job := DirectJob{UseCase: uc, Stakes: 3}
		if _, err := pickModels(eng, c, direct.Request{Prompt: "x"}, &job, proj); err == nil || !strings.Contains(err.Error(), "no model fits") {
			t.Fatalf("%s with a lead: %v", uc, err)
		}
	}
	os.WriteFile(eng.Dirs.EngineFile(), []byte("lead = \"none\"\n"), 0o600)
	for uc, want := range map[string]string{"qa-factual": "deepseek/deepseek-v4.1-flash", "invoice-receipt-extract": "google/gemini-3.8-flash"} {
		job := DirectJob{UseCase: uc, Stakes: 3}
		ms, err := pickModels(eng, c, direct.Request{Prompt: "x"}, &job, proj)
		if err != nil || len(ms) == 0 || ms[0] != want {
			t.Fatalf("%s with no lead: %v %v", uc, ms, err)
		}
		if job.OfferID == "" {
			t.Errorf("%s: no offer recorded", uc)
		}
	}
}
