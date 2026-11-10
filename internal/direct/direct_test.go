package direct

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testKey = "test-key-not-real-0123456789"

// fake is an OpenRouter stand-in that records every request and answers from a per-path handler.
type fake struct {
	t      *testing.T
	mu     sync.Mutex
	bodies map[string][]map[string]any
	paths  []string
	answer map[string]func(body map[string]any) (int, string)
}

func newFake(t *testing.T) (*fake, *Client) {
	f := &fake{t: t, bodies: map[string][]map[string]any{}, answer: map[string]func(map[string]any) (int, string){}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testKey {
			t.Errorf("auth header = %q", got)
		}
		path := r.URL.Path
		var body map[string]any
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, &body); err != nil {
				t.Errorf("request body is not JSON: %v", err)
			}
		}
		f.mu.Lock()
		f.paths = append(f.paths, r.Method+" "+path)
		f.bodies[path] = append(f.bodies[path], body)
		h := f.answer[path]
		f.mu.Unlock()
		if h == nil {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":404,"message":"no handler"}}`)
			return
		}
		code, out := h(body)
		w.WriteHeader(code)
		io.WriteString(w, out)
	}))
	t.Cleanup(srv.Close)
	c := New(testKey)
	c.BaseURL = srv.URL
	c.Catalog = NewCatalog(srv.URL, srv.Client())
	c.Catalog.NoLive = true
	c.StateDir = t.TempDir()
	c.GenerationWait = 0
	for _, m := range testModels {
		c.Catalog.Add(m)
	}
	return f, c
}

func (f *fake) last(path string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	bs := f.bodies[path]
	if len(bs) == 0 {
		f.t.Fatalf("no request to %s (saw %v)", path, f.paths)
	}
	return bs[len(bs)-1]
}

func (f *fake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

var chatRoutes = []string{"/api/v1/chat/completions"}

var testModels = []ModelInfo{
	{ID: "google/gemini-3.8-flash", Input: []string{"text", "image", "video", "file", "audio"}, Output: []string{"text"}, Routes: chatRoutes, InPerM: 0.825, OutPerM: 4.125, AudioPerM: 0.825, ReasonPerM: 4.125},
	{ID: "deepseek/deepseek-v4.1-flash", Input: []string{"text", "image"}, Output: []string{"text"}, Routes: chatRoutes, HasZDR: true, InPerM: 0.051, OutPerM: 0.567},
	{ID: "google/gemini-3.1-flash-image", Input: []string{"image", "text"}, Output: []string{"image", "text"}, Routes: append(chatRoutes, "/api/v1/images"), InPerM: 0.5, OutPerM: 3, ImageUnit: "token", ImageCost: 6e-05},
	{ID: "recraft/recraft-v4.1-flash", Input: []string{"text"}, Output: []string{"image"}, Routes: []string{"/api/v1/images"}, ImageUnit: "image", ImageCost: 0.007},
	{ID: "openai/whisper-large-v3-turbo", Input: []string{"audio"}, Output: []string{"transcription"}, Routes: []string{"/api/v1/audio/transcriptions"}, HasZDR: true, STTRaw: 3.33e-06},
}

func chatOK(text string, extra string) func(map[string]any) (int, string) {
	return func(map[string]any) (int, string) {
		t, _ := json.Marshal(text)
		return 200, `{"id":"gen-1","model":"m","provider":"Google","choices":[{"message":{"role":"assistant","content":` + string(t) + extra + `},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":100,"completion_tokens":20,"cost":0.00042,"completion_tokens_details":{"reasoning_tokens":5}}}`
	}
}

func userParts(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	msgs := body["messages"].([]any)
	user := msgs[len(msgs)-1].(map[string]any)
	var out []map[string]any
	for _, p := range user["content"].([]any) {
		out = append(out, p.(map[string]any))
	}
	return out
}

func partOfType(t *testing.T, parts []map[string]any, typ string) map[string]any {
	t.Helper()
	for _, p := range parts {
		if p["type"] == typ {
			return p
		}
	}
	t.Fatalf("no %s part in %v", typ, parts)
	return nil
}

func writePNG(t *testing.T, dir string) string {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	png.Encode(&b, img)
	p := filepath.Join(dir, "pic.png")
	os.WriteFile(p, b.Bytes(), 0o600)
	return p
}

// writeWAV writes `seconds` of 16 kHz mono 16-bit silence.
func writeWAV(t *testing.T, dir string, seconds int) string {
	n := 16000 * 2 * seconds
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+n))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(n))
	b.Write(make([]byte, n))
	p := filepath.Join(dir, "clip.wav")
	os.WriteFile(p, b.Bytes(), 0o600)
	return p
}

