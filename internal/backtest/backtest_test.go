package backtest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flagguard/internal/config"
)

func recorded(t *testing.T) (config.Config, Trace) {
	t.Helper()
	t.Setenv("FLAGSMITH_API_URL", "http://127.0.0.1:1")
	t.Setenv("PROMETHEUS_URL", "http://127.0.0.1:1")
	c, err := config.Load("../../guardrails.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c.StateFile = filepath.Join(t.TempDir(), "missing-directory", "state.json")
	b, err := os.ReadFile("../../docs/evidence/checkout-trace.json")
	if err != nil {
		t.Fatal(err)
	}
	var trace Trace
	if err = json.Unmarshal(b, &trace); err != nil {
		t.Fatal(err)
	}
	return c, trace
}

func TestRecordedIncident(t *testing.T) {
	for _, tc := range []struct {
		name            string
		edit            func(*config.Guardrail, *Trace)
		outcome, metric string
		eligible        int
	}{
		{"shipped policy", func(*config.Guardrail, *Trace) {}, "would_roll_back", "error_rate", 25},
		{"higher error limit", func(g *config.Guardrail, _ *Trace) { g.Rules[0].Threshold = .3 }, "would_roll_back", "p99_latency_ms", 25},
		{"higher limits", func(g *config.Guardrail, _ *Trace) { g.Rules[0].Threshold = .3; g.Rules[1].Threshold = 1000 }, "no_trigger_in_observed_data", "none", 25},
		{"longer confirmation", func(g *config.Guardrail, _ *Trace) {
			for i := range g.Rules {
				g.Rules[i].For = time.Minute
			}
		}, "no_trigger_in_observed_data", "none", 25},
		{"missing treatment breaks confirmation", func(g *config.Guardrail, t *Trace) { t.Series[g.SampleQuery][t.Polls-2] = nil }, "no_trigger_in_observed_data", "", 24},
		{"no traffic", func(g *config.Guardrail, t *Trace) { t.Series[g.SampleQuery] = make([]*float64, t.Polls) }, "insufficient_data", "", 0},
		{"missing control remains advisory", func(g *config.Guardrail, t *Trace) { t.Series[g.Control.SampleQuery] = make([]*float64, t.Polls) }, "would_roll_back", "error_rate", 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, trace := recorded(t)
			tc.edit(&c.Guardrails[0], &trace)
			r, err := Replay(context.Background(), c, trace)
			if err != nil {
				t.Fatal(err)
			}
			got := r.Guardrails[0]
			if got.Outcome != tc.outcome || got.Eligible != tc.eligible {
				t.Fatalf("outcome %s eligible %d", got.Outcome, got.Eligible)
			}
			if tc.metric != "" {
				if err = r.Check("checkout_flow=" + tc.metric); err != nil {
					t.Fatal(err)
				}
			} else if r.Check("checkout_flow=none") == nil {
				t.Fatal("missing evidence passed a no-trigger assertion")
			}
			if got.Decision != nil && (got.Decision.Time.Format(time.RFC3339) != "2026-11-21T17:48:37Z" || got.Decision.Change != nil) {
				t.Fatal("unexpected decision time or flag change")
			}
			if _, err = os.Stat(filepath.Dir(c.StateFile)); !os.IsNotExist(err) {
				t.Fatal("offline replay created live state")
			}
		})
	}
}
func TestTraceMismatch(t *testing.T) {
	c, trace := recorded(t)
	for _, bad := range []string{"query", "step", "version", "truncated"} {
		t.Run(bad, func(t *testing.T) {
			c, trace := recorded(t)
			switch bad {
			case "query":
				c.Guardrails[0].Rules[0].Query = "unknown"
			case "step":
				c.PollInterval = 2 * time.Second
			case "version":
				trace.Version++
			case "truncated":
				trace.Polls++
			}
			if _, err := Replay(context.Background(), c, trace); err == nil {
				t.Fatal("accepted incompatible trace")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Replay(ctx, c, trace); err == nil {
		t.Fatal("ignored cancellation")
	}
}
