package direct

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// The MCP tool for the direct path. It does not depend on an MCP package; a server registers it with
// ToolName, ToolDescription and ToolSchema, and calls CallTool with the raw arguments.

const ToolName = "direct"

const ToolDescription = `Run a one-shot job on an OpenRouter model in a single call, with local files attached by path. ` +
	`Use it for media Claude cannot read or make: describe or transcribe video and audio, read PDFs, caption images, generate an image. ` +
	`Files are checked locally (secret-looking names and .p0ssess-private repositories are refused) and the cost is estimated before sending; ` +
	`the call is refused when the estimate is above max_cost_usd. Set dry_run to get the estimate only. ` +
	`The answer comes back inside <answer>, followed by any output files, the model, provider and cost.`

var ToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "model": {"type": "string", "description": "OpenRouter model id, e.g. google/gemini-3.8-flash"},
    "prompt": {"type": "string"},
    "system": {"type": "string"},
    "files": {"type": "array", "items": {"type": "string"}, "description": "absolute local paths: images, PDFs, audio, video or text"},
    "output": {"type": "string", "enum": ["text", "json", "image", "transcript"], "default": "text"},
    "max_cost_usd": {"type": "number", "description": "refuse when the estimate is above this"},
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

// CallTool runs the tool and returns the text for the MCP result.
func (c *Client) CallTool(ctx context.Context, raw json.RawMessage) (text string, isError bool) {
	req, err := ParseArgs(raw)
	if err != nil {
		return err.Error(), true
	}
	res, err := c.Run(ctx, req)
	if err != nil {
		if res != nil && res.Usage.CostUSD > 0 {
			return fmt.Sprintf("%v\n(spent $%.6f before the error)", err, res.Usage.CostUSD), true
		}
		return err.Error(), true
	}
	return Format(res), false
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
	if r.Usage.CostSource == "" {
		fmt.Fprintf(&b, "; estimate $%.4f\n", r.Estimate.CostUSD)
	} else {
		fmt.Fprintf(&b, "; cost $%.6f (%s; estimate was $%.4f); tokens in %d, out %d",
			r.Usage.CostUSD, r.Usage.CostSource, r.Estimate.CostUSD, r.Usage.PromptTokens, r.Usage.CompletionTokens)
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
