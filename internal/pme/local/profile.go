package local

import (
	"encoding/json"
	"strings"

	"p0ssess/internal/pme/remote"
	"p0ssess/internal/pme/table"
)

// Profile describes a task when no use-case id is given, and overrides the default work size when one
// is.
type Profile struct {
	Text       string   `json:"text,omitempty"`
	Inputs     []string `json:"inputs,omitempty"`
	Outputs    []string `json:"outputs,omitempty"`
	Path       string   `json:"path,omitempty"`
	Size       string   `json:"size,omitempty"`
	Stakes     int      `json:"stakes,omitempty"`
	InTokens   int      `json:"in_tokens,omitempty"`
	OutTokens  int      `json:"out_tokens,omitempty"`
	Bytes      int      `json:"bytes,omitempty"`
	Turns      int      `json:"turns,omitempty"`
	Items      int      `json:"items,omitempty"`
	AudioSec   float64  `json:"audio_seconds,omitempty"`
	VideoSec   float64  `json:"video_seconds,omitempty"`
	Images     int      `json:"images,omitempty"`
	Pages      int      `json:"pages,omitempty"`
	Chars      int      `json:"chars,omitempty"`
	GenSec     float64  `json:"gen_seconds,omitempty"`
	NeedsTools bool     `json:"needs_tools,omitempty"`
}

// wire is the profile as the engine service takes it (stakes travel at the top level).
func (p *Profile) wire() *remote.Profile {
	if p == nil {
		return nil
	}
	w := &remote.Profile{Text: p.Text, Inputs: normAll(p.Inputs), Outputs: normAll(p.Outputs), Path: p.Path, Size: strings.ToUpper(p.Size),
		InTokens: p.InTokens, OutTokens: p.OutTokens, Bytes: p.Bytes, Turns: p.Turns, Items: p.Items, AudioSec: p.AudioSec,
		VideoSec: p.VideoSec, Images: p.Images, Pages: p.Pages, Chars: p.Chars, GenSec: p.GenSec, NeedsTools: p.NeedsTools}
	if b, _ := json.Marshal(w); string(b) == "{}" {
		return nil
	}
	return w
}

// work is the explicit sizes a profile gives.
func (p *Profile) work() Work {
	if p == nil {
		return Work{}
	}
	w := Work{Turns: p.Turns, Items: p.Items, AudioSec: p.AudioSec, VideoSec: p.VideoSec, Images: p.Images, Pages: p.Pages,
		Chars: p.Chars, GenSec: p.GenSec, InTokens: p.InTokens, OutTokens: p.OutTokens}
	if w.InTokens == 0 && p.Bytes > 0 {
		w.InTokens = p.Bytes / 4
	}
	return w
}

// NormalizeModality maps loose names onto the engine's modality words.
func NormalizeModality(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "pdf", "file", "files", "document", "doc", "documents":
		return "file"
	case "img", "images", "picture", "photo", "screenshot", "png", "jpg":
		return "image"
	case "sound", "voice", "mp3", "wav", "microphone audio", "speech-in", "music", "sfx":
		return "audio"
	case "clip", "movie", "mp4", "mov":
		return "video"
	case "url", "code", "repo", "diff", "json", "sql", "markdown":
		return "text"
	case "tts", "narration", "speech":
		return "speech"
	case "transcript", "transcription", "subtitles", "srt":
		return "transcript"
	case "embedding", "embeddings", "vectors":
		return "embeddings"
	}
	return s
}

func normAll(in []string) []string {
	var out []string
	for _, s := range in {
		if n := NormalizeModality(s); n != "" && !contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

func media(list []string) []string {
	var out []string
	for _, m := range list {
		if m != "text" && m != "transcript" {
			out = append(out, m)
		}
	}
	return out
}

// classify picks a use case for a profile from the table by its modalities, then its path. It returns
// nil when the profile names no media, or nothing matches.
func classify(t *table.Table, p *Profile) *table.UseCase {
	if t == nil || p == nil {
		return nil
	}
	ins, outs := media(normAll(p.Inputs)), media(normAll(p.Outputs))
	if len(ins) == 0 && len(outs) == 0 {
		return nil
	}
	var best *table.UseCase
	bestPath := false
	for i := range t.UseCases {
		u := &t.UseCases[i]
		uin, uout := media(u.Inputs), media(u.Outputs)
		if !subset(ins, uin) || !subset(outs, uout) {
			continue
		}
		need := uin[:0:0]
		for _, m := range uin {
			// A video carries its own soundtrack.
			if !(m == "audio" && contains(ins, "video")) {
				need = append(need, m)
			}
		}
		if !subset(need, ins) || !subset(uout, outs) {
			continue
		}
		path := p.Path != "" && p.Path == u.Path
		if best == nil || path && !bestPath || path == bestPath && u.N < best.N {
			best, bestPath = u, path
		}
	}
	return best
}

func subset(a, b []string) bool {
	for _, x := range a {
		if !contains(b, x) {
			return false
		}
	}
	return true
}
