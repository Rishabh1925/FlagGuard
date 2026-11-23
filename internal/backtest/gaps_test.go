package backtest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"flagguard/internal/config"
)

func TestPriceIncidentGapDeadlines(t *testing.T) {
	t.Setenv("FLAGSMITH_API_URL", "http://localhost:1")
	t.Setenv("PROMETHEUS_URL", "http://localhost:1")
	root := "../../deploy/blackbox"
	cfg, err := config.Load(filepath.Join(root, "guardrails.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	trace, err := ReadTrace(filepath.Join(root, "cases/sustained.json"))
	if err != nil {
		t.Fatal(err)
	}
	flag := cfg.Guardrails[0].Flag
	for _, tc := range []struct {
		hold     time.Duration
		baseline time.Duration
		// Seconds from trace start, with -1 for a decision outside the observed window.
		decisions []int
		meets     int
	}{
		{15 * time.Second, 45 * time.Second, []int{50, 55, 60, -1}, 3},
		{10 * time.Second, 40 * time.Second, []int{45, 50, 55}, 3},
	} {
		cfg.Guardrails[0].Rules[0].For = tc.hold
		r, err := CheckGaps(context.Background(), cfg, trace, flag)
		if err != nil {
			t.Fatal(err)
		}
		if r.BaselineAt.Sub(r.Start) != tc.baseline || len(r.Scenarios) != len(tc.decisions) || r.Deadline != nil {
			t.Fatalf("unexpected baseline: %+v", r)
		}
		for i, s := range r.Scenarios {
			if got := s.MissingAt.Sub(r.Start); got != time.Duration(30+i*5)*time.Second {
				t.Fatalf("scenario gap at %v", got)
			}
			got := -1
			if s.DecisionAt != nil {
				got = int(s.DecisionAt.Sub(r.Start) / time.Second)
			}
			if got != tc.decisions[i] {
				t.Fatalf("scenario %d observed +%d, want +%d", i, got, tc.decisions[i])
			}
		}
		err = r.RequireBy(60 * time.Second)
		wantPass := tc.meets == len(tc.decisions)
		if (err == nil) != wantPass || r.Deadline.Passed != wantPass || r.Deadline.Met != tc.meets {
			t.Fatalf("deadline error=%v result=%+v", err, r.Deadline)
		}
		if err := r.RequireBy(61 * time.Second); err == nil {
			t.Fatal("accepted a deadline beyond capture")
		}
		if err := r.RequireBy(30 * time.Second); err == nil || r.Deadline.Met != 0 {
			t.Fatal("failed to enforce tighter deadline")
		}
		suite, err := RunSuite(context.Background(), cfg, filepath.Join(root, "cases/suite.json"))
		if err != nil || suite.Passed != 3 {
			t.Fatalf("base cases failed: %+v %v", suite, err)
		}
	}
	quiet, err := ReadTrace(filepath.Join(root, "cases/healthy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CheckGaps(context.Background(), cfg, quiet, flag); err == nil {
		t.Fatal("inferred resilience without a baseline rollback")
	}
	if _, err := CheckGaps(context.Background(), cfg, trace, "unknown"); err == nil {
		t.Fatal("unknown flag")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CheckGaps(ctx, cfg, trace, flag); err == nil {
		t.Fatal("ignored cancellation")
	}
}
