package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"p0ssess/internal/config"
	"p0ssess/internal/direct"
	"p0ssess/internal/pme/engine"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/server"
)

const directUsage = `usage:
  p0ssess direct run      --model ID --prompt TEXT [--file PATH]... --max-cost USD [options]
  p0ssess direct estimate --model ID --prompt TEXT [--file PATH]... [options]
  p0ssess direct tool     < args.json          (the MCP tool, for testing)
  p0ssess direct probe    FILE
  p0ssess direct frames   FILE --out DIR [--every S | --scenes [--threshold T]] [--max N]
  p0ssess direct frame    FILE --at S --out FILE.jpg
  p0ssess direct downscale FILE --out FILE.mp4 [--height H] [--fps F]
  p0ssess direct split    FILE --out DIR --seconds S
  p0ssess direct audio    FILE --out FILE.mp3

run/estimate options:
  --output text|json|image|transcript   --system TEXT   --max-tokens N   --zdr
  --reasoning none|low|medium|high      --pdf-engine native|cloudflare-ai|mistral-ocr
  --video-prep downscale|keyframes|segment   --video-processing agentic|static
  --timestamps   --language CODE   --aspect R   --resolution R   --out DIR   --json
`

type files []string

func (f *files) String() string     { return strings.Join(*f, ",") }
func (f *files) Set(v string) error { *f = append(*f, v); return nil }

func directCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, directUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "run", "estimate":
		return directRun(ctx, args[0], args[1:])
	case "tool":
		return directTool(ctx)
	case "probe", "frames", "frame", "downscale", "split", "audio":
		return mediaCmd(ctx, args[0], args[1:])
	}
	fmt.Fprint(os.Stderr, directUsage)
	os.Exit(2)
	return nil
}

func directRun(ctx context.Context, mode string, args []string) error {
	fs := flag.NewFlagSet(mode, flag.ExitOnError)
	var fl files
	fs.Var(&fl, "file", "attach a local file (repeatable)")
	model := fs.String("model", "", "OpenRouter model id")
	prompt := fs.String("prompt", "", "prompt text")
	system := fs.String("system", "", "system prompt")
	output := fs.String("output", "text", "text, json, image or transcript")
	maxCost := fs.Float64("max-cost", 0, "refuse when the estimate is above this (USD)")
	maxTokens := fs.Int("max-tokens", 0, "completion cap")
	zdr := fs.Bool("zdr", false, "zero-data-retention endpoints only")
	reasoning := fs.String("reasoning", "", "reasoning effort")
	pdfEngine := fs.String("pdf-engine", "", "PDF engine")
	videoPrep := fs.String("video-prep", "", "downscale, keyframes or segment")
	videoProc := fs.String("video-processing", "", "agentic or static (Gemini)")
	timestamps := fs.Bool("timestamps", false, "transcript timestamps")
	language := fs.String("language", "", "transcript language")
	aspect := fs.String("aspect", "", "image aspect ratio")
	resolution := fs.String("resolution", "", "image resolution")
	out := fs.String("out", "", "output directory")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	useCase := fs.String("use-case", "", "catalog use case id")
	stakes := fs.Int("stakes", 0, "stakes 1-4")
	fs.Parse(args)
	req := direct.Request{
		Model: *model, Prompt: *prompt, System: *system, Files: fl, Output: direct.Output(*output),
		MaxTokens: *maxTokens, MaxCostUSD: *maxCost, StrictZDR: *zdr, Reasoning: *reasoning,
		PDFEngine: *pdfEngine, VideoPrep: direct.VideoPrep(*videoPrep), VideoProcessing: *videoProc,
		Timestamps: *timestamps, Language: *language, ImageAspect: *aspect, ImageResolution: *resolution,
		OutDir: *out, DryRun: mode == "estimate",
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	eng, err := engine.Load(paths.Default())
	if err != nil {
		return err
	}
	res, runID, err := server.RunDirect(ctx, cfg, eng, req, server.DirectJob{UseCase: *useCase, Stakes: *stakes})
	if res != nil {
		if *asJSON {
			b, _ := json.MarshalIndent(res, "", "  ")
			fmt.Println(string(b))
		} else {
			fmt.Print(direct.Format(res))
			if runID != "" {
				fmt.Println("run:", runID)
			}
		}
	}
	return err
}

func directTool(ctx context.Context) error {
	var raw json.RawMessage
	if err := json.NewDecoder(os.Stdin).Decode(&raw); err != nil {
		return err
	}
	req, err := direct.ParseArgs(raw)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	eng, err := engine.Load(paths.Default())
	if err != nil {
		return err
	}
	res, runID, err := server.RunDirect(ctx, cfg, eng, req, server.DirectJob{})
	if err != nil {
		return err
	}
	fmt.Print(direct.Format(res))
	if runID != "" {
		fmt.Println("run:", runID)
	}
	return nil
}

func mediaCmd(ctx context.Context, mode string, args []string) error {
	if len(args) == 0 {
		return errors.New(mode + ": missing input file")
	}
	in := args[0]
	fs := flag.NewFlagSet(mode, flag.ExitOnError)
	out := fs.String("out", "", "output file or directory")
	every := fs.Float64("every", 0, "one frame every S seconds")
	scenes := fs.Bool("scenes", false, "scene-change frames")
	threshold := fs.Float64("threshold", 0.3, "scene threshold 0..1")
	maxN := fs.Int("max", 24, "maximum frames")
	at := fs.Float64("at", 0, "time in seconds")
	height := fs.Int("height", 360, "output height")
	fps := fs.Float64("fps", 1, "output frame rate")
	seconds := fs.Float64("seconds", 300, "segment length")
	fs.Parse(args[1:])
	if err := direct.CheckSafe(in); err != nil {
		return err
	}
	print := func(v any) error {
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if mode != "probe" && *out == "" {
		return errors.New(mode + ": --out is required")
	}
	switch mode {
	case "probe":
		mi, err := direct.Probe(ctx, in)
		if err != nil {
			return err
		}
		return print(mi)
	case "frames":
		var fr []direct.Frame
		var err error
		if *scenes || *every == 0 {
			fr, err = direct.SceneFrames(ctx, in, *out, *threshold, *maxN)
		} else {
			fr, err = direct.FramesEvery(ctx, in, *out, *every, *maxN)
		}
		if err != nil {
			return err
		}
		return print(fr)
	case "frame":
		if err := direct.FrameAt(ctx, in, *at, *out); err != nil {
			return err
		}
		return print(direct.Frame{Path: abs(*out), Seconds: *at})
	case "downscale":
		if err := direct.Downscale(ctx, in, *out, *height, *fps); err != nil {
			return err
		}
		return print(map[string]string{"path": abs(*out)})
	case "split":
		segs, err := direct.SplitVideo(ctx, in, *out, *seconds)
		if err != nil {
			return err
		}
		return print(segs)
	case "audio":
		if err := direct.ExtractAudio(ctx, in, *out); err != nil {
			return err
		}
		return print(map[string]string{"path": abs(*out)})
	}
	return nil
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}
