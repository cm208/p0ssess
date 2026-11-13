package cloudsandbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// APIError is an error answer from the sandbox host or the accounts service.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("cloud sandbox: %s (%s)", e.Message, e.Code)
	}
	return fmt.Sprintf("cloud sandbox: HTTP %d %s", e.Status, e.Code)
}

func readAPIError(resp *http.Response) error {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = json.Unmarshal(b, &e)
	return &APIError{Status: resp.StatusCode, Code: e.Error, Message: e.Message}
}

// Transport is the session's HTTP/2 client: TLS 1.3, one connection. ca, when set, is a PEM file of
// roots to trust instead of the system's (tests and development hosts).
func Transport(ca string) (*http.Client, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS13}
	if ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no certificates", ca)
		}
		tc.RootCAs = pool
	}
	tr := &http.Transport{
		TLSClientConfig:     tc,
		ForceAttemptHTTP2:   true,
		MaxConnsPerHost:     1,
		IdleConnTimeout:     2 * time.Minute,
		TLSHandshakeTimeout: 15 * time.Second,
		Proxy:               http.ProxyFromEnvironment,
	}
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP2(true)
	return &http.Client{Transport: tr}, nil
}

// Spec is one cloud session.
type Spec struct {
	// URL is the sandbox host's base URL, https only.
	URL     string
	Ticket  string
	Workdir string
	// Internet is the project's shell-internet opt-in; without it the sandbox reaches package registries only.
	Internet bool
	Client   string
	// Model handles the sandbox's model calls: the cost guard around the key proxy.
	Model http.Handler
}

// Session is a live cloud sandbox session.
type Session struct {
	ID        string
	ExpiresAt time.Time
	Limits    map[string]int64

	base   *url.URL
	secret string
	hc     *http.Client
	model  http.Handler

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	ln       net.Listener
	srv      *http.Server
	password string

	mu     sync.Mutex
	ended  bool
	reason string
}

var workdirRE = regexp.MustCompile(`^/work/[A-Za-z0-9._-]{1,64}$`)

// RemoteWorkdir is where a repository called name appears in the sandbox (sandbox.md, create).
func RemoteWorkdir(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 64 {
		s = s[:64]
	}
	if s == "" || s == "." || s == ".." {
		s = "repo"
	}
	return "/work/" + s
}

