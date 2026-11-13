package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/paths"
)

func TestOutcomesNoticeOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	eng, err := local.Load(paths.Default())
	if err != nil {
		t.Fatal(err)
	}
	first := outcomesNotice(eng)
	if !strings.Contains(first, `"kind":"image.generate"`) || !strings.Contains(first, "turn off outcome records") {
		t.Fatalf("first summary shows a sample record and the opt-out: %q", first)
	}
	if again := outcomesNotice(eng); again != "" {
		t.Errorf("the notice shows once: %q", again)
	}
	off := t.TempDir()
	t.Setenv("P0SSESS_HOME", off)
	os.WriteFile(filepath.Join(off, "budget.toml"), []byte("[outcomes]\nsend = false\n"), 0o600)
	eng, _ = local.Load(paths.Default())
	if n := outcomesNotice(eng); n != "" {
		t.Errorf("no notice when records are off: %q", n)
	}
}
