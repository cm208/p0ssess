package direct

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	Usage *struct {
		PromptTokens            int      `json:"prompt_tokens"`
		CompletionTokens        int      `json:"completion_tokens"`
		Cost                    *float64 `json:"cost"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		PromptTokensDetails struct {
			AudioTokens int `json:"audio_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
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
	body := map[string]any{
		"model":      p.model.ID,
		"messages":   msgs,
		"max_tokens": p.req.MaxTokens,
		"provider":   c.provider(p),
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
	for i, im := range cr.Choices[0].Message.Images {
		f, err := c.saveImage(ctx, im.ImageURL.URL, outDir, len(res.Files)+i+1)
		if err != nil {
			return err
		}
		res.Files = append(res.Files, f)
	}
	if p.req.Output == OutImage && len(cr.Choices[0].Message.Images) == 0 {
		res.Warnings = append(res.Warnings, "the model returned no image")
	}
	c.addUsage(ctx, res, cr)
	return nil
}

func (c *Client) addUsage(ctx context.Context, res *Result, cr chatResponse) {
	u := &res.Usage
	known := false
	if cr.Usage != nil {
		u.PromptTokens += cr.Usage.PromptTokens
		u.CompletionTokens += cr.Usage.CompletionTokens
		u.ReasoningTokens += cr.Usage.CompletionTokensDetails.ReasoningTokens
		u.AudioTokens += cr.Usage.PromptTokensDetails.AudioTokens
		if cr.Usage.Cost != nil {
			u.CostUSD += *cr.Usage.Cost
			known = true
			if u.CostSource == "" {
				u.CostSource = "usage"
			}
		}
	}
	if !known && cr.ID != "" {
		cost, prov, err := c.generation(ctx, cr.ID)
		if err == nil {
			u.CostUSD += cost
			u.CostSource = "generation"
			if res.Provider == "" {
				res.Provider = prov
			}
			return
		}
		u.CostSource = "unknown"
		res.Warnings = append(res.Warnings, "cost not reported: "+err.Error())
	}
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

// saveImage writes a returned image (a data URL, or an https URL fetched once) to outDir.
func (c *Client) saveImage(ctx context.Context, u, outDir string, n int) (OutFile, error) {
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
	return writeImage(data, mt, outDir, n)
}

func writeImage(data []byte, mt, outDir string, n int) (OutFile, error) {
	if mt == "" || mt == "application/octet-stream" {
		mt = http.DetectContentType(data)
	}
	mt, _, _ = mime.ParseMediaType(mt)
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif", "image/svg+xml": ".svg"}[mt]
	if ext == "" {
		ext = ".bin"
	}
	path := filepath.Join(outDir, fmt.Sprintf("image-%02d%s", n, ext))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return OutFile{}, err
	}
	return OutFile{Path: path, MIME: mt, Bytes: int64(len(data))}, nil
}
