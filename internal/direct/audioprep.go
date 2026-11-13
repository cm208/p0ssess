package direct

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const speechFilter = "highpass=f=250,lowpass=f=3400,loudnorm"

// prepAudio applies Request.AudioPrep to the audio inputs of p. Each converted file replaces its input for
// the upload; the original is never touched. A file that cannot be converted goes as it is, silently. The
// returned func removes the temporary files.
func (c *Client) prepAudio(ctx context.Context, p *plan) func() {
	if p.req.AudioPrep != AudioPrepSpeech {
		return func() {}
	}
	var dir string
	ins := append([]Input(nil), p.inputs...)
	for i, in := range ins {
		if in.Kind != KindAudio {
			continue
		}
		if dir == "" {
			d, err := os.MkdirTemp("", "p0ssess-audio-")
			if err != nil {
				break
			}
			dir = d
		}
		if conv, ok := speechCleanup(ctx, in, filepath.Join(dir, "audio-"+strconv.Itoa(i+1)+".mp3")); ok {
			ins[i] = conv
		}
	}
	p.inputs = ins
	return func() {
		if dir != "" {
			os.RemoveAll(dir)
		}
	}
}

// speechCleanup writes the cleaned copy of in to dst (mono 16 kHz MP3, mode 0600). The input's duration is
// kept, so pricing and limits stay those of the original.
func speechCleanup(ctx context.Context, in Input, dst string) (Input, bool) {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return in, false
	}
	f.Close()
	timeout := min(30*time.Second+time.Duration(in.Seconds*0.5*float64(time.Second)), 10*time.Minute)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := execFFmpeg(ctx, "-i", in.Path, "-vn", "-af", speechFilter, "-ac", "1", "-ar", "16000",
		"-c:a", "libmp3lame", "-b:a", "64k", dst); err != nil {
		return in, false
	}
	st, err := os.Stat(dst)
	if err != nil || st.Size() == 0 {
		return in, false
	}
	in.Path, in.MIME, in.Format, in.Bytes = dst, "audio/mpeg", "mp3", st.Size()
	return in, true
}
