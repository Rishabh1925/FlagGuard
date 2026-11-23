package server

import (
	"bytes"
	"embed"
	"encoding/json"
	"flagguard/internal/backtest"
	"flagguard/internal/engine"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed static/*
var assets embed.FS

type Server struct {
	History            backtest.History
	Engine             *engine.Engine
	DemoURL, DemoToken string
	HTTP               *http.Client
	AuthToken          string
	AllowedHosts       []string
}

func (s *Server) Handler() http.Handler {
	if s.HTTP == nil {
		s.HTTP = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	mux := http.NewServeMux()
	// Only these bundled demonstration artifacts are public. Live reports require operator access.
	exhibit := map[string]string{
		"/exhibit/switch.webp":   "image/webp",
		"/exhibit/recorder.webp": "image/webp",
		"/exhibit/checkout.json": "application/json",
	}
	for path, contentType := range exhibit {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Cache-Control", "public, max-age=3600")
			http.ServeFileFS(w, r, assets, "static"+path)
		})
	}
	for path, contentType := range map[string]string{"/app.js": "text/javascript; charset=utf-8", "/app.css": "text/css; charset=utf-8", "/landing.css": "text/css; charset=utf-8", "/landing.js": "text/javascript; charset=utf-8"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			b, _ := assets.ReadFile("static" + path)
			w.Header().Set("Content-Type", contentType)
			w.Write(b)
		})
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := assets.ReadFile("static/landing.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	mux.HandleFunc("GET /workspace", func(w http.ResponseWriter, r *http.Request) {
		b, _ := assets.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		states, _ := s.Engine.Snapshot()
		ready := len(states) > 0 && s.Engine.PersistenceHealthy()
		for _, v := range states {
			if v.UpdatedAt.IsZero() || time.Since(v.UpdatedAt) > 30*time.Second || !v.FlagsmithOK || !v.PrometheusOK || v.Status == "ERROR" || v.Status == "ACTION_REQUIRED" || v.Status == "NO_DATA" || v.Status == "WAITING" {
				ready = false
			}
		}
		status := http.StatusOK
		if !ready {
			status = http.StatusServiceUnavailable
		}
		write(w, status, map[string]bool{"ready": ready})
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		states, _ := s.Engine.Snapshot()
		write(w, 200, map[string]any{"guardrails": states, "time": time.Now(), "demo_enabled": s.DemoURL != "", "storage_ok": s.Engine.PersistenceHealthy()})
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) { _, events := s.Engine.Snapshot(); write(w, 200, events) })
	mux.HandleFunc("GET /api/incident-test", s.incidentExport)
	mux.HandleFunc("GET /api/report", func(w http.ResponseWriter, r *http.Request) {
		states, events := s.Engine.Snapshot()
		w.Header().Set("Content-Disposition", `attachment; filename="flagguard-incident-report.json"`)
		write(w, 200, map[string]any{
			"schema_version": 1, "generated_at": time.Now().UTC(),
			"demo_enabled": s.DemoURL != "", "guardrails": states, "events": events,
			"scope": "Last 200 retained events. Evidence records observed guardrail decisions and API read-back, not a causal A/B result or proof of client propagation. Older events may lack evidence.",
		})
	})
	mux.HandleFunc("POST /api/reset/{flag}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Engine.Reset(r.Context(), r.PathValue("flag")); err != nil {
			write(w, 409, map[string]string{"error": err.Error()})
			return
		}
		write(w, 200, map[string]string{"status": "reset"})
	})
	mux.HandleFunc("GET /api/demo/status", func(w http.ResponseWriter, r *http.Request) { s.proxyDemo(w, r, "GET", "/status", nil) })
	mux.HandleFunc("POST /api/demo/chaos", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if dec.Decode(&input) != nil || input.Enabled == nil || dec.Decode(&struct{}{}) != io.EOF {
			write(w, 400, map[string]string{"error": "expected an enabled boolean"})
			return
		}
		b, _ := json.Marshal(input)
		s.proxyDemo(w, r, "POST", "/chaos", b)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if !s.allowedHost(r.Host) {
			write(w, 403, map[string]string{"error": "host is not allowed"})
			return
		}
		_, publicExhibit := exhibit[r.URL.Path]
		public := publicExhibit || r.URL.Path == "/" || r.URL.Path == "/landing.css" || r.URL.Path == "/landing.js" || r.URL.Path == "/healthz"
		if !public && !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="FlagGuard", charset="UTF-8"`)
			write(w, 401, map[string]string{"error": "operator authentication required"})
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'")
		// Loopback-only deployment plus same-origin JSON mutations protects local demo controls.
		if r.Method != "GET" && r.Method != "HEAD" {
			mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			origin := r.Header.Get("Origin")
			u, err := url.Parse(origin)
			if (origin != "" && (err != nil || u.Host != r.Host)) || r.Header.Get("Sec-Fetch-Site") == "cross-site" || mediaType != "application/json" {
				write(w, 403, map[string]string{"error": "same-origin JSON requests required"})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) proxyDemo(w http.ResponseWriter, r *http.Request, method, path string, body []byte) {
	if s.DemoURL == "" {
		write(w, 404, map[string]string{"error": "demo controls disabled"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), method, strings.TrimRight(s.DemoURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		write(w, 502, map[string]string{"error": "invalid demo URL"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.DemoToken)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		write(w, 502, map[string]string{"error": "demo app unavailable"})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
