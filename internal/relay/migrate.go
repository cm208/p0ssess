package relay

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"p0ssess/internal/config"
	"p0ssess/internal/pme/paths"
)

// Up to 0.3 the relay kept its state in the config directory. It now lives in the state directory:
//
//	~/.config/p0ssess/sessions/       -> ~/.local/state/p0ssess/relay/sessions/
//	~/.config/p0ssess/runs/           -> ~/.local/state/p0ssess/relay/runs/
//	~/.config/p0ssess/history.jsonl   -> ~/.local/state/p0ssess/relay/relay-runs.jsonl
//	~/.config/p0ssess/jails/          -> ~/.local/state/p0ssess/relay/legacy-jails/ (0.3's shared jails)
//
// Migrate renames each old path into place once, under a lock, when the new one does not exist yet. When
// a rename fails (the two directories on different filesystems, say) the old path stays and reads fall
// back to it. The fallback is kept through 0.4.x and goes in 0.5.0.

type move struct{ old, new string }

func moves() []move {
	return []move{
		{config.Path("sessions"), config.RelayPath("sessions")},
		{config.Path("runs"), config.RelayPath("runs")},
		{config.Path("history.jsonl"), config.RelayPath("relay-runs.jsonl")},
		{config.Path("jails"), config.RelayPath("legacy-jails")},
	}
}

// rename is os.Rename; tests replace it to simulate a cross-device move.
var rename = os.Rename

var (
	migrateMu   sync.Mutex
	migratedFor = map[string]bool{}
)

// Migrate moves 0.3's relay state into the state directory. It is safe to call from any number of
// processes at once and cheap after the first call.
func Migrate(log io.Writer) error {
	dir := config.RelayDir()
	migrateMu.Lock()
	defer migrateMu.Unlock()
	if migratedFor[dir] {
		return nil
	}
	var errs []error
	err := paths.WithLock(filepath.Join(dir, ".migrate"), func() error {
		for _, m := range moves() {
			if _, err := os.Lstat(m.old); err != nil {
				continue
			}
			if _, err := os.Lstat(m.new); err == nil {
				continue
			}
			if err := rename(m.old, m.new); err != nil {
				errs = append(errs, fmt.Errorf("%s stays where it is and is still read from there: %w", m.old, err))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	migratedFor[dir] = true
	if len(errs) > 0 && log != nil {
		fmt.Fprintln(log, "p0ssess: moving relay state to", dir+":", errors.Join(errs...))
	}
	return nil
}

// legacySessionFile is where 0.3 kept a session record. Read fallback; remove in 0.5.0.
func legacySessionFile(sid string) string { return config.Path("sessions", sid+".json") }
