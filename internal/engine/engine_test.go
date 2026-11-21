package engine

import (
	"context"
	"errors"
	"flagguard/internal/config"
	"flagguard/internal/flagsmith"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type sample struct {
	v   float64
	ok  bool
	err error
}
type fakeQuery map[string]sample

func (q fakeQuery) Query(_ context.Context, s string) (float64, bool, error) {
	v := q[s]
	return v.v, v.ok, v.err
}

type fakeFlags struct {
	enabled bool
	writes  int
	err     error
	findErr error
}

func (f *fakeFlags) Find(context.Context, string) (flagsmith.FeatureState, error) {
	return flagsmith.FeatureState{Enabled: f.enabled}, f.findErr
}
func (f *fakeFlags) SetEnabled(_ context.Context, _ string, b bool) (flagsmith.Change, error) {
	f.writes++
	if f.err != nil {
		return flagsmith.Change{}, f.err
	}
	c := flagsmith.Change{Before: f.enabled, After: b}
	f.enabled = b
	return c, nil
}
func fixture(t *testing.T) (*Engine, fakeQuery, *fakeFlags, time.Time) {
	t.Helper()
	c := config.Config{PollInterval: 5 * time.Second, StateFile: filepath.Join(t.TempDir(), "state.json"), Guardrails: []config.Guardrail{{Name: "Checkout", Flag: "checkout_flow", Variant: "variant_b", Action: "disable_flag", MinimumSamples: 20, SampleQuery: "count", Rules: []config.Rule{{Metric: "error_rate", Query: "errors", Threshold: .05, Comparison: "gt", For: 15 * time.Second}, {Metric: "latency", Query: "latency", Threshold: 500, Comparison: "gt", For: 15 * time.Second}}}}}
	q := fakeQuery{"count": {100, true, nil}, "errors": {.01, true, nil}, "latency": {60, true, nil}}
	f := &fakeFlags{enabled: true}
	e, err := New(c, q, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e, q, f, time.Now()
}
func status(t *testing.T, e *Engine, want string) {
	t.Helper()
	s, _ := e.Snapshot()
	if s[0].Status != want {
		t.Fatalf("status=%s want=%s (%s)", s[0].Status, want, s[0].Message)
	}
}
func TestHealthyAndTransientSpike(t *testing.T) {
	e, q, f, now := fixture(t)
	ctx := context.Background()
	e.Evaluate(ctx, now)
	status(t, e, "HEALTHY")
	q["errors"] = sample{.2, true, nil}
	e.Evaluate(ctx, now.Add(5*time.Second))
	status(t, e, "BREACHING")
	q["errors"] = sample{.01, true, nil}
	e.Evaluate(ctx, now.Add(10*time.Second))
	status(t, e, "HEALTHY")
	if f.writes != 0 {
		t.Fatal("transient spike changed flag")
	}
}
func TestSustainedBreachExactlyOnceAndRestart(t *testing.T) {
	e, q, f, now := fixture(t)
	q["errors"] = sample{.2, true, nil}
	for _, s := range []int{0, 5, 10, 15, 20, 25} {
		e.Evaluate(context.Background(), now.Add(time.Duration(s)*time.Second))
	}
	status(t, e, "ROLLED_BACK")
	if f.writes != 1 || f.enabled {
		t.Fatalf("writes=%d enabled=%v", f.writes, f.enabled)
	}
	restarted, err := New(e.cfg, q, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Evaluate(context.Background(), now.Add(30*time.Second))
	status(t, restarted, "ROLLED_BACK")
	if f.writes != 1 {
		t.Fatal("restart repeated rollback")
	}
	_, events := restarted.Snapshot()
	count := 0
	for _, v := range events {
		if v.Kind == "ROLLBACK" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("rollback events=%d", count)
	}
}
func TestMissingDataAndErrorsBreakContinuity(t *testing.T) {
	for _, missing := range []sample{{0, false, nil}, {0, false, errors.New("offline")}} {
		t.Run("missing", func(t *testing.T) {
			e, q, f, now := fixture(t)
			ctx := context.Background()
			q["errors"] = sample{.2, true, nil}
			e.Evaluate(ctx, now)
			e.Evaluate(ctx, now.Add(5*time.Second))
			q["latency"] = missing
			e.Evaluate(ctx, now.Add(10*time.Second))
			q["latency"] = sample{60, true, nil}
			e.Evaluate(ctx, now.Add(15*time.Second))
			e.Evaluate(ctx, now.Add(20*time.Second))
			if f.writes != 0 {
				t.Fatal("missing sample preserved breach continuity")
			}
		})
	}
}
func TestLowTrafficDoesNotRollback(t *testing.T) {
	e, q, f, now := fixture(t)
	q["count"] = sample{4, true, nil}
	q["errors"] = sample{1, true, nil}
	for i := 0; i < 8; i++ {
		e.Evaluate(context.Background(), now.Add(time.Duration(i)*5*time.Second))
	}
	status(t, e, "WAITING")
	if f.writes != 0 {
		t.Fatal("low volume rollback")
	}
}
func TestIndependentRuleTimers(t *testing.T) {
	e, q, f, now := fixture(t)
	ctx := context.Background()
	q["errors"] = sample{.3, true, nil}
	e.Evaluate(ctx, now)
	e.Evaluate(ctx, now.Add(5*time.Second))
	q["errors"] = sample{0, true, nil}
	q["latency"] = sample{800, true, nil}
	e.Evaluate(ctx, now.Add(10*time.Second))
	e.Evaluate(ctx, now.Add(15*time.Second))
	if f.writes != 0 {
		t.Fatal("timers shared between rules")
	}
	e.Evaluate(ctx, now.Add(20*time.Second))
	e.Evaluate(ctx, now.Add(25*time.Second))
	if f.writes != 1 {
		t.Fatal("second rule did not roll back")
	}
}
func TestResetAndWarmup(t *testing.T) {
	e, q, f, now := fixture(t)
	q["errors"] = sample{.2, true, nil}
	for i := 0; i <= 3; i++ {
		e.Evaluate(context.Background(), now.Add(time.Duration(i)*5*time.Second))
	}
	if err := e.Reset(context.Background(), "checkout_flow"); err != nil {
		t.Fatal(err)
	}
	status(t, e, "WARMING_UP")
	e.Evaluate(context.Background(), time.Now())
	status(t, e, "WARMING_UP")
	if !f.enabled || f.writes != 2 {
		t.Fatal("reset did not re-enable exactly once")
	}
}
func TestUnconfirmedWriteLocksForInspection(t *testing.T) {
	e, q, f, now := fixture(t)
	q["errors"] = sample{.2, true, nil}
	f.err = errors.New("timeout")
	for i := 0; i < 8; i++ {
		e.Evaluate(context.Background(), now.Add(time.Duration(i)*5*time.Second))
	}
	if f.writes != 1 {
		t.Fatal("ambiguous write retried")
	}
	status(t, e, "ACTION_REQUIRED")
}
func TestFailedPersistencePreventsWrite(t *testing.T) {
	e, q, f, now := fixture(t)
	e.cfg.StateFile = "/dev/null/state.json"
	q["errors"] = sample{.2, true, nil}
	for i := 0; i < 5; i++ {
		e.Evaluate(context.Background(), now.Add(time.Duration(i)*5*time.Second))
	}
	if f.writes != 0 {
		t.Fatal("mutation sent without durable lock")
	}
	status(t, e, "ERROR")
}
func TestEvaluationGapResetsTimer(t *testing.T) {
	e, q, f, now := fixture(t)
	q["errors"] = sample{.2, true, nil}
	e.Evaluate(context.Background(), now)
	e.Evaluate(context.Background(), now.Add(time.Minute))
	if f.writes != 0 {
		t.Fatal("gap treated as continuous evidence")
	}
}
func TestConcurrentSnapshots(t *testing.T) {
	e, _, _, now := fixture(t)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				e.Snapshot()
			}
		}()
	}
	for i := 0; i < 5; i++ {
		e.Evaluate(context.Background(), now.Add(time.Duration(i)*time.Second))
	}
	wg.Wait()
}

