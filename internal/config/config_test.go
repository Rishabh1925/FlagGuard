package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigValidation(t *testing.T) {
	base := `flagsmith:
  api_url: http://localhost:8000/api/v1
prometheus:
  url: http://localhost:9090
poll_interval: 5s
guardrails:
  - name: Checkout
    flag: checkout_flow
    variant: variant_b
    action: disable_flag
    rules:
      - metric: error_rate
        query: sum(errors)
        comparison: gt
        threshold: 0.05
        for: 15s
`
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{{"valid", base, true}, {"shadow", "mode: shadow\n" + base, true}, {"invalid mode", "mode: shadwo\n" + base, false}, {"bad interval", strings.Replace(base, "5s", "0s", 1), false}, {"unknown action", strings.Replace(base, "disable_flag", "set_weight_zero", 1), false}, {"typo", strings.Replace(base, "threshold:", "threshhold:", 1), false}, {"nan", strings.Replace(base, "0.05", ".nan", 1), false}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			os.WriteFile(path, []byte(tc.text), 0600)
			_, err := Load(path)
			if (err == nil) != tc.valid {
				t.Fatalf("unexpected validation: %v", err)
			}
		})
	}
}

func TestControlConfigurationRequiresCompleteQueries(t *testing.T) {
	c := Config{PollInterval: time.Second, Guardrails: []Guardrail{{Name: "Checkout", Flag: "checkout", Variant: "variant_b", Action: "disable_flag", Rules: []Rule{{Metric: "error_rate", Query: "errors", Comparison: "gt", Threshold: .05}}}}}
	c.Flagsmith.APIURL = "http://localhost:8000/api/v1"
	c.Prometheus.URL = "http://localhost:9090"
	c.Guardrails[0].Control = &Control{Variant: "control", SampleQuery: "count", MinimumSamples: 20}
	if c.Validate() == nil {
		t.Fatal("accepted incomplete control queries")
	}
	c.Guardrails[0].Rules[0].ControlQuery = "control_errors"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Guardrails[0].Control.Variant = "variant_b"
	if c.Validate() == nil {
		t.Fatal("accepted identical control and treatment")
	}
	c.Guardrails[0].Control = nil
	if c.Validate() == nil {
		t.Fatal("accepted orphaned control query")
	}
}

func TestRecoveryProbeValidation(t *testing.T) {
	c := Config{PollInterval: time.Second, Guardrails: []Guardrail{{Name: "Price", Flag: "price", Variant: "candidate", Action: "disable_flag", Rules: []Rule{{Metric: "errors", Query: "errors", Comparison: "gt"}}}}}
	c.Flagsmith.APIURL = "http://flagsmith.test"
	c.Prometheus.URL = "http://metrics.test"
	probe := RecoveryProbe{Metric: "probe_success", Labels: map[string]string{"variant": "candidate", "job": "price_contract"}, Window: 20 * time.Second, MaxGap: 8 * time.Second, MinimumSamples: 3}
	c.Guardrails[0].RecoveryProbe = &probe
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := probe.Selector(); got != `probe_success{job="price_contract",variant="candidate"}` {
		t.Fatal(got)
	}
	for _, edit := range []func(*RecoveryProbe){
		func(p *RecoveryProbe) { p.Metric = "rate(foo)" },
		func(p *RecoveryProbe) { p.Labels = nil },
		func(p *RecoveryProbe) { p.Labels = map[string]string{"1job": "bad"} },
		func(p *RecoveryProbe) { p.MaxGap = p.Window },
		func(p *RecoveryProbe) { p.MinimumSamples = 1 },
	} {
		invalid := probe
		edit(&invalid)
		c.Guardrails[0].RecoveryProbe = &invalid
		if c.Validate() == nil {
			t.Fatalf("invalid recovery probe accepted: %+v", invalid)
		}
	}
}
