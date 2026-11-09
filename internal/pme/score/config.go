// Package score is the Predictive Model Engine's cost model. For every
// candidate route it computes the expected cost per accepted result,
//
//	cost = cash + λ·claude + λ·(overhead + review) + p_fail·(redo + harm(stakes))
//
// applies the fit, privacy, rule, budget and stakes filters, and ranks what is
// left. Every number here is a preset the user can override in engine.toml;
// docs/ENGINE.md explains each one.
package score

import (
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/BurntSushi/toml"
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

// ByVerify holds one value per catalog verification level.
type ByVerify struct {
	None       float64 `toml:"none" json:"none"`
	Mechanical float64 `toml:"mechanical" json:"mechanical"`
	Spot       float64 `toml:"spot" json:"spot"`
	Full       float64 `toml:"full" json:"full"`
}

func (v ByVerify) At(level string) float64 {
	switch level {
	case "none":
		return v.None
	case "mechanical":
		return v.Mechanical
	case "full":
		return v.Full
	}
	return v.Spot
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

type SessionShape struct {
	ContextTokens int `toml:"context_tokens" json:"context_tokens"`
	NewInTokens   int `toml:"new_in_tokens" json:"new_in_tokens"`
	OutTokens     int `toml:"out_tokens" json:"out_tokens"`
}

type Media struct {
	AudioTokensPerSec float64 `toml:"audio_tokens_per_sec" json:"audio_tokens_per_sec"`
	VideoTokensPerSec float64 `toml:"video_tokens_per_sec" json:"video_tokens_per_sec"`
	ImageTokens       int     `toml:"image_tokens" json:"image_tokens"`
	PageTokens        int     `toml:"page_tokens" json:"page_tokens"`
}

type Config struct {
	Lambda LambdaConfig `toml:"lambda" json:"lambda"`
	// Harm is the cost of a bad result beyond redoing it, in dollars.
	Harm Levels `toml:"harm" json:"harm"`
	// Ceilings are the highest p_fail (upper bound) a route may have.
	Ceilings Levels `toml:"ceilings" json:"ceilings"`
	// Escape is the share of failures that get past review and cause harm.
	Escape ByVerify `toml:"escape" json:"escape"`
	// ReviewUSD is Claude's fixed checking cost per level (Sonnet list price).
	ReviewUSD ByVerify `toml:"review_usd" json:"review_usd"`
	// ReviewRead is the share of the result Claude reads at each level.
	ReviewRead ByVerify `toml:"review_read" json:"review_read"`
	// OverheadUSD is the lead model's hand-off cost per delegation at Sonnet
	// list price; it scales with the lead model's output price.
	OverheadUSD  float64 `toml:"overhead_usd" json:"overhead_usd"`
	LocalToolUSD float64 `toml:"local_tool_usd" json:"local_tool_usd"`
	// MechanicalRetries is how often a mechanically checked job is retried.
	MechanicalRetries int `toml:"mechanical_retries" json:"mechanical_retries"`
	// ReasoningFactor adds this share of the output as hidden reasoning tokens
	// for models whose reasoning cannot be turned off.
	ReasoningFactor float64 `toml:"reasoning_factor" json:"reasoning_factor"`
	// CashHeadroom multiplies list-price cash for the approval check.
	CashHeadroom float64 `toml:"cash_headroom" json:"cash_headroom"`
	CreditFee    float64 `toml:"credit_fee" json:"credit_fee"`
	// AgentResultTokens is what Claude reads back from an agent session.
	AgentResultTokens int          `toml:"agent_result_tokens" json:"agent_result_tokens"`
	Lead              SessionShape `toml:"lead_session" json:"lead_session"`
	Agent             SessionShape `toml:"agent_session" json:"agent_session"`
	Media             Media        `toml:"media" json:"media"`
	// Candidates is "evidence" (catalog-named or benchmarked models),
	// "catalog" (only the use case's own list) or "all".
	Candidates    string `toml:"candidates" json:"candidates"`
	SubagentModel string `toml:"subagent_model" json:"subagent_model"`
	DefaultLead   string `toml:"default_lead" json:"default_lead"`
}

func Default() Config {
	return Config{
		Lambda:            LambdaConfig{Preset: "balanced", EMin: 0.15, Beta: 1.0, StaleMinutes: 120},
		Harm:              Levels{S1: 0.01, S2: 0.10, S3: 1.00, S4: 10.00},
		Ceilings:          Levels{S1: 0.80, S2: 0.65, S3: 0.40, S4: 0.20},
		Escape:            ByVerify{None: 1.0, Spot: 0.5, Mechanical: 0.1, Full: 0.2},
		ReviewUSD:         ByVerify{None: 0.005, Mechanical: 0.01, Spot: 0.03, Full: 0.06},
		ReviewRead:        ByVerify{None: 0, Mechanical: 0.1, Spot: 0.5, Full: 1.0},
		OverheadUSD:       0.05,
		LocalToolUSD:      0.02,
		MechanicalRetries: 2,
		ReasoningFactor:   1.0,
		CashHeadroom:      1.5,
		CreditFee:         0.055,
		AgentResultTokens: 4000,
		Lead:              SessionShape{ContextTokens: 40000, NewInTokens: 3000, OutTokens: 1000},
		Agent:             SessionShape{ContextTokens: 40000, NewInTokens: 3000, OutTokens: 1000},
		Media:             Media{AudioTokensPerSec: 32, VideoTokensPerSec: 290, ImageTokens: 1500, PageTokens: 2000},
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
		return Default(), nil
	}
	if err != nil {
		return Default(), err
	}
	c, err := LoadConfigFrom(b)
	if err != nil {
		return Default(), fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// LoadConfigFrom decodes engine.toml content over the defaults.
func LoadConfigFrom(b []byte) (Config, error) {
	c := Default()
	if _, err := toml.Decode(string(b), &c); err != nil {
		return Default(), err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	for _, l := range []Levels{c.Harm, c.Ceilings} {
		for _, v := range []float64{l.S1, l.S2, l.S3, l.S4} {
			if v < 0 || math.IsNaN(v) {
				return errors.New("harm and ceilings must be 0 or more")
			}
		}
	}
	if c.Ceilings.S1 > 1 || c.Ceilings.S2 > 1 || c.Ceilings.S3 > 1 || c.Ceilings.S4 > 1 {
		return errors.New("ceilings are probabilities (0-1)")
	}
	if c.Lambda.Preset == "custom" && (c.Lambda.Floor < 0 || c.Lambda.Floor > 1 || c.Lambda.Exponent <= 0) {
		return errors.New("custom lambda needs 0 <= floor <= 1 and exponent > 0")
	}
	switch c.Candidates {
	case "evidence", "catalog", "all":
	default:
		return fmt.Errorf("candidates must be evidence, catalog or all, not %q", c.Candidates)
	}
	return nil
}
