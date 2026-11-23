package backtest

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"flagguard/internal/engine"
)

func reading(metric string, v float64) string {
	switch metric {
	case "error_rate":
		return fmt.Sprintf("%.2f%%", 100*v)
	case "p99_latency_ms":
		return fmt.Sprintf("%.2f ms", v)
	default:
		return fmt.Sprintf("%.5g", v)
	}
}

func decisionDetail(event *engine.Event) string {
	if event.Evidence == nil {
		return ""
	}
	for _, rule := range event.Evidence.Rules {
		if rule.Metric != event.Metric || rule.Value == nil {
			continue
		}
		comparison := "above"
		if rule.Comparison == "lt" {
			comparison = "below"
		}
		detail := fmt.Sprintf("%s reached %s (%s %s for at least %gs).",
			rule.Metric, reading(rule.Metric, *rule.Value), comparison, reading(rule.Metric, rule.Threshold), rule.ForSeconds)
		if rule.ControlValue != nil {
			detail += fmt.Sprintf(" Control: %s (%s).", reading(rule.Metric, *rule.ControlValue),
				strings.ToLower(strings.ReplaceAll(rule.Diagnosis, "_", " ")))
		}
		return detail
	}
	return ""
}

func (r Report) WriteText(w io.Writer) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Policy replay   %s to %s\n%d scheduled polls at %gs intervals\n", r.Start.Format(time.RFC3339Nano), r.End.Format(time.RFC3339Nano), r.Scheduled, r.StepSeconds)
	for _, g := range r.Guardrails {
		fmt.Fprintf(&b, "\n%s / %s\n", g.Flag, g.Variant)
		switch g.Outcome {
		case "would_roll_back":
			e := g.Decision
			fmt.Fprintf(&b, "  Would roll back at %s\n", e.Time.Format(time.RFC3339Nano))
			if detail := decisionDetail(e); detail != "" {
				fmt.Fprintf(&b, "  %s\n", detail)
			}
		case "insufficient_data":
			fmt.Fprintln(&b, "  Insufficient traffic or measurements throughout this window.")
		default:
			fmt.Fprintln(&b, "  No rollback triggered in the observed data.")
		}
		fmt.Fprintf(&b, "  Polls with sufficient treatment data: %d of %d evaluated.\n", g.Eligible, g.Evaluated)
	}
	fmt.Fprintln(&b, "\n"+r.Assumption)
	_, err := w.Write(b.Bytes())
	return err
}

// Check lets CI treat a recorded incident as a regression case for policy edits.
func (r Report) Check(expect string) error {
	flag, metric, ok := strings.Cut(expect, "=")
	if !ok || flag == "" || metric == "" {
		return fmt.Errorf("-expect requires flag=metric or flag=none")
	}
	for _, g := range r.Guardrails {
		if g.Flag != flag {
			continue
		}
		if g.Decision != nil {
			if g.Decision.Metric == metric && metric != "none" {
				return nil
			}
			return fmt.Errorf("%s: expected %s, triggered %s", flag, metric, g.Decision.Metric)
		}
		if metric == "none" && g.Eligible == r.Scheduled && g.Outcome == "no_trigger_in_observed_data" {
			return nil
		}
		return fmt.Errorf("%s: expected %s, outcome %s (%d of %d polls eligible)", flag, metric, g.Outcome, g.Eligible, r.Scheduled)
	}
	return fmt.Errorf("unknown expected flag %q", flag)
}
