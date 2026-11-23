// Package preflight checks integrations without evaluating or mutating guardrails.
package preflight

import (
	"context"
	"time"

	"flagguard/internal/config"
	"flagguard/internal/flagsmith"
	"flagguard/internal/prometheus"
	"fmt"
)

type Querier interface {
	QueryAt(context.Context, string, time.Time) (float64, bool, error)
}

// Only reads are exposed to the preflight checker.
type Reader interface {
	Find(context.Context, string) (flagsmith.FeatureState, error)
}

type Check struct {
	Flag     string   `json:"flag"`
	Name     string   `json:"name"`
	OK       bool     `json:"ok"`
	Detail   string   `json:"detail"`
	Advisory bool     `json:"advisory,omitempty"`
	Value    *float64 `json:"value,omitempty"`
}

type Report struct {
	Ready    bool    `json:"ready"`
	ReadOnly bool    `json:"read_only"`
	Checks   []Check `json:"checks"`
}

func Run(ctx context.Context, c config.Config, q Querier, f Reader) Report {
	r := Report{Ready: true, ReadOnly: true, Checks: []Check{}}
	add := func(ch Check) {
		r.Checks = append(r.Checks, ch)
		if !ch.Advisory && !ch.OK {
			r.Ready = false
		}
	}
	for _, g := range c.Guardrails {
		state, err := f.Find(ctx, g.Flag)
		ch := Check{Flag: g.Flag, Name: "flagsmith", OK: err == nil && state.Enabled, Detail: "Flag resolved and enabled"}
		if err != nil {
			ch.Detail = err.Error()
		} else if !state.Enabled {
			ch.Detail = "Flag is disabled; inspect it before enabling protection"
		}
		add(ch)
		at := time.Now()
		if probe := g.RecoveryProbe; probe != nil {
			ch := Check{Flag: g.Flag, Name: "recovery_probe", Advisory: true, Detail: "Raw probe sampling is unavailable"}
			if reader, ok := q.(prometheus.ProbeReader); ok {
				samples, err := reader.ProbeWindow(ctx, probe.Selector(), probe.Window, at)
				switch {
				case err != nil:
					ch.Detail = err.Error()
				case len(samples) == 0:
					ch.Detail = "No raw samples for the disabled-candidate probe"
				case at.Sub(samples[len(samples)-1].At) > probe.MaxGap:
					ch.Detail = "Latest candidate probe is stale"
				default:
					ch.OK = true
					ch.Detail = fmt.Sprintf("Read %d binary candidate probes. Recovery eligibility is evaluated while the flag is off.", len(samples))
				}
			}
			add(ch)
		}
		reading := func(name, query string, advisory bool) Check {
			v, present, err := q.QueryAt(ctx, query, at)
			ch := Check{Flag: g.Flag, Name: name, Advisory: advisory}
			switch {
			case err != nil:
				ch.Detail = err.Error()
			case !present:
				ch.Detail = "No finite sample"
			default:
				ch.OK = true
				ch.Value = &v
				ch.Detail = "Query returned one finite sample"
			}
			return ch
		}
		volume := func(name, query string, minimum float64, advisory bool) {
			ch := reading(name, query, advisory)
			if ch.OK && *ch.Value < minimum {
				ch.OK = false
				ch.Detail = "Not enough traffic for the configured minimum"
			}
			add(ch)
		}
		if g.SampleQuery != "" {
			volume("sample_volume", g.SampleQuery, g.MinimumSamples, false)
		}
		if g.Control != nil {
			volume("control_sample_volume", g.Control.SampleQuery, g.Control.MinimumSamples, true)
		}
		for _, rule := range g.Rules {
			ch := reading(rule.Metric, rule.Query, false)
			if ch.OK && ((rule.Comparison == "gt" && *ch.Value > rule.Threshold) || (rule.Comparison == "lt" && *ch.Value < rule.Threshold)) {
				ch.OK = false
				ch.Detail = "Threshold already breached; inspect before arming"
			}
			add(ch)
			if g.Control != nil {
				add(reading("control_"+rule.Metric, rule.ControlQuery, true))
			}
		}
	}
	return r
}
