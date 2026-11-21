package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	sdk "github.com/Flagsmith/flagsmith-go-client/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func main() {
	rps, err := strconv.Atoi(env("DEMO_RPS", "4"))
	if err != nil || rps < 1 || rps > 100 {
		slog.Error("DEMO_RPS must be between 1 and 100")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	key := os.Getenv("FLAGSMITH_ENV_KEY")
	mode := "remote"
	opts := []sdk.Option{sdk.WithBaseURL(env("FLAGSMITH_API_URL", "http://localhost:8000/api/v1/")), sdk.WithRequestTimeout(5 * time.Second), sdk.WithRetries(0, 0), sdk.WithEvents(ctx), sdk.WithEventsBaseURL(env("FLAGSMITH_EVENTS_URL", "http://localhost:8000/api/")), sdk.WithEventsFlushInterval(2 * time.Second), sdk.WithSlogLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))}
	if serverKey := os.Getenv("FLAGSMITH_SERVER_KEY"); serverKey != "" {
		if !strings.HasPrefix(serverKey, "ser.") {
			slog.Error("FLAGSMITH_SERVER_KEY must be a server-side SDK key")
			os.Exit(1)
		}
		key, mode = serverKey, "local"
		opts = append(opts, sdk.WithLocalEvaluation(ctx), sdk.WithEnvironmentRefreshInterval(2*time.Second))
	}
	client := sdk.NewClient(key, opts...)
	var chaos atomic.Bool
	var evaluationsOK atomic.Bool
	var total atomic.Uint64
	var failures atomic.Uint64
	var exposures atomic.Uint64
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "demo_http_requests_total", Help: "Completed checkout requests by served variant and HTTP status."}, []string{"variant", "status"})
	latency := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "demo_http_request_duration_seconds", Help: "Checkout business-operation latency after flag evaluation.", Buckets: []float64{.025, .05, .075, .1, .2, .3, .4, .5, .6, .75, .9, 1, 1.5, 2, 5}}, []string{"variant"})
	sdkErrors := prometheus.NewCounter(prometheus.CounterOpts{Name: "demo_flagsmith_evaluation_errors_total", Help: "Failed SDK evaluations; these requests fail closed with 503."})
	sdkLatency := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "demo_flagsmith_evaluation_duration_seconds", Help: "SDK evaluation latency, separate from checkout operation latency.", Buckets: prometheus.DefBuckets})
	prometheus.MustRegister(requests, latency, sdkErrors, sdkLatency, prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "demo_experiment_dropped_events_total", Help: "Experiment events dropped by the Flagsmith SDK."}, func() float64 { return float64(client.DroppedEvents()) }))
	for _, v := range []string{"control", "variant_b"} {
		for _, s := range []string{"200", "500"} {
			requests.WithLabelValues(v, s).Add(0)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"chaos": chaos.Load(), "evaluations_ok": evaluationsOK.Load(), "evaluation_mode": mode, "experiment_evaluations": exposures.Load(), "requests": total.Load(), "errors": failures.Load(), "traffic_rps": rps, "dropped_events": client.DroppedEvents(), "synthetic_traffic": true})
	})
	mux.HandleFunc("POST /chaos", func(w http.ResponseWriter, r *http.Request) {
		token := os.Getenv("DEMO_CONTROL_TOKEN")
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		var b struct {
			Enabled *bool `json:"enabled"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		d.DisallowUnknownFields()
		if d.Decode(&b) != nil || b.Enabled == nil {
			http.Error(w, "expected enabled boolean", 400)
			return
		}
		chaos.Store(*b.Enabled)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"enabled": chaos.Load()})
	})
	mux.HandleFunc("GET /checkout", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.URL.Query().Get("user_id")
		if id == "" {
			id = "visitor"
		}
		if len(id) > 128 {
			http.Error(w, "user_id too long", 400)
			return
		}
		flag, err := client.GetExperimentFlag(r.Context(), "checkout_flow", sdk.NewEvaluationContext(id, nil))
		sdkLatency.Observe(time.Since(start).Seconds())
		if err != nil {
			evaluationsOK.Store(false)
			sdkErrors.Inc()
			http.Error(w, "Flagsmith unavailable or checkout_flow missing; finish local setup", 503)
			return
		}
		evaluationsOK.Store(true)
		if flag.Enabled && !flag.IsDefault && flag.Experiment != nil && flag.Experiment.InExperiment && flag.Variant != "" {
			exposures.Add(1)
		}
		start = time.Now() // Guard the checkout operation; expose control-plane latency separately.
		variant := "control"
		if flag.Enabled && fmt.Sprint(flag.Value) == "variant_b" {
			variant = "variant_b"
		}
		delay := time.Duration(30+rand.IntN(30)) * time.Millisecond
		status := 200
		if variant == "variant_b" {
			delay = time.Duration(40+rand.IntN(30)) * time.Millisecond
			if chaos.Load() {
				delay = time.Duration(600+rand.IntN(300)) * time.Millisecond
				if rand.IntN(100) < 30 {
					status = 500
				}
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(delay):
		}
		requests.WithLabelValues(variant, strconv.Itoa(status)).Inc()
		latency.WithLabelValues(variant).Observe(time.Since(start).Seconds())
		total.Add(1)
		if status == 500 {
			failures.Add(1)
		}
		// Synthetic successful purchases are real SDK conversion events, not measured human conversions.
		if status == 200 {
			if err = client.TrackEvent("checkout_completed", &sdk.EventOptions{Identifier: id, Value: 49.99}); err != nil {
				slog.Warn("conversion could not be queued", "error", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"variant": variant, "success": status == 200, "duration_ms": time.Since(start).Milliseconds()})
	})
	srv := &http.Server{Addr: env("DEMO_LISTEN", ":8081"), Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		ticker := time.NewTicker(time.Second / time.Duration(rps))
		defer ticker.Stop()
		httpClient := &http.Client{Timeout: 5 * time.Second}
		semaphore := make(chan struct{}, 40)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case semaphore <- struct{}{}:
					go func() {
						defer func() { <-semaphore }()
						req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:8081/checkout?user_id=demo_"+strconv.Itoa(rand.IntN(1000)), nil)
						resp, err := httpClient.Do(req)
						if err == nil {
							resp.Body.Close()
						}
					}()
				default:
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	slog.Info("Checkout demo is listening", "address", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("demo server failed", "error", err)
		os.Exit(1)
	}
	flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = client.FlushEvents(flush)
}
