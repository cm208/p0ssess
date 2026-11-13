// Package remote is the client of the p0ssess engine service (POST /v1/recommend, GET /v1/table,
// POST /v1/outcomes). Every request carries the device token when one is saved, else the free
// install token. Tokens are never logged.
package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/paths"
)

// Version goes into the User-Agent; the main command sets it.
var Version = "dev"

// Error is a non-2xx answer.
type Error struct {
	Status     int
	Code       string
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("engine service: %d %s", e.Status, e.Code)
	}
	return fmt.Sprintf("engine service: %d", e.Status)
}

var errTestNetwork = errors.New("engine service: no network in tests (set P0SSESS_CLOUD_URL)")

// Client calls the engine service.
type Client struct {
	Dirs paths.Dirs
	HTTP *http.Client

	mu      sync.Mutex
	install string
}

func New(d paths.Dirs) *Client {
	return &Client{Dirs: d, HTTP: &http.Client{}}
}

// InstallTokenFile is where the free install token lives.
func InstallTokenFile(d paths.Dirs) string { return filepath.Join(d.State, "install-token") }

var installRe = regexp.MustCompile(`^p0i_[A-Za-z0-9_-]{43}$`)

// Linked reports whether a device token is saved.
func (c *Client) Linked() bool { return account.Token(c.Dirs) != "" }

// InstallToken returns the free install token, generating it once.
func (c *Client) InstallToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.install != "" {
		return c.install, nil
	}
	p := InstallTokenFile(c.Dirs)
	var tok string
	err := paths.WithLock(p, func() error {
		if b, err := os.ReadFile(p); err == nil {
			if t := strings.TrimSpace(string(b)); installRe.MatchString(t) {
				tok = t
				return nil
			}
		}
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return err
		}
		tok = "p0i_" + base64.RawURLEncoding.EncodeToString(raw[:])
		return paths.WriteFileAtomic(p, []byte(tok+"\n"), 0o600)
	})
	if err != nil {
		return "", err
	}
	c.install = tok
	return tok, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, hdr map[string]string) (*http.Response, error) {
	if testing.Testing() && os.Getenv("P0SSESS_CLOUD_URL") == "" {
		return nil, errTestNetwork
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, account.BaseURL()+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "p0ssess/"+Version)
	if tok := account.Token(c.Dirs); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	} else {
		it, err := c.InstallToken()
		if err != nil {
			return nil, err
		}
		req.Header.Set("P0ssess-Install", it)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	return hc.Do(req)
}

func apiError(r *http.Response) *Error {
	e := &Error{Status: r.StatusCode}
	var body struct {
		Error string `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body) == nil {
		e.Code = body.Error
	}
	if s, err := strconv.Atoi(strings.TrimSpace(r.Header.Get("Retry-After"))); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	return e
}

// Recommend asks the engine for the ranked routes of one task.
func (c *Client) Recommend(ctx context.Context, req Request) (*Response, error) {
	req.V = 1
	if req.Rules == nil {
		req.Rules = []Rule{}
	}
	if req.History == nil {
		req.History = []HistoryRow{}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	r, err := c.do(ctx, http.MethodPost, "/v1/recommend", body, nil)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return nil, apiError(r)
	}
	var out Response
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("engine service: %w", err)
	}
	if out.V != 1 {
		return nil, fmt.Errorf("engine service: answer version %d", out.V)
	}
	out.Plan = r.Header.Get("P0ssess-Plan")
	return &out, nil
}

// FetchTable downloads the signed route table. With etag set it asks for a change only; notModified
// is true on 304.
func (c *Client) FetchTable(ctx context.Context, etag string) (envelope []byte, newETag string, notModified bool, err error) {
	hdr := map[string]string{}
	if etag != "" {
		hdr["If-None-Match"] = etag
	}
	r, err := c.do(ctx, http.MethodGet, "/v1/table", nil, hdr)
	if err != nil {
		return nil, "", false, err
	}
	defer r.Body.Close()
	switch r.StatusCode {
	case http.StatusNotModified:
		return nil, etag, true, nil
	case http.StatusOK:
	default:
		return nil, "", false, apiError(r)
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		return nil, "", false, err
	}
	return b, r.Header.Get("ETag"), false, nil
}

// PostOutcomes sends one weekly batch of outcome records. status is the HTTP status; retryAfter is set
// on 429.
func (c *Client) PostOutcomes(ctx context.Context, body []byte) (status int, retryAfter time.Duration, err error) {
	r, err := c.do(ctx, http.MethodPost, "/v1/outcomes", body, nil)
	if err != nil {
		return 0, 0, err
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		e := apiError(r)
		return r.StatusCode, e.RetryAfter, e
	}
	io.Copy(io.Discard, io.LimitReader(r.Body, 8<<10))
	return r.StatusCode, 0, nil
}

// PutCalibration sends the account's whole set of calibration scores (POST /v1/calibration); with an
// empty body it forgets them (DELETE). Only a linked install calls it.
func (c *Client) PutCalibration(ctx context.Context, body []byte) error {
	if !c.Linked() {
		return errors.New("calibration needs a linked Pro install")
	}
	method := http.MethodPost
	if body == nil {
		method = http.MethodDelete
	}
	r, err := c.do(ctx, method, "/v1/calibration", body, nil)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return apiError(r)
	}
	io.Copy(io.Discard, io.LimitReader(r.Body, 8<<10))
	return nil
}
