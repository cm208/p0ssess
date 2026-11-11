// Package config resolves p0ssess's files under ~/.config/p0ssess, the OpenRouter key and the registry
// snapshot. Every command and the MCP server use it, so they agree on where things are.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"p0ssess"
	"p0ssess/internal/pme/paths"
)

type Config struct {
	// OpenRouterKeyFile holds the OpenRouter API key (one line). Never logged.
	OpenRouterKeyFile string `json:"openrouter_key_file,omitempty"`
	// Image is the jail image reference; empty means the built-in default.
	Image string `json:"image,omitempty"`
	// SELinux: "relabel" (default, shared :z label on mounts) or "disable" (label=disable).
	SELinux string `json:"selinux,omitempty"`
	// MaxRunCostUSD interrupts a run whose session cost passes this value.
	MaxRunCostUSD float64 `json:"max_run_cost_usd,omitempty"`
	// MaxRuntimeSec interrupts a run that is still busy after this many seconds.
	MaxRuntimeSec int `json:"max_runtime_s,omitempty"`
	// RegistryFile is a models.json snapshot for the models tool.
	RegistryFile string `json:"registry_file,omitempty"`
	// SessionIdleSec is how long a relay session's jail stays up after its last run ends, so a follow-up
	// can continue the session. Default 1800.
	SessionIdleSec int `json:"session_idle_s,omitempty"`
	// BlockedCIDRs are more destinations the jail must never reach, for example the network's own public
	// address when the router forwards ports back into it.
	BlockedCIDRs []string `json:"blocked_cidrs,omitempty"`
	// ZDR makes the jail's key proxy require zero-retention endpoints. Set per run from the budget's
	// privacy mode, never read from config.json.
	ZDR bool `json:"-"`
}

// Dir is ~/.config/p0ssess ($XDG_CONFIG_HOME honored, on macOS too), or $P0SSESS_HOME when set.
func Dir() string { return paths.Default().Config }

func Path(parts ...string) string { return filepath.Join(append([]string{Dir()}, parts...)...) }

// RelayDir holds the relay's state: sessions/, runs/, jails/ and relay-runs.jsonl, under
// ~/.local/state/p0ssess/relay ($XDG_STATE_HOME honored), or $P0SSESS_HOME/state/relay.
func RelayDir() string { return filepath.Join(paths.Default().State, "relay") }

func RelayPath(parts ...string) string {
	return filepath.Join(append([]string{RelayDir()}, parts...)...)
}

func Load() (Config, error) {
	c := Config{}
	b, err := os.ReadFile(Path("config.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &c); err != nil {
			return c, err
		}
	}
	if v := os.Getenv("P0SSESS_OPENROUTER_KEY_FILE"); v != "" {
		c.OpenRouterKeyFile = v
	}
	if c.OpenRouterKeyFile == "" {
		c.OpenRouterKeyFile = Path("secrets", "openrouter_key")
	}
	if c.SELinux == "" {
		c.SELinux = "relabel"
	}
	if c.MaxRunCostUSD == 0 {
		c.MaxRunCostUSD = 0.50
	}
	if c.MaxRuntimeSec == 0 {
		c.MaxRuntimeSec = 3600
	}
	if c.SessionIdleSec == 0 {
		c.SessionIdleSec = 1800
	}
	if !(c.MaxRunCostUSD > 0) || math.IsInf(c.MaxRunCostUSD, 0) {
		return c, errors.New("config.json: max_run_cost_usd must be a finite number above 0")
	}
	if c.MaxRuntimeSec < 0 {
		return c, errors.New("config.json: max_runtime_s must be 0 or more")
	}
	if c.SessionIdleSec < 0 {
		return c, errors.New("config.json: session_idle_s must be 0 or more")
	}
	for _, s := range c.BlockedCIDRs {
		if _, err := netip.ParsePrefix(s); err != nil {
			if _, err := netip.ParseAddr(s); err != nil {
				return c, fmt.Errorf("config.json: blocked_cidrs: %q is not a CIDR or address", s)
			}
		}
	}
	return c, nil
}

// OpenRouterKey reads the key file. The value must never be printed or logged.
func (c Config) OpenRouterKey() (string, error) {
	b, err := os.ReadFile(c.OpenRouterKeyFile)
	if err != nil {
		return "", errors.New("cannot read the OpenRouter key file " + c.OpenRouterKeyFile +
			" (put your key there with mode 0600, or set P0SSESS_OPENROUTER_KEY_FILE or openrouter_key_file in config.json)")
	}
	k := strings.TrimSpace(string(b))
	if k == "" {
		return "", errors.New("the OpenRouter key file is empty")
	}
	return k, nil
}

// RegistryPath finds the models.json snapshot: $P0SSESS_REGISTRY, registry_file in config.json,
// registry/models.json next to the binary's parent directory, or the snapshot built into the binary,
// written once to the cache directory.
func (c Config) RegistryPath() (string, error) {
	if p := os.Getenv("P0SSESS_REGISTRY"); p != "" {
		return p, nil
	}
	if c.RegistryFile != "" {
		return c.RegistryFile, nil
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			p := filepath.Join(filepath.Dir(real), "..", "registry", "models.json")
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	b, err := p0ssess.Data.ReadFile("registry/models.json")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	p := filepath.Join(CacheDir(), "registry-"+hex.EncodeToString(sum[:6])+".json")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", err
	}
	return p, paths.Rename(tmp, p)
}

// CacheDir is ~/.cache/p0ssess ($XDG_CACHE_HOME honored), or $P0SSESS_HOME/cache.
func CacheDir() string { return paths.Default().Cache }
