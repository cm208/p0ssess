package direct

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Local media helpers. They run ffmpeg and ffprobe on this machine and never call a model, so the engine
// can choose them instead of (or before) a model call: a frame grab for an on-screen timestamp, scene
// detection for the structure of a recording, a downscale or split before uploading.

var ErrNoFFmpeg = errors.New("ffmpeg/ffprobe not found on PATH")

type MediaInfo struct {
	Seconds  float64 `json:"seconds"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	HasVideo bool    `json:"has_video"`
	HasAudio bool    `json:"has_audio"`
}

type Frame struct {
	Path    string  `json:"path"`
	Seconds float64 `json:"seconds"`
}

type Segment struct {
	Path  string  `json:"path"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

func haveFFmpeg() bool {
	_, e1 := exec.LookPath("ffmpeg")
	_, e2 := exec.LookPath("ffprobe")
	return e1 == nil && e2 == nil
}

// Probe reads duration and stream facts with ffprobe.
func Probe(ctx context.Context, path string) (MediaInfo, error) {
	if !haveFFmpeg() {
		return MediaInfo{}, ErrNoFFmpeg
	}
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries",
		"format=duration:stream=codec_type,width,height", "-of", "json", path).Output()
	if err != nil {
		return MediaInfo{}, fmt.Errorf("ffprobe %s: %w", filepath.Base(path), err)
	}
	var p struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return MediaInfo{}, err
	}
	var mi MediaInfo
	mi.Seconds, _ = strconv.ParseFloat(p.Format.Duration, 64)
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			mi.HasVideo = true
			mi.Width, mi.Height = max(mi.Width, s.Width), max(mi.Height, s.Height)
		case "audio":
			mi.HasAudio = true
		}
	}
	return mi, nil
}

// mediaSeconds measures duration with ffprobe, then a WAV header, then a size-based guess at a low
// bitrate (bytesPerSec), which overestimates duration and so cost.
func mediaSeconds(path string, size int64, bytesPerSec int64) (float64, string) {
	if mi, err := Probe(context.Background(), path); err == nil && mi.Seconds > 0 {
		return mi.Seconds, "ffprobe"
	}
	if s, ok := wavSeconds(path); ok {
		return s, "header"
	}
	return float64(size) / float64(bytesPerSec), "size"
}

func wavSeconds(path string) (float64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	h := make([]byte, 4096)
	n, _ := f.Read(h)
	h = h[:n]
	if len(h) < 44 || string(h[0:4]) != "RIFF" || string(h[8:12]) != "WAVE" {
		return 0, false
	}
	var byteRate uint32
	for i := 12; i+8 <= len(h); {
		id := string(h[i : i+4])
		sz := binary.LittleEndian.Uint32(h[i+4 : i+8])
		switch id {
		case "fmt ":
			if i+20 <= len(h) {
				byteRate = binary.LittleEndian.Uint32(h[i+16 : i+20])
			}
		case "data":
			if byteRate == 0 {
				return 0, false
			}
			return float64(sz) / float64(byteRate), true
		}
		i += 8 + int(sz) + int(sz%2)
	}
	return 0, false
}

func runFF(ctx context.Context, args ...string) ([]byte, error) {
	if !haveFFmpeg() {
		return nil, ErrNoFFmpeg
	}
	return execFFmpeg(ctx, args...)
}

func execFFmpeg(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", append([]string{"-hide_banner", "-nostdin", "-y"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		tail := stderr.String()
		if len(tail) > 600 {
			tail = tail[len(tail)-600:]
		}
		return stderr.Bytes(), fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(tail))
	}
	return stderr.Bytes(), nil
}

// FrameAt grabs one JPEG frame at the given time.
func FrameAt(ctx context.Context, in string, seconds float64, out string) error {
	_, err := runFF(ctx, "-ss", ff(seconds), "-i", in, "-frames:v", "1", "-q:v", "3", out)
	return err
}

// FramesEvery writes one frame every `every` seconds into outDir.
func FramesEvery(ctx context.Context, in, outDir string, every float64, maxFrames int) ([]Frame, error) {
	if every <= 0 {
		return nil, errors.New("interval must be positive")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, err
	}
	args := []string{"-i", in, "-vf", "fps=1/" + ff(every), "-q:v", "3"}
	if maxFrames > 0 {
		args = append(args, "-frames:v", strconv.Itoa(maxFrames))
	}
	if _, err := runFF(ctx, append(args, filepath.Join(outDir, "frame-%04d.jpg"))...); err != nil {
		return nil, err
	}
	paths, _ := filepath.Glob(filepath.Join(outDir, "frame-*.jpg"))
	sort.Strings(paths)
	frames := make([]Frame, len(paths))
	for i, p := range paths {
		frames[i] = Frame{Path: p, Seconds: float64(i) * every}
	}
	return frames, nil
}

