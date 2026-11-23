// A suite exercises one proposed policy against labeled, recorded traffic windows.
package backtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flagguard/internal/config"
)

func ReadTrace(path string) (Trace, error) {
	var t Trace
	b, err := os.ReadFile(path)
	if err != nil {
		return t, err
	}
	if err = json.Unmarshal(b, &t); err != nil {
		return t, fmt.Errorf("read trace %s: %w", path, err)
	}
	return t, nil
}

type suiteCase struct {
	Name           string `json:"name"`
	Trace          string `json:"trace"`
	Expect         string `json:"expect,omitempty"`
	Flag           string `json:"flag,omitempty"`
	RollbackByPoll *int   `json:"rollback_by_poll,omitempty"`
	DecisionPoll   *int   `json:"decision_poll,omitempty"`
}

type CaseResult struct {
	Name        string     `json:"name"`
	Expect      string     `json:"expect"`
	Observed    string     `json:"observed,omitempty"`
	Eligible    int        `json:"eligible_polls"`
	Evaluated   int        `json:"evaluated_polls"`
	Measurement string     `json:"measurement,omitempty"`
	DecisionAt  *time.Time `json:"decision_at,omitempty"`
	Error       string     `json:"error,omitempty"`
}

type SuiteReport struct {
	Passed int          `json:"passed"`
	Cases  []CaseResult `json:"cases"`
}

func RunSuite(ctx context.Context, cfg config.Config, path string) (SuiteReport, error) {
	var out SuiteReport
	file, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer file.Close()
	var suite struct {
		Cases []suiteCase `json:"cases"`
	}
	dec := json.NewDecoder(file)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&suite); err != nil {
		return out, fmt.Errorf("read suite: %w", err)
	}
	if dec.Decode(new(any)) != io.EOF {
		return out, fmt.Errorf("expected a single suite document")
	}
	if len(suite.Cases) == 0 {
		return out, fmt.Errorf("suite requires at least one case")
	}
	seen := map[string]bool{}
	for _, c := range suite.Cases {
		if c.Name == "" || seen[c.Name] || c.Trace == "" || !filepath.IsLocal(c.Trace) {
			return out, fmt.Errorf("each case needs a unique name, a local trace path and an expectation: %q", c.Name)
		}
		flag, _, _ := strings.Cut(c.Expect, "=")
		expectation := c.Expect
		if c.RollbackByPoll != nil {
			if c.Flag == "" || c.Expect != "" || c.DecisionPoll != nil || *c.RollbackByPoll < 1 {
				return out, fmt.Errorf("%s: rollback_by_poll requires a positive poll and a flag, separate from an exact expectation", c.Name)
			}
			flag = c.Flag
			expectation = fmt.Sprintf("%s rolls back by poll %d", flag, *c.RollbackByPoll)
		} else if c.Expect == "" || c.Flag != "" {
			return out, fmt.Errorf("%s: specify expect or both flag and rollback_by_poll", c.Name)
		}
		seen[c.Name] = true
		result := CaseResult{Name: c.Name, Expect: expectation}
		err := result.evaluate(ctx, cfg, filepath.Join(filepath.Dir(path), c.Trace), flag, c)
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if err != nil {
			result.Error = err.Error()
		} else {
			out.Passed++
		}
		out.Cases = append(out.Cases, result)
	}
	return out, nil
}

func (r *CaseResult) evaluate(ctx context.Context, cfg config.Config, tracePath, flag string, c suiteCase) error {
	trace, err := ReadTrace(tracePath)
	if err != nil {
		return err
	}
	report, err := Replay(ctx, cfg, trace)
	if err != nil {
		return err
	}
	if c.RollbackByPoll != nil && *c.RollbackByPoll > trace.Polls {
		return fmt.Errorf("deadline poll %d exceeds the %d recorded polls", *c.RollbackByPoll, trace.Polls)
	}
	for _, g := range report.Guardrails {
		if g.Flag != flag {
			continue
		}
		r.Observed = g.Outcome
		r.Eligible = g.Eligible
		r.Evaluated = g.Evaluated
		if g.Decision != nil {
			r.Observed = g.Decision.Metric
			r.DecisionAt = &g.Decision.Time
			r.Measurement = decisionDetail(g.Decision)
		}
		if c.RollbackByPoll != nil {
			if r.DecisionAt == nil {
				return fmt.Errorf("%s: no rollback by poll %d (%s, %d/%d evaluated polls eligible)", flag, *c.RollbackByPoll, r.Observed, r.Eligible, r.Evaluated)
			}
			if r.Evaluated > *c.RollbackByPoll {
				return fmt.Errorf("%s: %s triggered at poll %d, after deadline poll %d", flag, r.Observed, r.Evaluated, *c.RollbackByPoll)
			}
			return nil
		}
		break
	}
	if c.RollbackByPoll != nil {
		return fmt.Errorf("unknown expected flag %q", flag)
	}
	if err := report.Check(r.Expect); err != nil {
		return err
	}
	if decisionPoll := c.DecisionPoll; decisionPoll != nil {
		if *decisionPoll < 1 || *decisionPoll > trace.Polls {
			return fmt.Errorf("decision_poll must be between 1 and %d", trace.Polls)
		}
		if r.DecisionAt == nil || r.Evaluated != *decisionPoll {
			return fmt.Errorf("expected decision at poll %d, observed %s after %d polls", *decisionPoll, r.Observed, r.Evaluated)
		}
	}
	return nil
}

func (r SuiteReport) WriteText(w io.Writer) error {
	var b strings.Builder
	for _, c := range r.Cases {
		status := "PASS"
		if c.Error != "" {
			status = "FAIL"
		}
		observed := c.Observed
		if observed == "no_trigger_in_observed_data" {
			observed = "no rollback"
		}
		fmt.Fprintf(&b, "%s  %s\n", status, c.Name)
		if observed != "" {
			if c.DecisionAt != nil {
				observed = fmt.Sprintf("%s triggered at poll %d", observed, c.Evaluated)
			}
			fmt.Fprintf(&b, "      %s | %d/%d evaluated polls eligible\n", observed, c.Eligible, c.Evaluated)
		}
		if c.Measurement != "" {
			fmt.Fprintf(&b, "      %s\n", c.Measurement)
		}
		if c.Error != "" {
			fmt.Fprintf(&b, "      %s\n", c.Error)
		}
	}
	fmt.Fprintf(&b, "%d/%d policy cases passed\n", r.Passed, len(r.Cases))
	_, err := io.WriteString(w, b.String())
	return err
}
