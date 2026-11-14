// Package table holds the signed route table: the engine's answers for every use case at default
// settings, and the facts about use cases and models the client's guard needs. The client routes from
// it when the engine service cannot be reached.
package table

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/remote"
)

// Envelope is the signed form the server sends and the client stores.
type Envelope struct {
	V       int    `json:"v"`
	KeyID   string `json:"key_id"`
	Payload string `json:"payload"`
	Sig     string `json:"sig"`
}

// UseCase is what the client knows about one use case.
type UseCase struct {
	ID            string   `json:"id"`
	N             int      `json:"n"`
	Task          string   `json:"task"`
	Kind          string   `json:"kind"`
	Family        string   `json:"family"`
	Path          string   `json:"path"`
	Inputs        []string `json:"inputs"`
	Outputs       []string `json:"outputs"`
	StakesRange   []int    `json:"stakes_range"`
	DefaultStakes int      `json:"default_stakes"`
	Turns         int      `json:"turns"`
	EditsCode     bool     `json:"edits_code"`
	Verify        string   `json:"verify"`
	Preprocess    string   `json:"preprocess,omitempty"`
}

// StakesBounds returns the level range the use case covers.
func (u *UseCase) StakesBounds() (int, int) {
	if len(u.StakesRange) == 2 {
		return u.StakesRange[0], u.StakesRange[1]
	}
	if u.DefaultStakes == 0 {
		return 1, 4
	}
	return u.DefaultStakes, u.DefaultStakes
}

// Route is the engine's answer for one use case, stakes level and λ point.
type Route struct {
	UseCase        string          `json:"use_case"`
	Stakes         int             `json:"stakes"`
	Lambda         string          `json:"lambda"`
	ClaudeFeasible bool            `json:"claude_feasible"`
	Pick           int             `json:"pick"`
	Options        []remote.Option `json:"options"`
}

// Table is a verified route table.
type Table struct {
	V            int                `json:"v"`
	Tier         string             `json:"tier"`
	ID           string             `json:"id"`
	Issued       string             `json:"issued,omitempty"`
	LambdaPoints map[string]float64 `json:"lambda_points"`
	UseCases     []UseCase          `json:"use_cases"`
	Models       []Model            `json:"models"`
	Routes       []Route            `json:"routes"`
	// NoLeadRoutes are the answers for a caller with no Claude lead, one per use case and stakes level.
	// Tables signed before 1.0.2 have none.
	NoLeadRoutes []Route `json:"no_lead_routes,omitempty"`

	models json.RawMessage
	byUC   map[string]*UseCase
	byM    map[string]*Model
}

func empty() *Table { return &Table{byUC: map[string]*UseCase{}, byM: map[string]*Model{}} }

// Empty reports a table with no use cases: none verified yet.
func (t *Table) Empty() bool { return t == nil || len(t.UseCases) == 0 }

func (t *Table) UseCase(id string) *UseCase {
	if t == nil {
		return nil
	}
	return t.byUC[id]
}

func (t *Table) Model(id string) *Model {
	if t == nil {
		return nil
	}
	return t.byM[id]
}

// Route returns the entry for a use case and stakes level whose λ point is closest to lambda.
func (t *Table) Route(useCase string, stakes int, lambda float64) *Route {
	if t == nil {
		return nil
	}
	var best *Route
	bestGap := math.Inf(1)
	for i := range t.Routes {
		r := &t.Routes[i]
		if r.UseCase != useCase || r.Stakes != stakes {
			continue
		}
		v, ok := t.LambdaPoints[r.Lambda]
		if !ok {
			continue
		}
		if gap := math.Abs(v - lambda); gap < bestGap {
			best, bestGap = r, gap
		}
	}
	return best
}

// NoLeadRoute returns the no-lead entry for a use case at the stakes level closest to stakes (the
// higher on a tie), or nil.
func (t *Table) NoLeadRoute(useCase string, stakes int) *Route {
	if t == nil {
		return nil
	}
	var best *Route
	for i := range t.NoLeadRoutes {
		r := &t.NoLeadRoutes[i]
		if r.UseCase != useCase {
			continue
		}
		if best == nil || gap(r.Stakes, stakes) < gap(best.Stakes, stakes) || gap(r.Stakes, stakes) == gap(best.Stakes, stakes) && r.Stakes > best.Stakes {
			best = r
		}
	}
	return best
}

