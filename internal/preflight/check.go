// Package preflight checks integrations without evaluating or mutating guardrails.
package preflight

import (
	"context"
	"flagguard/internal/config"
	"flagguard/internal/flagsmith"
)

type Querier interface {
	Query(context.Context, string) (float64, bool, error)
}

// Only reads are exposed to the preflight checker.
type Reader interface {
	Find(context.Context, string) (flagsmith.FeatureState, error)
}

type Check struct {
	Flag   string   `json:"flag"`
	Name   string   `json:"name"`
	OK     bool     `json:"ok"`
	Detail string   `json:"detail"`
	Value  *float64 `json:"value,omitempty"`
}

type Report struct {
	Ready    bool    `json:"ready"`
	ReadOnly bool    `json:"read_only"`
	Checks   []Check `json:"checks"`
}

func Run(ctx context.Context, c config.Config, q Querier, f Reader) Report {
	r := Report{Ready: true, ReadOnly: true, Checks: []Check{}}
	add := func(ch Check) { r.Checks = append(r.Checks, ch); r.Ready = r.Ready && ch.OK }
	for _, g := range c.Guardrails {
		state, err := f.Find(ctx, g.Flag)
		ch := Check{Flag: g.Flag, Name: "flagsmith", OK: err == nil && state.Enabled, Detail: "Flag resolved and enabled"}
		if err != nil {
			ch.Detail = err.Error()
		} else if !state.Enabled {
			ch.Detail = "Flag is disabled; inspect it before enabling protection"
		}
		add(ch)
		if g.SampleQuery != "" {
			v, ok, err := q.Query(ctx, g.SampleQuery)
			ch := Check{Flag: g.Flag, Name: "sample_volume", OK: err == nil && ok && v >= g.MinimumSamples, Detail: "Enough observed traffic"}
			if err != nil {
				ch.Detail = err.Error()
			} else if !ok {
				ch.Detail = "No finite sample"
			} else {
				ch.Value = &v
				if v < g.MinimumSamples {
					ch.Detail = "Not enough traffic for the configured minimum"
				}
			}
			add(ch)
		}
		for _, rule := range g.Rules {
			v, ok, err := q.Query(ctx, rule.Query)
			ch := Check{Flag: g.Flag, Name: rule.Metric, OK: err == nil && ok, Detail: "Query returned one finite sample within threshold"}
			if err != nil {
				ch.Detail = err.Error()
			} else if !ok {
				ch.Detail = "No finite sample"
			} else {
				ch.Value = &v
				if (rule.Comparison == "gt" && v > rule.Threshold) || (rule.Comparison == "lt" && v < rule.Threshold) {
					ch.OK = false
					ch.Detail = "Threshold already breached; inspect before arming"
				}
			}
			add(ch)
		}
	}
	return r
}
