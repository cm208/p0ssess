package direct

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"p0ssess/internal/pme/table"
)

// Live tests make real, paid OpenRouter calls. They run only with P0SSESS_LIVE=1 and a key file in
// P0SSESS_OPENROUTER_KEY_FILE. P0SSESS_LIVE_MEDIA may point at a folder with test-video.mp4,
// test-audio.mp3, test-table.pdf and test-image.png; missing files are rendered with ffmpeg.
// P0SSESS_LIVE_MAX caps the whole run (default $0.10).

var liveSpent struct {
	sync.Mutex
	usd float64
}

func liveClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("P0SSESS_LIVE") != "1" {
		t.Skip("set P0SSESS_LIVE=1 to run paid live tests")
	}
	b, err := os.ReadFile(os.Getenv("P0SSESS_OPENROUTER_KEY_FILE"))
	if err != nil {
		t.Skip("P0SSESS_OPENROUTER_KEY_FILE is not readable")
	}
	c := New(strings.TrimSpace(string(b)))
	c.StateDir = t.TempDir()
	if p, err := table.Open(t.TempDir()).ModelsFile(); err != nil || c.Catalog.LoadRegistry(p) != nil {
		t.Log("no route table models; using live prices")
	}
	return c
}

func liveCap() float64 {
	if v := os.Getenv("P0SSESS_LIVE_MAX"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return 0.10
}

func liveRun(t *testing.T, c *Client, req Request) *Result {
	t.Helper()
	liveSpent.Lock()
	left := liveCap() - liveSpent.usd
	liveSpent.Unlock()
	if req.MaxCostUSD > left {
		req.MaxCostUSD = left
	}
	res, err := c.Run(context.Background(), req)
	if res != nil {
		liveSpent.Lock()
		liveSpent.usd += res.Usage.CostUSD
		liveSpent.Unlock()
		t.Logf("%s via %s: $%.6f (%s), estimate $%.4f, tokens %d/%d", res.Model, res.Provider, res.Usage.CostUSD,
			res.Usage.CostSource, res.Estimate.CostUSD, res.Usage.PromptTokens, res.Usage.CompletionTokens)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("answer: %.300s", res.Text)
	return res
}

func liveMedia(t *testing.T, name string) string {
	t.Helper()
	if d := os.Getenv("P0SSESS_LIVE_MEDIA"); d != "" {
		if p := filepath.Join(d, name); fileExists(p) {
			return p
		}
	}
	dir := t.TempDir()
	switch name {
	case "test-video.mp4":
		return makeVideo(t, dir, 6)
	case "test-audio.mp3":
		v := makeVideo(t, dir, 6)
		out := filepath.Join(dir, "tone.mp3")
		if err := ExtractAudio(context.Background(), v, out); err != nil {
			t.Fatal(err)
		}
		return out
	case "test-image.png":
		return writePNG(t, dir)
	}
	t.Skipf("no %s in P0SSESS_LIVE_MEDIA", name)
	return ""
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestLiveVideo(t *testing.T) {
	c := liveClient(t)
	res := liveRun(t, c, Request{Model: "google/gemini-3.8-flash", Prompt: "Describe this video: colors, any on-screen text, and what changes when.",
		Files: []string{liveMedia(t, "test-video.mp4")}, MaxTokens: 600, Reasoning: "low", MaxCostUSD: 0.03})
	if !strings.Contains(strings.ToLower(res.Text), "blue") {
		t.Errorf("answer does not mention the color change")
	}
}

func TestLiveAudio(t *testing.T) {
	c := liveClient(t)
	liveRun(t, c, Request{Model: "google/gemini-3.8-flash", Prompt: "Transcribe this audio. If it has no speech, describe the sound.",
		Files: []string{liveMedia(t, "test-audio.mp3")}, MaxTokens: 400, Reasoning: "low", MaxCostUSD: 0.02})
}

func TestLiveImageCaption(t *testing.T) {
	c := liveClient(t)
	liveRun(t, c, Request{Model: "google/gemini-3.8-flash", Prompt: "Write one sentence of alt text for this image.",
		Files: []string{liveMedia(t, "test-image.png")}, MaxTokens: 200, Reasoning: "low", MaxCostUSD: 0.02})
}

func TestLivePDF(t *testing.T) {
	c := liveClient(t)
	liveRun(t, c, Request{Model: "deepseek/deepseek-v4.1-flash", Prompt: `Return the table in this PDF as JSON: {"rows":[{...}]}.`,
		Files: []string{liveMedia(t, "test-table.pdf")}, Output: OutJSON, MaxTokens: 600, MaxCostUSD: 0.02})
}

func TestLiveImageGeneration(t *testing.T) {
	c := liveClient(t)
	res := liveRun(t, c, Request{Model: "recraft/recraft-v4.1-flash", Prompt: "A flat, minimal ghost icon on a plain background",
		Output: OutImage, MaxCostUSD: 0.02})
	if len(res.Files) == 0 {
		t.Fatal("no image file")
	}
}
