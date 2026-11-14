package direct

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"p0ssess/internal/costbasis"
)

type chatResponse struct {
	ID       string `json:"id"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Choices  []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
			Images  []struct {
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"images"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
		Error        *struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"choices"`
	Usage *apiUsage `json:"usage"`
}

func (r *chatResponse) text() string {
	if len(r.Choices) == 0 {
		return ""
	}
	raw := r.Choices[0].Message.Content
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// parts turns the prompt and inputs into chat content parts.
func parts(prompt string, inputs []Input, videoProcessing string, labels map[string]string) ([]map[string]any, error) {
	var out []map[string]any
	for _, in := range inputs {
		if l := labels[in.Path]; l != "" {
			out = append(out, map[string]any{"type": "text", "text": l})
		}
		switch in.Kind {
		case KindText:
			b, err := os.ReadFile(in.Path)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"type": "text", "text": fmt.Sprintf("<file name=%q>\n%s\n</file>", filepath.Base(in.Path), b)})
		case KindImage:
			d, err := encodeFile(in.Path)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + in.MIME + ";base64," + d}})
		case KindPDF:
			d, err := encodeFile(in.Path)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"type": "file", "file": map[string]any{"filename": filepath.Base(in.Path), "file_data": "data:application/pdf;base64," + d}})
		case KindAudio:
			d, err := encodeFile(in.Path)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": d, "format": in.Format}})
		case KindVideo:
			d, err := encodeFile(in.Path)
			if err != nil {
				return nil, err
			}
			v := map[string]any{"url": "data:" + in.MIME + ";base64," + d}
			if videoProcessing != "" {
				v["processing"] = videoProcessing
			}
			out = append(out, map[string]any{"type": "video_url", "video_url": v})
		}
	}
	return append(out, map[string]any{"type": "text", "text": prompt}), nil
}

func (c *Client) chatBody(p *plan, inputs []Input, prompt string, labels map[string]string) (map[string]any, error) {
	content, err := parts(prompt, inputs, p.req.VideoProcessing, labels)
	if err != nil {
		return nil, err
	}
	var msgs []map[string]any
	if p.req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": p.req.System})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": content})
	pr := c.provider(p)
	// Cap endpoint prices at the ones the estimate used, so routing cannot pick a pricier endpoint.
	if in, out := p.model.InPerM, max(p.model.OutPerM, p.model.ReasonPerM); in > 0 && out > 0 {
		pr["max_price"] = map[string]string{"prompt": ff(in), "completion": ff(out)}
	}
	body := map[string]any{
		"model":      p.model.ID,
		"messages":   msgs,
		"max_tokens": p.req.MaxTokens,
		"provider":   pr,
	}
	if p.engine != "" {
		body["plugins"] = []any{map[string]any{"id": "file-parser", "pdf": map[string]any{"engine": p.engine}}}
	}
	if p.req.Output == OutJSON {
		body["response_format"] = map[string]any{"type": "json_object"}
	}
	if p.req.Output == OutImage {
		body["modalities"] = []string{"image", "text"}
		ic := map[string]any{}
		if p.req.ImageAspect != "" {
			ic["aspect_ratio"] = p.req.ImageAspect
		}
		if p.req.ImageResolution != "" {
			ic["image_size"] = p.req.ImageResolution
		}
		if len(ic) > 0 {
			body["image_config"] = ic
		}
	}
	if p.req.Reasoning != "" {
		if p.req.Reasoning == "none" {
			body["reasoning"] = map[string]any{"enabled": false}
		} else {
			body["reasoning"] = map[string]any{"effort": p.req.Reasoning, "exclude": true}
		}
	}
	return body, nil
}

// runChatPrepared applies the opt-in video preparation, then sends one call (or one per segment).
func (c *Client) runChatPrepared(ctx context.Context, p *plan, res *Result, outDir string) error {
	lim := c.Limits.withDefaults()
	switch p.req.VideoPrep {
	case PrepNone:
		return c.chatOnce(ctx, p, p.inputs, p.req.Prompt, nil, res, outDir)
	case PrepDownscale:
		ins, err := c.prepDownscale(ctx, p, lim, outDir, res)
		if err != nil {
			return err
		}
		return c.chatOnce(ctx, p, ins, p.req.Prompt, nil, res, outDir)
	case PrepKeyframes:
		ins, labels, err := c.prepKeyframes(ctx, p, lim, outDir, res)
		if err != nil {
			return err
		}
		return c.chatOnce(ctx, p, ins, p.req.Prompt, labels, res, outDir)
	case PrepSegment:
		return c.runSegments(ctx, p, lim, outDir, res)
	}
	return fmt.Errorf("unknown video_prep %q", p.req.VideoPrep)
}

