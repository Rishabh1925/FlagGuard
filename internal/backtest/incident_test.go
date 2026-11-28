package backtest

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"flagguard/internal/engine"
	"flagguard/internal/flagsmith"
)

type observedFlag struct{ disabled bool }

func (f *observedFlag) Find(context.Context, string) (flagsmith.FeatureState, error) {
	return flagsmith.FeatureState{Enabled: !f.disabled}, nil
}
func (f *observedFlag) SetEnabled(_ context.Context, _ string, enabled bool) (flagsmith.Change, error) {
	f.disabled = !enabled
	return flagsmith.Change{Before: true, After: enabled}, nil
}

// The fourth latency poll falls a fraction short of the confirmation deadline.
// On an ideal grid latency wins there. On the live schedule both rules qualify
// at the following poll and the configured first rule (errors) wins.
func TestExportReplaysActualPollTimes(t *testing.T) {
	ctx := context.Background()
	cfg, _ := recorded(t)
	cfg.Mode = "enforce"
	cfg.StateFile = ""
	g := cfg.Guardrails[0]
	start := time.Date(2026, 11, 23, 20, 6, 25, 0, time.UTC)
	offsets := []time.Duration{0, 5*time.Second + time.Millisecond, 10 * time.Second, 15 * time.Second, 20 * time.Second, 25*time.Second + time.Millisecond}
	input := Trace{Version: 2, Start: start, Polls: len(offsets), StepMillis: 5000, Series: map[string][]*float64{}}
	for i, delta := range offsets {
		input.Times = append(input.Times, start.Add(delta))
		values := map[string]float64{g.SampleQuery: 60, g.Control.SampleQuery: 50, g.Rules[0].Query: 0, g.Rules[0].ControlQuery: 0, g.Rules[1].Query: 75, g.Rules[1].ControlQuery: 75}
		if i >= 1 {
			values[g.Rules[1].Query] = 897
		}
		if i >= 2 {
			values[g.Rules[0].Query] = 0.24
		}
		for q, v := range values {
			input.Series[q] = append(input.Series[q], &v)
		}
	}
	grid := input
	grid.Version = 1
	grid.Times = nil
	ideal, err := Replay(ctx, cfg, grid)
	if err != nil || ideal.Check("checkout_flow=p99_latency_ms") != nil || ideal.Guardrails[0].Evaluated != 5 {
		t.Fatalf("fixed grid: %+v %v", ideal, err)
	}
	flag := &observedFlag{}
	live, err := engine.New(cfg, input, flag, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range input.Times {
		live.Evaluate(ctx, at)
	}
	_, events := live.Snapshot()
	e := events[len(events)-1]
	if !flag.disabled || e.Kind != "ROLLBACK" || e.Metric != "error_rate" {
		t.Fatalf("unexpected live decision %+v", e)
	}
	archive, err := IncidentArchive(ctx, e, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	file, err := z.Open("trace.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var trace Trace
	if err = json.NewDecoder(file).Decode(&trace); err != nil {
		t.Fatal(err)
	}
	report, err := Replay(ctx, cfg, trace)
	if err != nil || report.Check("checkout_flow=error_rate") != nil || !report.Guardrails[0].Decision.Time.Equal(e.Time) {
		t.Fatalf("archived replay %+v %v", report, err)
	}
	cfg.Guardrails[0].Rules[0].For = time.Minute
	proposed, err := Replay(ctx, cfg, trace)
	if err != nil || proposed.Check("checkout_flow=p99_latency_ms") != nil || !proposed.Guardrails[0].Decision.Time.Equal(e.Time) {
		t.Fatalf("one-minute policy %+v %v", proposed, err)
	}
}

func TestIncidentArchiveScopesHandoffToSelectedFlag(t *testing.T) {
	raw, err := os.ReadFile("../../docs/evidence/ubuntu-checkout/decision.json")
	if err != nil {
		t.Fatal(err)
	}
	var incident engine.Event
	if err := json.Unmarshal(raw, &incident); err != nil {
		t.Fatal(err)
	}
	reset := engine.Event{Time: incident.Time.Add(time.Minute), Kind: "RESET", Flag: incident.Flag, Change: incident.Change}
	other := reset
	other.Flag = "other_flag"
	archive, err := IncidentArchive(context.Background(), incident, nil, []engine.Event{other, reset})
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	file, err := z.Open("report.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var report struct {
		Version int `json:"schema_version"`
		Events  []engine.Event
	}
	if err := json.NewDecoder(file).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.Version != 1 || len(report.Events) != 2 || !report.Events[0].Time.Equal(incident.Time) ||
		report.Events[1].Kind != "RESET" || report.Events[1].Flag != incident.Flag || report.Events[1].Change == nil {
		t.Fatalf("wrong scoped recovery history: %+v", report)
	}
}
