package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"flagguard/internal/backtest"
)

func (s *Server) incidentExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	at, err := time.Parse(time.RFC3339Nano, q.Get("time"))
	if err != nil || q.Get("flag") == "" {
		write(w, 400, map[string]string{"error": "Choose a retained rollback decision."})
		return
	}
	_, events := s.Engine.Snapshot()
	for i, event := range events {
		if event.Flag != q.Get("flag") || !event.Time.Equal(at) || event.Kind != q.Get("kind") {
			continue
		}
		if event.Evidence == nil || (event.Kind != "ROLLBACK" && event.Kind != "WOULD_ROLLBACK") {
			break
		}
		if s.History == nil && len(event.Evidence.Observations) == 0 {
			write(w, 503, map[string]string{"error": "Prometheus history is unavailable."})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		archive, err := backtest.IncidentArchive(ctx, event, s.History, events[i+1:])
		if errors.Is(err, backtest.ErrNotReproduced) {
			write(w, 409, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "Could not build this test. Check the recorded policy and Prometheus history."})
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="flagguard-incident-test.zip"`)
		w.Write(archive)
		return
	}
	write(w, 404, map[string]string{"error": "This rollback decision is unavailable in retained history."})
}
