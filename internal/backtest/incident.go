package backtest

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"flagguard/internal/config"
	"flagguard/internal/engine"
	"gopkg.in/yaml.v3"
)

var ErrNotReproduced = errors.New("the retained measurements did not reproduce the recorded rule at its decision poll")

// IncidentArchive exports only the inputs which influenced the original decision.
// The historical reader is the sole integration this function can access.
func IncidentArchive(ctx context.Context, event engine.Event, history History) ([]byte, error) {
	ev := event.Evidence
	if (event.Kind != "ROLLBACK" && event.Kind != "WOULD_ROLLBACK") || ev == nil || ev.EvaluatedAt.IsZero() || event.Metric == "" {
		return nil, fmt.Errorf("a confirmed or shadow rollback with decision evidence is required")
	}
	step := time.Duration(ev.PollSeconds * float64(time.Second))
	if step < time.Second || step%time.Millisecond != 0 {
		return nil, fmt.Errorf("unsupported recorded poll interval")
	}
	g := config.Guardrail{Name: event.Flag, Flag: event.Flag, Variant: event.Variant, Action: ev.Action, ExperimentID: ev.ExperimentID, MinimumSamples: ev.MinimumSamples, SampleQuery: ev.SampleQuery}
	if ev.Control != nil {
		g.Control = &config.Control{Variant: ev.Control.Variant, SampleQuery: ev.Control.SampleQuery, MinimumSamples: ev.Control.MinimumSamples}
	}
	var since *time.Time
	for _, r := range ev.Rules {
		g.Rules = append(g.Rules, config.Rule{Metric: r.Metric, Query: r.Query, ControlQuery: r.ControlQuery, Comparison: r.Comparison, Threshold: r.Threshold, For: time.Duration(r.ForSeconds * float64(time.Second))})
		if r.Metric == event.Metric {
			since = r.Since
		}
	}
	if since == nil || since.After(ev.EvaluatedAt) {
		return nil, fmt.Errorf("missing recorded breach start")
	}
	cfg := config.Config{Mode: "shadow", PollInterval: step, Guardrails: []config.Guardrail{g}}
	cfg.Flagsmith.APIURL = "http://flagsmith.invalid/api/v1"
	cfg.Prometheus.URL = "http://prometheus.invalid"
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	trace, source, err := incidentTrace(ctx, cfg, ev, *since, history)
	if err != nil {
		return nil, err
	}
	report, err := Replay(ctx, cfg, trace)
	if err != nil {
		return nil, err
	}
	decision := report.Guardrails[0].Decision
	if decision == nil || decision.Metric != event.Metric || !decision.Time.Equal(report.End) {
		r := report.Guardrails[0]
		observed := fmt.Sprintf("%s (%d/%d evaluated polls eligible)", r.Outcome, r.Eligible, r.Evaluated)
		if decision != nil {
			observed = fmt.Sprintf("%s at %s", decision.Metric, decision.Time.Format(time.RFC3339Nano))
		}
		return nil, fmt.Errorf("%w. Recorded %s at %s. Replay observed %s", ErrNotReproduced, event.Metric, ev.EvaluatedAt.Format(time.RFC3339Nano), observed)
	}

	// JSON is valid YAML. Encode literal dollar signs so config.Load's environment
	// expansion cannot alter a captured query on the machine replaying the case.
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	policy, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	policy = bytes.ReplaceAll(policy, []byte("$"), []byte(`\u0024`))
	testCase := suiteCase{Name: event.Flag + " rollback", Trace: "trace.json", Expect: event.Flag + "=" + event.Metric, DecisionPoll: &trace.Polls}
	if err := report.Check(testCase.Expect); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	write := func(name string, data []byte) error {
		f, err := z.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	if err = write("policy.yaml", policy); err != nil {
		return nil, err
	}
	for _, entry := range []struct {
		name  string
		value any
	}{
		{"trace.json", trace}, {"suite.json", map[string]any{"cases": []suiteCase{testCase}}},
		{"decision.json", event},
		{"protection.json", map[string]any{"cases": []suiteCase{{Name: event.Flag + " rollback deadline", Trace: "trace.json", Flag: event.Flag, RollbackByPoll: &trace.Polls}}}},
	} {
		data, err := json.MarshalIndent(entry.value, "", "  ")
		if err != nil {
			return nil, err
		}
		if err = write(entry.name, data); err != nil {
			return nil, err
		}
	}
	readme := `# Incident regression test

From this extracted directory, using the FlagGuard binary:

    flagguard -config policy.yaml -suite suite.json -format text

One case should pass. To review a proposed policy change, change the thresholds
in a copy of policy.yaml and run the same command with that copy.

To check whether any rule still catches the incident by the recorded poll, run:

    flagguard -config policy.yaml -suite protection.json -format text

Use the proposed policy for both commands to compare rule identity and deadline.

decision.json is the recorded decision. ` + source + ` Before export, FlagGuard
reproduced the same first triggering rule at the same decision poll.  The suite checks the triggering rule and its poll.
It makes no flag writes. Query text and metric labels are included.
`
	if err = write("README.md", []byte(readme)); err != nil {
		return nil, err
	}
	if err = z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func incidentTrace(ctx context.Context, cfg config.Config, ev *engine.Evidence, since time.Time, history History) (Trace, string, error) {
	step := cfg.PollInterval
	if len(ev.Observations) > 0 {
		// Match the compact ordering used by engine.Observation.
		var keys []string
		for _, q := range queries(cfg.Guardrails[0]) {
			if q != "" {
				keys = append(keys, q)
			}
		}
		polls := ev.Observations
		t := Trace{Version: 2, Start: polls[0].At, StepMillis: step.Milliseconds(), Polls: len(polls), Series: make(map[string][]*float64)}
		if !polls[len(polls)-1].At.Equal(ev.EvaluatedAt) {
			return Trace{}, "", fmt.Errorf("recorded samples do not end at the decision")
		}
		for _, q := range keys {
			t.Series[q] = make([]*float64, t.Polls)
		}
		for i, p := range polls {
			if len(p.Values) != len(keys) {
				return Trace{}, "", fmt.Errorf("recorded sample does not match policy")
			}
			t.Times = append(t.Times, p.At)
			for k, q := range keys {
				t.Series[q][i] = p.Values[k]
			}
		}
		return t, "trace.json contains the recorded live readings and actual controller poll times.", nil
	}
	if history == nil {
		return Trace{}, "", fmt.Errorf("Prometheus history is unavailable")
	}
	// Include two polls of context before the winning rule's recorded breach.
	span := ev.EvaluatedAt.Sub(since)
	intervals := span/step + 2
	if span%step != 0 {
		intervals++
	}
	if intervals >= 2000 || step > time.Duration((1<<63-1)/intervals) {
		return Trace{}, "", fmt.Errorf("incident exceeds the replay window limit")
	}
	trace, err := Capture(ctx, cfg, history, ev.EvaluatedAt, intervals*step)
	if err != nil {
		return Trace{}, "", fmt.Errorf("read incident history: %w", err)
	}
	return trace, "trace.json contains reconstructed Prometheus readings on a fixed time grid.", nil
}
