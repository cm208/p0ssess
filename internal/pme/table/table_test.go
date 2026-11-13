package table_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"p0ssess/internal/pme/table"
	"p0ssess/internal/pme/table/tabletest"
)

func TestBuiltInVerifiesWithThePinnedKey(t *testing.T) {
	s := table.Open(t.TempDir())
	tb := s.Table()
	if tb.Empty() || tb.Tier != "free" {
		t.Fatalf("the built-in table did not verify: %d use cases, tier %q", len(tb.UseCases), tb.Tier)
	}
	u := tb.UseCase("audio-transcribe-noisy")
	if u == nil || u.Preprocess != "speech_bandpass" {
		t.Fatalf("audio-transcribe-noisy: %+v", u)
	}
	if tb.Issued != "" {
		t.Error("a free table carries no issue date")
	}
	if tb.Model("anthropic/claude-sonnet-5.5") == nil {
		t.Error("the table lacks the default lead model")
	}
	if s.ETag() != "" {
		t.Error("a built-in table has no etag to send")
	}
}

func TestPinnedKeyID(t *testing.T) {
	b, _ := base64.StdEncoding.DecodeString("geck4OX4R4kwzDil6EIboqNdUzW8gpqnLLElaXongys=")
	if id := table.KeyID(ed25519.PublicKey(b)); id != "dd8c7efb6dac450c" {
		t.Errorf("key id %s", id)
	}
}

func TestRejectsWhatItCannotTrust(t *testing.T) {
	pub, priv := tabletest.Key()
	keys := map[string]ed25519.PublicKey{table.KeyID(pub): pub}
	good := tabletest.Envelope()
	if _, err := table.Verify(good, keys, "free"); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var env table.Envelope
	json.Unmarshal(good, &env)
	payload, _ := base64.StdEncoding.DecodeString(env.Payload)

	tampered := env
	tampered.Payload = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(payload), "0.0926", "0.0001", 1)))
	b, _ := json.Marshal(tampered)
	if _, err := table.Verify(b, keys, "free"); err == nil {
		t.Error("a changed payload verified")
	}
	_, other, _ := ed25519.GenerateKey(nil)
	if _, err := table.Verify(tabletest.Seal(other, payload), keys, "free"); err == nil {
		t.Error("an unknown key verified")
	}
	if _, err := table.Verify(good, keys, "pro"); err == nil {
		t.Error("a free table was taken where only pro was asked for")
	}
	if _, err := table.Verify(tabletest.Seal(priv, []byte("{not json")), keys, "free"); err == nil {
		t.Error("bad JSON verified")
	}
	if _, err := table.Verify(good, nil, "free"); err == nil {
		t.Error("the fixture verified against the production keys")
	}
}

func TestStoreKeepsTheLastGoodTable(t *testing.T) {
	dir := t.TempDir()
	s := tabletest.Store(dir)
	if s.Table().Empty() {
		t.Fatal("fixture not loaded")
	}
	_, other, _ := ed25519.GenerateKey(nil)
	if err := s.Accept(tabletest.Seal(other, tabletest.Payload()), "", false); err == nil {
		t.Error("accepted a table signed by an unknown key")
	}
	if s.Table().Empty() {
		t.Error("a rejected table replaced the good one")
	}
	if err := s.Accept(tabletest.Envelope(), `"x"`, false); err != nil {
		t.Fatal(err)
	}
	if s.ETag() != `"x"` {
		t.Errorf("etag %q", s.ETag())
	}
	pub, _ := tabletest.Key()
	again := table.Open(dir, table.WithKeys(pub), table.WithBuiltIn([]byte("{}")))
	if again.Table().ID != s.Table().ID || again.ETag() != `"x"` {
		t.Error("the cached table was not reloaded")
	}
	if none := table.Open(t.TempDir(), table.WithBuiltIn([]byte(`{"v":1}`))); !none.Table().Empty() {
		t.Error("a placeholder built-in table should leave the store empty")
	}
	p, err := s.ModelsFile()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Models []table.Model `json:"models"`
	}
	b, _ := os.ReadFile(p)
	if json.Unmarshal(b, &doc) != nil || len(doc.Models) != len(s.Table().Models) || filepath.Dir(p) != dir {
		t.Errorf("models file %s: %d models", p, len(doc.Models))
	}
}

func TestRouteTakesTheClosestLambda(t *testing.T) {
	tb := tabletest.Store(t.TempDir()).Table()
	if r := tb.Route("code-implement-feature", 3, 0.9); r == nil || r.Lambda != "api" {
		t.Errorf("λ 0.9: %+v", r)
	}
	if r := tb.Route("code-implement-feature", 3, 0.2); r == nil || r.Lambda != "subscription" {
		t.Errorf("λ 0.2: %+v", r)
	}
	if r := tb.Route("code-implement-feature", 4, 1); r != nil {
		t.Error("no stakes-4 entry exists")
	}
}

func TestModelFacts(t *testing.T) {
	tb := tabletest.Store(t.TempDir()).Table()
	ds := tb.Model("deepseek/deepseek-v4.1-flash")
	if !ds.PriceKnown() || ds.IsClaude() || !ds.Endpoints.HasZDR || ds.SingleProvider() {
		t.Errorf("deepseek facts: %+v", ds)
	}
	if tp, ok := ds.Prices(true, false); !ok || tp.In <= 0 {
		t.Error("no zero-retention price")
	}
	var unknown *table.Model
	if unknown.PriceKnown() || unknown.IsClaude() || unknown.Providers() != nil {
		t.Error("a nil model must know nothing")
	}
	if !(&table.Model{ID: "x/y:free"}).Free() {
		t.Error(":free is free")
	}
}
