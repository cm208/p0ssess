package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// The local jail's key proxy meters every model call itself, so a run's cost never depends on whether
// OpenCode knows the model's price. Each billed call is one line in the meter file (calls.jsonl, in a
// directory only the proxy container can write); the relay sums a run's lines. The relay names the
// current run and its cost cap in a run file the proxy can only read; once a run's metered spend reaches
// its cap, further model calls are refused, and a call whose cost cannot be read is charged the rest of
// the cap.

// MeterFile is the meter's file name inside its directory.
const MeterFile = "calls.jsonl"

// RunSpec is the run file: which run is current, its cap and model.
type RunSpec struct {
	Run    string  `json:"run"`
	CapUSD float64 `json:"cap_usd"`
	Model  string  `json:"model,omitempty"`
}

// Call is one metered model call. It never holds prompt or answer content.
type Call struct {
	Time  time.Time `json:"time"`
	Run   string    `json:"run,omitempty"`
	Model string    `json:"model"`
	// Known says CostUSD is OpenRouter's own figure (from the answer or the generation lookup).
	Known   bool    `json:"known"`
	CostUSD float64 `json:"cost_usd"`
	// ChargedUSD is what the call counted against the run's cap: its cost, or for an unknown cost the
	// rest of the cap.
	ChargedUSD   float64 `json:"charged_usd"`
	InputTokens  float64 `json:"input_tokens,omitempty"`
	OutputTokens float64 `json:"output_tokens,omitempty"`
}

// WriteRunSpec writes the run file atomically.
func WriteRunSpec(file string, s RunSpec) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func readRunSpec(file string) (RunSpec, bool) {
	var s RunSpec
	b, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(b, &s) != nil || math.IsNaN(s.CapUSD) || math.IsInf(s.CapUSD, 0) {
		return RunSpec{}, false
	}
	return s, true
}

// RunCost sums one run's metered calls.
type RunCost struct {
	// Calls is the number of billed calls; Unknown how many of them had no readable cost.
	Calls, Unknown int
	// KnownUSD is the sum of the known costs; ChargedUSD what the calls counted against the cap.
	KnownUSD, ChargedUSD float64
	// UnknownInput and UnknownOutput are the token counts the unknown calls reported, if any.
	UnknownInput, UnknownOutput float64
}

// ReadRunCost reads a meter file and sums the calls of run. ok is false when there is no meter file.
func ReadRunCost(file, run string) (RunCost, bool) {
	f, err := os.Open(file)
	if err != nil {
		return RunCost{}, false
	}
	defer f.Close()
	var rc RunCost
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var c Call
		if json.Unmarshal(sc.Bytes(), &c) != nil || c.Run != run {
			continue
		}
		rc.add(c)
	}
	return rc, true
}

func (rc *RunCost) add(c Call) {
	rc.Calls++
	rc.ChargedUSD += c.ChargedUSD
	if c.Known {
		rc.KnownUSD += c.CostUSD
	} else {
		rc.Unknown++
		rc.UnknownInput += c.InputTokens
		rc.UnknownOutput += c.OutputTokens
	}
}

// Meter wraps the key proxy with per-call metering and the run's cost cap.
type Meter struct {
	Dir     string // where calls.jsonl is written
	RunFile string // the relay's run file; "" or unreadable means no cap is known
	// Lookup reads a generation's cost when the answer carried none. Nil skips the lookup.
	Lookup func(ctx context.Context, id string) (float64, bool)

	mu      sync.Mutex
	run     string
	charged float64
	loaded  bool
}

// spentOn returns what run has been charged so far, reading the meter file the first time a run is seen
// (the proxy may have restarted mid-run). Callers hold mu.
func (m *Meter) spentOn(run string) float64 {
	if !m.loaded || run != m.run {
		rc, _ := ReadRunCost(filepath.Join(m.Dir, MeterFile), run)
		m.run, m.charged, m.loaded = run, rc.ChargedUSD, true
	}
	return m.charged
}

func (m *Meter) record(c Call) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spentOn(c.Run)
	m.charged += c.ChargedUSD
	b, _ := json.Marshal(c)
	f, err := os.OpenFile(filepath.Join(m.Dir, MeterFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

// Wrap meters next's model calls. Other requests pass through unchanged.
func (m *Meter) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ModelCall(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if enc := r.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
			APIError(w, http.StatusUnsupportedMediaType, "compressed request bodies are not supported")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil || len(body) > maxBody {
			APIError(w, http.StatusRequestEntityTooLarge, "request too large")
			return
		}
		body, model, err := WithUsage(body)
		if err != nil {
			APIError(w, http.StatusBadRequest, err.Error())
			return
		}
		spec, haveSpec := RunSpec{}, false
		if m.RunFile != "" {
			spec, haveSpec = readRunSpec(m.RunFile)
		}
		if haveSpec && spec.CapUSD > 0 {
			m.mu.Lock()
			over := m.spentOn(spec.Run) >= spec.CapUSD-1e-12
			m.mu.Unlock()
			if over {
				APIError(w, http.StatusPaymentRequired, "this run's cost cap is reached")
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Length", strconv.Itoa(len(body)))
		tap := NewCostTap(w)
		next.ServeHTTP(tap, r)
		s, rd := tap.Outcome(r.Context(), r.Context().Err() != nil, m.Lookup)
		if s == SettleNone {
			return
		}
		c := Call{Time: time.Now().UTC(), Run: spec.Run, Model: model, InputTokens: rd.InputTokens, OutputTokens: rd.OutputTokens}
		if s == SettleKnown {
			c.Known, c.CostUSD, c.ChargedUSD = true, rd.CostUSD, rd.CostUSD
		} else if haveSpec && spec.CapUSD > 0 {
			m.mu.Lock()
			rest := spec.CapUSD - m.spentOn(spec.Run)
			m.mu.Unlock()
			c.ChargedUSD = math.Max(0, rest)
		}
		m.record(c)
	})
}

// Options configure Serve.
type Options struct {
	Addr string
	Key  string
	ZDR  bool
	// DeadlineFile, when set, stops requests at the Unix time in it.
	DeadlineFile string
	// MeterDir, when set, meters every model call into MeterDir/calls.jsonl; RunFile names the relay's
	// run file with the current run and its cap.
	MeterDir, RunFile string
}

// ServeOptions listens on o.Addr until the process exits.
func ServeOptions(o Options) error {
	if o.Addr == "" || o.Key == "" {
		return errors.New("proxy: an address and a key are needed")
	}
	h := Handler(Upstream, o.Key, o.ZDR)
	if o.MeterDir != "" {
		m := &Meter{Dir: o.MeterDir, RunFile: o.RunFile,
			Lookup: GenerationLookup(&http.Client{Timeout: 10 * time.Second}, Upstream, o.Key)}
		h = m.Wrap(h)
	}
	if o.DeadlineFile != "" {
		h = WithDeadline(h, o.DeadlineFile)
	}
	srv := &http.Server{Addr: o.Addr, Handler: h, ReadHeaderTimeout: 30 * time.Second}
	return srv.ListenAndServe()
}
