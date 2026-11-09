// Package direct runs one-shot delegated jobs as a single OpenRouter call: text, images, PDFs, audio and
// video in; text, transcripts or generated images out. It is the path for media OpenCode cannot carry.
package direct

import "time"

// Output selects what the call should produce.
type Output string

const (
	OutText       Output = "text"
	OutJSON       Output = "json"       // response_format json_object
	OutImage      Output = "image"      // image generation
	OutTranscript Output = "transcript" // speech-to-text route
)

// VideoPrep is an opt-in local ffmpeg step for videos over the limits.
type VideoPrep string

const (
	PrepNone      VideoPrep = ""
	PrepDownscale VideoPrep = "downscale" // re-encode to a small, 1 fps copy; keeps audio
	PrepKeyframes VideoPrep = "keyframes" // scene-change frames as images, plus the audio track if the model takes audio
	PrepSegment   VideoPrep = "segment"   // split into segments and run one call per segment
)

type Request struct {
	Model  string
	Prompt string
	System string
	Files  []string // local paths; never URLs
	Output Output

	MaxTokens  int     // completion cap (reasoning included); 0 means DefaultMaxTokens
	MaxCostUSD float64 // refuse when the estimate is above this; 0 means no call is made
	StrictZDR  bool    // provider.zdr = true on top of data_collection = deny
	Reasoning  string  // optional reasoning effort ("low", "medium", "high", "none")

	PDFEngine       string // "native", "cloudflare-ai" or "mistral-ocr"; empty picks per model
	VideoProcessing string // Gemini only: "agentic" or "static"
	VideoPrep       VideoPrep
	Timestamps      bool   // transcripts: ask for segment timestamps
	Language        string // transcripts: ISO-639-1 hint

	ImageAspect     string // image generation: e.g. "1:1"
	ImageResolution string // image generation: e.g. "1K"

	OutDir string // where generated files go; empty means a new folder under the state directory
	DryRun bool   // estimate only
}

// Limits are the per-modality input caps. Zero fields take the defaults.
type Limits struct {
	ImageBytes      int64
	PDFBytes        int64
	AudioBytes      int64
	VideoBytes      int64
	VideoSeconds    float64
	TotalBytes      int64
	SegmentSeconds  float64
	MaxKeyframes    int
	SceneThreshold  float64
	DownscaleHeight int
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.ImageBytes == 0 {
		l.ImageBytes = d.ImageBytes
	}
	if l.PDFBytes == 0 {
		l.PDFBytes = d.PDFBytes
	}
	if l.AudioBytes == 0 {
		l.AudioBytes = d.AudioBytes
	}
	if l.VideoBytes == 0 {
		l.VideoBytes = d.VideoBytes
	}
	if l.VideoSeconds == 0 {
		l.VideoSeconds = d.VideoSeconds
	}
	if l.TotalBytes == 0 {
		l.TotalBytes = d.TotalBytes
	}
	if l.SegmentSeconds == 0 {
		l.SegmentSeconds = d.SegmentSeconds
	}
	if l.MaxKeyframes == 0 {
		l.MaxKeyframes = d.MaxKeyframes
	}
	if l.SceneThreshold == 0 {
		l.SceneThreshold = d.SceneThreshold
	}
	if l.DownscaleHeight == 0 {
		l.DownscaleHeight = d.DownscaleHeight
	}
	return l
}

func DefaultLimits() Limits {
	return Limits{
		ImageBytes:      20 << 20,
		PDFBytes:        32 << 20,
		AudioBytes:      25 << 20,
		VideoBytes:      20 << 20,
		VideoSeconds:    300,
		TotalBytes:      48 << 20,
		SegmentSeconds:  300,
		MaxKeyframes:    24,
		SceneThreshold:  0.3,
		DownscaleHeight: 360,
	}
}

const DefaultMaxTokens = 4096

type OutFile struct {
	Path  string `json:"path"`
	MIME  string `json:"mime"`
	Bytes int64  `json:"bytes"`
}

type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	ReasoningTokens  int     `json:"reasoning_tokens,omitempty"`
	AudioTokens      int     `json:"audio_tokens,omitempty"`
	AudioSeconds     float64 `json:"audio_seconds,omitempty"`
	CostUSD          float64 `json:"cost_usd"`
	CostSource       string  `json:"cost_source"` // "usage", "generation" or "unknown"
}

type Result struct {
	Text     string        `json:"text"`
	Files    []OutFile     `json:"files,omitempty"`
	Model    string        `json:"model"`
	Provider string        `json:"provider,omitempty"`
	IDs      []string      `json:"generation_ids,omitempty"`
	Usage    Usage         `json:"usage"`
	Estimate Estimate      `json:"estimate"`
	Route    string        `json:"route"`
	Warnings []string      `json:"warnings,omitempty"`
	Elapsed  time.Duration `json:"elapsed_ns"`
}
