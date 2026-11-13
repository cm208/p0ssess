package direct

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fakeFFmpeg = `#!/bin/sh
echo run >> "$FAKE_LOG"
for a in "$@"; do last=$a; printf '%s\n' "$a" >> "$FAKE_LOG.args"; done
printf -- '--\n' >> "$FAKE_LOG.args"
/usr/bin/stat -c '%a' "$last" > "$FAKE_LOG.mode"
/usr/bin/stat -c '%a' "${last%/*}" > "$FAKE_LOG.dirmode"
echo "$last" > "$FAKE_LOG.out"
case "$FAKE_MODE" in
fail) exit 1 ;;
empty) : > "$last" ;;
*) printf CLEANED > "$last" ;;
esac
`

// fakeTools puts a recording ffmpeg first (and only) on PATH and returns the log path.
func fakeTools(t *testing.T, mode string) string {
	t.Helper()
	if _, err := os.Stat("/usr/bin/stat"); err != nil {
		t.Skip("needs /usr/bin/stat")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte(fakeFFmpeg), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("PATH", bin)
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_MODE", mode)
	return log
}

func noTools(t *testing.T) { t.Setenv("PATH", t.TempDir()) }

func invoked(log string) bool {
	_, err := os.Stat(log)
	return err == nil
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// sentAudio runs the request against the fake server and returns the audio format and the decoded bytes
// that were uploaded.
func sentAudio(t *testing.T, route string, req Request) (format string, data []byte, res *Result) {
	t.Helper()
	f, c := newFake(t)
	var err error
	if route == "stt" {
		f.answer["/audio/transcriptions"] = func(map[string]any) (int, string) {
			return 200, `{"text":"hello","usage":{"seconds":2,"cost":0.000001}}`
		}
		req.Model, req.Output = "openai/whisper-large-v3-turbo", OutTranscript
		req.MaxCostUSD = 0.01
		res, err = c.Run(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		a := f.last("/audio/transcriptions")["input_audio"].(map[string]any)
		data, _ = base64.StdEncoding.DecodeString(a["data"].(string))
		return a["format"].(string), data, res
	}
	f.answer["/chat/completions"] = chatOK("ok", "")
	req.Model, req.Prompt, req.MaxTokens, req.MaxCostUSD = "google/gemini-3.8-flash", "transcribe", 100, 0.05
	res, err = c.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	a := partOfType(t, userParts(t, f.last("/chat/completions")), "input_audio")["input_audio"].(map[string]any)
	data, _ = base64.StdEncoding.DecodeString(a["data"].(string))
	return a["format"].(string), data, res
}

func TestAudioPrepConverts(t *testing.T) {
	for _, route := range []string{"chat", "stt"} {
		t.Run(route, func(t *testing.T) {
			log := fakeTools(t, "ok")
			wav := writeWAV(t, t.TempDir(), 2)
			orig, _ := os.ReadFile(wav)
			format, data, res := sentAudio(t, route, Request{Files: []string{wav}, AudioPrep: AudioPrepSpeech})
			if format != "mp3" || string(data) != "CLEANED" {
				t.Errorf("uploaded format %q data %q", format, data)
			}
			want := []string{"-hide_banner", "-nostdin", "-y", "-i", wav, "-vn", "-af", "highpass=f=250,lowpass=f=3400,loudnorm",
				"-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-b:a", "64k"}
			got := readLines(t, log+".args")
			out := got[len(got)-2]
			if strings.Join(got[:len(got)-2], " ") != strings.Join(want, " ") || got[len(got)-1] != "--" {
				t.Errorf("ffmpeg args = %q", got)
			}
			if !strings.HasSuffix(out, ".mp3") || filepath.Dir(out) == filepath.Dir(wav) {
				t.Errorf("output path %q", out)
			}
			if m := readLines(t, log+".mode")[0]; m != "600" {
				t.Errorf("temp file mode %s", m)
			}
			if m := readLines(t, log+".dirmode")[0]; m != "700" {
				t.Errorf("temp dir mode %s", m)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("temp file still exists: %v", err)
			}
			if _, err := os.Stat(filepath.Dir(out)); !os.IsNotExist(err) {
				t.Errorf("temp dir still exists: %v", err)
			}
			if now, _ := os.ReadFile(wav); !bytes.Equal(now, orig) {
				t.Error("original file changed")
			}
			if len(res.Warnings) != 0 {
				t.Errorf("warnings = %v", res.Warnings)
			}
		})
	}
}

func TestAudioPrepKeepsEstimate(t *testing.T) {
	wav := writeWAV(t, t.TempDir(), 3)
	_, _, plain := sentAudio(t, "chat", Request{Files: []string{wav}})
	fakeTools(t, "ok")
	_, _, prep := sentAudio(t, "chat", Request{Files: []string{wav}, AudioPrep: AudioPrepSpeech})
	if plain.Estimate.CostUSD != prep.Estimate.CostUSD || plain.Estimate.InputTokens != prep.Estimate.InputTokens {
		t.Errorf("estimate changed: %+v vs %+v", plain.Estimate, prep.Estimate)
	}
}

func TestAudioPrepFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) string
		prep  string
		ran   bool
	}{
		{"ffmpeg missing", func(t *testing.T) string { noTools(t); return "" }, AudioPrepSpeech, false},
		{"ffmpeg exits 1", func(t *testing.T) string { return fakeTools(t, "fail") }, AudioPrepSpeech, true},
		{"empty output", func(t *testing.T) string { return fakeTools(t, "empty") }, AudioPrepSpeech, true},
		{"no prep", func(t *testing.T) string { return fakeTools(t, "ok") }, "", false},
		{"unknown prep", func(t *testing.T) string { return fakeTools(t, "ok") }, "denoise_everything", false},
	} {
		for _, route := range []string{"chat", "stt"} {
			t.Run(tc.name+"/"+route, func(t *testing.T) {
				log := tc.setup(t)
				wav := writeWAV(t, t.TempDir(), 2)
				orig, _ := os.ReadFile(wav)
				format, data, res := sentAudio(t, route, Request{Files: []string{wav}, AudioPrep: tc.prep})
				if format != "wav" || !bytes.Equal(data, orig) {
					t.Errorf("original not sent: format %q, %d bytes of %d", format, len(data), len(orig))
				}
				if log != "" && invoked(log) != tc.ran {
					t.Errorf("ffmpeg invoked = %v, want %v", invoked(log), tc.ran)
				}
				if len(res.Warnings) != 0 {
					t.Errorf("warnings = %v", res.Warnings)
				}
				if tc.ran {
					if out := readLines(t, log+".out")[0]; func() bool { _, err := os.Stat(out); return err == nil }() {
						t.Errorf("temp file %s left behind", out)
					}
				}
			})
		}
	}
}

