package direct

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func (c *Client) prepDownscale(ctx context.Context, p *plan, lim Limits, outDir string, res *Result) ([]Input, error) {
	var out []Input
	for i, in := range p.inputs {
		if in.Kind != KindVideo {
			out = append(out, in)
			continue
		}
		dst := filepath.Join(outDir, "work", fmt.Sprintf("video-%02d-small.mp4", i+1))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		if err := Downscale(ctx, in.Path, dst, lim.DownscaleHeight, 1); err != nil {
			return nil, err
		}
		small, err := Inspect(dst, lim)
		if err != nil {
			return nil, err
		}
		if why := VideoOverLimit(small, Limits{VideoBytes: lim.VideoBytes, VideoSeconds: 1e9}); why != "" {
			return nil, fmt.Errorf("%s: still too large after downscaling (%s); use video_prep segment", in.Path, why)
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s downscaled locally to %dp at 1 fps (%s to %s)",
			filepath.Base(in.Path), lim.DownscaleHeight, human(in.Bytes), human(small.Bytes)))
		out = append(out, small)
	}
	return out, nil
}

func (c *Client) prepKeyframes(ctx context.Context, p *plan, lim Limits, outDir string, res *Result) ([]Input, map[string]string, error) {
	var out []Input
	labels := map[string]string{}
	for i, in := range p.inputs {
		if in.Kind != KindVideo {
			out = append(out, in)
			continue
		}
		dir := filepath.Join(outDir, "work", fmt.Sprintf("video-%02d-frames", i+1))
		frames, err := SceneFrames(ctx, in.Path, dir, lim.SceneThreshold, lim.MaxKeyframes)
		if err != nil {
			return nil, nil, err
		}
		for _, f := range frames {
			fi, err := Inspect(f.Path, lim)
			if err != nil {
				return nil, nil, err
			}
			labels[f.Path] = fmt.Sprintf("Frame at %s of %s:", clock(f.Seconds), filepath.Base(in.Path))
			out = append(out, fi)
		}
		note := fmt.Sprintf("%s sent as %d scene-change frames", filepath.Base(in.Path), len(frames))
		if p.model.Takes("audio") {
			if mi, err := Probe(ctx, in.Path); err == nil && mi.HasAudio {
				a := filepath.Join(dir, "audio.mp3")
				if err := ExtractAudio(ctx, in.Path, a); err != nil {
					return nil, nil, err
				}
				ai, err := Inspect(a, lim)
				if err != nil {
					return nil, nil, err
				}
				labels[a] = "Audio track of " + filepath.Base(in.Path) + ":"
				out = append(out, ai)
				note += " plus its audio track"
			}
		} else {
			note += "; audio dropped (the model takes no audio)"
		}
		res.Warnings = append(res.Warnings, note)
	}
	return out, labels, nil
}

func (c *Client) runSegments(ctx context.Context, p *plan, lim Limits, outDir string, res *Result) error {
	var video *Input
	var rest []Input
	for i := range p.inputs {
		if p.inputs[i].Kind == KindVideo && video == nil {
			video = &p.inputs[i]
		} else {
			rest = append(rest, p.inputs[i])
		}
	}
	if video == nil {
		return c.chatOnce(ctx, p, p.inputs, p.req.Prompt, nil, res, outDir)
	}
	segs, err := SplitVideo(ctx, video.Path, filepath.Join(outDir, "work", "segments"), lim.SegmentSeconds)
	if err != nil {
		return err
	}
	for i, s := range segs {
		si, err := Inspect(s.Path, lim)
		if err != nil {
			return err
		}
		if why := VideoOverLimit(si, Limits{VideoBytes: lim.VideoBytes, VideoSeconds: 1e9}); why != "" {
			return fmt.Errorf("segment %d is still too large (%s); lower segment_seconds or downscale first", i+1, why)
		}
		head := fmt.Sprintf("[segment %d of %d, %s to %s]", i+1, len(segs), clock(s.Start), clock(s.End))
		prompt := p.req.Prompt + fmt.Sprintf("\n\nThis is segment %d of %d of a longer video, covering %s to %s. Give times relative to the full video.",
			i+1, len(segs), clock(s.Start), clock(s.End))
		res.Text += head + "\n"
		before := res.Text
		if err := c.chatOnce(ctx, p, append(append([]Input{}, rest...), si), prompt, nil, res, outDir); err != nil {
			return fmt.Errorf("segment %d: %w", i+1, err)
		}
		if res.Text == before {
			res.Text += "(no answer)"
		}
		res.Text += "\n\n"
		if p.req.MaxCostUSD > 0 && res.Usage.CostUSD > p.req.MaxCostUSD {
			return fmt.Errorf("%w: spent $%.4f after %d of %d segments; stopped", ErrOverBudget, res.Usage.CostUSD, i+1, len(segs))
		}
	}
	return nil
}

func clock(s float64) string {
	t := int(s + 0.5)
	if t >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", t/3600, t/60%60, t%60)
	}
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}