// Create opens a session on the sandbox host. Nothing runs until Upload.
func Create(ctx context.Context, hc *http.Client, spec Spec) (*Session, error) {
	u, err := url.Parse(strings.TrimRight(spec.URL, "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("cloud sandbox URL %q must be https", spec.URL)
	}
	if !workdirRE.MatchString(spec.Workdir) {
		return nil, fmt.Errorf("bad sandbox workdir %q", spec.Workdir)
	}
	network := "registries"
	if spec.Internet {
		network = "internet"
	}
	body, _ := json.Marshal(map[string]any{"v": 1, "workdir": spec.Workdir, "network": network, "client": spec.Client})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String()+"/v1/sessions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+spec.Ticket)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud sandbox: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, readAPIError(resp)
	}
	var out struct {
		Session   string           `json:"session"`
		Secret    string           `json:"secret"`
		ExpiresAt time.Time        `json:"expires_at"`
		Limits    map[string]int64 `json:"limits"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return nil, fmt.Errorf("cloud sandbox: create answer: %w", err)
	}
	if !strings.HasPrefix(out.Session, "sbx_") || !strings.HasPrefix(out.Secret, "p0s_") || strings.ContainsAny(out.Session, "/?#%") {
		return nil, errors.New("cloud sandbox: create answer is malformed")
	}
	sctx, cancel := context.WithCancel(context.Background())
	return &Session{ID: out.Session, ExpiresAt: out.ExpiresAt, Limits: out.Limits, base: u, secret: out.Secret,
		hc: hc, model: spec.Model, ctx: sctx, cancel: cancel}, nil
}

func (s *Session) path(p string) string { return s.base.String() + "/v1/sessions/" + s.ID + p }

func (s *Session) do(ctx context.Context, method, p string, body io.Reader, ctype string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.path(p), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.secret)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	return s.hc.Do(req)
}

// UploadResult is the host's answer to an upload.
type UploadResult struct {
	Files    int     `json:"files"`
	Bytes    int64   `json:"bytes"`
	Base     string  `json:"base"`
	OpenCode string  `json:"opencode"`
	StartupS float64 `json:"startup_s"`
}

// Upload streams the tracked files of dir to the sandbox and returns once its OpenCode server is ready.
// The model tunnel starts first, so the sandbox can reach a model as soon as it is up.
func (s *Session) Upload(ctx context.Context, dir string) (Manifest, *UploadResult, error) {
	s.start()
	pr, pw := io.Pipe()
	var man Manifest
	done := make(chan error, 1)
	go func() {
		m, err := WriteUpload(dir, pw)
		man = m
		pw.CloseWithError(err)
		done <- err
	}()
	resp, err := s.do(ctx, http.MethodPut, "/files", pr, "application/x-tar+gzip")
	pr.CloseWithError(errors.New("upload ended"))
	werr := <-done
	if werr != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return man, nil, werr
	}
	if err != nil {
		return man, nil, fmt.Errorf("cloud sandbox upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return man, nil, readAPIError(resp)
	}
	var res UploadResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&res); err != nil {
		return man, nil, err
	}
	return man, &res, nil
}

// pollers is how many model polls stay open; the cost guard still lets one call through at a time.
const pollers = 4

func (s *Session) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil || s.ended {
		return
	}
	for i := 0; i < pollers; i++ {
		s.wg.Add(1)
		go s.poll()
	}
	s.startAPI()
}

func (s *Session) end(reason string) {
	s.mu.Lock()
	if !s.ended {
		s.ended, s.reason = true, reason
	}
	s.mu.Unlock()
	s.cancel()
}

// Ended reports whether the session is over, and why.
func (s *Session) Ended() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended, s.reason
}

type modelRequest struct {
	ID        string            `json:"id"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Query     string            `json:"query"`
	Headers   map[string]string `json:"headers"`
	BodyBytes int64             `json:"body_bytes"`
}

const maxEnvelope = 64 << 10

func (s *Session) poll() {
	defer s.wg.Done()
	failingSince := time.Time{}
	for s.ctx.Err() == nil {
		resp, err := s.do(s.ctx, http.MethodPost, "/model/next", nil, "")
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			if failingSince.IsZero() {
				failingSince = time.Now()
			} else if time.Since(failingSince) > time.Minute {
				s.end("the connection to the sandbox host was lost")
				return
			}
			select {
			case <-s.ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}
		failingSince = time.Time{}
		switch resp.StatusCode {
		case http.StatusNoContent:
			resp.Body.Close()
			continue
		case http.StatusOK:
		case http.StatusGone, http.StatusNotFound:
			err := readAPIError(resp)
			resp.Body.Close()
			s.end(err.Error())
			return
		default:
			resp.Body.Close()
			select {
			case <-s.ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}
		req, err := readModelRequest(s.ctx, resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}
		s.serveModel(req)
	}
}

func readModelRequest(ctx context.Context, r io.Reader) (*http.Request, error) {
	br := bufio.NewReaderSize(io.LimitReader(r, maxEnvelope+maxModelBody+1), 64<<10)
	line, err := br.ReadSlice('\n')
	if err != nil {
		return nil, err
	}
	var env modelRequest
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, err
	}
	if env.ID == "" || strings.ContainsAny(env.ID, "/?#%") || env.BodyBytes < 0 || env.BodyBytes > maxModelBody ||
		!strings.HasPrefix(env.Path, "/api/v1/") {
		return nil, errors.New("malformed model request")
	}
	body := make([]byte, env.BodyBytes)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	target := env.Path
	if env.Query != "" {
		target += "?" + env.Query
	}
	req, err := http.NewRequestWithContext(ctx, env.Method, "http://sandbox"+target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for _, h := range []string{"Content-Type", "Accept"} {
		if v := env.Headers[h]; v != "" {
			req.Header.Set(h, v)
		}
	}
	req.Header.Set("P0ssess-Request-Id", env.ID)
	return req, nil
}

// serveModel runs one model call through the guard and streams the answer back to the host.
func (s *Session) serveModel(req *http.Request) {
	rid := req.Header.Get("P0ssess-Request-Id")
	req.Header.Del("P0ssess-Request-Id")
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	tw := &tunnelWriter{s: s, rid: rid, h: http.Header{}, cancel: cancel}
	s.model.ServeHTTP(tw, req)
	tw.finish()
}

// tunnelWriter is the ResponseWriter for a tunneled model call: the first write opens the answer POST,
// and every later write streams into it.
type tunnelWriter struct {
	s       *Session
	rid     string
	h       http.Header
	cancel  context.CancelFunc
	started bool
	pw      *io.PipeWriter
	result  chan error
}

func (t *tunnelWriter) Header() http.Header { return t.h }

func (t *tunnelWriter) WriteHeader(code int) {
	if t.started {
		return
	}
	t.started = true
	hdr := map[string]string{}
	for _, k := range []string{"Content-Type", "Retry-After"} {
		if v := t.h.Get(k); v != "" {
			hdr[k] = v
		}
	}
	line, _ := json.Marshal(map[string]any{"status": code, "headers": hdr})
	pr, pw := io.Pipe()
	t.pw, t.result = pw, make(chan error, 1)
	go func() {
		body := io.MultiReader(bytes.NewReader(append(line, '\n')), pr)
		resp, err := t.s.do(t.s.ctx, http.MethodPost, "/model/"+t.rid, body, "application/x-p0ssess-model-response")
		if err == nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
		}
		if err != nil {
			t.cancel() // the sandbox stopped waiting: stop the upstream call
		}
		pr.CloseWithError(errors.New("answer closed"))
		t.result <- err
	}()
}

func (t *tunnelWriter) Write(b []byte) (int, error) {
	if !t.started {
		t.WriteHeader(http.StatusOK)
	}
	return t.pw.Write(b)
}

func (t *tunnelWriter) Flush() {}

func (t *tunnelWriter) finish() {
	if !t.started {
		t.WriteHeader(http.StatusBadGateway)
	}
	t.pw.Close()
	<-t.result
}

// startAPI serves the session's OpenCode API on a loopback port for the relay, behind a random password.
func (s *Session) startAPI() {
	b := make([]byte, 32)
	rand.Read(b)
	s.password = base64.RawURLEncoding.EncodeToString(b)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.ended, s.reason = true, "no loopback port: "+err.Error()
		return
	}
	target, _ := url.Parse(s.path("/opencode"))
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Set("Authorization", "Bearer "+s.secret)
		},
		Transport:     s.hc.Transport,
		FlushInterval: -1,
		ErrorLog:      nil,
	}
	pw := []byte(s.password)
	s.ln = ln
	s.srv = &http.Server{ReadHeaderTimeout: 30 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, got, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(got), pw) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		rp.ServeHTTP(w, r)
	})}
	go s.srv.Serve(ln)
}

