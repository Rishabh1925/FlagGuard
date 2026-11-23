package preflight

import (
	"context"
	"errors"
	"flagguard/internal/config"
	"flagguard/internal/flagsmith"
	"flagguard/internal/prometheus"
	"testing"
	"time"
)

type reader struct{}

func (reader) Find(context.Context, string) (flagsmith.FeatureState, error) {
	return flagsmith.FeatureState{Enabled: true}, nil
}

type query struct {
	value   float64
	present bool
	err     error
}

func (q query) QueryAt(context.Context, string, time.Time) (float64, bool, error) {
	return q.value, q.present, q.err
}
func TestReadinessRequiresHealthyFiniteTelemetry(t *testing.T) {
	c := config.Config{Guardrails: []config.Guardrail{{Flag: "checkout", Rules: []config.Rule{{Metric: "errors", Query: "errors", Comparison: "gt", Threshold: .05}}}}}
	for _, tc := range []struct {
		name  string
		q     query
		ready bool
	}{
		{"healthy", query{.01, true, nil}, true}, {"breached", query{.2, true, nil}, false},
		{"missing", query{0, false, nil}, false}, {"offline", query{0, false, errors.New("offline")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Run(context.Background(), c, tc.q, reader{})
			if r.Ready != tc.ready || !r.ReadOnly {
				t.Fatalf("unexpected report: %+v", r)
			}
		})
	}
}

type capturedQueries map[string]time.Time

func (q capturedQueries) QueryAt(_ context.Context, expr string, at time.Time) (float64, bool, error) {
	q[expr] = at
	switch expr {
	case "candidate_count":
		return 6, true, nil
	case "candidate_passes":
		return 1, true, nil
	case "control_count":
		return 1, true, nil
	default:
		return 0, false, nil
	}
}

func TestControlDiagnosticsRemainAdvisory(t *testing.T) {
	g := config.Guardrail{Flag: "price", SampleQuery: "candidate_count", MinimumSamples: 4,
		Control: &config.Control{SampleQuery: "control_count", MinimumSamples: 4},
		Rules:   []config.Rule{{Metric: "pass", Query: "candidate_passes", ControlQuery: "control_passes", Comparison: "lt", Threshold: .9}}}
	captured := capturedQueries{}
	report := Run(context.Background(), config.Config{Guardrails: []config.Guardrail{g}}, captured, reader{})
	if !report.Ready || len(captured) != 4 {
		t.Fatalf("unexpected diagnostics: %+v", report)
	}
	missing := map[string]bool{}
	for _, ch := range report.Checks {
		if !ch.OK && ch.Advisory {
			missing[ch.Name] = true
		}
	}
	if !missing["control_sample_volume"] || !missing["control_pass"] {
		t.Fatalf("missing advisory failures: %+v", report)
	}
	for _, at := range captured {
		if !at.Equal(captured["candidate_count"]) {
			t.Fatal("queries used different evaluation times")
		}
	}
}

type probeQueries struct {
	query
	age time.Duration
}

func (q probeQueries) ProbeWindow(_ context.Context, _ string, _ time.Duration, at time.Time) ([]prometheus.ProbeSample, error) {
	return []prometheus.ProbeSample{{At: at.Add(-q.age), Passed: false}}, nil
}
func TestProbePreflightChecksFreshnessWithoutAuthorizingRecovery(t *testing.T) {
	c := config.Config{Guardrails: []config.Guardrail{{Flag: "price", RecoveryProbe: &config.RecoveryProbe{Metric: "probe_success", Window: 20 * time.Second, MaxGap: 8 * time.Second}}}}
	for _, age := range []time.Duration{time.Second, 10 * time.Second} {
		result := Run(context.Background(), c, probeQueries{age: age}, reader{})
		probe := result.Checks[1]
		if !result.Ready || !result.ReadOnly || !probe.Advisory || probe.Name != "recovery_probe" || probe.OK != (age <= 8*time.Second) {
			t.Fatalf("unexpected diagnostic: %+v", result)
		}
	}
}
