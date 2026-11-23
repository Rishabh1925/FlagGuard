package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flagguard/internal/backtest"
	"flagguard/internal/config"
	"flagguard/internal/engine"
)

// Serve archived Prometheus readings at their original timestamps.
type recordedHistory struct{ backtest.Trace }

func (h recordedHistory) Range(_ context.Context, query string, start, end time.Time, step time.Duration) (map[int64]float64, error) {
	values := make(map[int64]float64)
	for i, sample := range h.Series[query] {
		at := h.Start.Add(time.Duration(int64(i)*h.StepMillis) * time.Millisecond)
		if sample != nil && !at.Before(start) && !at.After(end) {
			values[at.UnixMilli()] = *sample
		}
	}
	return values, nil
}

func TestRecordedIncidentDownload(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	raw, err := os.ReadFile("../../docs/evidence/price-contract-rollback.json")
	if err != nil {
		t.Fatal(err)
	}
	var recorded struct{ Rollback engine.Event }
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}
	event := recorded.Rollback
	trace, err := backtest.ReadTrace("../../deploy/blackbox/incident.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(map[string]any{"events": []engine.Event{event}})
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, raw, 0600); err != nil {
		t.Fatal(err)
	}
	controller, err := engine.New(config.Config{StateFile: state}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := Server{Engine: controller, History: recordedHistory{trace}, AuthToken: "operator-test"}
	handler := s.Handler()
	params := url.Values{"time": {event.Time.Format(time.RFC3339Nano)}, "flag": {event.Flag}, "kind": {event.Kind}}
	request := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://localhost/api/incident-test?"+params.Encode(), nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request(""); w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
	w := request(s.AuthToken)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("download: %d %s", w.Code, w.Body.String())
	}
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"policy.yaml", "trace.json", "decision.json", "suite.json", "protection.json", "README.md"} {
		f, err := z.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(filepath.Join(dir, "policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "shadow" || cfg.Flagsmith.AdminToken != "" || cfg.Guardrails[0].Rules[0].Threshold != 0.9 {
		t.Fatal("unexpected exported policy")
	}
	run := func() backtest.SuiteReport {
		report, err := backtest.RunSuite(ctx, cfg, filepath.Join(dir, "suite.json"))
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	if report := run(); report.Passed != 1 {
		t.Fatalf("baseline: %+v", report)
	}
	cfg.Guardrails[0].Rules[0].For = 5 * time.Second
	if report := run(); report.Passed != 0 || !strings.Contains(report.Cases[0].Error, "expected decision at poll") {
		t.Fatalf("premature rollback escaped test: %+v", report)
	}
	if report, err := backtest.RunSuite(ctx, cfg, filepath.Join(dir, "protection.json")); err != nil || report.Passed != 1 {
		t.Fatalf("earlier decision missed protection deadline: %+v, %v", report, err)
	}
	cfg.Guardrails[0].Rules[0].Threshold = 0
	if report := run(); report.Passed != 0 || report.Cases[0].Observed != "no_trigger_in_observed_data" {
		t.Fatalf("missed rollback escaped test: %+v", report)
	}
	s.History = recordedHistory{}
	if w := request(s.AuthToken); w.Code != http.StatusConflict {
		t.Fatalf("unreproducible history returned %d", w.Code)
	}
	params.Set("flag", "another_flag")
	if w := request(s.AuthToken); w.Code != http.StatusNotFound {
		t.Fatal(w.Code)
	}
}
