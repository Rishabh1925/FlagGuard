package config

import (
	"bytes"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Mode      string `yaml:"mode"`
	Flagsmith struct {
		APIURL         string `yaml:"api_url"`
		AdminToken     string `yaml:"admin_token"`
		EnvironmentKey string `yaml:"environment_key"`
	} `yaml:"flagsmith"`
	Prometheus struct {
		URL string `yaml:"url"`
	} `yaml:"prometheus"`
	PollInterval   time.Duration `yaml:"poll_interval"`
	RecoveryWarmup time.Duration `yaml:"recovery_warmup"`
	StateFile      string        `yaml:"state_file"`
	Notifier       struct {
		WebhookURL string `yaml:"webhook_url"`
		Format     string `yaml:"format"`
	} `yaml:"notifier"`
	Guardrails []Guardrail `yaml:"guardrails"`
}
type Guardrail struct {
	RecoveryProbe  *RecoveryProbe `yaml:"recovery_probe" json:"recovery_probe,omitempty"`
	Control        *Control       `yaml:"control" json:"control,omitempty"`
	ExperimentID   int            `yaml:"experiment_id" json:"experiment_id"`
	Name           string         `yaml:"name" json:"name"`
	Flag           string         `yaml:"flag" json:"flag"`
	Variant        string         `yaml:"variant" json:"variant"`
	Action         string         `yaml:"action" json:"action"`
	MinimumSamples float64        `yaml:"minimum_samples" json:"minimum_samples"`
	SampleQuery    string         `yaml:"sample_query" json:"sample_query"`
	Rules          []Rule         `yaml:"rules" json:"rules"`
}

// RecoveryProbe identifies a binary series scraped directly from the disabled variant.
type RecoveryProbe struct {
	Metric         string            `yaml:"metric" json:"metric"`
	Labels         map[string]string `yaml:"labels" json:"labels"`
	Window         time.Duration     `yaml:"window" json:"window"`
	MaxGap         time.Duration     `yaml:"max_gap" json:"max_gap"`
	MinimumSamples int               `yaml:"minimum_samples" json:"minimum_samples"`
}

var metricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func (p RecoveryProbe) Selector() string {
	labels := make([]string, 0, len(p.Labels))
	for key, value := range p.Labels {
		labels = append(labels, key+"="+strconv.Quote(value))
	}
	sort.Strings(labels)
	return p.Metric + "{" + strings.Join(labels, ",") + "}"
}

type Control struct {
	Variant        string  `yaml:"variant" json:"variant"`
	SampleQuery    string  `yaml:"sample_query" json:"sample_query"`
	MinimumSamples float64 `yaml:"minimum_samples" json:"minimum_samples"`
}
type Rule struct {
	ControlQuery string        `yaml:"control_query" json:"control_query,omitempty"`
	Metric       string        `yaml:"metric" json:"metric"`
	Query        string        `yaml:"query" json:"query"`
	Threshold    float64       `yaml:"threshold" json:"threshold"`
	Comparison   string        `yaml:"comparison" json:"comparison"`
	For          time.Duration `yaml:"for" json:"-"`
}

func Load(path string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	// The config is trusted local input; environment credentials stay server-side.
	expanded := os.Expand(string(raw), func(k string) string { return os.Getenv(k) })
	dec := yaml.NewDecoder(bytes.NewBufferString(expanded))
	dec.KnownFields(true)
	if err = dec.Decode(&c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	return c, c.Validate()
}
func ValidURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
func (c Config) Validate() error {
	if c.Mode != "" && c.Mode != "enforce" && c.Mode != "shadow" {
		return fmt.Errorf("mode must be enforce or shadow")
	}
	if !ValidURL(c.Flagsmith.APIURL) || !ValidURL(c.Prometheus.URL) {
		return fmt.Errorf("flagsmith.api_url and prometheus.url must be HTTP(S) URLs")
	}
	if c.RecoveryWarmup < 0 {
		return fmt.Errorf("recovery_warmup cannot be negative")
	}
	if c.PollInterval < time.Second {
		return fmt.Errorf("poll_interval must be at least 1s")
	}
	if len(c.Guardrails) == 0 {
		return fmt.Errorf("at least one guardrail is required")
	}
	if c.Notifier.WebhookURL != "" {
		u, e := url.Parse(c.Notifier.WebhookURL)
		if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("invalid webhook URL")
		}
	}
	if c.Notifier.Format != "" && c.Notifier.Format != "generic" && c.Notifier.Format != "slack" && c.Notifier.Format != "discord" {
		return fmt.Errorf("notifier format must be generic, slack or discord")
	}
	seen := map[string]bool{}
	for _, g := range c.Guardrails {
		if g.Name == "" || g.Flag == "" || g.Variant == "" || strings.ContainsAny(g.Flag, "/\\") || seen[g.Flag] {
			return fmt.Errorf("guardrails require names, variants and unique flag names")
		}
		seen[g.Flag] = true
		if p := g.RecoveryProbe; p != nil {
			if !metricName.MatchString(p.Metric) || len(p.Labels) == 0 || p.Window < time.Second || p.Window%time.Millisecond != 0 || p.MaxGap <= 0 || p.MaxGap >= p.Window || p.MinimumSamples < 2 {
				return fmt.Errorf("%s: recovery_probe needs a binary metric, identifying labels, at least two samples and a max_gap shorter than the window", g.Flag)
			}
			for key, value := range p.Labels {
				if !labelName.MatchString(key) || key == "__name__" || value == "" {
					return fmt.Errorf("%s: invalid recovery probe label", g.Flag)
				}
			}
		}
		if g.Control != nil && (g.Control.Variant == "" || g.Control.Variant == g.Variant || g.Control.SampleQuery == "" || g.Control.MinimumSamples <= 0 || math.IsNaN(g.Control.MinimumSamples) || math.IsInf(g.Control.MinimumSamples, 0)) {
			return fmt.Errorf("%s: invalid control comparison", g.Flag)
		}
		if g.Action != "disable_flag" {
			return fmt.Errorf("%s: supported action is disable_flag", g.Flag)
		}
		if g.MinimumSamples < 0 || math.IsNaN(g.MinimumSamples) || math.IsInf(g.MinimumSamples, 0) || (g.MinimumSamples > 0 && g.SampleQuery == "") {
			return fmt.Errorf("%s: invalid sample gate", g.Flag)
		}
		if len(g.Rules) == 0 {
			return fmt.Errorf("%s: rules cannot be empty", g.Flag)
		}
		metrics := map[string]bool{}
		for _, r := range g.Rules {
			if (g.Control != nil) != (r.ControlQuery != "") {
				return fmt.Errorf("%s: control comparison requires a control query on every rule", g.Flag)
			}
			if r.Metric == "" || metrics[r.Metric] || r.Query == "" || r.For < 0 || math.IsNaN(r.Threshold) || math.IsInf(r.Threshold, 0) || (r.Comparison != "gt" && r.Comparison != "lt") {
				return fmt.Errorf("%s: invalid or duplicate rule %q", g.Flag, r.Metric)
			}
			metrics[r.Metric] = true
		}
	}
	return nil
}

// Warmup must cover the longest configured metric query window.
func (c Config) Warmup() time.Duration {
	if c.RecoveryWarmup == 0 {
		return 35 * time.Second
	}
	return c.RecoveryWarmup
}
