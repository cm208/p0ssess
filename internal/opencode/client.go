// Package opencode is a small client for the OpenCode 2.2.x server API (/api/*).
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// SupportedVersions lists the OpenCode server versions this client was checked against.
var SupportedVersions = map[string]bool{"2.2.5": true}

type Client struct {
	BaseURL  string
	Password string
	HTTP     *http.Client // for short calls
	Stream   *http.Client // for SSE; no overall timeout
}

func New(baseURL, password string) *Client {
	return &Client{
		BaseURL:  baseURL,
		Password: password,
		HTTP:     &http.Client{Timeout: 60 * time.Second},
		Stream:   &http.Client{},
	}
}

type APIError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *APIError) Error() string {
	b := e.Body
	if len(b) > 300 {
		b = b[:300] + "…"
	}
	return fmt.Sprintf("opencode %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, b)
}

func (c *Client) req(ctx context.Context, method, path string, q url.Values, body any) (*http.Request, error) {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	r.SetBasicAuth("opencode", c.Password)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r, nil
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	r, err := c.req(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &APIError{method, path, resp.StatusCode, string(raw)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("opencode %s %s: decode: %w", method, path, err)
	}
	return nil
}

type envelope[T any] struct {
	Data T `json:"data"`
}

func (c *Client) Info(ctx context.Context) (ServerInfo, error) {
	var v ServerInfo
	err := c.do(ctx, "GET", "/api/info", nil, nil, &v)
	return v, err
}

var ErrUnsupportedVersion = errors.New("unsupported OpenCode version")

// CheckVersion refuses servers whose version was not verified against this client.
func (c *Client) CheckVersion(ctx context.Context) (string, error) {
	info, err := c.Info(ctx)
	if err != nil {
		return "", err
	}
	if !SupportedVersions[info.Version] {
		return info.Version, fmt.Errorf("%w: server reports %q", ErrUnsupportedVersion, info.Version)
	}
	return info.Version, nil
}

func (c *Client) CreateSession(ctx context.Context, in CreateSession) (Session, error) {
	var v envelope[Session]
	err := c.do(ctx, "POST", "/api/session", nil, in, &v)
	return v.Data, err
}

func (c *Client) GetSession(ctx context.Context, id string) (Session, error) {
	var v envelope[Session]
	err := c.do(ctx, "GET", "/api/session/"+url.PathEscape(id), nil, nil, &v)
	return v.Data, err
}

func (c *Client) Prompt(ctx context.Context, id string, p Prompt) (InboxItem, error) {
	var v envelope[InboxItem]
	err := c.do(ctx, "POST", "/api/session/"+url.PathEscape(id)+"/prompt", nil, p, &v)
	return v.Data, err
}

func (c *Client) SwitchModel(ctx context.Context, id string, m ModelRef) error {
	return c.do(ctx, "POST", "/api/session/"+url.PathEscape(id)+"/model", nil, map[string]any{"model": m}, nil)
}

func (c *Client) Interrupt(ctx context.Context, id string) (bool, error) {
	var v struct {
		Interrupted bool `json:"interrupted"`
	}
	err := c.do(ctx, "POST", "/api/session/"+url.PathEscape(id)+"/interrupt", nil, nil, &v)
	return v.Interrupted, err
}

// Wait blocks until the session's agent loop is idle (experimental endpoint).
func (c *Client) Wait(ctx context.Context, id string) error {
	r, err := c.req(ctx, "POST", "/api/experimental/session/"+url.PathEscape(id)+"/wait", nil, nil)
	if err != nil {
		return err
	}
	resp, err := c.Stream.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &APIError{"POST", r.URL.Path, resp.StatusCode, string(b)}
	}
	return nil
}

type MessageQuery struct {
	Limit  int
	Order  string // asc | desc
	Type   string
	Cursor string
}

func (c *Client) Messages(ctx context.Context, id string, mq MessageQuery) ([]Message, string, error) {
	q := url.Values{}
	if mq.Limit > 0 {
		q.Set("limit", strconv.Itoa(mq.Limit))
	}
	if mq.Order != "" {
		q.Set("order", mq.Order)
	}
	if mq.Type != "" {
		q.Set("type", mq.Type)
	}
	if mq.Cursor != "" {
		q.Set("cursor", mq.Cursor)
	}
	var v struct {
		Data   []Message `json:"data"`
		Cursor struct {
			Next *string `json:"next"`
		} `json:"cursor"`
	}
	err := c.do(ctx, "GET", "/api/session/"+url.PathEscape(id)+"/message", q, nil, &v)
	next := ""
	if v.Cursor.Next != nil {
		next = *v.Cursor.Next
	}
	return v.Data, next, err
}

// AllMessages pages through the whole session history, oldest first.
func (c *Client) AllMessages(ctx context.Context, id string) ([]Message, error) {
	var all []Message
	cur := ""
	for i := 0; i < 200; i++ {
		q := MessageQuery{Limit: 100, Order: "asc"}
		if cur != "" {
			q = MessageQuery{Limit: 100, Cursor: cur} // the server rejects a cursor combined with order
		}
		page, next, err := c.Messages(ctx, id, q)
		if err != nil {
			return all, err
		}
		all = append(all, page...)
		if next == "" || len(page) == 0 {
			break
		}
		cur = next
	}
	return all, nil
}

func (c *Client) Diff(ctx context.Context, id string) ([]FileDiff, error) {
	var v envelope[[]FileDiff]
	err := c.do(ctx, "GET", "/api/session/"+url.PathEscape(id)+"/diff", nil, nil, &v)
	return v.Data, err
}

func (c *Client) Permissions(ctx context.Context, id string) ([]PermissionRequest, error) {
	var v envelope[[]PermissionRequest]
	err := c.do(ctx, "GET", "/api/session/"+url.PathEscape(id)+"/permission", nil, nil, &v)
	return v.Data, err
}

var ErrAlways = errors.New(`refusing to answer "always": it saves a project-wide allow rule`)

// ReplyPermission answers a pending request with "once" or "reject". "always" is refused.
func (c *Client) ReplyPermission(ctx context.Context, sessionID, requestID, decision, message string) error {
	switch decision {
	case "once", "reject":
	case "always":
		return ErrAlways
	default:
		return fmt.Errorf("invalid decision %q", decision)
	}
	body := PermissionReply{Decision: decision}
	if message != "" {
		body.Message = &message
	}
	return c.do(ctx, "POST", "/api/session/"+url.PathEscape(sessionID)+"/permission/"+url.PathEscape(requestID)+"/reply", nil, body, nil)
}

func (c *Client) stream(ctx context.Context, path string, q url.Values, fn func(Frame) bool) error {
	r, err := c.req(ctx, "GET", path, q, nil)
	if err != nil {
		return err
	}
	r.Header.Set("Accept", "text/event-stream")
	resp, err := c.Stream.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &APIError{"GET", path, resp.StatusCode, string(b)}
	}
	return ReadFrames(resp.Body, fn)
}

// Events follows the volatile global feed. It has no replay; use Log to catch up.
func (c *Client) Events(ctx context.Context, types []string, fn func(Frame) bool) error {
	q := url.Values{}
	for _, t := range types {
		q.Add("types", t)
	}
	return c.stream(ctx, "/api/event", q, fn)
}

// Log reads a session's durable events after seq (exclusive; after < 0 reads from the start).
// With follow it keeps streaming live durable events until ctx ends or fn returns false.
func (c *Client) Log(ctx context.Context, id string, after int64, follow bool, fn func(Frame) bool) error {
	q := url.Values{}
	if after >= 0 {
		q.Set("after", strconv.FormatInt(after, 10))
	}
	if follow {
		q.Set("follow", "true")
	} else {
		q.Set("follow", "false")
	}
	return c.stream(ctx, "/api/experimental/session/"+url.PathEscape(id)+"/log", q, fn)
}
