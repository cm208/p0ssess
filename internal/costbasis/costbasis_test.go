package costbasis

import "testing"

func f(v float64) *float64 { return &v }

func TestFromUsage(t *testing.T) {
	for _, c := range []struct {
		name  string
		cost  *float64
		byok  bool
		up    *float64
		usd   float64
		basis string
		ok    bool
	}{
		{"paid", f(0.00001184), false, f(0.00001184), 0.00001184, Reported, true},
		{"paid, no details", f(0.002), false, nil, 0.002, Reported, true},
		{"free", f(0), false, nil, 0, Reported, true},
		{"byok with upstream", f(0), true, f(0.0000195), 0.0000195, BYOKUpstream, true},
		{"byok with a fee", f(0.000001), true, f(0.0000195), 0.0000205, BYOKUpstream, true},
		{"byok without upstream", f(0), true, nil, 0, "", false},
		{"unflagged byok", f(0), false, f(0.004), 0.004, BYOKUpstream, true},
		{"no cost", nil, false, nil, 0, "", false},
		{"negative", f(-1), false, nil, 0, "", false},
	} {
		usd, basis, ok := FromUsage(c.cost, c.byok, c.up)
		if usd != c.usd || basis != c.basis || ok != c.ok {
			t.Errorf("%s: %v %q %v", c.name, usd, basis, ok)
		}
	}
}

func TestCombine(t *testing.T) {
	if Combine("", Reported) != Reported || Combine(Reported, BYOKUpstream) != BYOKUpstream ||
		Combine(Tokens, BYOKUpstream) != Tokens || Combine(Tokens, Reserved) != Reserved || Combine(Reserved, Reported) != Reserved {
		t.Error("Combine")
	}
}