func writePDF(t *testing.T, dir string, text bool) string {
	res := "/Resources << >>"
	if text {
		res = "/Resources << /Font << /F1 4 0 R >> >>"
	}
	pdf := "%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n" +
		"2 0 obj << /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >> endobj\n" +
		"3 0 obj << /Type /Page /Parent 2 0 R " + res + " >> endobj\n" +
		"5 0 obj << /Type /Page /Parent 2 0 R >> endobj\n%%EOF\n"
	p := filepath.Join(dir, "doc.pdf")
	os.WriteFile(p, []byte(pdf), 0o600)
	return p
}

func TestTextChat(t *testing.T) {
	f, c := newFake(t)
	f.answer["/chat/completions"] = chatOK("hello back", "")
	dir := t.TempDir()
	notes := filepath.Join(dir, "notes.md")
	os.WriteFile(notes, []byte("# notes\nsome text"), 0o600)
	res, err := c.Run(context.Background(), Request{Model: "deepseek/deepseek-v4.1-flash", Prompt: "summarize", System: "be brief", Files: []string{notes}, MaxCostUSD: 0.01})
	if err != nil {
		t.Fatal(err)
	}
	body := f.last("/chat/completions")
	pr := body["provider"].(map[string]any)
	if pr["data_collection"] != "deny" || pr["zdr"] != nil {
		t.Errorf("provider = %v", pr)
	}
	if mp := pr["max_price"].(map[string]any); mp["prompt"] != "0.051" || mp["completion"] != "0.567" {
		t.Errorf("max_price = %v", mp)
	}
	if body["max_tokens"].(float64) != DefaultMaxTokens {
		t.Errorf("max_tokens = %v", body["max_tokens"])
	}
	if msgs := body["messages"].([]any); msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("first message is not system")
	}
	parts := userParts(t, body)
	if !strings.Contains(parts[0]["text"].(string), "some text") || parts[len(parts)-1]["text"] != "summarize" {
		t.Errorf("parts = %v", parts)
	}
	if res.Text != "hello back" || res.Usage.CostUSD != 0.00042 || res.Usage.CostSource != "usage" || res.Provider != "Google" {
		t.Errorf("result = %+v", res)
	}
	if res.Usage.ReasoningTokens != 5 || res.IDs[0] != "gen-1" {
		t.Errorf("usage = %+v ids %v", res.Usage, res.IDs)
	}
}

func TestImageInput(t *testing.T) {
	f, c := newFake(t)
	f.answer["/chat/completions"] = chatOK("a red dot", "")
	img := writePNG(t, t.TempDir())
	if _, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "caption", Files: []string{img}, MaxCostUSD: 0.05}); err != nil {
		t.Fatal(err)
	}
	p := partOfType(t, userParts(t, f.last("/chat/completions")), "image_url")
	url := p["image_url"].(map[string]any)["url"].(string)
	raw, _ := os.ReadFile(img)
	if url != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(raw) {
		t.Errorf("image url = %.60s", url)
	}
}

func TestPDFEngines(t *testing.T) {
	cases := []struct {
		model, asked string
		text         bool
		want         string
	}{
		{"google/gemini-3.8-flash", "", true, "native"},
		{"deepseek/deepseek-v4.1-flash", "", true, "cloudflare-ai"},
		{"deepseek/deepseek-v4.1-flash", "", false, "mistral-ocr"},
		{"google/gemini-3.8-flash", "mistral-ocr", true, "mistral-ocr"},
	}
	for _, tc := range cases {
		f, c := newFake(t)
		f.answer["/chat/completions"] = chatOK(`{"rows":[]}`, "")
		pdf := writePDF(t, t.TempDir(), tc.text)
		res, err := c.Run(context.Background(), Request{Model: tc.model, Prompt: "table as JSON", Files: []string{pdf}, Output: OutJSON, PDFEngine: tc.asked, MaxCostUSD: 0.1})
		if err != nil {
			t.Fatal(err)
		}
		body := f.last("/chat/completions")
		eng := body["plugins"].([]any)[0].(map[string]any)["pdf"].(map[string]any)["engine"]
		if eng != tc.want {
			t.Errorf("%s text=%v: engine %v, want %s", tc.model, tc.text, eng, tc.want)
		}
		if body["response_format"].(map[string]any)["type"] != "json_object" {
			t.Errorf("no json response_format")
		}
		file := partOfType(t, userParts(t, body), "file")["file"].(map[string]any)
		if file["filename"] != "doc.pdf" || !strings.HasPrefix(file["file_data"].(string), "data:application/pdf;base64,") {
			t.Errorf("file part = %v", file)
		}
		if tc.want == "mistral-ocr" && res.Estimate.FeeUSD != 2*ocrUSDPerPage {
			t.Errorf("OCR fee = %v for 2 pages", res.Estimate.FeeUSD)
		}
	}
}

func TestAudioInput(t *testing.T) {
	f, c := newFake(t)
	f.answer["/chat/completions"] = chatOK("silence", "")
	wav := writeWAV(t, t.TempDir(), 3)
	res, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "transcribe", Files: []string{wav}, MaxTokens: 100, MaxCostUSD: 0.01})
	if err != nil {
		t.Fatal(err)
	}
	a := partOfType(t, userParts(t, f.last("/chat/completions")), "input_audio")["input_audio"].(map[string]any)
	if a["format"] != "wav" || len(a["data"].(string)) < 1000 {
		t.Errorf("audio part format %v len %d", a["format"], len(a["data"].(string)))
	}
	// 3 s at 32 tokens per second plus the prompt.
	if res.Estimate.InputTokens < 96 || res.Estimate.InputTokens > 110 {
		t.Errorf("estimate input tokens = %d", res.Estimate.InputTokens)
	}
}

func TestAudioRefusedByTextModel(t *testing.T) {
	f, c := newFake(t)
	wav := writeWAV(t, t.TempDir(), 1)
	_, err := c.Run(context.Background(), Request{Model: "deepseek/deepseek-v4.1-flash", Prompt: "x", Files: []string{wav}, MaxCostUSD: 1})
	if err == nil || !strings.Contains(err.Error(), "does not take audio") {
		t.Fatalf("err = %v", err)
	}
	if f.count() != 0 {
		t.Error("a request was sent")
	}
}

func fakeVideo(t *testing.T, dir string, size int) string {
	p := filepath.Join(dir, "clip.mp4")
	os.WriteFile(p, bytes.Repeat([]byte{0}, size), 0o600)
	return p
}

func TestVideoInput(t *testing.T) {
	f, c := newFake(t)
	f.answer["/chat/completions"] = chatOK("a clip", "")
	v := fakeVideo(t, t.TempDir(), 4096)
	if _, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "describe", Files: []string{v}, VideoProcessing: "static", MaxCostUSD: 0.05}); err != nil {
		t.Fatal(err)
	}
	vu := partOfType(t, userParts(t, f.last("/chat/completions")), "video_url")["video_url"].(map[string]any)
	if !strings.HasPrefix(vu["url"].(string), "data:video/mp4;base64,") || vu["processing"] != "static" {
		t.Errorf("video part = %.80v", vu)
	}
}

func TestVideoOverLimitNotUploaded(t *testing.T) {
	f, c := newFake(t)
	c.Limits = Limits{VideoBytes: 1000}
	v := fakeVideo(t, t.TempDir(), 5000)
	_, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "describe", Files: []string{v}, MaxCostUSD: 1})
	if err == nil || !strings.Contains(err.Error(), "Nothing was uploaded") || !strings.Contains(err.Error(), "keyframes") {
		t.Fatalf("err = %v", err)
	}
	if f.count() != 0 {
		t.Error("a request was sent")
	}
}

// A video over the whole-request limit gets the same video_prep options as one over the per-video limit,
// so the caller retries with one instead of re-encoding the file itself (MEDIA.md, run B).
func TestRequestLimitOffersVideoPrep(t *testing.T) {
	f, c := newFake(t)
	c.Limits = Limits{TotalBytes: 1000, VideoBytes: 1 << 20, VideoSeconds: 1e9}
	v := fakeVideo(t, t.TempDir(), 5000)
	_, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "describe", Files: []string{v}, MaxCostUSD: 1})
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"over the 1000 B request limit", "clip.mp4", "Nothing was uploaded", "set video_prep", "\"downscale\"", "\"keyframes\"", "\"segment\""} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %q: %v", want, err)
		}
	}
	if f.count() != 0 {
		t.Error("a request was sent")
	}

	// Without a video there is nothing to prepare, so no video_prep hint.
	img := filepath.Join(t.TempDir(), "big.png")
	os.WriteFile(img, append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 5000)...), 0o600)
	_, err = c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "describe", Files: []string{img}, MaxCostUSD: 1})
	if err == nil || !strings.Contains(err.Error(), "request limit") || strings.Contains(err.Error(), "video_prep") {
		t.Errorf("err = %v", err)
	}
}

func TestImageGenerationChat(t *testing.T) {
	f, c := newFake(t)
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	data := base64.StdEncoding.EncodeToString(b.Bytes())
	f.answer["/chat/completions"] = chatOK("here it is", `,"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`+data+`"}}]`)
	out := t.TempDir()
	res, err := c.Run(context.Background(), Request{Model: "google/gemini-3.1-flash-image", Prompt: "a ghost", Output: OutImage, ImageAspect: "1:1", OutDir: out, MaxCostUSD: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	body := f.last("/chat/completions")
	if m := body["modalities"].([]any); len(m) != 2 || m[0] != "image" {
		t.Errorf("modalities = %v", m)
	}
	if body["image_config"].(map[string]any)["aspect_ratio"] != "1:1" {
		t.Errorf("image_config = %v", body["image_config"])
	}
	if len(res.Files) != 1 || res.Files[0].MIME != "image/png" || filepath.Dir(res.Files[0].Path) != out {
		t.Fatalf("files = %+v", res.Files)
	}
	got, _ := os.ReadFile(res.Files[0].Path)
	if !bytes.Equal(got, b.Bytes()) {
		t.Error("saved image differs")
	}
	if !strings.HasPrefix(Format(res), "<answer>\nhere it is\n</answer>\nfiles:\n- "+out) {
		t.Errorf("format = %q", Format(res))
	}
}

func TestImageGenerationImagesAPI(t *testing.T) {
	f, c := newFake(t)
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}
	f.answer["/images"] = func(map[string]any) (int, string) {
		return 200, `{"created":1,"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(jpeg) + `","media_type":"image/jpeg"}],"usage":{"cost":0.007}}`
	}
	res, err := c.Run(context.Background(), Request{Model: "recraft/recraft-v4.1-flash", Prompt: "a ghost", Output: OutImage, MaxCostUSD: 0.01})
	if err != nil {
		t.Fatal(err)
	}
	body := f.last("/images")
	if body["prompt"] != "a ghost" || body["provider"].(map[string]any)["data_collection"] != "deny" {
		t.Errorf("body = %v", body)
	}
	if res.Route != "images" || len(res.Files) != 1 || !strings.HasSuffix(res.Files[0].Path, ".jpg") || res.Usage.CostUSD != 0.007 {
		t.Errorf("result = %+v", res)
	}
}

func TestImageAPIRefusedUnderStrictZDR(t *testing.T) {
	f, c := newFake(t)
	_, err := c.Run(context.Background(), Request{Model: "recraft/recraft-v4.1-flash", Prompt: "x", Output: OutImage, StrictZDR: true, MaxCostUSD: 1})
	if err == nil || !strings.Contains(err.Error(), "strict ZDR") || f.count() != 0 {
		t.Fatalf("err = %v, requests %d", err, f.count())
	}
}

func TestStrictZDRFlag(t *testing.T) {
	f, c := newFake(t)
	f.answer["/chat/completions"] = chatOK("ok", "")
	if _, err := c.Run(context.Background(), Request{Model: "deepseek/deepseek-v4.1-flash", Prompt: "x", StrictZDR: true, MaxCostUSD: 0.01}); err != nil {
		t.Fatal(err)
	}
	pr := f.last("/chat/completions")["provider"].(map[string]any)
	if pr["zdr"] != true || pr["data_collection"] != "deny" {
		t.Errorf("provider = %v", pr)
	}
}

func TestTranscription(t *testing.T) {
	f, c := newFake(t)
	f.answer["/audio/transcriptions"] = func(map[string]any) (int, string) {
		return 200, `{"text":"hello there. hi","segments":[{"start":0,"end":1.2,"text":" hello there."},{"start":61.5,"end":63,"text":"hi","speaker":1}],"usage":{"seconds":63,"cost":0.0002}}`
	}
	wav := writeWAV(t, t.TempDir(), 2)
	res, err := c.Run(context.Background(), Request{Model: "openai/whisper-large-v3-turbo", Output: OutTranscript, Files: []string{wav}, Timestamps: true, Language: "en", MaxCostUSD: 0.01})
	if err != nil {
		t.Fatal(err)
	}
	body := f.last("/audio/transcriptions")
	if body["response_format"] != "verbose_json" || body["language"] != "en" || body["input_audio"].(map[string]any)["format"] != "wav" {
		t.Errorf("body = %v", body)
	}
	want := "[0:00–0:01] hello there.\n[1:02–1:03] speaker 1: hi"
	if res.Text != want || res.Usage.CostUSD != 0.0002 || res.Usage.AudioSeconds != 63 {
		t.Errorf("text = %q usage %+v", res.Text, res.Usage)
	}
}

func TestCostGuardSendsNothing(t *testing.T) {
	f, c := newFake(t)
	_, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "x", MaxTokens: 100000, MaxCostUSD: 0.01})
	if !errors.Is(err, ErrOverBudget) || f.count() != 0 {
		t.Fatalf("err = %v, requests %d", err, f.count())
	}
	_, err = c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "x"})
	if !errors.Is(err, ErrOverBudget) {
		t.Fatalf("zero limit: err = %v", err)
	}
}

func TestDryRun(t *testing.T) {
	f, c := newFake(t)
	res, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "x", MaxTokens: 1000, DryRun: true})
	if err != nil || f.count() != 0 {
		t.Fatalf("err %v requests %d", err, f.count())
	}
	want := 1000 * 4.125 / 1e6
	if res.Estimate.CostUSD < want || res.Estimate.CostUSD > want*1.01 {
		t.Errorf("estimate = %v, want about %v", res.Estimate.CostUSD, want)
	}
}

func TestGenerationLookup(t *testing.T) {
	f, c := newFake(t)
	c.GenerationWait = 0
	f.answer["/chat/completions"] = func(map[string]any) (int, string) {
		return 200, `{"id":"gen-9","choices":[{"message":{"content":"x"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`
	}
	f.answer["/generation"] = func(map[string]any) (int, string) {
		return 200, `{"data":{"id":"gen-9","total_cost":0.0011,"provider_name":"Vertex"}}`
	}
	res, err := c.Run(context.Background(), Request{Model: "deepseek/deepseek-v4.1-flash", Prompt: "x", MaxCostUSD: 0.01})
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage.CostUSD != 0.0011 || res.Usage.CostSource != "generation" || res.Provider != "Vertex" {
		t.Errorf("result = %+v", res)
	}
}

func TestBYOKCostCountsUpstream(t *testing.T) {
	f, c := newFake(t)
	f.answer["/chat/completions"] = func(map[string]any) (int, string) {
		return 200, `{"id":"gen-2","provider":"Google AI Studio","choices":[{"message":{"content":"x"}}],` +
			`"usage":{"prompt_tokens":936,"completion_tokens":704,"cost":0,"is_byok":true,` +
			`"prompt_tokens_details":{"video_tokens":910},"cost_details":{"upstream_inference_cost":0.006684}}}`
	}
	res, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "x", MaxCostUSD: 0.05})
	if err != nil {
		t.Fatal(err)
	}
	u := res.Usage
	if !u.BYOK || u.CostUSD != 0.006684 || u.OpenRouterUSD != 0 || u.UpstreamUSD != 0.006684 || u.VideoTokens != 910 {
		t.Errorf("usage = %+v", u)
	}
	if !strings.Contains(Format(res), "bring-your-own-key: OpenRouter $0.000000 + provider $0.006684") {
		t.Errorf("format = %q", Format(res))
	}
}

func TestAPIErrorHasNoKey(t *testing.T) {
	f, c := newFake(t)
	f.answer["/chat/completions"] = func(map[string]any) (int, string) {
		return 404, `{"error":{"code":404,"message":"No endpoints found matching your data policy","metadata":{"provider_name":"X"}}}`
	}
	_, err := c.Run(context.Background(), Request{Model: "deepseek/deepseek-v4.1-flash", Prompt: "x", MaxCostUSD: 0.01})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 404 || !strings.Contains(err.Error(), "data policy") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Error("error contains the key")
	}
}

func TestSecretFilesRefused(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".env", ".env.local", "prod.env", "server.pem", "api_key.txt", "id_ed25519", "aws_credentials", "client-secret.json"} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("x"), 0o600)
		if err := CheckSafe(p); !errors.Is(err, ErrSecret) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	ssh := filepath.Join(dir, ".ssh")
	os.Mkdir(ssh, 0o700)
	os.WriteFile(filepath.Join(ssh, "config"), []byte("x"), 0o600)
	if err := CheckSafe(filepath.Join(ssh, "config")); !errors.Is(err, ErrSecret) {
		t.Errorf(".ssh/config: err = %v", err)
	}
	ok := filepath.Join(dir, "photo.png")
	os.WriteFile(ok, []byte("x"), 0o600)
	if err := CheckSafe(ok); err != nil {
		t.Errorf("photo.png: %v", err)
	}
	// A harmless name pointing at a secret is still refused.
	link := filepath.Join(dir, "innocent.txt")
	os.Symlink(filepath.Join(dir, ".env"), link)
	if err := CheckSafe(link); !errors.Is(err, ErrSecret) {
		t.Errorf("symlink to .env: err = %v", err)
	}
}

func TestPrivateRepoRefused(t *testing.T) {
	repo := t.TempDir()
	os.Mkdir(filepath.Join(repo, ".git"), 0o700)
	sub := filepath.Join(repo, "media", "clips")
	os.MkdirAll(sub, 0o700)
	clip := filepath.Join(sub, "a.png")
	os.WriteFile(clip, []byte("x"), 0o600)
	if err := CheckSafe(clip); err != nil {
		t.Fatalf("before marker: %v", err)
	}
	os.WriteFile(filepath.Join(repo, PrivateMarker), nil, 0o600)
	if err := CheckSafe(clip); !errors.Is(err, ErrPrivate) {
		t.Fatalf("after marker: %v", err)
	}
	f, c := newFake(t)
	if _, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "x", Files: []string{clip}, MaxCostUSD: 1}); !errors.Is(err, ErrPrivate) || f.count() != 0 {
		t.Fatalf("run: %v", err)
	}
}

func TestSizeLimits(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "big.png")
	os.WriteFile(img, make([]byte, 2048), 0o600)
	if _, err := Inspect(img, Limits{ImageBytes: 1024}); err == nil || !strings.Contains(err.Error(), "over the 1.0 KiB limit") {
		t.Errorf("image: %v", err)
	}
	wav := writeWAV(t, dir, 1)
	if _, err := Inspect(wav, Limits{AudioBytes: 100}); err == nil || !strings.Contains(err.Error(), "audio") {
		t.Errorf("audio: %v", err)
	}
	bin := filepath.Join(dir, "blob.xyz")
	os.WriteFile(bin, []byte{1}, 0o600)
	if _, err := Inspect(bin, Limits{}); err == nil || !strings.Contains(err.Error(), "unsupported file type") {
		t.Errorf("unknown type: %v", err)
	}
}

func TestPDFPageCount(t *testing.T) {
	in, err := Inspect(writePDF(t, t.TempDir(), true), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if in.Pages != 2 || !in.HasText {
		t.Errorf("pages %d text %v", in.Pages, in.HasText)
	}
}

func TestWAVDuration(t *testing.T) {
	s, ok := wavSeconds(writeWAV(t, t.TempDir(), 4))
	if !ok || s != 4 {
		t.Errorf("seconds = %v ok %v", s, ok)
	}
}

func TestToolArgsAndFormat(t *testing.T) {
	f, c := newFake(t)
	f.answer["/chat/completions"] = chatOK("tool answer", "")
	req, err := ParseArgs(json.RawMessage(`{"model":"deepseek/deepseek-v4.1-flash","prompt":"hi","max_cost_usd":0.01}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if text := Format(res); !strings.HasPrefix(text, "<answer>\ntool answer\n</answer>\nmodel: m via Google; cost $0.000420 (usage") {
		t.Errorf("text = %q", text)
	}
	if r, err := ParseArgs(json.RawMessage(`{"prompt":"hi","files":["/tmp/a.mp4"]}`)); err != nil || r.MaxGiven || r.Model != "" {
		t.Errorf("model and max_cost_usd are optional: %+v %v", r, err)
	}
	if !req.MaxGiven {
		t.Error("a given max_cost_usd is not marked")
	}
	if text := Compact(res, " · run d-1"); !strings.HasSuffix(text, "</answer>\ndeepseek-v4.1-flash · $0.0004 · run d-1\n") && !strings.Contains(text, "· run d-1") {
		t.Errorf("compact = %q", text)
	}
	res.Files = []OutFile{{Path: "/tmp/out/image-1.png", MIME: "image/png"}, {Path: "/tmp/out/deck.pptx"}}
	if text := Compact(res, ""); !strings.Contains(text, `files: [{"path":"/tmp/out/image-1.png","kind":"image"},{"path":"/tmp/out/deck.pptx","kind":"document"}]`) {
		t.Errorf("files hint = %q", text)
	}
	if _, err := ParseArgs(json.RawMessage(`{"model":"x","prompt":"hi","max_cost_usd":1,"bogus":1}`)); err == nil {
		t.Error("unknown field accepted")
	}
	var schema map[string]any
	if err := json.Unmarshal(ToolSchema, &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
}

func TestLiveCatalogLookup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/models/acme/omni/endpoints", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":{"architecture":{"input_modalities":["text","audio","video"],"output_modalities":["text"]},"endpoints":[`+
			`{"pricing":{"prompt":"0.0000002","completion":"0.000001"}},{"pricing":{"prompt":"0.0000003","completion":"0.0000008","audio":"0.0000005"}}]}}`)
	})
	mux.HandleFunc("/models/acme/painter/endpoints", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":{"architecture":{"input_modalities":["text"],"output_modalities":["image"]},"endpoints":[{"pricing":{"image_output":"0.0000017"}}]}}`)
	})
	mux.HandleFunc("/images/models/acme/painter/endpoints", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"endpoints":[{"pricing":[{"billable":"output_image","unit":"image","cost_usd":0.004}]},{"pricing":[{"billable":"output_image","unit":"image","cost_usd":0.006}]}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cat := NewCatalog(srv.URL, srv.Client())
	m, err := cat.Lookup(context.Background(), "acme/omni")
	if err != nil {
		t.Fatal(err)
	}
	if m.InPerM != 0.3 || m.OutPerM != 1 || m.AudioPerM != 0.5 || !m.Takes("video") || m.Source != "live" {
		t.Errorf("omni = %+v", m)
	}
	p, err := cat.Lookup(context.Background(), "acme/painter")
	if err != nil {
		t.Fatal(err)
	}
	if p.ImageUnit != "image" || p.ImageCost != 0.006 || !p.ImagesRoute() || p.Makes("text") {
		t.Errorf("painter = %+v", p)
	}
}

func TestRegistrySnapshotLoads(t *testing.T) {
	cat := NewCatalog("http://127.0.0.1:0", http.DefaultClient)
	cat.NoLive = true
	if err := cat.LoadRegistry("../../registry/models.json"); err != nil {
		t.Skip("registry snapshot not available:", err)
	}
	m, err := cat.Lookup(context.Background(), "google/gemini-3.8-flash")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Takes("video") || !m.Takes("audio") || m.InPerM <= 0 || m.OutPerM <= 0 {
		t.Errorf("gemini = %+v", m)
	}
	h, _ := cat.Lookup(context.Background(), "anthropic/claude-haiku-5.5")
	if len(h.LongContext) == 0 || h.LongContext[0].MinPromptTokens != 100000 {
		t.Errorf("haiku long context = %+v", h.LongContext)
	}
}

// TestUnknownPriceIsNotZero covers M6 on the direct path: a model with no known price does not estimate
// as $0. It needs an explicit max_cost_usd above 0, its estimate says "unknown", and no max_price is sent.
func TestUnknownPriceIsNotZero(t *testing.T) {
	f, c := newFake(t)
	c.Catalog.Add(ModelInfo{ID: "newco/unpriced", Input: []string{"text"}, Output: []string{"text"}, Routes: chatRoutes})
	c.Catalog.Add(ModelInfo{ID: "newco/gratis:free", Input: []string{"text"}, Output: []string{"text"}, Routes: chatRoutes})
	f.answer["/chat/completions"] = chatOK("ok", "")
	_, err := c.Run(context.Background(), Request{Model: "newco/unpriced", Prompt: "x", MaxCostUSD: 0})
	if !errors.Is(err, ErrPriceUnknown) || f.count() != 0 {
		t.Fatalf("zero limit on an unpriced model: err %v, requests %d", err, f.count())
	}
	dry, err := c.Run(context.Background(), Request{Model: "newco/unpriced", Prompt: "x", DryRun: true})
	if err != nil || !dry.Estimate.CostUnknown || !strings.Contains(dry.Text, "cost unknown") {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	res, err := c.Run(context.Background(), Request{Model: "newco/unpriced", Prompt: "x", MaxCostUSD: 0.05})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Estimate.CostUnknown || !strings.Contains(Format(res), "estimate was unknown") {
		t.Errorf("estimate not marked unknown: %+v\n%s", res.Estimate, Format(res))
	}
	if pr, _ := f.last("/chat/completions")["provider"].(map[string]any); pr["max_price"] != nil {
		t.Errorf("max_price sent for an unknown price: %v", pr["max_price"])
	}
	if _, err := c.Run(context.Background(), Request{Model: "newco/gratis:free", Prompt: "x", MaxCostUSD: 0}); err != nil {
		t.Errorf("free model with a zero limit: %v", err)
	}
}

// TestLiveImageListingKeepsRegistryPrice: an image listing with no output_image price must not wipe the
// snapshot's image price to $0.
func TestLiveImageListingKeepsRegistryPrice(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/models/acme/painter/endpoints", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	mux.HandleFunc("/images/models/acme/painter/endpoints", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"endpoints":[{"pricing":[{"billable":"input_image","unit":"image","cost_usd":0.001}]}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cat := NewCatalog(srv.URL, srv.Client())
	cat.Add(ModelInfo{ID: "acme/painter", Input: []string{"text"}, Output: []string{"image"}, Routes: []string{"/api/v1/images"}, ImageUnit: "image", ImageCost: 0.02})
	m, err := cat.Lookup(context.Background(), "acme/painter")
	if err != nil {
		t.Fatal(err)
	}
	if m.ImageUnit != "image" || m.ImageCost != 0.02 {
		t.Errorf("image price after a live listing with none = %s %v, want image 0.02", m.ImageUnit, m.ImageCost)
	}
}
