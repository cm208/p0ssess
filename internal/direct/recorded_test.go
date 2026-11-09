package direct

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Replays answers recorded from real OpenRouter calls (testdata/recorded; image payloads and encrypted
// reasoning trimmed) through the client.

func recorded(t *testing.T, name string) func(map[string]any) (int, string) {
	b, err := os.ReadFile(filepath.Join("testdata", "recorded", name))
	if err != nil {
		t.Fatal(err)
	}
	code := 200
	if strings.HasPrefix(name, "error-") {
		code = 404
	}
	return func(map[string]any) (int, string) { return code, string(b) }
}

func TestRecordedAnswers(t *testing.T) {
	dir := t.TempDir()
	video := fakeVideo(t, dir, 2048)
	wav := writeWAV(t, dir, 2)
	pdf := writePDF(t, dir, true)
	img := writePNG(t, dir)

	cases := []struct {
		fixture, path string
		req           Request
		check         func(t *testing.T, r *Result)
	}{
		{"video-gemini-byok.json", "/chat/completions", Request{Model: "google/gemini-3.8-flash", Prompt: "describe", Files: []string{video}, MaxTokens: 800}, func(t *testing.T, r *Result) {
			if !strings.Contains(r.Text, "TEST CARD 42") || !r.Usage.BYOK || r.Usage.UpstreamUSD != 0.006684 || r.Usage.VideoTokens != 910 || r.Provider != "Google AI Studio" {
				t.Errorf("%+v", r.Usage)
			}
		}},
		{"audio-gemini-byok.json", "/chat/completions", Request{Model: "google/gemini-3.8-flash", Prompt: "transcribe", Files: []string{wav}, MaxTokens: 800}, func(t *testing.T, r *Result) {
			if !strings.Contains(r.Text, "Thursday") || r.Usage.AudioTokens != 406 || r.Usage.CostUSD != 0.001605 {
				t.Errorf("%q %+v", r.Text, r.Usage)
			}
		}},
		{"stt-whisper-verbose.json", "/audio/transcriptions", Request{Model: "openai/whisper-large-v3-turbo", Output: OutTranscript, Files: []string{wav}, Timestamps: true}, func(t *testing.T, r *Result) {
			if !strings.HasPrefix(r.Text, "[0:00–0:03] This is a short test recording") || r.Usage.AudioSeconds < 16 || r.Usage.CostUSD < 0.00018 {
				t.Errorf("%q %+v", r.Text, r.Usage)
			}
		}},
		{"pdf-deepseek-mistral-ocr.json", "/chat/completions", Request{Model: "deepseek/deepseek-v4.1-flash", Prompt: "table", Files: []string{pdf}, Output: OutJSON, PDFEngine: "mistral-ocr", MaxTokens: 800}, func(t *testing.T, r *Result) {
			if !strings.Contains(r.Text, `"Total":32.5`) || r.Usage.CostUSD != 0.00220398 {
				t.Errorf("%q %+v", r.Text, r.Usage)
			}
		}},
		{"image-caption-glm.json", "/chat/completions", Request{Model: "deepseek/deepseek-v4.1-flash", Prompt: "alt text", Files: []string{img}, MaxTokens: 800}, func(t *testing.T, r *Result) {
			if !strings.Contains(r.Text, "blue square") || r.Usage.ReasoningTokens != 68 {
				t.Errorf("%q %+v", r.Text, r.Usage)
			}
		}},
		{"imagegen-recraft.json", "/images", Request{Model: "recraft/recraft-v4.1-flash", Prompt: "ghost", Output: OutImage}, func(t *testing.T, r *Result) {
			if len(r.Files) != 1 || r.Files[0].MIME != "image/png" || r.Usage.CostUSD != 0.007 {
				t.Errorf("%+v %+v", r.Files, r.Usage)
			}
		}},
		{"imagegen-gemini-chat.json", "/chat/completions", Request{Model: "google/gemini-3.1-flash-image", Prompt: "ghost", Output: OutImage, MaxTokens: 4000}, func(t *testing.T, r *Result) {
			if len(r.Files) != 1 || !r.Usage.BYOK || r.Usage.UpstreamUSD != 0.0340875 {
				t.Errorf("%+v %+v", r.Files, r.Usage)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			f, c := newFake(t)
			f.answer[tc.path] = recorded(t, tc.fixture)
			tc.req.MaxCostUSD = 1
			res, err := c.Run(context.Background(), tc.req)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, res)
		})
	}

	t.Run("error-max-price.json", func(t *testing.T) {
		f, c := newFake(t)
		f.answer["/chat/completions"] = recorded(t, "error-max-price.json")
		_, err := c.Run(context.Background(), Request{Model: "deepseek/deepseek-v4.1-flash", Prompt: "x", MaxCostUSD: 1})
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != 404 || !strings.Contains(ae.Message, "max price") {
			t.Errorf("err = %v", err)
		}
	})
}