func TestAudioPrepLeavesOtherInputsAlone(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"image": writePNG(t, dir),
		"pdf":   writePDF(t, dir, true),
		"video": fakeVideo(t, dir, 4096),
	}
	for name, path := range files {
		t.Run(name, func(t *testing.T) {
			log := fakeTools(t, "ok")
			f, c := newFake(t)
			f.answer["/chat/completions"] = chatOK("ok", "")
			orig, _ := os.ReadFile(path)
			_, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "look", Files: []string{path},
				AudioPrep: AudioPrepSpeech, MaxTokens: 100, MaxCostUSD: 0.05})
			if err != nil {
				t.Fatal(err)
			}
			if invoked(log) {
				t.Error("ffmpeg was invoked")
			}
			body := string(mustJSON(t, f.last("/chat/completions")))
			if !strings.Contains(body, base64.StdEncoding.EncodeToString(orig)) {
				t.Error("original bytes were not sent")
			}
		})
	}
}

func TestAudioPrepMixedInputs(t *testing.T) {
	log := fakeTools(t, "ok")
	dir := t.TempDir()
	png := writePNG(t, dir)
	wav := writeWAV(t, dir, 1)
	f, c := newFake(t)
	f.answer["/chat/completions"] = chatOK("ok", "")
	if _, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "x", Files: []string{png, wav},
		AudioPrep: AudioPrepSpeech, MaxTokens: 100, MaxCostUSD: 0.05}); err != nil {
		t.Fatal(err)
	}
	if n := len(readLines(t, log)); n != 1 {
		t.Errorf("ffmpeg ran %d times, want 1", n)
	}
	parts := userParts(t, f.last("/chat/completions"))
	if partOfType(t, parts, "input_audio")["input_audio"].(map[string]any)["format"] != "mp3" {
		t.Error("audio was not converted")
	}
	img, _ := os.ReadFile(png)
	if !strings.Contains(string(mustJSON(t, parts)), base64.StdEncoding.EncodeToString(img)) {
		t.Error("image changed")
	}
}

func TestSpeechCleanupRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	wav := filepath.Join(dir, "tone.wav")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"sine=frequency=440:duration=1", "-ar", "16000", "-ac", "1", wav).CombinedOutput(); err != nil {
		t.Skipf("cannot render the test tone: %v %s", err, b)
	}
	in, err := Inspect(wav, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.mp3")
	conv, ok := speechCleanup(context.Background(), in, dst)
	if !ok {
		t.Skip("this ffmpeg cannot run the filter or encode MP3")
	}
	st, err := os.Stat(dst)
	if err != nil || st.Size() == 0 || st.Mode().Perm() != 0o600 {
		t.Fatalf("output %v %v", st, err)
	}
	if conv.Format != "mp3" || conv.Kind != KindAudio || conv.Seconds != in.Seconds || conv.Bytes != st.Size() {
		t.Errorf("converted input = %+v", conv)
	}
	if _, err := Probe(context.Background(), dst); err != nil {
		t.Errorf("output does not probe: %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
