package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/pme/paths"
)

func TestSessionEndStartsTheSenderAndReturnsAtOnce(t *testing.T) {
	var started int
	old := startSender
	startSender = func() { started++ }
	t.Cleanup(func() { startSender = old })
	t.Setenv("P0SSESS_HOME", t.TempDir())
	d := paths.Default()

	proj := t.TempDir()
	begin := time.Now()
	if code := sessionEnd(strings.NewReader(`{"hook_event_name":"SessionEnd","reason":"other","cwd":`+quote(proj)+`}`), d); code != 0 || started != 1 {
		t.Fatalf("code %d, started %d", code, started)
	}
	if time.Since(begin) > time.Second {
		t.Error("the hook waited")
	}
	// A bad or empty hook input still sends: the records are not tied to the directory.
	if sessionEnd(strings.NewReader(``), d); started != 2 {
		t.Errorf("started %d", started)
	}
}

func TestSessionEndSendsNothingFromAPrivateRepository(t *testing.T) {
	var started int
	old := startSender
	startSender = func() { started++ }
	t.Cleanup(func() { startSender = old })
	t.Setenv("P0SSESS_HOME", t.TempDir())

	proj := t.TempDir()
	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	sub := filepath.Join(proj, "sub")
	os.Mkdir(sub, 0o700)
	sessionEnd(strings.NewReader(`{"cwd":`+quote(sub)+`}`), paths.Default())
	if started != 0 {
		t.Error("a private repository's session end started an upload")
	}
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }
