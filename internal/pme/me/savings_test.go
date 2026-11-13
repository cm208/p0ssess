package me

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/calibrate"
	"p0ssess/internal/pme/me/pdf"
)

func f(v float64) *float64 { return &v }

func fixture() calibrate.Report {
	return calibrate.Report{
		Pro:         true,
		GeneratedAt: time.Date(2026, 3, 4, 15, 0, 0, 0, time.Local),
		Calibrated:  time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC),
		Baselined:   time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC),
		Kinds: []calibrate.KindRow{
			{UseCase: "transcribe-audio", Task: "Transcribe a short clip (with names)", Model: "acme/ear-1",
				Alone: &calibrate.Side{Runs: 10, Correct: 8, CostPerCorrectUSD: f(0.41), MeanSeconds: 42, WeeklySharePerCorrect: f(0.004), Source: "baseline"},
				With:  &calibrate.Side{Runs: 10, Correct: 9, CostPerCorrectUSD: f(0.0021), MeanSeconds: 6.2, Source: "calibration"}},
			{UseCase: "read-chart", Task: "Read a value off a chart", Model: "acme/eye-2",
				Alone: &calibrate.Side{Runs: 5, Correct: 0, MeanSeconds: 30, Source: "baseline"},
				With:  &calibrate.Side{Runs: 5, Correct: 4, CostPerCorrectUSD: f(0.013), MeanSeconds: 9, Source: "calibration"}},
		},
		Models: []calibrate.ModelRow{
			{UseCase: "transcribe-audio", Model: "acme/ear-1", Runs: 10, Correct: 9, CostPerCorrectUSD: f(0.0021), MeanSeconds: 6.2, Picked: true},
			{UseCase: "transcribe-audio", Model: "acme/ear-0", Runs: 10, Correct: 0, MeanSeconds: 3},
		},
		Totals: &calibrate.Totals{Kinds: 1, AloneUSD: 0.41, WithUSD: 0.0021, AloneSeconds: 42, WithSeconds: 6.2},
		Prices: []calibrate.Price{{Model: "claude-test", In: 3, Out: 15, CacheRead: 0.3}},
		Notes:  []string{"Each kind ran on ten samples."},
	}
}