func (c *Client) chatOnce(ctx context.Context, p *plan, inputs []Input, prompt string, labels map[string]string, res *Result, outDir string) error {
	body, err := c.chatBody(p, inputs, prompt, labels)
	if err != nil {
		return err
	}
	var cr chatResponse
	if err := c.post(ctx, "/chat/completions", body, &cr); err != nil {
		return err
	}
	if len(cr.Choices) > 0 && cr.Choices[0].Error != nil {
		return &APIError{Status: 200, Code: cr.Choices[0].Error.Code, Message: cr.Choices[0].Error.Message, Provider: cr.Provider}
	}
	if len(cr.Choices) == 0 {
		return errors.New("openrouter: answer had no choices")
	}
	text := cr.text()
	if res.Text != "" && text != "" && !strings.HasSuffix(res.Text, "\n") {
		res.Text += "\n\n"
	}
	res.Text += text
	if cr.Model != "" {
		res.Model = cr.Model
	}
	if cr.Provider != "" {
		res.Provider = joinUnique(res.Provider, cr.Provider)
	}
	if cr.ID != "" {
		res.IDs = append(res.IDs, cr.ID)
	}
	if fr := cr.Choices[0].FinishReason; fr == "length" {
		res.Warnings = append(res.Warnings, "the answer hit max_tokens and is cut off")
	}
	// Count the cost before saving images, so a failed save still records what the call was billed.
	c.settleCost(ctx, res, p, cr.ID, cr.Usage, "the answer reported no cost")
	for i, im := range cr.Choices[0].Message.Images {
		f, err := c.saveImage(ctx, im.ImageURL.URL, p.dest(outDir), len(res.Files)+i+1)
		if err != nil {
			return err
		}
		res.Files = append(res.Files, f)
	}
	if p.req.Output == OutImage && len(cr.Choices[0].Message.Images) == 0 {
		res.Warnings = append(res.Warnings, "the model returned no image")
	}
	return nil
}

// settleCost records a finished call's cost: from its usage block, else from OpenRouter's generation
// record, else (for a chat call) from its tokens at the model's list price, else as unmeasured. A call on
// the user's own provider key (BYOK) whose provider charge is not in the usage block takes the same
// fallbacks: OpenRouter's own cost there is only its fee.
func (c *Client) settleCost(ctx context.Context, res *Result, p *plan, id string, u *apiUsage, noCost string) {
	if res.Usage.add(u) {
		return
	}
	why := noCost
	byok := u != nil && u.IsBYOK
	if id != "" {
		g, err := c.generation(ctx, id)
		if err == nil {
			if res.Provider == "" {
				res.Provider = g.ProviderName
			}
			if res.Usage.bill(&g.TotalCost, g.IsBYOK, g.Upstream) {
				res.Usage.CostSource = "generation"
				if g.IsBYOK {
					res.Usage.BYOK = true
				}
				return
			}
			byok = byok || g.IsBYOK
			why = "OpenRouter did not report the provider's charge on your own key"
		} else {
			why = "cost not reported: " + err.Error()
		}
	}
	// Only a call on the user's own key is priced from its tokens; any other call whose cost is missing
	// counts at the reserved maximum.
	if byok && u != nil && p != nil {
		if usd, ok := p.model.tokenCost(p.route, p.req.Output, u); ok {
			var fee float64
			if u.Cost != nil && *u.Cost >= 0 {
				fee = *u.Cost
			}
			res.Usage.priced(fee, usd)
			res.Warnings = append(res.Warnings, why+"; the call is priced from its tokens at the model's list price")
			return
		}
	}
	res.Usage.unmeasured()
	res.Warnings = append(res.Warnings, why)
}

func joinUnique(have, add string) string {
	if have == "" {
		return add
	}
	for _, p := range strings.Split(have, ", ") {
		if p == add {
			return have
		}
	}
	return have + ", " + add
}

