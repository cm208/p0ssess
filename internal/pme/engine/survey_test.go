package engine

import (
	"fmt"
	"os"
	"testing"

	"p0ssess/internal/pme/paths"
)

// TestSurvey prints the pick for every use case; run with -run Survey -v.
func TestSurvey(t *testing.T) {
	if os.Getenv("PME_SURVEY") == "" {
		t.Skip("set PME_SURVEY=1")
	}
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	if plan := os.Getenv("PME_PLAN"); plan != "" {
		os.MkdirAll(home+"/config", 0o700)
		os.WriteFile(home+"/config/budget.toml", []byte("plan = \""+plan+"\"\n"), 0o600)
	}
	e, err := Load(paths.Default())
	if err != nil {
		t.Fatal(err)
	}
	for _, uc := range e.Cat.UseCases {
		r, err := e.Recommend(Request{UseCase: uc.ID, Scope: Scope{Cwd: home}})
		if err != nil {
			t.Fatal(err)
		}
		line := fmt.Sprintf("%3d %-28s s%d λ%.2f |", uc.N, uc.ID, r.Stakes, r.Lambda.Value)
		for i, o := range r.Options {
			if i >= 3 {
				break
			}
			line += fmt.Sprintf(" %s/%s $%.3f(c%.3f p%.2f)", o.Route, short(o.Model), o.Total, o.Breakdown.Cash, o.PFail)
		}
		cat := "-"
		if uc.Recommended != nil {
			cat = short(*uc.Recommended)
		}
		fmt.Printf("%s  [catalog %s %s]\n", line, uc.Path, cat)
	}
}

func short(s string) string {
	for i := range s {
		if s[i] == '/' {
			return s[i+1:]
		}
	}
	return s
}