func TestDecisionEvidenceSurvivesResetAndRestart(t *testing.T) {
	e, q, f, now := fixture(t)
	q["errors"] = sample{.2, true, nil}
	for i := 0; i <= 3; i++ {
		e.Evaluate(context.Background(), now.Add(time.Duration(i)*5*time.Second))
	}
	_, events := e.Snapshot()
	var evidence *Evidence
	for _, event := range events {
		if event.Kind == "ROLLBACK" {
			evidence = event.Evidence
		}
	}
	if evidence == nil || evidence.Samples == nil || *evidence.Samples != 100 || evidence.Rules[0].Since == nil || *evidence.Rules[0].Value != .2 {
		t.Fatal("decision inputs missing")
	}
	// Mutating an exported snapshot must not rewrite the audit history.
	*evidence.Rules[0].Value = 999
	if err := e.Reset(context.Background(), "checkout_flow"); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(e.cfg, q, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, events = restarted.Snapshot()
	for _, event := range events {
		if event.Kind == "ROLLBACK" {
			if *event.Evidence.Rules[0].Value != .2 || event.Evidence.Rules[0].Since == nil {
				t.Fatal("historical evidence changed")
			}
			return
		}
	}
	t.Fatal("rollback lost")
}

func TestShadowRehearsalNeverMutatesAndRearmsAfterRecovery(t *testing.T) {
	e, q, f, now := fixture(t)
	c := e.cfg
	c.Mode = "shadow"
	e, err := New(c, q, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	q["errors"] = sample{.2, true, nil}
	for i := 0; i < 8; i++ {
		e.Evaluate(context.Background(), now.Add(time.Duration(i)*5*time.Second))
	}
	status(t, e, "SHADOW_TRIGGERED")
	if err := e.Reset(context.Background(), "checkout_flow"); err == nil {
		t.Fatal("shadow allowed reset")
	}
	if f.writes != 0 || !f.enabled || e.latched["checkout_flow"] {
		t.Fatal("shadow changed or locked flag")
	}
	_, events := e.Snapshot()
	count := 0
	for _, v := range events {
		if v.Kind == "WOULD_ROLLBACK" {
			count++
			if v.Change != nil || v.Evidence.Mode != "shadow" || !v.Evidence.ActionFinishedAt.IsZero() {
				t.Fatal("shadow claimed a mutation")
			}
		}
	}
	if count != 1 {
		t.Fatalf("duplicate rehearsal events: %d", count)
	}
	q["errors"] = sample{0, true, nil}
	e.Evaluate(context.Background(), now.Add(40*time.Second))
	status(t, e, "HEALTHY")
	q["errors"] = sample{.2, true, nil}
	for i := 9; i <= 12; i++ {
		e.Evaluate(context.Background(), now.Add(time.Duration(i)*5*time.Second))
	}
	_, events = e.Snapshot()
	count = 0
	for _, v := range events {
		if v.Kind == "WOULD_ROLLBACK" {
			count++
		}
	}
	if count != 2 || f.writes != 0 {
		t.Fatal("shadow did not rearm safely")
	}
	// Switching modes must start a fresh confirmation interval.
	c.Mode = "enforce"
	armed, err := New(c, q, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	armed.Evaluate(context.Background(), now.Add(65*time.Second))
	if f.writes != 0 {
		t.Fatal("mode switch reused historical shadow timer")
	}
	status(t, armed, "BREACHING")
}

func TestControlDiagnosisPreservesAbsoluteSafety(t *testing.T) {
	for _, tc := range []struct {
		name               string
		treatment, control float64
		present            bool
		count              float64
		diagnosis          string
		writes             int
	}{
		{"treatment regression", .2, .01, true, 100, "TREATMENT_ONLY_BREACH", 1},
		{"shared outage", .2, .2, true, 100, "SHARED_DEGRADATION", 1},
		{"control only", .01, .2, true, 100, "CONTROL_ONLY_BREACH", 0},
		{"healthy", .01, .01, true, 100, "BOTH_WITHIN_LIMITS", 0},
		{"missing control", .2, 0, false, 100, "INSUFFICIENT_COMPARISON_DATA", 1},
		{"low control traffic", .2, .01, true, 3, "INSUFFICIENT_COMPARISON_DATA", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, q, f, now := fixture(t)
			e.cfg.Guardrails[0].Control = &config.Control{Variant: "control", SampleQuery: "control_count", MinimumSamples: 20}
			e.cfg.Guardrails[0].Rules[0].ControlQuery = "control_errors"
			e.cfg.Guardrails[0].Rules[1].ControlQuery = "control_latency"
			q["errors"] = sample{tc.treatment, true, nil}
			q["control_errors"] = sample{tc.control, tc.present, nil}
			q["control_count"] = sample{tc.count, true, nil}
			q["control_latency"] = sample{50, true, nil}
			for i := 0; i <= 3; i++ {
				e.Evaluate(context.Background(), now.Add(time.Duration(i)*5*time.Second))
			}
			states, events := e.Snapshot()
			if states[0].Rules[0].Diagnosis != tc.diagnosis || f.writes != tc.writes {
				t.Fatalf("diagnosis=%s writes=%d", states[0].Rules[0].Diagnosis, f.writes)
			}
			if tc.writes > 0 {
				for _, event := range events {
					if event.Kind == "ROLLBACK" {
						if event.Evidence.Control == nil || event.Evidence.Rules[0].Diagnosis != tc.diagnosis {
							t.Fatal("comparison evidence missing")
						}
					}
				}
			}
		})
	}
}

func TestRecoveryWarmupSurvivesRestart(t *testing.T) {
	e, q, f, now := fixture(t)
	q["errors"] = sample{.2, true, nil}
	e.Evaluate(context.Background(), now)
	e.Evaluate(context.Background(), now.Add(5*time.Second))
	e.Evaluate(context.Background(), now.Add(10*time.Second))
	e.Evaluate(context.Background(), now.Add(15*time.Second))
	if err := e.Reset(context.Background(), "checkout_flow"); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(e.cfg, q, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	writes := f.writes
	restarted.Evaluate(context.Background(), time.Now().Add(time.Second))
	status(t, restarted, "WARMING_UP")
	if f.writes != writes {
		t.Fatal("restart rolled back inside recovery warmup")
	}
}
func TestRecoveryPersistsIntentBeforeWrite(t *testing.T) {
	e, q, f, now := fixture(t)
	f.enabled = false
	e.Evaluate(context.Background(), now)
	status(t, e, "PAUSED")
	f.err = errors.New("ambiguous API timeout")
	if e.Reset(context.Background(), "checkout_flow") == nil {
		t.Fatal("ambiguous recovery accepted")
	}
	restarted, err := New(e.cfg, q, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	status(t, restarted, "ACTION_REQUIRED")
	f.err = nil
	writes := f.writes
	restarted.Evaluate(context.Background(), now.Add(time.Second))
	if f.writes != writes {
		t.Fatal("unconfirmed recovery retried automatically")
	}
	e.cfg.StateFile = "/dev/null/state.json"
	if e.Reset(context.Background(), "checkout_flow") == nil {
		t.Fatal("recovery with broken storage accepted")
	}
	if f.writes != writes {
		t.Fatal("recovery changed flag without persisted intent")
	}
}
