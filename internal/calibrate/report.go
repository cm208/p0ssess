package calibrate

import "time"

// Report is the savings report: per task kind, what a correct answer cost and how long it took, Claude
// alone against Claude with p0ssess. Every number comes from a measured run on this machine. A side
// with no runs is nil, and the page shows an empty state for it; nothing is ever filled in.
type Report struct {
	// Pro is false when the install is on the free plan; the page then explains the report is part of Pro.
	Pro         bool      `json:"pro"`
	GeneratedAt time.Time `json:"generated_at"`
	// Calibrated is when the last calibration run finished; zero when there was none.
	Calibrated time.Time `json:"calibrated,omitzero"`
	// Baselined is when the last Claude baseline run finished; zero when there was none.
	Baselined time.Time `json:"baselined,omitzero"`
	// Kinds has one row per task kind that has any measured run, sorted by use-case id.
	Kinds []KindRow `json:"kinds"`
	// Models is the calibration table: every model measured on every kind.
	Models []ModelRow `json:"models"`
	// Totals sums every kind where both sides have at least one correct answer.
	Totals *Totals `json:"totals,omitempty"`
	// Prices are the Claude API prices the dollar column uses, per million tokens.
	Prices []Price `json:"prices"`
	// Notes are short sentences about how the numbers were measured, for the page footer and the PDF.
	Notes []string `json:"notes"`
}

// KindRow is one task kind.
type KindRow struct {
	UseCase string `json:"use_case"`
	// Task is the catalog's short description of the kind.
	Task  string `json:"task"`
	Alone *Side  `json:"alone,omitempty"`
	With  *Side  `json:"with,omitempty"`
	// Model is the model p0ssess routed this kind to on the "with" side.
	Model string `json:"model,omitempty"`
}

// Side is one way of doing a kind's samples.
type Side struct {
	Runs    int `json:"runs"`
	Correct int `json:"correct"`
	// CostPerCorrectUSD is all the money spent on the side's runs divided by its correct answers: Claude
	// at API prices plus OpenRouter. Nil when no answer was correct.
	CostPerCorrectUSD *float64 `json:"cost_per_correct_usd,omitempty"`
	// ClaudeUSD and OpenRouterUSD split the side's total spend.
	ClaudeUSD     float64 `json:"claude_usd"`
	OpenRouterUSD float64 `json:"openrouter_usd"`
	// MeanSeconds is the average wall-clock time from start to answer.
	MeanSeconds float64 `json:"mean_seconds"`
	// WeeklySharePerCorrect is the fraction of Claude's weekly subscription limit used per correct
	// answer (0.004 is 0.4%). Nil when the weekly meter was not read during the runs.
	WeeklySharePerCorrect *float64 `json:"weekly_share_per_correct,omitempty"`
	// Source is "baseline" for headless Claude Code runs, or "calibration" for the p0ssess side when
	// only the routed model's own calls were measured (Claude's handoff is then not in the numbers).
	Source string `json:"source"`
}

// ModelRow is one model measured on one kind by calibration.
type ModelRow struct {
	UseCase           string   `json:"use_case"`
	Model             string   `json:"model"`
	Runs              int      `json:"runs"`
	Correct           int      `json:"correct"`
	CostPerCorrectUSD *float64 `json:"cost_per_correct_usd,omitempty"`
	MeanSeconds       float64  `json:"mean_seconds"`
	// Picked is true for the model p0ssess routes this kind to now.
	Picked bool `json:"picked,omitempty"`
}

// Totals compares the kinds where both sides got at least one answer right.
type Totals struct {
	Kinds int `json:"kinds"`
	// AloneUSD and WithUSD are the summed cost per correct answer over those kinds (one answer each).
	AloneUSD float64 `json:"alone_usd"`
	WithUSD  float64 `json:"with_usd"`
	// AloneSeconds and WithSeconds are the summed mean times over those kinds.
	AloneSeconds float64 `json:"alone_seconds"`
	WithSeconds  float64 `json:"with_seconds"`
	// WeeklyShareSaved is the summed weekly-limit share per correct answer saved; nil unless both sides
	// of every counted kind were measured.
	WeeklyShareSaved *float64 `json:"weekly_share_saved,omitempty"`
}

// Price is one Claude model's API price, dollars per million tokens.
type Price struct {
	Model     string  `json:"model"`
	In        float64 `json:"in"`
	Out       float64 `json:"out"`
	CacheRead float64 `json:"cache_read"`
}