var ptsTime = regexp.MustCompile(`pts_time:\s*([0-9.]+)`)

// SceneFrames writes the first frame plus every frame whose scene-change score is above threshold
// (0..1; 0.3 is a common default), capped at maxFrames, with the time of each.
func SceneFrames(ctx context.Context, in, outDir string, threshold float64, maxFrames int) ([]Frame, error) {
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, err
	}
	if threshold <= 0 || threshold >= 1 {
		threshold = 0.3
	}
	vf := fmt.Sprintf("select='eq(n\\,0)+gt(scene\\,%s)',showinfo", ff(threshold))
	args := []string{"-i", in, "-vf", vf, "-fps_mode", "vfr", "-q:v", "3"}
	if maxFrames > 0 {
		args = append(args, "-frames:v", strconv.Itoa(maxFrames))
	}
	logb, err := runFF(ctx, append(args, filepath.Join(outDir, "frame-%04d.jpg"))...)
	if err != nil {
		return nil, err
	}
	var times []float64
	for _, line := range strings.Split(string(logb), "\n") {
		if !strings.Contains(line, "Parsed_showinfo") {
			continue
		}
		if m := ptsTime.FindStringSubmatch(line); m != nil {
			t, _ := strconv.ParseFloat(m[1], 64)
			times = append(times, t)
		}
	}
	paths, _ := filepath.Glob(filepath.Join(outDir, "frame-*.jpg"))
	sort.Strings(paths)
	frames := make([]Frame, len(paths))
	for i, p := range paths {
		frames[i].Path = p
		if i < len(times) {
			frames[i].Seconds = times[i]
		}
	}
	return frames, nil
}

// Downscale re-encodes a video to the given height at fps frames per second, with mono 32 kbit/s audio.
func Downscale(ctx context.Context, in, out string, height int, fps float64) error {
	if height <= 0 {
		height = 360
	}
	if fps <= 0 {
		fps = 1
	}
	vf := fmt.Sprintf("scale=-2:'min(%d,ih)',fps=%s", height, ff(fps))
	_, err := runFF(ctx, "-i", in, "-vf", vf, "-c:v", "libx264", "-preset", "veryfast", "-crf", "30",
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "32k", "-ac", "1", "-movflags", "+faststart", out)
	return err
}

// SplitVideo cuts a video into segments of about `seconds` each (re-encoded so every cut is exact).
func SplitVideo(ctx context.Context, in, outDir string, seconds float64) ([]Segment, error) {
	return splitVideo(ctx, in, outDir, seconds, 0)
}

// splitVideo lists every segment but cuts only those from index `from` on; the others have no Path.
func splitVideo(ctx context.Context, in, outDir string, seconds float64, from int) ([]Segment, error) {
	mi, err := Probe(ctx, in)
	if err != nil {
		return nil, err
	}
	if seconds <= 0 {
		return nil, errors.New("segment length must be positive")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, err
	}
	var segs []Segment
	for start, i := 0.0, 1; start < mi.Seconds; start, i = start+seconds, i+1 {
		end := min(start+seconds, mi.Seconds)
		if i <= from {
			segs = append(segs, Segment{Start: start, End: end})
			continue
		}
		out := filepath.Join(outDir, fmt.Sprintf("segment-%03d.mp4", i))
		if _, err := runFF(ctx, "-ss", ff(start), "-i", in, "-t", ff(end-start), "-c:v", "libx264",
			"-preset", "veryfast", "-crf", "28", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "64k", out); err != nil {
			return segs, err
		}
		segs = append(segs, Segment{Path: out, Start: start, End: end})
	}
	return segs, nil
}

// ExtractAudio writes the audio track as mono MP3 at 48 kbit/s.
func ExtractAudio(ctx context.Context, in, out string) error {
	_, err := runFF(ctx, "-i", in, "-vn", "-ac", "1", "-c:a", "libmp3lame", "-b:a", "48k", out)
	return err
}

func ff(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