// HasNoLead reports whether the table carries no-lead routes.
func (t *Table) HasNoLead() bool { return t != nil && len(t.NoLeadRoutes) > 0 }

func gap(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

// Levels lists the stakes levels the table has routes for, for one use case.
func (t *Table) Levels(useCase string) []int {
	seen := map[int]bool{}
	var out []int
	for _, r := range t.Routes {
		if r.UseCase == useCase && !seen[r.Stakes] {
			seen[r.Stakes] = true
			out = append(out, r.Stakes)
		}
	}
	return out
}

// ModelsJSON is the models section in the {"models":[...]} shape the direct path and the relay read.
func (t *Table) ModelsJSON() []byte {
	if t == nil || len(t.models) == 0 {
		return nil
	}
	var b bytes.Buffer
	b.WriteString(`{"models":`)
	b.Write(t.models)
	b.WriteString("}\n")
	return b.Bytes()
}

// KeyID is the first 8 bytes, in hex, of the SHA-256 of a public key.
func KeyID(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return hex.EncodeToString(s[:8])
}

// prodKey is the public half of the engine's production signing key (key id 5f3c395ee96ca932).
// The development key is no longer trusted.
const prodKey = "E2QMzvvrMFvaKJxjJPJPNyxZ7q7wfIHtNAxNXF+laQc="

func pinned() map[string]ed25519.PublicKey {
	b, err := base64.StdEncoding.DecodeString(prodKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		panic("table: bad pinned key")
	}
	pub := ed25519.PublicKey(b)
	return map[string]ed25519.PublicKey{KeyID(pub): pub}
}

//go:embed default.json
var builtIn []byte

var errTier = errors.New("route table: unexpected tier")

// PinnedKeys returns the public keys this client trusts for signed envelopes, by key id.
func PinnedKeys() map[string]ed25519.PublicKey { return pinned() }

// Unseal checks an envelope's signature against keys and returns the payload it carries. what names the
// payload in errors.
func Unseal(raw []byte, keys map[string]ed25519.PublicKey, what string) ([]byte, error) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if env.V != 1 {
		return nil, fmt.Errorf("%s: envelope version %d", what, env.V)
	}
	pub, ok := keys[env.KeyID]
	if !ok {
		return nil, fmt.Errorf("%s: unknown signing key", what)
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	sig, err := base64.StdEncoding.DecodeString(env.Sig)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if !ed25519.Verify(pub, payload, sig) {
		return nil, fmt.Errorf("%s: bad signature", what)
	}
	return payload, nil
}

// Verify checks an envelope against keys and returns the table it carries. tiers lists the tiers that
// are accepted.
func Verify(raw []byte, keys map[string]ed25519.PublicKey, tiers ...string) (*Table, error) {
	payload, err := Unseal(raw, keys, "route table")
	if err != nil {
		return nil, err
	}
	t := empty()
	var raw2 struct {
		Models json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(payload, t); err != nil {
		return nil, fmt.Errorf("route table: %w", err)
	}
	if err := json.Unmarshal(payload, &raw2); err != nil {
		return nil, fmt.Errorf("route table: %w", err)
	}
	if t.V != 1 {
		return nil, fmt.Errorf("route table: version %d", t.V)
	}
	if !has(tiers, t.Tier) {
		return nil, errTier
	}
	t.models = raw2.Models
	for i := range t.UseCases {
		t.byUC[t.UseCases[i].ID] = &t.UseCases[i]
	}
	for i := range t.Models {
		t.byM[t.Models[i].ID] = &t.Models[i]
	}
	return t, nil
}

// Store keeps the current table: the last good one fetched, else the one built into the binary.
type Store struct {
	dir      string
	keys     map[string]ed25519.PublicKey
	builtIn  []byte
	mu       sync.RWMutex
	cur      *Table
	fromFile bool
}

type Option func(*Store)

// WithKeys pins these public keys instead of the built-in ones.
func WithKeys(pubs ...ed25519.PublicKey) Option {
	return func(s *Store) {
		s.keys = map[string]ed25519.PublicKey{}
		for _, p := range pubs {
			s.keys[KeyID(p)] = p
		}
	}
}

// WithBuiltIn replaces the envelope built into the binary.
func WithBuiltIn(env []byte) Option { return func(s *Store) { s.builtIn = env } }

var hexID = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

var allTiers = []string{"free", "pro", "team"}

// Open loads the cached table from cacheDir, or the built-in one. A table that fails to verify is
// ignored; with neither, the store holds an empty table.
func Open(cacheDir string, opts ...Option) *Store {
	s := &Store{dir: cacheDir, keys: pinned(), builtIn: builtIn}
	for _, o := range opts {
		o(s)
	}
	s.cur = empty()
	if b, err := os.ReadFile(s.file()); err == nil {
		if t, err := Verify(b, s.keys, allTiers...); err == nil {
			s.cur, s.fromFile = t, true
			return s
		}
	}
	if t, err := Verify(s.builtIn, s.keys, "free"); err == nil {
		s.cur = t
	}
	return s
}

func (s *Store) file() string     { return filepath.Join(s.dir, "route-table.json") }
func (s *Store) etagFile() string { return filepath.Join(s.dir, "route-table.etag") }
func (s *Store) checkFile() string {
	return filepath.Join(s.dir, "route-table.checked")
}

// Keys returns the public keys the store trusts.
func (s *Store) Keys() map[string]ed25519.PublicKey {
	if s == nil {
		return pinned()
	}
	return s.keys
}

// Table returns the current table; it is never nil.
func (s *Store) Table() *Table {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// ETag is the tag of the cached table, or "" when the table in use was not fetched.
func (s *Store) ETag() string {
	s.mu.RLock()
	fromFile := s.fromFile
	s.mu.RUnlock()
	if !fromFile {
		return ""
	}
	b, _ := os.ReadFile(s.etagFile())
	return string(bytes.TrimSpace(b))
}

// Accept verifies a fetched envelope and, when it is good, stores it and makes it current. linked
// says the request carried a device token; without one only a free table is accepted.
func (s *Store) Accept(env []byte, etag string, linked bool) error {
	tiers := []string{"free"}
	if linked {
		tiers = allTiers
	}
	t, err := Verify(env, s.keys, tiers...)
	if err != nil {
		return err
	}
	if err := paths.WriteFileAtomic(s.file(), env, 0o600); err != nil {
		return err
	}
	if etag == "" {
		etag = `"` + t.ID + `"`
	}
	if err := paths.WriteFileAtomic(s.etagFile(), []byte(etag+"\n"), 0o600); err != nil {
		return err
	}
	s.mu.Lock()
	s.cur, s.fromFile = t, true
	s.mu.Unlock()
	return nil
}

// MaxAge is how long a check of the server holds before the next one.
const MaxAge = 24 * time.Hour

// Due reports whether the server should be asked for a newer table.
func (s *Store) Due(now time.Time) bool {
	st, err := os.Stat(s.checkFile())
	return err != nil || now.Sub(st.ModTime()) >= MaxAge
}

// Checked records an attempt to fetch, whatever its result, so the next one waits a day.
func (s *Store) Checked(now time.Time) {
	p := s.checkFile()
	if err := paths.WriteFileAtomic(p, []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o600); err == nil {
		os.Chtimes(p, now, now)
	}
}

// ModelsFile writes the table's models section to the cache in the {"models":[...]} shape, once per
// table, and returns its path.
func (s *Store) ModelsFile() (string, error) {
	t := s.Table()
	b := t.ModelsJSON()
	if b == nil {
		return "", errors.New("no route table yet")
	}
	id := t.ID
	if !hexID.MatchString(id) {
		sum := sha256.Sum256(b)
		id = hex.EncodeToString(sum[:8])
	}
	p := filepath.Join(s.dir, "models-"+id+".json")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	if err := paths.WriteFileAtomic(p, b, 0o600); err != nil {
		return "", err
	}
	if old, _ := filepath.Glob(filepath.Join(s.dir, "models-*.json")); len(old) > 1 {
		for _, o := range old {
			if o != p {
				os.Remove(o)
			}
		}
	}
	return p, nil
}
