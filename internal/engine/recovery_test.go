package engine

import (
	"context"
	"errors"
	"flagguard/internal/config"
	"flagguard/internal/prometheus"
	"testing"
	"time"
)

type probeFixture struct {
	fakeQuery
	samples []prometheus.ProbeSample
	err     error
}

func (q *probeFixture) ProbeWindow(context.Context, string, time.Duration, time.Time) ([]prometheus.ProbeSample, error) {
	return q.samples, q.err
}

func TestRecoveryRequiresCoveredPostDisableWindowAndRechecksOnWrite(t *testing.T) {
	e, q, f, _ := fixture(t)
	ctx := context.Background()
	p := &config.RecoveryProbe{Metric: "probe_success", Labels: map[string]string{"variant": "candidate"}, Window: 20 * time.Second, MaxGap: 8 * time.Second, MinimumSamples: 3}
	e.cfg.Guardrails[0].RecoveryProbe = p
	raw := &probeFixture{fakeQuery: q}
	e.query = raw
	f.enabled = false
	now := time.Now()
	for _, ago := range []int{19, 14, 9, 4} {
		raw.samples = append(raw.samples, prometheus.ProbeSample{At: now.Add(-time.Duration(ago) * time.Second), Passed: true})
	}
	check := func(want string) {
		t.Helper()
		rows, _ := e.Snapshot()
		if rows[0].Recovery == nil || rows[0].Recovery.Status != want {
			t.Fatalf("want %s, got %+v", want, rows[0].Recovery)
		}
	}
	e.Evaluate(ctx, now.Add(-25*time.Second))
	e.Evaluate(ctx, now.Add(-12*time.Second))
	check("WAITING")
	e.Evaluate(ctx, now)
	check("READY")
	raw.samples[1].Passed = false
	if err := e.Reset(ctx, "checkout_flow"); err == nil || f.writes != 0 {
		t.Fatal("click used obsolete ready result")
	}
	check("FAILING")
	_, rejected := e.Snapshot()
	held := rejected[len(rejected)-1]
	if held.Kind != "RECOVERY_HELD" || held.Recovery == nil || held.Recovery.Passed != 3 || held.Change != nil {
		t.Fatalf("missing held-recovery evidence: %+v", held)
	}
	raw.samples[1].Passed = true
	raw.err = errors.New("no response")
	if err := e.Reset(ctx, "checkout_flow"); err == nil {
		t.Fatal("reset without telemetry")
	}
	check("NO_DATA")
	raw.err = nil
	all := raw.samples
	raw.samples = []prometheus.ProbeSample{all[0], all[2], all[3]}
	if err := e.Reset(ctx, "checkout_flow"); err == nil {
		t.Fatal("reset despite internal scrape gap")
	}
	raw.samples = all[:2]
	if err := e.Reset(ctx, "checkout_flow"); err == nil {
		t.Fatal("reset despite missing tail")
	}
	raw.samples = all
	restarted, err := New(e.cfg, raw, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Evaluate(ctx, now)
	if err := restarted.Reset(ctx, "checkout_flow"); err == nil {
		t.Fatal("pre-restart probes authorized a reset")
	}
	if err := e.Reset(ctx, "checkout_flow"); err != nil || !f.enabled || f.writes != 1 {
		t.Fatalf("reset=%v writes=%d", err, f.writes)
	}
	_, events := e.Snapshot()
	last := events[len(events)-1]
	if last.Kind != "RESET" || last.Recovery == nil || last.Recovery.Passed != 4 || last.Recovery.Status != "READY" {
		t.Fatalf("missing reset evidence: %+v", last)
	}
}
