// Package backtest evaluates recorded traffic with the controller's shadow decision loop.
package backtest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"flagguard/internal/config"
	"flagguard/internal/engine"
	"flagguard/internal/flagsmith"
)

type History interface {
	Range(context.Context, string, time.Time, time.Time, time.Duration) (map[int64]float64, error)
}

type Report struct {
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	StepSeconds float64   `json:"step_seconds"`
	Scheduled   int       `json:"scheduled_polls"`
	Assumption  string    `json:"assumption"`
	Guardrails  []Result  `json:"guardrails"`
}

type Result struct {
	Flag      string        `json:"flag"`
	Variant   string        `json:"variant"`
	Outcome   string        `json:"outcome"`
	Evaluated int           `json:"polls_evaluated"`
	Eligible  int           `json:"polls_with_sufficient_data"`
	Decision  *engine.Event `json:"first_decision,omitempty"`
}

func (t Trace) QueryAt(_ context.Context, q string, at time.Time) (float64, bool, error) {
	i := at.Sub(t.Start).Milliseconds() / t.StepMillis
	if t.Version == 2 {
		i = int64(sort.Search(len(t.Times), func(i int) bool { return !t.Times[i].Before(at) }))
		if i >= int64(len(t.Times)) || !t.Times[i].Equal(at) {
			return 0, false, nil
		}
	}
	v := t.Series[q][i]
	if v == nil {
		return 0, false, nil
	}
	return *v, true, nil
}

// Historical flag settings are unknown. The simulation starts armed with empty timers.
type armedFlag struct{}

func (armedFlag) Find(context.Context, string) (flagsmith.FeatureState, error) {
	return flagsmith.FeatureState{Enabled: true}, nil
}
func (armedFlag) SetEnabled(context.Context, string, bool) (flagsmith.Change, error) {
	return flagsmith.Change{}, errors.New("backtest cannot change flags")
}

type Trace struct {
	Times      []time.Time           `json:"poll_times,omitempty"`
	Version    int                   `json:"version"`
	Start      time.Time             `json:"start"`
	StepMillis int64                 `json:"step_milliseconds"`
	Polls      int                   `json:"polls"`
	Series     map[string][]*float64 `json:"series"`
}

func queries(g config.Guardrail) []string {
	qs := []string{g.SampleQuery}
	if g.Control != nil {
		qs = append(qs, g.Control.SampleQuery)
	}
	for _, rule := range g.Rules {
		qs = append(qs, rule.Query, rule.ControlQuery)
	}
	return qs
}

func Capture(ctx context.Context, cfg config.Config, h History, end time.Time, window time.Duration) (Trace, error) {
	step := cfg.PollInterval
	if step < time.Second || step%time.Millisecond != 0 || window < step || window%step != 0 || window/step >= 2000 {
		return Trace{}, fmt.Errorf("choose a whole number of poll intervals (1 to 1999), with millisecond precision")
	}
	end = end.UTC().Truncate(time.Millisecond)
	t := Trace{Version: 1, Start: end.Add(-window), StepMillis: step.Milliseconds(), Polls: int(window/step) + 1, Series: make(map[string][]*float64)}
	for _, g := range cfg.Guardrails {
		for _, q := range queries(g) {
			if _, ok := t.Series[q]; ok || q == "" {
				continue
			}
			values, err := h.Range(ctx, q, t.Start, end, step)
			if err != nil {
				return Trace{}, fmt.Errorf("history for %s (%s): %w", g.Flag, q, err)
			}
			points := make([]*float64, t.Polls)
			for i := range points {
				if v, ok := values[t.Start.UnixMilli()+int64(i)*t.StepMillis]; ok {
					points[i] = &v
				}
			}
			t.Series[q] = points
		}
	}
	return t, nil
}

func Replay(ctx context.Context, cfg config.Config, t Trace) (Report, error) {
	return replayWithReadings(ctx, cfg, t, t)
}

func replayWithReadings(ctx context.Context, cfg config.Config, t Trace, readings engine.Querier) (Report, error) {
	step := cfg.PollInterval
	if (t.Version != 1 && t.Version != 2) || t.Start.IsZero() || t.Polls < 1 || (t.Version == 1 && (t.Polls < 2 || len(t.Times) != 0)) || t.Polls > 2000 || step < time.Second || step%time.Millisecond != 0 || t.StepMillis != step.Milliseconds() || (t.Polls > 1 && step > time.Duration(math.MaxInt64/int64(t.Polls-1))) {
		return Report{}, fmt.Errorf("invalid trace grid or policy poll interval differs from capture")
	}
	end := t.Start.Add(time.Duration(t.Polls-1) * step)
	if t.Version == 2 {
		if len(t.Times) != t.Polls || !t.Times[0].Equal(t.Start) {
			return Report{}, fmt.Errorf("invalid recorded poll times")
		}
		for i := 1; i < t.Polls; i++ {
			if !t.Times[i].After(t.Times[i-1]) {
				return Report{}, fmt.Errorf("recorded poll times must increase")
			}
		}
		end = t.Times[t.Polls-1]
	}
	report := Report{Start: t.Start, End: end, StepSeconds: step.Seconds(), Scheduled: t.Polls, Assumption: "Flag enabled at the start with fresh timers. Evaluates observed traffic until the first decision. Excludes flag writes, rollout propagation and later traffic under hypothetical changes."}
	cfg.Mode = "shadow"
	cfg.StateFile = ""

	guards := cfg.Guardrails
	for _, g := range guards {
		for _, q := range queries(g) {
			if q != "" && len(t.Series[q]) != t.Polls {
				return Report{}, fmt.Errorf("trace requires %d samples (null for missing data) for %s query %q", t.Polls, g.Flag, q)
			}
		}
		cfg.Guardrails = []config.Guardrail{g}
		sim, err := engine.New(cfg, readings, armedFlag{}, nil)
		if err != nil {
			return Report{}, err
		}
		r := Result{Flag: g.Flag, Variant: g.Variant, Outcome: "no_trigger_in_observed_data"}
		for i := 0; i < t.Polls; i++ {
			at := t.pollTime(i)
			if err := ctx.Err(); err != nil {
				return Report{}, err
			}

			sim.Evaluate(ctx, at)
			states, events := sim.Snapshot()
			r.Evaluated++
			switch states[0].Status {
			case "HEALTHY", "BREACHING", "SHADOW_TRIGGERED":
				r.Eligible++
			}
			if states[0].Status == "SHADOW_TRIGGERED" {
				ev := events[len(events)-1]
				r.Decision = &ev
				r.Outcome = "would_roll_back"
				break
			}
		}
		if r.Eligible == 0 {
			r.Outcome = "insufficient_data"
		}
		report.Guardrails = append(report.Guardrails, r)
	}
	return report, nil
}

func (t Trace) pollTime(i int) time.Time {
	if t.Version == 2 {
		return t.Times[i]
	}
	return t.Start.Add(time.Duration(i) * time.Duration(t.StepMillis) * time.Millisecond)
}
