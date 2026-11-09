package direct

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The MCP tool for the direct path. It does not depend on an MCP package; the plugin's server registers it
// with ToolName, ToolDescription and ToolSchema, parses arguments with ParseArgs and runs the request
// through its budget guard (server.RunDirect), never through the client alone.

const ToolName = "direct"

const ToolDescription = `Run a one-shot job on an OpenRouter model in a single call, with local files attached by path. ` +
	`Use it for media Claude cannot read or make: describe or transcribe video and audio, read PDFs, caption images, generate an image. ` +
	`Files are checked locally (secret-looking names and .p0ssess-private repositories are refused) and the cost is estimated before sending; ` +
	`the call is refused when the estimate is above max_cost_usd. max_cost_usd is checked against every limit (rules, allow and deny lists, ` +
	`stakes, privacy mode, the per-task, daily and monthly limits) and reserved before sending; above the per-task limit with no standing ` +
	`approval the result is "needs approval": ask the user. Set dry_run to get the estimate only. ` +
	`The answer comes back inside <answer>, followed by any output files, the model, provider and cost.`

var ToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "model": {"type": "string", "description": "OpenRouter model id, e.g. google/gemini-3.8-flash"},
    "prompt": {"type": "string"},
    "system": {"type": "string"},
    "files": {"type": "array", "items": {"type": "string"}, "description": "absolute local paths: images, PDFs, audio, video or text"},
    "output": {"type": "string", "enum": ["text", "json", "image", "transcript"], "default": "text"},
    "max_cost_usd": {"type": "number", "description": "the most this call may spend: refused when the estimate is above it, reserved against your limits; must be above 0 when the model has no known price"},
    "max_tokens": {"type": "integer"},
    "strict_zdr": {"type": "boolean", "description": "zero-data-retention endpoints only"},
    "reasoning": {"type": "string", "enum": ["none", "low", "medium", "high"]},
    "pdf_engine": {"type": "string", "enum": ["native", "cloudflare-ai", "mistral-ocr"]},
    "video_processing": {"type": "string", "enum": ["agentic", "static"], "description": "Gemini only"},
    "video_prep": {"type": "string", "enum": ["downscale", "keyframes", "segment"], "description": "local ffmpeg step for videos over the size or length limit"},
    "timestamps": {"type": "boolean", "description": "transcripts: segment timestamps"},
    "language": {"type": "string"},
    "image_aspect": {"type": "string"},
    "image_resolution": {"type": "string"},
    "out_dir": {"type": "string"},
    "dry_run": {"type": "boolean"}
  },
  "required": ["model", "prompt", "max_cost_usd"],
  "additionalProperties": false
}`)

type toolArgs struct {
	Model           string   `json:"model"`
	Prompt          string   `json:"prompt"`
	System          string   `json:"system"`
	Files           []string `json:"files"`
	Output          string   `json:"output"`
	MaxCostUSD      *float64 `json:"max_cost_usd"`
	MaxTokens       int      `json:"max_tokens"`
	StrictZDR       bool     `json:"strict_zdr"`
	Reasoning       string   `json:"reasoning"`
	PDFEngine       string   `json:"pdf_engine"`
	VideoProcessing string   `json:"video_processing"`
	VideoPrep       string   `json:"video_prep"`
	Timestamps      bool     `json:"timestamps"`
	Language        string   `json:"language"`
	ImageAspect     string   `json:"image_aspect"`
	ImageResolution string   `json:"image_resolution"`
	OutDir          string   `json:"out_dir"`
	DryRun          bool     `json:"dry_run"`
}

// ParseArgs turns tool arguments into a Request.
func ParseArgs(raw json.RawMessage) (Request, error) {
	var a toolArgs
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return Request{}, fmt.Errorf("bad arguments: %w", err)
	}
	if a.MaxCostUSD == nil && !a.DryRun {
		return Request{}, fmt.Errorf("max_cost_usd is required")
	}
	r := Request{
		Model: a.Model, Prompt: a.Prompt, System: a.System, Files: a.Files, Output: Output(a.Output),
		MaxTokens: a.MaxTokens, StrictZDR: a.StrictZDR, Reasoning: a.Reasoning, PDFEngine: a.PDFEngine,
		VideoProcessing: a.VideoProcessing, VideoPrep: VideoPrep(a.VideoPrep), Timestamps: a.Timestamps,
		Language: a.Language, ImageAspect: a.ImageAspect, ImageResolution: a.ImageResolution,
		OutDir: a.OutDir, DryRun: a.DryRun,
	}
	if a.MaxCostUSD != nil {
		r.MaxCostUSD = *a.MaxCostUSD
	}
	return r, nil
}

// Format renders a result: answer first, then files, then the accounting line.
func Format(r *Result) string {
	var b strings.Builder
	b.WriteString("<answer>\n")
	b.WriteString(strings.TrimSpace(r.Text))
	b.WriteString("\n</answer>\n")
	if len(r.Files) > 0 {
		b.WriteString("files:\n")
		for _, f := range r.Files {
			fmt.Fprintf(&b, "- %s (%s, %s)\n", f.Path, f.MIME, human(f.Bytes))
		}
	}
	fmt.Fprintf(&b, "model: %s", r.Model)
	if r.Provider != "" {
		fmt.Fprintf(&b, " via %s", r.Provider)
	}
	est := fmt.Sprintf("$%.4f", r.Estimate.CostUSD)
	if r.Estimate.CostUnknown {
		est = "unknown"
	}
	if r.Usage.CostSource == "" {
		fmt.Fprintf(&b, "; estimate %s\n", est)
	} else {
		fmt.Fprintf(&b, "; cost $%.6f (%s; estimate was %s); tokens in %d, out %d",
			r.Usage.CostUSD, r.Usage.CostSource, est, r.Usage.PromptTokens, r.Usage.CompletionTokens)
		if r.Usage.ReasoningTokens > 0 {
			fmt.Fprintf(&b, " (%d reasoning)", r.Usage.ReasoningTokens)
		}
		if r.Usage.BYOK {
			fmt.Fprintf(&b, "; bring-your-own-key: OpenRouter $%.6f + provider $%.6f", r.Usage.OpenRouterUSD, r.Usage.UpstreamUSD)
		}
		b.WriteString("\n")
	}
	for _, w := range r.Warnings {
		b.WriteString("note: " + w + "\n")
	}
	return b.String()
}