func serve(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/t/"+s.Token+"/"+path, nil)
	req.Host = s.Addr
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func testServer(fn func() (calibrate.Report, error)) *Server {
	return &Server{Token: "tok", Addr: "127.0.0.1:9", Savings: fn}
}

func TestSavingsJSON(t *testing.T) {
	w := serve(t, testServer(nil), "api/savings")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("empty: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	var raw map[string]any
	json.Unmarshal(w.Body.Bytes(), &raw)
	if raw["pro"] != false {
		t.Errorf("nil Savings must report pro false: %s", w.Body)
	}
	for _, k := range []string{"kinds", "models", "prices", "notes"} {
		if _, ok := raw[k].([]any); !ok {
			t.Errorf("%s must be an array, got %v", k, raw[k])
		}
	}

	w = serve(t, testServer(func() (calibrate.Report, error) { return fixture(), nil }), "api/savings")
	var got calibrate.Report
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Pro || len(got.Kinds) != 2 || *got.Kinds[0].With.CostPerCorrectUSD != 0.0021 || got.Kinds[1].Alone.CostPerCorrectUSD != nil {
		t.Errorf("report did not round-trip: %s", w.Body)
	}

	w = serve(t, testServer(func() (calibrate.Report, error) { return calibrate.Report{}, fmt.Errorf("no store") }), "api/savings")
	if w.Code != 500 {
		t.Errorf("error: %d", w.Code)
	}
}

// streams returns the page content streams of a PDF this package wrote.
func streams(b []byte) string {
	var out strings.Builder
	for _, m := range regexp.MustCompile(`(?s)stream\n(.*?)endstream`).FindAllSubmatch(b, -1) {
		out.Write(m[1])
	}
	return out.String()
}

func TestSavingsPDFHandler(t *testing.T) {
	w := serve(t, testServer(func() (calibrate.Report, error) { return fixture(), nil }), "savings.pdf")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename="p0ssess-savings-2026-03-04.pdf"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	b := w.Body.Bytes()
	if !strings.HasPrefix(string(b), "%PDF-") || !strings.HasSuffix(string(b), "%%EOF\n") {
		t.Fatal("not a PDF")
	}
	if err := pdf.Verify(b); err != nil {
		t.Fatal(err)
	}
	text := streams(b)
	for _, want := range []string{"(p0ssess savings report)", "(Transcribe audio)", "($0.41)", "($0.0021 *)", "(0.40%)",
		"(no correct)", "(answers in 5)", "(Routed to acme/ear-1)", `(Transcribe a short clip \(with names\))`, "(routed here)",
		"(claude-test)", "($15.00)", "Each kind ran on ten samples.", "195\\327 cheaper", "Model call only", "(Page 1 of 1)"} {
		if !strings.Contains(text, want) {
			t.Errorf("content lacks %q", want)
		}
	}
}

func TestSavingsPDFEmptyStates(t *testing.T) {
	money := regexp.MustCompile(`\$\d|\d%`)
	for name, c := range map[string]struct {
		r    calibrate.Report
		want string
	}{
		"free":    {calibrate.Report{Pro: false, Prices: fixture().Prices}, "part of p0ssess Pro"},
		"no runs": {calibrate.Report{Pro: true, Prices: fixture().Prices}, "Nothing has been measured yet"},
	} {
		b, err := SavingsPDF(c.r)
		if err != nil {
			t.Fatal(err)
		}
		if err := pdf.Verify(b); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := streams(b)
		if !strings.Contains(text, c.want) {
			t.Errorf("%s: lacks %q", name, c.want)
		}
		if money.MatchString(text) {
			t.Errorf("%s: an empty report printed a number: %s", name, money.FindString(text))
		}
	}

	r := fixture()
	r.Kinds, r.Totals, r.Baselined = nil, nil, time.Time{}
	text := streams(mustPDF(t, r))
	if !strings.Contains(text, "Claude alone has not been measured yet") || !strings.Contains(text, "(acme/ear-0)") {
		t.Error("calibration without a baseline must show the model table and say Claude alone was not measured")
	}
}

func TestSavingsPDFPages(t *testing.T) {
	r := fixture()
	for i := 0; i < 90; i++ {
		r.Models = append(r.Models, calibrate.ModelRow{UseCase: "describe-image", Model: fmt.Sprintf("acme/model-%d", i), Runs: 4, Correct: 2, CostPerCorrectUSD: f(0.01), MeanSeconds: 5})
	}
	b := mustPDF(t, r)
	m := regexp.MustCompile(`/Count (\d+)`).FindSubmatch(b)
	if m == nil || string(m[1]) == "1" {
		t.Fatalf("a long table should run onto more pages: %s", m)
	}
	if text := streams(b); !strings.Contains(text, "(acme/model-89)") || strings.Count(text, "(Cost per correct)") < 2 {
		t.Error("table heads must repeat on each page and every row must print")
	}
}

func mustPDF(t *testing.T, r calibrate.Report) []byte {
	t.Helper()
	b, err := SavingsPDF(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := pdf.Verify(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFormats(t *testing.T) {
	for _, c := range [][2]string{
		{usd(0.41), "$0.41"}, {usd(0.0021), "$0.0021"}, {usd(0.1), "$0.10"}, {usd(12.345), "$12.35"}, {usd(1234), "$1,234"}, {usd(0.00001), "<$0.0001"},
		{secs(4.24), "4.2 s"}, {secs(42), "42 s"}, {secs(150), "2.5 min"},
		{share(0.004), "0.40%"}, {share(0.25), "25%"}, {share(0.012), "1.2%"},
		{compare(0.41, 0.0021, "cheaper", "more expensive"), "195× cheaper"},
		{compare(1, 0.7, "cheaper", "more expensive"), "30% cheaper"},
		{compare(1, 3, "cheaper", "more expensive"), "3.0× more expensive"},
		{compare(1, 1.01, "cheaper", "more expensive"), "about the same"},
		{sideCost(&calibrate.Side{Runs: 1}), "no correct answers in 1 run"},
		{sideCost(nil), "not measured"},
	} {
		if c[0] != c[1] {
			t.Errorf("got %q, want %q", c[0], c[1])
		}
	}
}
