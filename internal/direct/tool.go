package direct

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// The MCP tool for the direct path. It does not depend on an MCP package; the plugin's server registers it
// with ToolName, ToolDescription and ToolSchema, parses arguments with ParseArgs and runs the request
// through its budget guard (server.RunDirect), never through the client alone.

const ToolName = "direct"

const ToolDescription = `For the p0ssess agent: from the main conversation, hand the request to that agent (subagent_type "p0ssess:p0ssess") instead. ` +
	`One OpenRouter call with local files attached by path, for media Claude cannot read or make: ` +
	`watch or describe video, listen to or transcribe audio, generate or edit an image, read a PDF past Claude's limits. ` +
	`Leave model out and it picks the best model for the use case (given, or read from the files and output). ` +
	`It runs without asking when the estimate is within the user's limits; otherwise it answers "needs approval" with the cost. ` +
	`The answer comes back in <answer>, then any generated files as JSON (present them), then one line with the model and cost.`

var ToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "model": {"type": "string", "description": "OpenRouter model id; leave out to let p0ssess pick"},
    "prompt": {"type": "string"},
    "system": {"type": "string"},
    "files": {"type": "array", "items": {"type": "string"}, "description": "absolute local paths: images, PDFs, audio, video or text"},
    "output": {"type": "string", "enum": ["text", "json", "image", "transcript"], "default": "text"},
    "max_cost_usd": {"type": "number", "description": "only when the user named a ceiling: the most this call may spend. Without it the estimate is the ceiling"},
    "max_tokens": {"type": "integer"},
    "strict_zdr": {"type": "boolean", "description": "zero-data-retention endpoints only"},
    "reasoning": {"type": "string", "enum": ["none", "low", "medium", "high"]},
    "pdf_engine": {"type": "string", "enum": ["native", "cloudflare-ai", "mistral-ocr"]},
    "video_processing": {"type": "string", "enum": ["agentic", "static"], "description": "Gemini only"},
    "video_prep": {"type": "string", "enum": ["downscale", "keyframes", "segment"], "description": "local ffmpeg step for videos over the size or length limit; left out, segment (too long) or downscale (too large) is chosen"},
    "timestamps": {"type": "boolean", "description": "transcripts: segment timestamps"},
    "language": {"type": "string"},
    "image_aspect": {"type": "string"},
    "image_resolution": {"type": "string"},
    "out_dir": {"type": "string"},
    "dry_run": {"type": "boolean"}
  },
  "required": ["prompt"],
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
	r := Request{
		Model: a.Model, Prompt: a.Prompt, System: a.System, Files: a.Files, Output: Output(a.Output),
		MaxTokens: a.MaxTokens, StrictZDR: a.StrictZDR, Reasoning: a.Reasoning, PDFEngine: a.PDFEngine,
		VideoProcessing: a.VideoProcessing, VideoPrep: VideoPrep(a.VideoPrep), Timestamps: a.Timestamps,
		Language: a.Language, ImageAspect: a.ImageAspect, ImageResolution: a.ImageResolution,
		OutDir: a.OutDir, DryRun: a.DryRun,
	}
	if a.MaxCostUSD != nil {
		r.MaxCostUSD, r.MaxGiven = *a.MaxCostUSD, true
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
		if r.Usage.BYOK && r.Usage.Priced == 0 {
			fmt.Fprintf(&b, "; bring-your-own-key: OpenRouter $%.6f + provider $%.6f", r.Usage.OpenRouterUSD, r.Usage.UpstreamUSD)
		} else if r.Usage.BYOK {
			b.WriteString("; bring-your-own-key: the provider's charge was not reported, so the tokens are priced at the model's list price")
		}
		if r.Usage.CostBasis != "" {
			fmt.Fprintf(&b, "; cost basis %s", r.Usage.CostBasis)
		}
		b.WriteString("\n")
	}
	for _, w := range r.Warnings {
		b.WriteString("note: " + w + "\n")
	}
	return b.String()
}

// FileHint is a generated file as the tool result lists it, for Claude to present to the user.
type FileHint struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// FileKind sorts a file into image, video, audio, document or file, from its MIME type or name.
func FileKind(path, mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg":
		return "image"
	case ".mp4", ".mov", ".webm", ".mkv", ".m4v":
		return "video"
	case ".mp3", ".wav", ".m4a", ".flac", ".ogg", ".opus", ".aac":
		return "audio"
	case ".pdf", ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".odp", ".csv", ".epub":
		return "document"
	}
	return "file"
}

// Compact renders a result for the MCP tool: the answer, the generated files as one JSON line, and
// one line with the model and cost. tail is appended to that line (the run id).
func Compact(r *Result, tail string) string {
	var b strings.Builder
	b.WriteString("<answer>\n")
	b.WriteString(strings.TrimSpace(r.Text))
	b.WriteString("\n</answer>\n")
	if len(r.Files) > 0 {
		hints := make([]FileHint, len(r.Files))
		for i, f := range r.Files {
			hints[i] = FileHint{Path: f.Path, Kind: FileKind(f.Path, f.MIME)}
		}
		js, _ := json.Marshal(hints)
		fmt.Fprintf(&b, "files: %s\n", js)
	}
	model := r.Model
	if _, after, ok := strings.Cut(model, "/"); ok {
		model = after
	}
	cost := fmt.Sprintf("$%.4f", r.Usage.CostUSD)
	switch {
	case r.Usage.CostSource == "" && r.Estimate.CostUnknown:
		cost = "cost unknown"
	case r.Usage.CostSource == "":
		cost = fmt.Sprintf("about $%.4f", r.Estimate.CostUSD)
	case r.Usage.Unmeasured == 0 && r.Usage.Priced > 0:
		cost = "about " + cost + " (priced from tokens)"
	case !r.Measured():
		cost += " so far"
	}
	b.WriteString(model + " · " + cost + tail + "\n")
	if len(r.Warnings) > 0 {
		b.WriteString("note: " + strings.Join(r.Warnings, "; ") + "\n")
	}
	return b.String()
}
