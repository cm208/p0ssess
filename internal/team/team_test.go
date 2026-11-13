package team

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table"
	"p0ssess/internal/pme/table/tabletest"
	"p0ssess/internal/rules"
)

func keys() (map[string]ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv := tabletest.Key()
	return map[string]ed25519.PublicKey{table.KeyID(pub): pub}, priv
}

func payload(mut func(map[string]any)) []byte {
	p := map[string]any{"v": 1, "type": "team-policy", "org_id": "org_1", "org_name": "Acme", "account_id": "acc_1",
		"role": "member", "issued": "2026-11-13T07:00:00Z", "report_spend": true, "monthly_share_usd": 25,
		"rules": []map[string]any{{"id": "t-1a2b3c4d", "kind": "only", "models": []string{"openai/whisper-large-v3"},
			"use_cases": []string{"audio-transcribe"}}}}
	if mut != nil {
		mut(p)
	}
	b, _ := json.Marshal(p)
	return b
}

func sealed(mut func(map[string]any)) []byte {
	_, priv := keys()
	return tabletest.Seal(priv, payload(mut))
}

func TestVerify(t *testing.T) {
	ks, _ := keys()
	good, err := Verify(sealed(nil), ks, "acc_1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if good.OrgName != "Acme" || len(good.Rules) != 1 || *good.MonthlyShareUSD != 25 || !good.ReportSpend {
		t.Errorf("policy %+v", good)
	}

	seed := sha256.Sum256([]byte("another key"))
	other := ed25519.NewKeyFromSeed(seed[:])
	tampered := sealed(nil)
	var env table.Envelope
	json.Unmarshal(tampered, &env)
	env.Payload = strings.Replace(env.Payload, env.Payload[10:14], "AAAA", 1)
	tampered, _ = json.Marshal(env)
	older := sealed(func(p map[string]any) { p["issued"] = "2026-11-12T07:00:00Z" })

	cases := map[string]struct {
		raw    []byte
		acc    string
		cached *Policy
	}{
		"wrong key":     {tabletest.Seal(other, payload(nil)), "acc_1", nil},
		"wrong type":    {sealed(func(p map[string]any) { p["type"] = "route-table" }), "acc_1", nil},
		"route table":   {tabletest.Envelope(), "acc_1", nil},
		"wrong account": {sealed(nil), "acc_2", nil},
		"no account":    {sealed(nil), "", nil},
		"older issued":  {older, "acc_1", good},
		"tampered":      {tampered, "acc_1", nil},
		"bad issued":    {sealed(func(p map[string]any) { p["issued"] = "yesterday" }), "acc_1", nil},
	}
	for name, tc := range cases {
		if _, err := Verify(tc.raw, ks, tc.acc, tc.cached); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Verify(sealed(nil), ks, "acc_1", good); err != nil {
		t.Errorf("the same issued time is not older: %v", err)
	}
}

func TestLocalRulesAddsShare(t *testing.T) {
	ks, _ := keys()
	p, _ := Verify(sealed(nil), ks, "acc_1", nil)
	rs := p.LocalRules()
	if len(rs) != 2 || rs[0].Scope != rules.ScopeTeam || rs[1].ID != ShareRuleID || rs[1].Field != "monthly_usd" ||
		rs[1].Value != 25 || rs[1].Note != "team budget share" {
		t.Errorf("rules %+v", rs)
	}
	p, _ = Verify(sealed(func(m map[string]any) { delete(m, "monthly_share_usd") }), ks, "acc_1", nil)
	if rs := p.LocalRules(); len(rs) != 1 {
		t.Errorf("no share, no budget rule: %+v", rs)
	}
}

func TestCheck(t *testing.T) {
	v := func(f float64) *float64 { return &f }
	good := []Rule{
		{Kind: "forbid", Target: "model", Models: []string{"x-ai/grok"}},
		{Kind: "only", Models: []string{"openai/whisper"}, UseCases: []string{"audio-transcribe"}},
		{Kind: "budget", Field: "monthly_usd", Value: v(0)},
		{ID: "t-0123abcd", Kind: "ask", Kinds: []string{"agent"}},
	}
	if err := Check(good); err != nil {
		t.Error(err)
	}
	for name, r := range map[string]Rule{
		"allow":       {Kind: "allow", Target: "delegation"},
		"approve":     {Kind: "approve"},
		"pace daily":  {Kind: "budget", Field: "daily_usd", Value: v(0)},
		"no value":    {Kind: "budget", Field: "monthly_usd"},
		"bad id":      {ID: "r-0123abcd", Kind: "forbid", Target: "opencode"},
		"long note":   {Kind: "forbid", Target: "opencode", Note: strings.Repeat("x", 201)},
		"only, empty": {Kind: "only"},
	} {
		if err := Check([]Rule{r}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := Check(make([]Rule, MaxRules+1)); err == nil {
		t.Error("too many rules accepted")
	}
}

// fake is the Team side of the cloud service.
type fake struct {
	mu        sync.Mutex
	srv       *httptest.Server
	policy    []byte
	policyErr string
	spendErr  string
	batches   [][]Run
	bodies    []string
}

func newFake(t *testing.T) *fake {
	f := &fake{policy: sealed(nil)}
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, code string) {
		status := map[string]int{"not_in_team": 404, "not_entitled": 403, "bad_request": 400, "unavailable": 503, "invalid_token": 401}[code]
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	mux.HandleFunc("GET /v1/team/policy", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.policyErr != "" {
			fail(w, f.policyErr)
			return
		}
		w.Write(f.policy)
	})
	mux.HandleFunc("POST /v1/team/spend", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		f.bodies = append(f.bodies, string(raw))
		if f.spendErr != "" {
			fail(w, f.spendErr)
			return
		}
		var in struct {
			V    int   `json:"v"`
			Runs []Run `json:"runs"`
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if dec.Decode(&in) != nil || in.V != 1 || len(in.Runs) > MaxBatch {
			fail(w, "bad_request")
			return
		}
		f.batches = append(f.batches, in.Runs)
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(map[string]int{"accepted": len(in.Runs)})
	})
	mux.HandleFunc("GET /v1/account", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"account":{"id":"acc_1"},"entitlements":{"plan":"team","status":"active","org_id":"org_1"}}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// linked is an install linked to acc_1 in org_1, with the cloud at f.
func linked(t *testing.T) (paths.Dirs, *fake, *Client, map[string]ed25519.PublicKey) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	f := newFake(t)
	t.Setenv("P0SSESS_CLOUD_URL", f.srv.URL)
	d := paths.Default()
	account.SaveToken(d, "p0d_abc")
	c := NewClient(d, "test")
	if _, err := account.Refresh(context.Background(), d, c.API); err != nil {
		t.Fatal(err)
	}
	ks, _ := keys()
	return d, f, c, ks
}

func TestSyncCachesPolicy(t *testing.T) {
	d, f, c, ks := linked(t)
	res, err := Sync(context.Background(), c, ks)
	if err != nil || res.Policy == nil || res.Policy.OrgName != "Acme" {
		t.Fatalf("sync %+v %v", res, err)
	}
	st, err := os.Stat(policyFile(d))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	if p := Load(d, ks); p == nil || len(p.LocalRules()) != 2 {
		t.Errorf("cached policy %+v", p)
	}

	// An older policy, or one for another account, never replaces the cached one; offline keeps it.
	f.policy = sealed(func(p map[string]any) { p["issued"] = "2026-11-01T00:00:00Z"; p["rules"] = []any{} })
	if _, err := Sync(context.Background(), c, ks); err == nil {
		t.Error("older policy accepted")
	}
	f.policy = sealed(func(p map[string]any) { p["account_id"] = "acc_2"; p["issued"] = "2026-12-01T00:00:00Z" })
	if _, err := Sync(context.Background(), c, ks); err == nil {
		t.Error("another account's policy accepted")
	}
	f.policyErr = "unavailable"
	if res, err := Sync(context.Background(), c, ks); err == nil || res.Policy == nil {
		t.Errorf("offline should keep the policy: %+v %v", res, err)
	}
	if p := Load(d, ks); p == nil || len(p.Rules) != 1 {
		t.Errorf("cached policy changed: %+v", p)
	}

	// Without a token, no policy is in force.
	os.Remove(d.Config + "/secrets/p0ssess_token")
	if Load(d, ks) != nil {
		t.Error("policy in force without a token")
	}
}

func TestSyncForgets(t *testing.T) {
	for _, code := range []string{"not_in_team", "not_entitled"} {
		d, f, c, ks := linked(t)
		Sync(context.Background(), c, ks)
		Enqueue(d, ks, NewRun(time.Now(), "", "openai/gpt-x", "direct", 0.5), "")
		f.policyErr = code
		res, err := Sync(context.Background(), c, ks)
		if err != nil || !res.Left || Load(d, ks) != nil || len(Queued(d)) != 0 {
			t.Errorf("%s: %+v %v queue=%d", code, res, err, len(Queued(d)))
		}
	}

	// The account answering with no org forgets without a fetch.
	d, f, c, ks := linked(t)
	Sync(context.Background(), c, ks)
	st := account.Current(d)
	st.OrgID = ""
	b, _ := json.Marshal(st)
	paths.WriteFileAtomic(d.State+"/account.json", b, 0o600)
	f.policyErr = "unavailable"
	if res, _ := Sync(context.Background(), c, ks); !res.Left || Load(d, ks) != nil {
		t.Errorf("no org should forget: %+v", res)
	}
}

func TestEnqueueOnlyWithPolicy(t *testing.T) {
	d, f, c, ks := linked(t)
	now := time.Now()
	Enqueue(d, ks, NewRun(now, "", "openai/gpt-x", "direct", 0.5), "")
	if len(Queued(d)) != 0 {
		t.Error("queued without a policy")
	}
	f.policy = sealed(func(p map[string]any) { p["report_spend"] = false })
	Sync(context.Background(), c, ks)
	Enqueue(d, ks, NewRun(now, "", "openai/gpt-x", "direct", 0.5), "")
	if len(Queued(d)) != 0 {
		t.Error("queued with report_spend off")
	}
	f.policy = sealed(func(p map[string]any) { p["issued"] = "2026-11-14T00:00:00Z" })
	Sync(context.Background(), c, ks)
	for _, r := range []Run{
		NewRun(now, "", "openai/gpt-x", "direct", 0),
		NewRun(now, "", "openai/gpt-x", "reported", 1),
		NewRun(now, "Free text", "openai/gpt-x", "direct", 1),
		NewRun(now, "", "not a model", "direct", 1),
	} {
		Enqueue(d, ks, r, "")
	}
	if len(Queued(d)) != 0 {
		t.Errorf("bad runs queued: %+v", Queued(d))
	}
	Enqueue(d, ks, NewRun(now, "audio-transcribe", "openai/whisper-large-v3", "direct", 0.0123), "d-1")
	q := Queued(d)
	if len(q) != 1 || q[0].Day != now.UTC().Format("2006-01-02") || q[0].UseCase != "audio-transcribe" {
		t.Errorf("queue %+v", q)
	}
	Settle(d, "d-1", 0.01)
	if q := Queued(d); q[0].CostUSD != 0.01 {
		t.Errorf("settle %+v", q)
	}
}

func TestFlush(t *testing.T) {
	d, f, c, ks := linked(t)
	Sync(context.Background(), c, ks)
	now := time.Now()
	for i := 0; i < MaxBatch+20; i++ {
		Enqueue(d, ks, NewRun(now, "audio-transcribe", "openai/whisper-large-v3", "direct", 0.01), "d-x")
	}
	n, err := Flush(context.Background(), c)
	if err != nil || n != MaxBatch+20 || len(f.batches) != 2 || len(f.batches[0]) != MaxBatch || len(f.batches[1]) != 20 {
		t.Fatalf("sent %d err %v batches %d", n, err, len(f.batches))
	}
	if len(Queued(d)) != 0 {
		t.Error("queue not cleared")
	}
	// Exactly the five fields go out; the local run id never does.
	for _, b := range f.bodies {
		if strings.Contains(b, "d-x") || strings.Contains(b, `"run"`) {
			t.Errorf("body carries the run id: %s", b[:120])
		}
	}

	// Other failures keep the queue; bad_request drops the batch it was about.
	Enqueue(d, ks, NewRun(now, "", "openai/gpt-x", "agent", 0.2), "")
	f.spendErr = "unavailable"
	if _, err := Flush(context.Background(), c); err == nil || len(Queued(d)) != 1 {
		t.Errorf("unavailable should keep the queue: %v %d", err, len(Queued(d)))
	}
	f.spendErr = "bad_request"
	if _, err := Flush(context.Background(), c); err != nil || len(Queued(d)) != 0 {
		t.Errorf("bad_request should drop the batch: %v %d", err, len(Queued(d)))
	}

	// not_in_team clears the queue and the policy.
	Enqueue(d, ks, NewRun(now, "", "openai/gpt-x", "agent", 0.2), "")
	f.spendErr = "not_in_team"
	if _, err := Flush(context.Background(), c); err != nil || len(Queued(d)) != 0 || Load(d, ks) != nil {
		t.Errorf("not_in_team should clear: %v %d", err, len(Queued(d)))
	}
}

func TestFlushDropsStaleRuns(t *testing.T) {
	d, f, c, ks := linked(t)
	Sync(context.Background(), c, ks)
	Enqueue(d, ks, NewRun(time.Now().AddDate(0, 0, -70), "", "openai/gpt-x", "direct", 1), "")
	Enqueue(d, ks, NewRun(time.Now(), "", "openai/gpt-x", "direct", 1), "")
	if n, err := Flush(context.Background(), c); err != nil || n != 1 || len(f.batches[0]) != 1 {
		t.Errorf("sent %d %v", n, err)
	}
}

func TestSyncFlushes(t *testing.T) {
	d, f, c, ks := linked(t)
	Sync(context.Background(), c, ks)
	Enqueue(d, ks, NewRun(time.Now(), "", "openai/gpt-x", "calibrate", 0.3), "")
	res, err := Sync(context.Background(), c, ks)
	if err != nil || res.Sent != 1 || len(f.batches) != 1 {
		t.Errorf("sync %+v %v", res, err)
	}
}

func TestWatchSyncsAtStart(t *testing.T) {
	d, _, _, ks := linked(t)
	old := startDelay
	startDelay = time.Millisecond
	t.Cleanup(func() { startDelay = old })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Watch(ctx, d, ks); close(done) }()
	for i := 0; i < 200 && Load(d, ks) == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if Load(d, ks) == nil {
		t.Error("no policy after start")
	}
}
