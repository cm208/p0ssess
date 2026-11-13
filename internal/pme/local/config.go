package local

import (
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/BurntSushi/toml"

	"p0ssess/internal/pme/remote"
)

// Levels holds one value per stakes level, 1 to 4.
type Levels struct {
	S1 float64 `toml:"s1" json:"s1"`
	S2 float64 `toml:"s2" json:"s2"`
	S3 float64 `toml:"s3" json:"s3"`
	S4 float64 `toml:"s4" json:"s4"`
}

func (l Levels) At(stakes int) float64 {
	switch stakes {
	case 1:
		return l.S1
	case 2:
		return l.S2
	case 3:
		return l.S3
	}
	return l.S4
}

// ByVerify holds one value per verification level.
type ByVerify struct {
	None       float64 `toml:"none" json:"none"`
	Mechanical float64 `toml:"mechanical" json:"mechanical"`
	Spot       float64 `toml:"spot" json:"spot"`
	Full       float64 `toml:"full" json:"full"`
}

type LambdaConfig struct {
	// Preset is list, gentle, balanced, eager or custom.
	Preset   string  `toml:"preset" json:"preset"`
	Floor    float64 `toml:"floor" json:"floor"`
	Exponent float64 `toml:"exponent" json:"exponent"`
	// EMin bounds early-window extrapolation: û = u / max(elapsed, e_min).
	EMin float64 `toml:"e_min" json:"e_min"`
	// Beta steepens λ past projected exhaustion when usage credits are off.
	Beta         float64 `toml:"beta" json:"beta"`
	StaleMinutes int     `toml:"stale_minutes" json:"stale_minutes"`
}

// Config is engine.toml: the λ curve, which stays on this machine, and the scoring keys, which are
// sent to the engine only when the user set them.
type Config struct {
	Lambda LambdaConfig `toml:"lambda" json:"lambda"`
	// Harm is the cost of a bad result beyond redoing it, in dollars.
	Harm Levels `toml:"harm" json:"harm"`
	// Ceilings are the highest p_fail (upper bound) a route may have.
	Ceilings Levels `toml:"ceilings" json:"ceilings"`
	// Escape is the share of failures that get past review and cause harm.
	Escape            ByVerify `toml:"escape" json:"escape"`
	OverheadUSD       float64  `toml:"overhead_usd" json:"overhead_usd"`
	MechanicalRetries int      `toml:"mechanical_retries" json:"mechanical_retries"`
	// CashHeadroom multiplies list-price cash for the ceiling.
	CashHeadroom  float64 `toml:"cash_headroom" json:"cash_headroom"`
	Candidates    string  `toml:"candidates" json:"candidates"`
	SubagentModel string  `toml:"subagent_model" json:"subagent_model"`
	DefaultLead   string  `toml:"default_lead" json:"default_lead"`
}

func DefaultConfig() Config {
	return Config{
		Lambda:            LambdaConfig{Preset: "balanced", EMin: 0.15, Beta: 1.0, StaleMinutes: 120},
		Harm:              Levels{S1: 0.01, S2: 0.10, S3: 1.00, S4: 10.00},
		Ceilings:          Levels{S1: 0.80, S2: 0.65, S3: 0.40, S4: 0.20},
		Escape:            ByVerify{None: 1.0, Spot: 0.5, Mechanical: 0.1, Full: 0.2},
		OverheadUSD:       0.05,
		MechanicalRetries: 2,
		CashHeadroom:      1.5,
		Candidates:        "evidence",
		SubagentModel:     "anthropic/claude-haiku-5.5",
		DefaultLead:       "anthropic/claude-sonnet-5.5",
	}
}

// Presets are the λ shapes. None is a sourced fact; each is a stance.
var Presets = map[string]struct {
	Floor, Exponent float64
	Note            string
}{
	"list":     {1, 1, "Claude always at list price (option A)"},
	"gentle":   {0.10, 3, "Claude cheap until a window is nearly spent"},
	"balanced": {0.15, 2, "smooth rise from a 15% floor to list price at projected exhaustion"},
	"eager":    {0.35, 1, "save Claude: linear rise from a 35% floor"},
}

// Shape resolves the preset into floor and exponent.
func (l LambdaConfig) Shape(saveClaude bool) (floor, exponent float64, name string) {
	name = l.Preset
	if saveClaude && name != "list" && name != "custom" {
		name = "eager"
	}
	if name == "custom" {
		return l.Floor, l.Exponent, name
	}
	p, ok := Presets[name]
	if !ok {
		p, name = Presets["balanced"], "balanced"
	}
	return p.Floor, p.Exponent, name
}

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return DefaultConfig(), err
	}
	c, err := LoadConfigFrom(b)
	if err != nil {
		return DefaultConfig(), fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// LoadConfigFrom decodes engine.toml content over the defaults.
func LoadConfigFrom(b []byte) (Config, error) {
	c := DefaultConfig()
	if _, err := toml.Decode(string(b), &c); err != nil {
		return DefaultConfig(), err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	for _, l := range []Levels{c.Harm, c.Ceilings} {
		for _, v := range []float64{l.S1, l.S2, l.S3, l.S4} {
			if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
				return errors.New("harm and ceilings must be finite numbers, 0 or more")
			}
		}
	}
	if c.Ceilings.S1 > 1 || c.Ceilings.S2 > 1 || c.Ceilings.S3 > 1 || c.Ceilings.S4 > 1 {
		return errors.New("ceilings are probabilities (0-1)")
	}
	for _, v := range []float64{c.Escape.None, c.Escape.Mechanical, c.Escape.Spot, c.Escape.Full} {
		if v < 0 || v > 1 || math.IsNaN(v) {
			return errors.New("escape shares are probabilities (0-1)")
		}
	}
	if c.Lambda.Preset == "custom" && (c.Lambda.Floor < 0 || c.Lambda.Floor > 1 || c.Lambda.Exponent <= 0) {
		return errors.New("custom lambda needs 0 <= floor <= 1 and exponent > 0")
	}
	if !(c.CashHeadroom >= 1) || math.IsInf(c.CashHeadroom, 0) {
		return errors.New("cash_headroom must be a finite number, 1 or more")
	}
	if !(c.OverheadUSD >= 0) || math.IsInf(c.OverheadUSD, 0) {
		return errors.New("overhead_usd must be a finite number, 0 or more")
	}
	if c.MechanicalRetries < 0 {
		return errors.New("mechanical_retries must be 0 or more")
	}
	switch c.Candidates {
	case "evidence", "catalog", "all":
	default:
		return fmt.Errorf("candidates must be evidence, catalog or all, not %q", c.Candidates)
	}
	return nil
}

// tuningKeys are the engine.toml keys the engine scores with.
var tuningKeys = []string{"harm", "ceilings", "escape", "overhead_usd", "mechanical_retries", "cash_headroom",
	"candidates", "subagent_model", "default_lead"}

// Tuning returns the scoring keys present in engine.toml, as written there. [lambda] and [statusline]
// never leave the machine. An invalid file yields nothing.
func Tuning(path string) remote.Tuning {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if _, err := LoadConfigFrom(b); err != nil {
		return nil
	}
	doc := map[string]any{}
	if _, err := toml.Decode(string(b), &doc); err != nil {
		return nil
	}
	var out remote.Tuning
	for _, k := range tuningKeys {
		v, ok := doc[k]
		if !ok {
			continue
		}
		if out == nil {
			out = remote.Tuning{}
		}
		out[k] = v
	}
	return out
}
