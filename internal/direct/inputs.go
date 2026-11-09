package direct

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Kind string

const (
	KindText  Kind = "text"
	KindImage Kind = "image"
	KindPDF   Kind = "pdf"
	KindAudio Kind = "audio"
	KindVideo Kind = "video"
)

// Input is one checked local file.
type Input struct {
	Path     string
	Kind     Kind
	MIME     string
	Format   string // audio format string for input_audio
	Bytes    int64
	Seconds  float64 // audio and video duration, when known
	Pages    int     // PDF page count, when known
	HasText  bool    // PDF has a text layer
	Duration string  // how Seconds was measured: "ffprobe", "header" or "size"
}

var extKinds = map[string]struct {
	kind   Kind
	mime   string
	format string
}{
	".png":  {KindImage, "image/png", ""},
	".jpg":  {KindImage, "image/jpeg", ""},
	".jpeg": {KindImage, "image/jpeg", ""},
	".webp": {KindImage, "image/webp", ""},
	".gif":  {KindImage, "image/gif", ""},
	".pdf":  {KindPDF, "application/pdf", ""},
	".wav":  {KindAudio, "audio/wav", "wav"},
	".mp3":  {KindAudio, "audio/mpeg", "mp3"},
	".flac": {KindAudio, "audio/flac", "flac"},
	".m4a":  {KindAudio, "audio/mp4", "m4a"},
	".aac":  {KindAudio, "audio/aac", "aac"},
	".ogg":  {KindAudio, "audio/ogg", "ogg"},
	".opus": {KindAudio, "audio/ogg", "ogg"},
	".aiff": {KindAudio, "audio/aiff", "aiff"},
	".aif":  {KindAudio, "audio/aiff", "aiff"},
	".mp4":  {KindVideo, "video/mp4", ""},
	".m4v":  {KindVideo, "video/mp4", ""},
	".mpeg": {KindVideo, "video/mpeg", ""},
	".mpg":  {KindVideo, "video/mpeg", ""},
	".mov":  {KindVideo, "video/mov", ""},
	".webm": {KindVideo, "video/webm", ""},
}

var textExts = map[string]bool{
	".txt": true, ".md": true, ".csv": true, ".tsv": true, ".json": true, ".jsonl": true, ".yaml": true,
	".yml": true, ".toml": true, ".xml": true, ".html": true, ".log": true, ".srt": true, ".vtt": true,
	".go": true, ".py": true, ".js": true, ".ts": true, ".swift": true, ".rs": true, ".c": true, ".h": true,
	".java": true, ".kt": true, ".rb": true, ".sh": true, ".sql": true, ".diff": true, ".patch": true,
}

// ErrSecret and ErrPrivate are returned (wrapped) when a file must never leave the machine.
var (
	ErrSecret  = errors.New("looks like a secret")
	ErrPrivate = errors.New("is in a repository marked .p0ssess-private")
)

const PrivateMarker = ".p0ssess-private"

var secretName = regexp.MustCompile(`(?i)^(\.env(\..*)?|.*\.env|.*\.pem|.*\.p12|.*\.pfx|.*\.kdbx|.*key.*|id_.*|.*credential.*|.*secret.*|\.netrc|\.pgpass|\.npmrc|\.pypirc|htpasswd|\.htpasswd)$`)

var secretDirs = map[string]bool{".ssh": true, ".gnupg": true, ".aws": true, ".kube": true, ".docker": true, "secrets": true}

// CheckSafe refuses secret-looking files and files under a .p0ssess-private repository. Symlinks are
// resolved first and both names are checked.
func CheckSafe(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	names := []string{abs}
	if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
		names = append(names, real)
	}
	for _, p := range names {
		if secretName.MatchString(filepath.Base(p)) {
			return fmt.Errorf("%s %w (name matches the secret patterns; rename or copy it if it is not)", path, ErrSecret)
		}
		for _, part := range strings.Split(filepath.Dir(p), string(filepath.Separator)) {
			if secretDirs[part] {
				return fmt.Errorf("%s %w (inside a %s directory)", path, ErrSecret, part)
			}
		}
		if dir, ok := privateRoot(filepath.Dir(p)); ok {
			return fmt.Errorf("%s %w (%s)", path, ErrPrivate, filepath.Join(dir, PrivateMarker))
		}
	}
	return nil
}

