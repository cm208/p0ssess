package direct

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngAnswer(t *testing.T, f *fake, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Pix[0] = shade
	var b bytes.Buffer
	png.Encode(&b, img)
	data := base64.StdEncoding.EncodeToString(b.Bytes())
	f.answer["/chat/completions"] = chatOK("done", `,"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`+data+`"}}]`)
	return b.Bytes()
}

// Several runs into one output folder never overwrite each other's images.
func TestImagesNeverOverwrite(t *testing.T) {
	f, c := newFake(t)
	out := t.TempDir()
	seen := map[string][]byte{}
	for i := 0; i < 3; i++ {
		want := pngAnswer(t, f, uint8(i+1))
		res, err := c.Run(context.Background(), Request{Model: "google/gemini-3.1-flash-image", Prompt: "edit", Output: OutImage, OutDir: out, MaxCostUSD: 0.5})
		if err != nil || len(res.Files) != 1 {
			t.Fatalf("run %d: %v %+v", i, err, res)
		}
		p := res.Files[0].Path
		if _, dup := seen[p]; dup {
			t.Fatalf("run %d wrote over %s", i, p)
		}
		seen[p] = want
	}
	for p, want := range seen {
		if got, _ := os.ReadFile(p); !bytes.Equal(got, want) {
			t.Errorf("%s was overwritten", p)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "image-03.png")); err != nil {
		t.Errorf("third image not at the next free name: %v", err)
	}
}

// An explicit out_file is used as given; an existing one is refused before anything is sent, unless
// overwrite is set.
func TestImageOutFile(t *testing.T) {
	f, c := newFake(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "sub", "ghost.png")
	want := pngAnswer(t, f, 9)
	res, err := c.Run(context.Background(), Request{Model: "google/gemini-3.1-flash-image", Prompt: "x", Output: OutImage, OutFile: dst, MaxCostUSD: 0.5})
	if err != nil || len(res.Files) != 1 || res.Files[0].Path != dst {
		t.Fatalf("%v %+v", err, res)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, want) {
		t.Error("out_file content differs")
	}
	sent := f.count()
	if _, err := c.Run(context.Background(), Request{Model: "google/gemini-3.1-flash-image", Prompt: "x", Output: OutImage, OutFile: dst, MaxCostUSD: 0.5}); err == nil ||
		!strings.Contains(err.Error(), "exists") {
		t.Fatalf("existing out_file: %v", err)
	}
	if f.count() != sent {
		t.Error("a request was sent for an out_file that exists")
	}
	want = pngAnswer(t, f, 10)
	if res, err := c.Run(context.Background(), Request{Model: "google/gemini-3.1-flash-image", Prompt: "x", Output: OutImage, OutFile: dst, Overwrite: true, MaxCostUSD: 0.5}); err != nil || res.Files[0].Path != dst {
		t.Fatalf("overwrite: %v %+v", err, res)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, want) {
		t.Error("overwrite did not replace the file")
	}
	// A name without an extension takes the image's.
	res, err = c.Run(context.Background(), Request{Model: "google/gemini-3.1-flash-image", Prompt: "x", Output: OutImage, OutFile: "plain", OutDir: dir, MaxCostUSD: 0.5})
	if err != nil || res.Files[0].Path != filepath.Join(dir, "plain.png") {
		t.Fatalf("no extension: %v %+v", err, res)
	}
	if _, err := c.Run(context.Background(), Request{Model: "google/gemini-3.8-flash", Prompt: "x", OutFile: dst, MaxCostUSD: 0.5}); err == nil {
		t.Error("out_file on text output was accepted")
	}
}

func TestParseArgsOutFile(t *testing.T) {
	r, err := ParseArgs([]byte(`{"prompt":"x","output":"image","out_file":"/tmp/a.png","overwrite":true}`))
	if err != nil || r.OutFile != "/tmp/a.png" || !r.Overwrite {
		t.Fatalf("%+v %v", r, err)
	}
}
