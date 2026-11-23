package backtest

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"flagguard/internal/config"
)

// Each replay keeps the original controller cadence and masks query results at
// one evaluation. Later rolling PromQL readings stay exactly as recorded.
type gapReadings struct {
	Trace
	missingAt time.Time
}

func (g gapReadings) QueryAt(ctx context.Context, q string, at time.Time) (float64, bool, error) {
	if at.Equal(g.missingAt) {
		return 0, false, nil
	}
	return g.Trace.QueryAt(ctx, q, at)
}

type GapScenario struct {
	MissingAt  time.Time  `json:"missing_at"`
	DecisionAt *time.Time `json:"decision_at,omitempty"`
	Metric     string     `json:"trigger_metric,omitempty"`
	Outcome    string     `json:"outcome"`
}

type GapDeadline struct {
	At     time.Time `json:"at"`
	Met    int       `json:"scenarios_meeting_deadline"`
	Passed bool      `json:"passed"`
}

type GapReport struct {
	Flag           string        `json:"flag"`
	Start          time.Time     `json:"trace_start"`
	End            time.Time     `json:"trace_end"`
	BaselineAt     time.Time     `json:"baseline_decision_at"`
	BaselineMetric string        `json:"baseline_metric"`
	Scope          string        `json:"scope"`
	Scenarios      []GapScenario `json:"scenarios"`
	Deadline       *GapDeadline  `json:"deadline,omitempty"`
}

func CheckGaps(ctx context.Context, cfg config.Config, trace Trace, flag string) (GapReport, error) {
	var out GapReport
	var selected *config.Guardrail
	for i := range cfg.Guardrails {
		if cfg.Guardrails[i].Flag == flag {
			selected = &cfg.Guardrails[i]
			break
		}
	}
	if selected == nil {
		return out, fmt.Errorf("unknown gap-check flag %q", flag)
	}
	cfg.Guardrails = []config.Guardrail{*selected}
	baseline, err := Replay(ctx, cfg, trace)
	if err != nil {
		return out, err
	}
	decision := baseline.Guardrails[0].Decision
	if decision == nil {
		return out, fmt.Errorf("%s: the original capture has no rollback decision to stress (%s)", flag, baseline.Guardrails[0].Outcome)
	}
	var start *time.Time
	for _, rule := range decision.Evidence.Rules {
		if rule.Metric == decision.Metric {
			start = rule.Since
			break
		}
	}
	if start == nil {
		return out, fmt.Errorf("baseline decision lacks a confirmation start")
	}
	out = GapReport{Flag: flag, Start: baseline.Start, End: baseline.End, BaselineAt: decision.Time, BaselineMetric: decision.Metric, Scope: "One unavailable controller evaluation per scenario, within the original triggering rule's confirmation interval. Later recorded readings stay unchanged. No decision by trace end is inconclusive beyond that point."}
	for i := 0; i < trace.Polls; i++ {
		at := trace.pollTime(i)
		if at.Before(*start) || at.After(decision.Time) {
			continue
		}
		replay, err := replayWithReadings(ctx, cfg, trace, gapReadings{Trace: trace, missingAt: at})
		if err != nil {
			return GapReport{}, err
		}
		result := replay.Guardrails[0]
		s := GapScenario{MissingAt: at, Outcome: result.Outcome}
		if result.Decision != nil {
			s.DecisionAt = &result.Decision.Time
			s.Metric = result.Decision.Metric
		}
		out.Scenarios = append(out.Scenarios, s)
	}
	return out, nil
}

// RequireBy checks time from trace start. Any rule protecting the selected flag
// may supply the decision. The result includes the baseline and every tested gap.
func (r *GapReport) RequireBy(d time.Duration) error {
	if d < 0 || d > r.End.Sub(r.Start) {
		return fmt.Errorf("deadline lies outside the capture")
	}
	deadline := r.Start.Add(d)
	r.Deadline = &GapDeadline{At: deadline}
	for _, s := range r.Scenarios {
		if s.DecisionAt != nil && !s.DecisionAt.After(deadline) {
			r.Deadline.Met++
		}
	}
	r.Deadline.Passed = !r.BaselineAt.After(deadline) && r.Deadline.Met == len(r.Scenarios)
	if !r.Deadline.Passed {
		return fmt.Errorf("%s: deadline +%s unmet; %d/%d gap scenarios triggered by then", r.Flag, d, r.Deadline.Met, len(r.Scenarios))
	}
	return nil
}

func (r GapReport) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Telemetry gap rehearsal   %s\nOriginal decision: +%s from trace start (%s). Capture ends at +%s.\n", r.Flag, r.BaselineAt.Sub(r.Start), r.BaselineMetric, r.End.Sub(r.Start))
	for _, s := range r.Scenarios {
		fmt.Fprintf(&b, "  Remove poll at +%-8s ", s.MissingAt.Sub(r.Start))
		if s.DecisionAt == nil {
			fmt.Fprintln(&b, "No rollback observed before the capture ended.")
			continue
		}
		fmt.Fprintf(&b, "Rollback at +%s (%s, delay %s).\n", s.DecisionAt.Sub(r.Start), s.Metric, s.DecisionAt.Sub(r.BaselineAt))
	}
	if r.Deadline != nil {
		fmt.Fprintf(&b, "%d/%d gap scenarios reached a decision by +%s.\n", r.Deadline.Met, len(r.Scenarios), r.Deadline.At.Sub(r.Start))
	}
	fmt.Fprintln(&b, r.Scope)
	_, err := io.WriteString(w, b.String())
	return err
}