// privateRoot walks up from dir to the repository root (the first directory holding .git), or to the
// filesystem root outside a repository, looking for the marker.
func privateRoot(dir string) (string, bool) {
	for {
		if _, err := os.Stat(filepath.Join(dir, PrivateMarker)); err == nil {
			return dir, true
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Inspect checks one file and fills in its kind, size, duration and pages. It reads only what it needs.
func Inspect(path string, lim Limits) (Input, error) {
	lim = lim.withDefaults()
	if err := CheckSafe(path); err != nil {
		return Input{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return Input{}, err
	}
	if !st.Mode().IsRegular() {
		return Input{}, fmt.Errorf("%s is not a regular file", path)
	}
	in := Input{Path: path, Bytes: st.Size()}
	ext := strings.ToLower(filepath.Ext(path))
	if k, ok := extKinds[ext]; ok {
		in.Kind, in.MIME, in.Format = k.kind, k.mime, k.format
	} else if textExts[ext] || ext == "" {
		in.Kind, in.MIME = KindText, "text/plain"
	} else {
		return Input{}, fmt.Errorf("%s: unsupported file type %q (images: png jpg webp gif; pdf; audio: wav mp3 flac m4a aac ogg aiff; video: mp4 mpeg mov webm; or plain text)", path, ext)
	}
	switch in.Kind {
	case KindImage:
		if in.Bytes > lim.ImageBytes {
			return in, sizeErr(path, "image", in.Bytes, lim.ImageBytes, "downscale it first (for example: magick in.png -resize 2048x2048 out.jpg)")
		}
	case KindPDF:
		if in.Bytes > lim.PDFBytes {
			return in, sizeErr(path, "PDF", in.Bytes, lim.PDFBytes, "split it (for example: qpdf in.pdf --split-pages=50 out.pdf)")
		}
		in.Pages, in.HasText = pdfInfo(path, in.Bytes)
	case KindAudio:
		if in.Bytes > lim.AudioBytes {
			return in, sizeErr(path, "audio", in.Bytes, lim.AudioBytes, "re-encode to mono MP3 or Opus, or split it (ffmpeg -i in.wav -ac 1 -b:a 48k out.mp3)")
		}
		in.Seconds, in.Duration = mediaSeconds(path, in.Bytes, 8_000)
	case KindVideo:
		in.Seconds, in.Duration = mediaSeconds(path, in.Bytes, 31_250)
	case KindText:
		if in.Bytes > lim.TotalBytes {
			return in, sizeErr(path, "text", in.Bytes, lim.TotalBytes, "summarize or split it locally first")
		}
		head := make([]byte, 8192)
		f, err := os.Open(path)
		if err != nil {
			return in, err
		}
		n, _ := f.Read(head)
		f.Close()
		if bytes.IndexByte(head[:n], 0) >= 0 {
			return in, fmt.Errorf("%s looks binary, not text", path)
		}
	}
	return in, nil
}

// VideoOverLimit reports why a video needs pre-processing, or "" when it can go as is.
func VideoOverLimit(in Input, lim Limits) string {
	lim = lim.withDefaults()
	var why []string
	if in.Bytes > lim.VideoBytes {
		why = append(why, fmt.Sprintf("%s is over the %s inline limit", human(in.Bytes), human(lim.VideoBytes)))
	}
	if in.Seconds > lim.VideoSeconds {
		why = append(why, fmt.Sprintf("%.0f s is over the %.0f s limit", in.Seconds, lim.VideoSeconds))
	}
	return strings.Join(why, "; ")
}

func sizeErr(path, what string, n, max int64, hint string) error {
	return fmt.Errorf("%s: %s is %s, over the %s limit; %s", path, what, human(n), human(max), hint)
}

func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

var (
	pdfCount = regexp.MustCompile(`/Type\s*/Pages\b[^>]*?/Count\s+(\d+)|/Count\s+(\d+)[^>]*?/Type\s*/Pages\b`)
	pdfPage  = regexp.MustCompile(`/Type\s*/Page\b`)
	pdfFont  = regexp.MustCompile(`/Font\b`)
)

// pdfInfo estimates page count and whether the PDF has a text layer. Compressed object streams can hide
// both, so the fallbacks are conservative: about one page per 50 KB, and "has text" when unsure.
func pdfInfo(path string, size int64) (pages int, hasText bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return int(max(1, size/50_000)), true
	}
	for _, m := range pdfCount.FindAllSubmatch(b, -1) {
		for _, g := range m[1:] {
			if len(g) > 0 {
				var n int
				fmt.Sscanf(string(g), "%d", &n)
				pages = max(pages, n)
			}
		}
	}
	if pages == 0 {
		pages = len(pdfPage.FindAllIndex(b, -1))
	}
	if pages == 0 {
		pages = int(max(1, size/50_000))
	}
	hasText = pdfFont.Match(b) || bytes.Contains(b, []byte("/ObjStm"))
	return pages, hasText
}
