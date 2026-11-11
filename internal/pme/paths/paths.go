// Package paths resolves where p0ssess keeps its config, cache and state.
//
// P0SSESS_HOME, when set, is the config directory (as for the relay), with
// cache/ and state/ inside it; tests and scratch setups use it. Otherwise the
// XDG variables apply, falling back to ~/.config, ~/.cache and ~/.local/state.
package paths

import (
	"os"
	"path/filepath"
)

type Dirs struct {
	Config string
	Cache  string
	State  string
}

func Default() Dirs {
	if root := os.Getenv("P0SSESS_HOME"); root != "" {
		return Dirs{
			Config: root,
			Cache:  filepath.Join(root, "cache"),
			State:  filepath.Join(root, "state"),
		}
	}
	home, _ := os.UserHomeDir()
	return Dirs{
		Config: filepath.Join(xdg("XDG_CONFIG_HOME", filepath.Join(home, ".config")), "p0ssess"),
		Cache:  filepath.Join(xdg("XDG_CACHE_HOME", filepath.Join(home, ".cache")), "p0ssess"),
		State:  filepath.Join(xdg("XDG_STATE_HOME", filepath.Join(home, ".local", "state")), "p0ssess"),
	}
}

func xdg(name, fallback string) string {
	if v := os.Getenv(name); v != "" && filepath.IsAbs(v) {
		return v
	}
	return fallback
}

func (d Dirs) BudgetFile() string  { return filepath.Join(d.Config, "budget.toml") }
func (d Dirs) EngineFile() string  { return filepath.Join(d.Config, "engine.toml") }
func (d Dirs) RulesFile() string   { return filepath.Join(d.Config, "rules.toml") }
func (d Dirs) UsageFile() string   { return filepath.Join(d.Cache, "usage.json") }
func (d Dirs) ModelsCache() string { return filepath.Join(d.Cache, "openrouter-models.json") }
func (d Dirs) RunningDir() string  { return filepath.Join(d.Cache, "running") }
func (d Dirs) HistoryFile() string { return filepath.Join(d.State, "history.jsonl") }
func (d Dirs) SessionsDir() string { return filepath.Join(d.State, "sessions") }

// SessionRulesFile is where session-scoped rules live.
func (d Dirs) SessionRulesFile(session string) string {
	return filepath.Join(d.SessionsDir(), safe(session), "rules.toml")
}

func safe(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(out) < 80; i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "default"
	}
	return string(out)
}

// ProjectRoot walks up from dir to the nearest directory holding .git or
// .p0ssess. It returns "" when there is none.
func ProjectRoot(dir string) string {
	if dir == "" {
		return ""
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		for _, marker := range []string{".p0ssess", ".git", ".p0ssess-private"} {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ProjectRulesFile is <root>/.p0ssess/rules.toml.
func ProjectRulesFile(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(root, ".p0ssess", "rules.toml")
}

// Private reports whether a .p0ssess-private file in dir or any directory above it blocks delegation
// from dir, so a nested repository or submodule inside a private one is private too.
func Private(dir string) bool {
	if dir == "" {
		return false
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".p0ssess-private")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// WriteFileAtomic writes via a temp file and rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return Rename(tmp.Name(), path)
}
