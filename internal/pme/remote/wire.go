package remote

// Request is the body of POST /v1/recommend. The server rejects unknown fields, so nothing is added
// here that the contract does not list.
type Request struct {
	V         int          `json:"v"`
	UseCase   string       `json:"use_case,omitempty"`
	Profile   *Profile     `json:"profile,omitempty"`
	Stakes    int          `json:"stakes"`
	LeadModel string       `json:"lead_model,omitempty"`
	Lambda    float64      `json:"lambda"`
	Top       int          `json:"top,omitempty"`
	Rules     []Rule       `json:"rules"`
	Lists     Lists        `json:"lists"`
	Privacy   Privacy      `json:"privacy"`
	Tuning    Tuning       `json:"tuning,omitempty"`
	History   []HistoryRow `json:"history"`
}

// Profile describes a task: a short description Claude writes, modalities and sizes.
type Profile struct {
	Text       string   `json:"text,omitempty"`
	Inputs     []string `json:"inputs,omitempty"`
	Outputs    []string `json:"outputs,omitempty"`
	Path       string   `json:"path,omitempty"`
	Size       string   `json:"size,omitempty"`
	InTokens   int      `json:"in_tokens,omitempty"`
	OutTokens  int      `json:"out_tokens,omitempty"`
	Bytes      int      `json:"bytes,omitempty"`
	Turns      int      `json:"turns,omitempty"`
	Items      int      `json:"items,omitempty"`
	AudioSec   float64  `json:"audio_seconds,omitempty"`
	VideoSec   float64  `json:"video_seconds,omitempty"`
	Images     int      `json:"images,omitempty"`
	Pages      int      `json:"pages,omitempty"`
	Chars      int      `json:"chars,omitempty"`
	GenSec     float64  `json:"gen_seconds,omitempty"`
	NeedsTools bool     `json:"needs_tools,omitempty"`
}

// Rule is one forbid, allow, only or mix rule as the server sees it.
type Rule struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	Kind      string    `json:"kind"`
	Target    string    `json:"target,omitempty"`
	Models    []string  `json:"models,omitempty"`
	Weights   []float64 `json:"weights,omitempty"`
	UseCases  []string  `json:"use_cases,omitempty"`
	Kinds     []string  `json:"kinds,omitempty"`
	MaxStakes int       `json:"max_stakes,omitempty"`
}

type Lists struct {
	ModelsAllow    []string `json:"models_allow"`
	ModelsDeny     []string `json:"models_deny"`
	ProvidersAllow []string `json:"providers_allow"`
	ProvidersDeny  []string `json:"providers_deny"`
}

type Privacy struct {
	Mode                 string `json:"mode"`
	AllowSafetyRetention bool   `json:"allow_safety_retention"`
}

// Tuning holds the scoring keys the user changed in engine.toml, as written there.
type Tuning map[string]any

// HistoryRow is the user's outcome counts for one use case and model.
type HistoryRow struct {
	UseCase    string `json:"use_case"`
	Model      string `json:"model"`
	Accepted   int    `json:"accepted"`
	Redo       int    `json:"redo"`
	Declined   int    `json:"declined"`
	Overridden int    `json:"overridden"`
}

// Response is the 200 answer of POST /v1/recommend.
type Response struct {
	V              int            `json:"v"`
	UseCase        UseCase        `json:"use_case"`
	Classification []Match        `json:"classification,omitempty"`
	Stakes         int            `json:"stakes"`
	StakesNote     string         `json:"stakes_note,omitempty"`
	Work           Work           `json:"work"`
	LeadModel      string         `json:"lead_model"`
	Pick           int            `json:"pick"`
	Announce       string         `json:"announce"`
	Options        []Option       `json:"options"`
	Excluded       []Exclusion    `json:"excluded,omitempty"`
	ClaudeFeasible bool           `json:"claude_feasible"`
	StakesCeiling  float64        `json:"stakes_ceiling"`
	SecondOpinion  *SecondOpinion `json:"second_opinion,omitempty"`
	Notes          []string       `json:"notes,omitempty"`
	DataAsOf       string         `json:"data_as_of,omitempty"`
	// Plan is the P0ssess-Plan response header.
	Plan string `json:"-"`
}

type UseCase struct {
	ID          string `json:"id"`
	N           int    `json:"n"`
	Task        string `json:"task"`
	Kind        string `json:"kind"`
	Family      string `json:"family"`
	Path        string `json:"path"`
	Verify      string `json:"verify"`
	Method      string `json:"verify_method"`
	StakesRange []int  `json:"stakes_range"`
	QualityBar  string `json:"quality_bar"`
	EditsCode   bool   `json:"edits_code"`
	Preprocess  string `json:"preprocess,omitempty"`
}

type Match struct {
	ID         string  `json:"id"`
	Confidence float64 `json:"confidence"`
	Why        string  `json:"why"`
}

// Work is the size of one run.
type Work struct {
	InTokens  int     `json:"in_tokens,omitempty"`
	OutTokens int     `json:"out_tokens,omitempty"`
	Turns     int     `json:"turns,omitempty"`
	Items     int     `json:"items,omitempty"`
	AudioSec  float64 `json:"audio_seconds,omitempty"`
	VideoSec  float64 `json:"video_seconds,omitempty"`
	Images    int     `json:"images,omitempty"`
	Pages     int     `json:"pages,omitempty"`
	Chars     int     `json:"chars,omitempty"`
	GenSec    float64 `json:"gen_seconds,omitempty"`
}

type Breakdown struct {
	Cash     float64 `json:"cash"`
	Claude   float64 `json:"claude"`
	Overhead float64 `json:"overhead"`
	Review   float64 `json:"review"`
	Risk     float64 `json:"risk"`
	Friction float64 `json:"friction,omitempty"`
}

// Option is one ranked route. The approval fields are never sent by the server; the client fills them.
type Option struct {
	Rank            int       `json:"rank"`
	Route           string    `json:"route"`
	Model           string    `json:"model"`
	Total           float64   `json:"total_usd"`
	Breakdown       Breakdown `json:"breakdown"`
	PFail           float64   `json:"p_fail"`
	PFailUpper      float64   `json:"p_fail_upper"`
	Evidence        string    `json:"evidence,omitempty"`
	History         string    `json:"history,omitempty"`
	CashMax         float64   `json:"cash_max_usd"`
	EstimateUSD     float64   `json:"estimate_usd,omitempty"`
	ClaudeListUSD   float64   `json:"claude_list_usd"`
	NeedsApproval   bool      `json:"needs_approval"`
	ApprovalReasons []string  `json:"approval_reasons,omitempty"`
	ApprovalID      string    `json:"standing_approval,omitempty"`
	Review          string    `json:"review"`
	Reason          string    `json:"reason"`
	Notes           []string  `json:"notes,omitempty"`
	PriceSource     string    `json:"price_source,omitempty"`
	MixNext         bool      `json:"mix_next,omitempty"`
}

type Exclusion struct {
	Model  string `json:"model"`
	Route  string `json:"route,omitempty"`
	Why    string `json:"why"`
	RuleID string `json:"rule_id,omitempty"`
}

// SecondOpinion is the server's part of the cross-family review; the client adds approval.
type SecondOpinion struct {
	UseCase    string   `json:"use_case"`
	Models     []string `json:"models"`
	CashUSD    float64  `json:"cash_usd"`
	CashMaxUSD float64  `json:"cash_max_usd"`
}
