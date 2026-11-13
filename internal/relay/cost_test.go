package relay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"p0ssess/internal/config"
	"p0ssess/internal/jail"
	"p0ssess/internal/opencode"
	"p0ssess/internal/proxy"
)

func writeCalls(t *testing.T, name string, cs ...proxy.Call) {
	t.Helper()
	f := jail.MeterFile(name)
	os.MkdirAll(filepath.Dir(f), 0o700)
	var b []byte
	for _, c := range cs {
		l, _ := json.Marshal(c)
		b = append(append(b, l...), '\n')
	}
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRunCostBasis covers how a run's cost is decided: OpenCode reports $0 for google/gemini-3.8-flash
// because it has no price for it, so the key proxy's measured figure must win, and a run nothing measured
// is priced from its tokens or counted at its cap, never at $0.
func TestRunCostBasis(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	e := NewEngine(config.Config{MaxRunCostUSD: 0.4}, "test", nil)
	priced := 0
	e.SetPricer(func(model string, tk opencode.Tokens) (float64, bool) {
		priced++
		if model != "google/gemini-3.8-flash" {
			return 0, false
		}
		return (tk.Input*0.3 + tk.Output*2.5) / 1e6, true
	})
	rec := &SessionRecord{Jail: "p0-test", MeterRun: "r1", Model: "google/gemini-3.8-flash", Grant: Grant{CapUSD: 0.25}}
	toks := opencode.Tokens{Input: 10000, Output: 2000}

	// Measured by the proxy: OpenCode's $0 is ignored, and calls of other runs do not count.
	writeCalls(t, "p0-test",
		proxy.Call{Run: "r1", Model: rec.Model, Known: true, CostUSD: 0.003, ChargedUSD: 0.003},
		proxy.Call{Run: "r0", Model: rec.Model, Known: true, CostUSD: 9, ChargedUSD: 9},
		proxy.Call{Run: "r1", Model: rec.Model, Known: true, CostUSD: 0.002, ChargedUSD: 0.002})
	if c, b := e.runCost(rec, 0, true, toks); b != CostMeasured || c < 0.00499 || c > 0.00501 {
		t.Errorf("measured: %v %s", c, b)
	}
	if got := e.liveCost(rec, 0); got < 0.00499 || got > 0.00501 {
		t.Errorf("the cap check while running must see the metered spend, got %v", got)
	}
	// Calls on the user's own provider key carry the provider's charge in their cost, and say so.
	writeCalls(t, "p0-test",
		proxy.Call{Run: "r1", Model: rec.Model, Known: true, Basis: "byok-upstream", CostUSD: 0.003, ChargedUSD: 0.003},
		proxy.Call{Run: "r1", Model: rec.Model, Known: true, Basis: "reported", CostUSD: 0.002, ChargedUSD: 0.002})
	if c, b := e.runCost(rec, 0, true, toks); b != CostBYOK || c < 0.00499 || c > 0.00501 {
		t.Errorf("byok: %v %s", c, b)
	}
	// A call whose cost could not be read is priced from its tokens.
	writeCalls(t, "p0-test",
		proxy.Call{Run: "r1", Model: rec.Model, Known: true, CostUSD: 0.003, ChargedUSD: 0.003},
		proxy.Call{Run: "r1", Model: rec.Model, ChargedUSD: 0.247, InputTokens: 1e6, OutputTokens: 0})
	if c, b := e.runCost(rec, 0, true, toks); b != CostTokens || c < 0.30299 || c > 0.30301 {
		t.Errorf("unknown call priced from tokens: %v %s", c, b)
	}
	// With no price either, the run counts at its cap.
	rec.Model = "newco/unpriced"
	if c, b := e.runCost(rec, 0, true, toks); b != CostCap || c != 0.25 {
		t.Errorf("unknown and unpriced: %v %s", c, b)
	}

	// No meter record of the run (an older jail): OpenCode's figure when it has one,
	rec = &SessionRecord{Jail: "p0-none", MeterRun: "r1", Model: "google/gemini-3.8-flash", Grant: Grant{CapUSD: 0.25}}
	if c, b := e.runCost(rec, 0.01, true, toks); b != CostOpenCode || c != 0.01 {
		t.Errorf("opencode: %v %s", c, b)
	}
	// its $0 priced from the tokens at the table's price (the gemini-3.8-flash case),
	if c, b := e.runCost(rec, 0, true, toks); b != CostTokens || c < 0.00799 || c > 0.00801 {
		t.Errorf("$0 for used tokens must be priced: %v %s", c, b)
	}
	// the cap when no price is known or the session could not be read,
	rec.Model = "newco/unpriced"
	if c, b := e.runCost(rec, 0, true, toks); b != CostCap || c != 0.25 {
		t.Errorf("$0, no price: %v %s", c, b)
	}
	if c, b := e.runCost(rec, 0.3, false, toks); b != CostCap || c != 0.3 {
		t.Errorf("unreadable session: %v %s", c, b)
	}
	// and $0 when nothing ran.
	if c, b := e.runCost(rec, 0, true, opencode.Tokens{}); b != CostOpenCode || c != 0 {
		t.Errorf("nothing ran: %v %s", c, b)
	}
	// The config's run ceiling is the cap when the grant names none.
	rec.Grant.CapUSD = 0
	if c, _ := e.runCost(rec, 0, true, toks); c != 0.4 {
		t.Errorf("default cap: %v", c)
	}
	if priced == 0 {
		t.Error("the pricer was never asked")
	}
}

func TestLedgerBasis(t *testing.T) {
	for in, want := range map[string]string{CostMeasured: "reported", CostOpenCode: "reported", CostBYOK: "byok-upstream", CostTokens: "tokens", CostCap: "reserved"} {
		if got := LedgerBasis(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}
