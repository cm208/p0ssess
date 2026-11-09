package relay

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"p0ssess/internal/config"
)

// oldLayout writes 0.3's relay state into the config directory.
func oldLayout(t *testing.T) {
	t.Helper()
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(config.Path("sessions", "ses_old.json"), `{"session_id":"ses_old","repo":"/r","mode":"read","model":"x/y"}`)
	write(config.Path("runs", "ses_old", "transcript.md"), "# old\n")
	write(config.Path("history.jsonl"), `{"model":"x/y"}`+"\n")
	write(config.Path("jails", "p0ssess-0123456789.json"), `{}`)
}

func useHome(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	t.Cleanup(func() { migrateMu.Lock(); migratedFor = map[string]bool{}; migrateMu.Unlock() })
}

func TestMigrateMovesOldState(t *testing.T) {
	useHome(t)
	oldLayout(t)
	if err := Migrate(nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{config.RelayPath("sessions", "ses_old.json"), config.RelayPath("runs", "ses_old", "transcript.md"),
		config.RelayPath("relay-runs.jsonl"), config.RelayPath("legacy-jails", "p0ssess-0123456789.json")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("not moved: %v", err)
		}
	}
	for _, p := range []string{config.Path("sessions"), config.Path("runs"), config.Path("history.jsonl"), config.Path("jails")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s is still there", p)
		}
	}
	if rec, err := loadRecord("ses_old"); err != nil || rec.Model != "x/y" {
		t.Errorf("moved record: %v %+v", err, rec)
	}
}

// A new path that already exists is never overwritten, and a second run changes nothing.
func TestMigrateKeepsNewState(t *testing.T) {
	useHome(t)
	oldLayout(t)
	os.MkdirAll(config.RelayPath("sessions"), 0o700)
	os.WriteFile(config.RelayPath("sessions", "ses_new.json"), []byte(`{"session_id":"ses_new"}`), 0o600)
	if err := Migrate(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config.RelayPath("sessions", "ses_old.json")); err == nil {
		t.Error("the old sessions directory was merged over the new one")
	}
	if _, err := os.Stat(config.RelayPath("sessions", "ses_new.json")); err != nil {
		t.Error("the new session record is gone")
	}
	// Both stay readable: the new one from the state directory, the old one through the fallback.
	for _, sid := range []string{"ses_new", "ses_old"} {
		if _, err := loadRecord(sid); err != nil {
			t.Errorf("%s: %v", sid, err)
		}
	}
}

// When a rename fails (another filesystem), the old files stay and are still read.
func TestMigrateFallsBackWhenRenameFails(t *testing.T) {
	useHome(t)
	oldLayout(t)
	rename = func(string, string) error { return &os.LinkError{Op: "rename", Err: syscall.EXDEV} }
	defer func() { rename = os.Rename }()
	var log strings.Builder
	if err := Migrate(&log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "still read from there") {
		t.Errorf("no notice: %q", log.String())
	}
	if _, err := os.Stat(config.Path("sessions", "ses_old.json")); err != nil {
		t.Fatal("the old record moved although the rename failed")
	}
	rec, err := loadRecord("ses_old")
	if err != nil || rec.SessionID != "ses_old" {
		t.Errorf("fallback read: %v %+v", err, rec)
	}
	if _, err := loadRecord("../sessions/ses_old"); err == nil {
		t.Error("a session id named a path")
	}
}

// Many goroutines and several processes migrate at once: each old path moves exactly once and none fails.
func TestMigrateConcurrent(t *testing.T) {
	if os.Getenv("P0SSESS_MIGRATE_HELPER") == "1" {
		if err := Migrate(os.Stderr); err != nil {
			os.Exit(1)
		}
		os.WriteFile(filepath.Join(os.Getenv("P0SSESS_HOME"), "helper-"+strconv.Itoa(os.Getpid())), nil, 0o600)
		os.Exit(0)
	}
	useHome(t)
	oldLayout(t)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestMigrateConcurrent$")
			cmd.Env = append(os.Environ(), "P0SSESS_MIGRATE_HELPER=1")
			if out, err := cmd.CombinedOutput(); err != nil {
				errs <- errors.New(string(out))
			}
		}()
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Migrate(nil); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if ran, _ := filepath.Glob(filepath.Join(os.Getenv("P0SSESS_HOME"), "helper-*")); len(ran) != 6 {
		t.Errorf("%d of 6 helper processes ran", len(ran))
	}
	b, err := os.ReadFile(config.RelayPath("sessions", "ses_old.json"))
	if err != nil || !strings.Contains(string(b), "ses_old") {
		t.Errorf("record after concurrent migration: %v", err)
	}
	if _, err := os.Stat(config.RelayPath("sessions", "sessions")); err == nil {
		t.Error("a second rename nested the directory")
	}
}
