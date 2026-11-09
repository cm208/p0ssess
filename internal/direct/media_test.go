package direct

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeVideo renders a red-then-blue clip with a tone, so scene detection has one cut at half time.
func makeVideo(t *testing.T, dir string, seconds int) string {
	t.Helper()
	if !haveFFmpeg() {
		t.Skip("ffmpeg not installed")
	}
	half := seconds / 2
	out := filepath.Join(dir, "cut.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=160x90:r=10:d="+itoa(half),
		"-f", "lavfi", "-i", "color=c=blue:s=160x90:r=10:d="+itoa(seconds-half),
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+itoa(seconds),
		"-filter_complex", "[0][1]concat=n=2:v=1:a=0[v]", "-map", "[v]", "-map", "2:a",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot render the test clip: %v %s", err, b)
	}
	return out
}

func itoa(n int) string { return strings.TrimSpace(ff(float64(n))) }

func TestProbeAndScenes(t *testing.T) {
	dir := t.TempDir()
	v := makeVideo(t, dir, 4)
	mi, err := Probe(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if mi.Seconds < 3.5 || mi.Seconds > 4.5 || !mi.HasAudio || !mi.HasVideo || mi.Width != 160 {
		t.Errorf("probe = %+v", mi)
	}
	frames, err := SceneFrames(context.Background(), v, filepath.Join(dir, "scenes"), 0.3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[0].Seconds != 0 || frames[1].Seconds < 1.5 || frames[1].Seconds > 2.5 {
		t.Errorf("scenes = %+v", frames)
	}
	every, err := FramesEvery(context.Background(), v, filepath.Join(dir, "every"), 1, 0)
	if err != nil || len(every) < 4 {
		t.Errorf("every: %v %+v", err, every)
	}
	if err := FrameAt(context.Background(), v, 3, filepath.Join(dir, "at3.jpg")); err != nil {
		t.Error(err)
	}
	in, err := Inspect(v, Limits{})
	if err != nil || in.Duration != "ffprobe" || in.Seconds < 3.5 {
		t.Errorf("inspect = %+v %v", in, err)
	}
}

func TestDownscaleSplitAudio(t *testing.T) {
	dir := t.TempDir()
	v := makeVideo(t, dir, 4)
	small := filepath.Join(dir, "small.mp4")
	if err := Downscale(context.Background(), v, small, 60, 1); err != nil {
		t.Fatal(err)
	}
	mi, _ := Probe(context.Background(), small)
	if mi.Height != 60 || !mi.HasAudio {
		t.Errorf("downscaled = %+v", mi)
	}
	segs, err := SplitVideo(context.Background(), v, filepath.Join(dir, "segs"), 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 3 || segs[2].Start != 3 {
		t.Errorf("segments = %+v", segs)
	}
	if err := ExtractAudio(context.Background(), v, filepath.Join(dir, "a.mp3")); err != nil {
		t.Error(err)
	}
}

func TestVideoPrepThroughClient(t *testing.T) {
	dir := t.TempDir()
	v := makeVideo(t, dir, 4)
	lim := Limits{VideoSeconds: 2, SegmentSeconds: 2, MaxKeyframes: 5, DownscaleHeight: 60}

	t.Run("keyframes", func(t *testing.T) {
		f, c := newFake(t)
		c.Limits = lim
		f.answer["/chat/completions"] = chatOK("red then blue", "")
		res, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "what happens", Files: []string{v}, VideoPrep: PrepKeyframes, MaxCostUSD: 0.1})
		if err != nil {
			t.Fatal(err)
		}
		parts := userParts(t, f.last("/chat/completions"))
		var images, audio int
		var labels []string
		for _, p := range parts {
			switch p["type"] {
			case "image_url":
				images++
			case "input_audio":
				audio++
			case "text":
				labels = append(labels, p["text"].(string))
			}
		}
		if images != 2 || audio != 1 || !strings.Contains(strings.Join(labels, "|"), "Frame at 0:02 of cut.mp4") {
			t.Errorf("images %d audio %d labels %q", images, audio, labels)
		}
		if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "2 scene-change frames plus its audio track") {
			t.Errorf("warnings = %v", res.Warnings)
		}
	})

	t.Run("segment", func(t *testing.T) {
		f, c := newFake(t)
		c.Limits = lim
		f.answer["/chat/completions"] = chatOK("part", "")
		res, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "describe", Files: []string{v}, VideoPrep: PrepSegment, MaxTokens: 200, MaxCostUSD: 0.1})
		if err != nil {
			t.Fatal(err)
		}
		if f.count() != 2 || res.Estimate.Calls != 3 && res.Estimate.Calls != 2 {
			t.Errorf("requests %d, estimated calls %d", f.count(), res.Estimate.Calls)
		}
		if !strings.Contains(res.Text, "[segment 1 of 2, 0:00 to 0:02]\npart") || !strings.Contains(res.Text, "[segment 2 of 2, 0:02 to 0:04]") {
			t.Errorf("text = %q", res.Text)
		}
		if res.Usage.CostUSD != 2*0.00042 {
			t.Errorf("cost = %v", res.Usage.CostUSD)
		}
	})

	t.Run("downscale", func(t *testing.T) {
		f, c := newFake(t)
		c.Limits = Limits{VideoSeconds: 2, DownscaleHeight: 60}
		f.answer["/chat/completions"] = chatOK("ok", "")
		res, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "describe", Files: []string{v}, VideoPrep: PrepDownscale, MaxCostUSD: 0.1})
		if err != nil {
			t.Fatal(err)
		}
		partOfType(t, userParts(t, f.last("/chat/completions")), "video_url")
		if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "downscaled locally to 60p") {
			t.Errorf("warnings = %v", res.Warnings)
		}
	})
}
