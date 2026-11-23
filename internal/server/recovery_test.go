package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"flagguard/internal/config"
	"flagguard/internal/engine"
	"flagguard/internal/flagsmith"
	"flagguard/internal/prometheus"
)

// This transport exercises the production clients without a network listener.
// Only Flagsmith storage and the Prometheus response bodies are fixtures.
type priceRehearsal struct {
	*prometheus.Client
	t           *testing.T
	flagEnabled bool
	writes      int
	samples     []prometheus.ProbeSample
}

func (p *priceRehearsal) RoundTrip(r *http.Request) (*http.Response, error) {
	p.t.Helper()
	var body any
	switch {
	case r.URL.Host == "flags.test" && r.Method == http.MethodGet:
		body = []any{map[string]any{"id": 1, "feature": map[string]any{"id": 1, "name": "price_response_v2"}, "enabled": p.flagEnabled}}
	case r.URL.Host == "flags.test" && r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/featurestates/1/"):
		var change struct{ Enabled *bool }
		if err := json.NewDecoder(r.Body).Decode(&change); err != nil || change.Enabled == nil {
			p.t.Fatalf("invalid Flagsmith write: %v", err)
		}
		p.flagEnabled = *change.Enabled
		p.writes++
		body = map[string]bool{"enabled": p.flagEnabled}
	case r.URL.Host == "metrics.test" && r.URL.Path == "/api/v1/query":
		q := r.URL.Query()
		at, err := time.Parse(time.RFC3339Nano, q.Get("time"))
		if err != nil {
			p.t.Fatal(err)
		}
		if !strings.HasSuffix(q.Get("query"), "[20000ms]") {
			p.t.Fatalf("unexpected raw probe query: %s", q.Get("query"))
		}
		pairs := make([][]any, 0)
		for _, sample := range p.samples {
			if sample.At.After(at) || !sample.At.After(at.Add(-20*time.Second)) {
				continue
			}
			pass := "0"
			if sample.Passed {
				pass = "1"
			}
			pairs = append(pairs, []any{float64(sample.At.UnixMilli()) / 1000, pass})
		}
		body = map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{"values": pairs}}}}

	default:
		p.t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		p.t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
}

// Script aggregate rollout readings on the test clock. Raw recovery probes
// still go through the production Prometheus matrix decoder.
func (p *priceRehearsal) QueryAt(_ context.Context, query string, _ time.Time) (float64, bool, error) {
	switch {
	case strings.HasPrefix(query, "count_over_time("):
		return 6, true, nil
	case strings.HasPrefix(query, "avg_over_time("):
		if strings.Contains(query, `variant="candidate"`) {
			return 0, true, nil
		}
		return 1, true, nil
	default:
		p.t.Fatalf("unexpected rollout expression: %s", query)
	}
	return 0, false, nil
}

func priceContract(t *testing.T, fixture string) bool {
	t.Helper()
	var v struct {
		SKU         string
		Currency    string
		AmountCents int `json:"amount_cents"`
	}
	raw, err := os.ReadFile("../../deploy/blackbox/fixtures/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v.SKU == "coffee" && v.Currency == "USD" && v.AmountCents == 1299
}

func TestPriceRecoveryRehearsal(t *testing.T) {
	t.Setenv("FLAGSMITH_API_URL", "http://flags.test")
	t.Setenv("PROMETHEUS_URL", "http://metrics.test")
	t.Setenv("FLAGGUARD_STATE_FILE", "")
	policy, err := config.Load("../../deploy/blackbox/guardrails.yaml")
	if err != nil {
		t.Fatal(err)
	}
	policy.Mode = "enforce"
	store := &priceRehearsal{t: t, flagEnabled: true, Client: prometheus.New(policy.Prometheus.URL)}
	flags := flagsmith.New(policy.Flagsmith.APIURL, "fixture", "env")

	flags.HTTP.Transport = store
	store.HTTP.Transport = store
	controller, err := engine.New(policy, store, flags, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Engine: controller}).Handler()
	reset := func(want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "http://localhost/api/reset/price_response_v2", strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("reset got %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	broken := priceContract(t, "renamed.json")
	fixed := priceContract(t, "price.json")
	if broken || !fixed {
		t.Fatal("price fixtures no longer demonstrate this contract")
	}
	now := time.Now().Truncate(time.Millisecond)
	ctx := context.Background()
	// Run the shipped policy on a simulated timeline with a sustained contract failure.
	for _, ago := range []int{45, 40, 35, 30, 25, 20, 15, 10, 5, 0} {
		at := now.Add(-time.Duration(ago) * time.Second)
		store.samples = append(store.samples, prometheus.ProbeSample{At: at, Passed: broken})
		controller.Evaluate(ctx, at)
	}
	rows, _ := controller.Snapshot()
	if store.flagEnabled || store.writes != 1 || rows[0].Status != "ROLLED_BACK" || rows[0].Recovery.Status != "FAILING" {
		t.Fatalf("expected confirmed rollback and a broken probe: %+v, writes=%d", rows[0], store.writes)
	}
	reset(http.StatusConflict)
	if store.writes != 1 || store.flagEnabled {
		t.Fatal("premature reset reached Flagsmith")
	}
	t.Log("Broken price contract: confirmed rollback, reset returned 409, flag stayed off.")
	// Substitute a clean synthetic probe window at the same decision point.
	// This is a deterministic comparison, not a timed live repair.
	for i := range store.samples {
		store.samples[i].Passed = fixed
	}
	reset(http.StatusOK)
	rows, history := controller.Snapshot()
	last := history[len(history)-1]
	if !store.flagEnabled || store.writes != 2 || last.Kind != "RESET" || last.Change == nil || !last.Change.After || last.Recovery == nil || last.Recovery.Observed < 3 || last.Recovery.Passed != last.Recovery.Observed || rows[0].Status != "WARMING_UP" {
		t.Fatalf("incomplete reset: %+v", last)
	}
	t.Logf("Clean price contract: %d/%d probes passed, operator reset returned 200, enabled state read back.", last.Recovery.Passed, last.Recovery.Observed)
}
