package preflight

import (
	"context"
	"errors"
	"flagguard/internal/config"
	"flagguard/internal/flagsmith"
	"testing"
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

func (q query) Query(context.Context, string) (float64, bool, error) {
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
