package backtest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flagguard/internal/config"
)

func TestPriceSuiteRejectsFalseAlarmAndMissedIncident(t *testing.T) {
	t.Setenv("FLAGSMITH_API_URL", "http://localhost:1")
	t.Setenv("PROMETHEUS_URL", "http://localhost:1")
	root := "../../deploy/blackbox"
	cfg, err := config.Load(filepath.Join(root, "guardrails.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		limit  float64
		failed string
	}{
		{"shipped", cfg.Guardrails[0].Rules[0].Threshold, ""},
		{"over-sensitive", .9, "One malformed response"},
		{"insensitive", 0, "Sustained schema change"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.Guardrails[0].Rules[0].Threshold = tc.limit
			report, err := RunSuite(context.Background(), cfg, filepath.Join(root, "cases", "suite.json"))
			if err != nil {
				t.Fatal(err)
			}
			want := 3
			if tc.failed != "" {
				want = 2
			}
			if len(report.Cases) != 3 || report.Passed != want {
				t.Fatalf("unexpected report %+v", report)
			}
			for _, c := range report.Cases {
				if (c.Error != "") != (c.Name == tc.failed) {
					t.Fatalf("unexpected case %+v", c)
				}
			}
		})
	}
}

func TestSuiteCannotSilentlySkipCases(t *testing.T) {
	for _, input := range []string{
		`{"cases":[{"name":"x","trace":"x.json","flag":"x","rollback_by_poll":0}]}`,
		`{"cases":[{"name":"x","trace":"x.json","expect":"x=a","flag":"x","rollback_by_poll":1}]}`, `{"cases":[]}`, `{"case":[]}`, `{"cases":[]} {}`,
		`{"cases":[{"name":"x","trace":"x.json","expect":"x=none"},{"name":"x","trace":"y.json","expect":"x=none"}]}`} {
		path := filepath.Join(t.TempDir(), "suite.json")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := RunSuite(context.Background(), config.Config{}, path); err == nil {
			t.Fatal("accepted invalid suite", input)
		}
	}
}

// The captured Linux incident has two independent rules eligible at poll six.
func TestUbuntuIncidentContracts(t *testing.T) {
	root := "../../docs/evidence/ubuntu-checkout"
	for _, tc := range []struct {
		name                   string
		errorHold, latencyHold time.Duration
		exact, protected       int
		metric                 string
	}{
		{"recorded", 15 * time.Second, 15 * time.Second, 1, 1, "error_rate"},
		{"longer error confirmation", time.Minute, 15 * time.Second, 0, 1, "p99_latency_ms"},
		{"both rules delayed", time.Minute, time.Minute, 0, 0, "no_trigger_in_observed_data"},
		{"early decision", 0, 15 * time.Second, 0, 1, "error_rate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(filepath.Join(root, "policy.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.Guardrails[0].Rules[0].For = tc.errorHold
			cfg.Guardrails[0].Rules[1].For = tc.latencyHold
			for manifest, want := range map[string]int{"suite.json": tc.exact, "protection.json": tc.protected} {
				report, err := RunSuite(context.Background(), cfg, filepath.Join(root, manifest))
				if err != nil || report.Passed != want || len(report.Cases) != 1 || report.Cases[0].Observed != tc.metric {
					t.Fatalf("%s: %+v %v", manifest, report, err)
				}
			}
		})
	}
}

func TestUbuntuDeadlineBoundaries(t *testing.T) {
	root := "../../docs/evidence/ubuntu-checkout"
	cfg, err := config.Load(filepath.Join(root, "policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		flag      string
		poll      int
		errorText string
	}{
		{"checkout_flow", 5, "after deadline poll 5"},
		{"checkout_flow", 7, "exceeds the 6 recorded polls"},
		{"other_flag", 6, "unknown expected flag"},
	} {
		r := CaseResult{}
		c := suiteCase{Flag: tc.flag, RollbackByPoll: &tc.poll}
		err := r.evaluate(context.Background(), cfg, filepath.Join(root, "trace.json"), tc.flag, c)
		if err == nil || !strings.Contains(err.Error(), tc.errorText) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestPostgresReleaseCheckUsesMeasuredEvidence(t *testing.T) {
	t.Setenv("FLAGSMITH_API_URL", "http://flagsmith.invalid")
	t.Setenv("PROMETHEUS_URL", "http://prometheus.invalid")
	root := "../../deploy/postgres"
	cfg, err := config.Load(filepath.Join(root, "guardrails.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file      string
		outcome   string
		releaseOK bool
	}{
		{"indexed.json", "no_trigger_in_observed_data", true},
		{"scan.json", "would_roll_back", false},
		{"unavailable.json", "insufficient_data", false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			trace, err := ReadTrace(filepath.Join(root, "cases", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			report, err := Replay(context.Background(), cfg, trace)
			if err != nil {
				t.Fatal(err)
			}
			if report.Guardrails[0].Outcome != tc.outcome || (report.Check("catalog_lookup_v2=none") == nil) != tc.releaseOK {
				t.Fatalf("unexpected release decision: %+v", report)
			}
		})
	}
}