// APIURL and Password are what the relay's OpenCode client uses for this session.
func (s *Session) APIURL() string {
	if s.ln == nil {
		return ""
	}
	return "http://" + s.ln.Addr().String()
}

func (s *Session) Password() string { return s.password }

// Status asks the host for the session's state.
func (s *Session) Status(ctx context.Context) (string, error) {
	if ended, why := s.Ended(); ended {
		return "ended", errors.New(why)
	}
	resp, err := s.do(ctx, http.MethodGet, "", nil, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "ended", readAPIError(resp)
	}
	var st struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&st); err != nil {
		return "", err
	}
	if st.State == "ended" {
		return st.State, fmt.Errorf("the sandbox session ended: %s", st.Reason)
	}
	return st.State, nil
}

// MaxDiff is the largest diff the client takes from the host.
const MaxDiff = 64 << 20

// Diff returns the sandbox's changes against the uploaded base, as a git binary diff.
func (s *Session) Diff(ctx context.Context) ([]byte, error) {
	resp, err := s.do(ctx, http.MethodGet, "/diff", nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxDiff+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxDiff {
		return nil, errors.New("the sandbox's diff is larger than 64 MiB")
	}
	return b, nil
}

// Close ends the session on the host and stops the tunnel. It is safe to call more than once.
func (s *Session) Close(ctx context.Context) error {
	var err error
	if ended, _ := s.Ended(); !ended || s.srv != nil {
		resp, derr := s.do(ctx, http.MethodDelete, "", nil, "")
		if derr == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusGone && resp.StatusCode != http.StatusNotFound {
				derr = fmt.Errorf("cloud sandbox delete: HTTP %d", resp.StatusCode)
			}
		}
		err = derr
	}
	s.end("closed")
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
	s.wg.Wait()
	return err
}
