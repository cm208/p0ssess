package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/notices"
	"p0ssess/internal/pme/paths"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b), err
}

func TestSavingsCommand(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.Local)
	notices.Append(paths.Default().NoticesFile(), notices.Record{TS: at, Receipt: "(kimi, $0.10)", Model: "moonshotai/kimi-k3.1-pro", Kind: "summarize-long-doc",
		CostUSD: 0.1, ClaudeUSD: 1.1, SavedUSD: 1, Basis: "measured", Tok: &notices.Tokens{Prompt: 1000}})
	out, err := captureStdout(t, func() error { return savingsCmd([]string{"--month", "2026-10"}) })
	if err != nil || !strings.Contains(out, "October 2026") || !strings.Contains(out, "~$1.00 saved against Claude (91%)") || !strings.Contains(out, "kimi-k3.1-pro") {
		t.Fatalf("%v\n%s", err, out)
	}
	out, _ = captureStdout(t, func() error { return savingsCmd([]string{"--month", "2026-09"}) })
	if !strings.Contains(out, "no runs yet") {
		t.Errorf("empty month:\n%s", out)
	}
	if out, _ = captureStdout(t, func() error { return savingsCmd([]string{"--all"}) }); !strings.Contains(out, "all time") {
		t.Errorf("all:\n%s", out)
	}
	if err := savingsCmd([]string{"--month", "October"}); err == nil {
		t.Error("a bad month was accepted")
	}
}
