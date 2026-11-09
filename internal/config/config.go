// Package config resolves p0ssess's files under ~/.config/p0ssess.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
}

// Dir is ~/.config/p0ssess, or $P0SSESS_HOME when set.
func Dir() string {
	if d := os.Getenv("P0SSESS_HOME"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(base, "p0ssess")
}

func Path(parts ...string) string { return filepath.Join(append([]string{Dir()}, parts...)...) }

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
	return c, nil
}

// OpenRouterKey reads the key file. The value must never be printed or logged.
func (c Config) OpenRouterKey() (string, error) {
	b, err := os.ReadFile(c.OpenRouterKeyFile)
	if err != nil {
		return "", errors.New("cannot read the OpenRouter key file (set openrouter_key_file in config.json)")
	}
	k := strings.TrimSpace(string(b))
	if k == "" {
		return "", errors.New("the OpenRouter key file is empty")
	}
	return k, nil
}