// saveImage writes a returned image (a data URL, or an https URL fetched once) to dst.
func (c *Client) saveImage(ctx context.Context, u string, dst imageDest, n int) (OutFile, error) {
	var data []byte
	var mt string
	switch {
	case strings.HasPrefix(u, "data:"):
		meta, payload, ok := strings.Cut(strings.TrimPrefix(u, "data:"), ",")
		if !ok {
			return OutFile{}, errors.New("bad data URL in answer")
		}
		mt = strings.TrimSuffix(meta, ";base64")
		var err error
		if strings.HasSuffix(meta, ";base64") {
			data, err = base64.StdEncoding.DecodeString(payload)
		} else {
			data = []byte(payload)
		}
		if err != nil {
			return OutFile{}, fmt.Errorf("decode image: %w", err)
		}
	case strings.HasPrefix(u, "https://"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return OutFile{}, err
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return OutFile{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return OutFile{}, fmt.Errorf("download image: HTTP %d", resp.StatusCode)
		}
		data, err = io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			return OutFile{}, err
		}
		mt = resp.Header.Get("Content-Type")
	default:
		return OutFile{}, errors.New("answer image is neither a data URL nor an https URL")
	}
	return writeImage(data, mt, dst, n)
}

// imageDest is where a call's generated images go: File when the caller named one, else Dir.
type imageDest struct {
	Dir       string
	File      string
	Overwrite bool
}

func (p *plan) dest(outDir string) imageDest {
	return imageDest{Dir: outDir, File: p.req.OutFile, Overwrite: p.req.Overwrite}
}

// outFilePath resolves a caller's out_file: a relative name is inside outDir, or the working directory.
func outFilePath(file, outDir string) (string, error) {
	if !filepath.IsAbs(file) && outDir != "" {
		file = filepath.Join(outDir, file)
	}
	return filepath.Abs(file)
}

// writeImage writes the n-th image of a call (from 1). It never replaces an existing file unless the
// caller named the file and asked to overwrite it: a taken name moves on to the next free one
// (image-02.png, ghost-2.png), so a paid image is never lost.
func writeImage(data []byte, mt string, dst imageDest, n int) (OutFile, error) {
	if mt == "" || mt == "application/octet-stream" {
		mt = http.DetectContentType(data)
	}
	mt, _, _ = mime.ParseMediaType(mt)
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif", "image/svg+xml": ".svg"}[mt]
	if ext == "" {
		ext = ".bin"
	}
	name := func(i int) string { return filepath.Join(dst.Dir, fmt.Sprintf("image-%02d%s", i, ext)) }
	if dst.File != "" {
		file, err := outFilePath(dst.File, dst.Dir)
		if err != nil {
			return OutFile{}, err
		}
		stem, fext := file, filepath.Ext(file)
		if fext == "" {
			fext = ext
		} else {
			stem = strings.TrimSuffix(file, fext)
		}
		name = func(i int) string {
			if i == 1 {
				return stem + fext
			}
			return fmt.Sprintf("%s-%d%s", stem, i, fext)
		}
		if err := os.MkdirAll(filepath.Dir(stem), 0o700); err != nil {
			return OutFile{}, err
		}
		if dst.Overwrite && n == 1 {
			path := name(1)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return OutFile{}, err
			}
			return OutFile{Path: path, MIME: mt, Bytes: int64(len(data))}, nil
		}
	}
	for i := n; i < n+10000; i++ {
		path := name(i)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return OutFile{}, err
		}
		_, werr := f.Write(data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return OutFile{}, werr
		}
		return OutFile{Path: path, MIME: mt, Bytes: int64(len(data))}, nil
	}
	return OutFile{}, fmt.Errorf("no free file name for image %d in %s", n, filepath.Dir(name(n)))
}

// ErrNoProviderCost says OpenRouter has not (or will not) report what the provider billed on the user's
// own key, so the generation's cost is not known.
var ErrNoProviderCost = errors.New("OpenRouter does not report the provider's charge for this call")

// GenerationCost looks up the billed cost of a finished call by its generation id: OpenRouter's charge
// plus, for bring-your-own-key calls, the upstream one, and which of the two that is (costbasis.Reported
// or BYOKUpstream). A bring-your-own-key call without the upstream figure is an error, not a cheap call.
func (c *Client) GenerationCost(ctx context.Context, id string) (float64, string, error) {
	g, err := c.generation(ctx, id)
	if err != nil {
		return 0, "", err
	}
	usd, basis, ok := costbasis.FromUsage(&g.TotalCost, g.IsBYOK, g.Upstream)
	if !ok {
		return 0, "", ErrNoProviderCost
	}
	return usd, basis, nil
}
