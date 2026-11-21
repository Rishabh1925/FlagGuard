package main

import (
	"context"
	"encoding/json"
	"flag"
	"flagguard/internal/config"
	"flagguard/internal/engine"
	"flagguard/internal/flagsmith"
	"flagguard/internal/notifier"
	"flagguard/internal/preflight"
	"flagguard/internal/prometheus"
	"flagguard/internal/runtimeguard"
	"flagguard/internal/server"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	path := flag.String("config", "guardrails.yaml", "guardrail config path")
	listenDefault := os.Getenv("FLAGGUARD_LISTEN")
	if listenDefault == "" {
		listenDefault = "127.0.0.1:8085"
	}
	listen := flag.String("listen", listenDefault, "HTTP listen address")
	check := flag.Bool("check", false, "read-only configuration and integration check; no server or flag changes")
	mode := flag.String("mode", "", "override configured protection mode: shadow or enforce")
	flag.Parse()
	if err := runtimeguard.LoadSecrets("FLAGSMITH_ADMIN_TOKEN", "FLAGSMITH_ENV_KEY", "FLAGGUARD_AUTH_TOKEN", "WEBHOOK_URL"); err != nil {
		slog.Error("secret configuration failed", "error", err)
		os.Exit(1)
	}
	defaults := map[string]string{"FLAGSMITH_API_URL": "http://localhost:8000/api/v1", "PROMETHEUS_URL": "http://localhost:9090", "FLAGGUARD_STATE_FILE": "data/state.json"}
	for k, v := range defaults {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
	c, err := config.Load(*path)
	if err == nil && *mode != "" {
		c.Mode = *mode
		err = c.Validate()
	}
	if err != nil {
		slog.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	fs := flagsmith.New(c.Flagsmith.APIURL, c.Flagsmith.AdminToken, c.Flagsmith.EnvironmentKey)
	fs.Experiments = map[string]int{}
	for _, g := range c.Guardrails {
		fs.Experiments[g.Flag] = g.ExperimentID
	}
	if *check {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		report := preflight.Run(ctx, c, prometheus.New(c.Prometheus.URL), fs)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil || !report.Ready {
			os.Exit(1)
		}
		return
	}
	auth := os.Getenv("FLAGGUARD_AUTH_TOKEN")
	if err := runtimeguard.ValidateListener(*listen, auth, os.Getenv("FLAGGUARD_LOCAL_DEMO") == "true"); err != nil {
		slog.Error("unsafe listener configuration", "error", err)
		os.Exit(1)
	}
	unlock, err := runtimeguard.Lock(c.StateFile)
	if err != nil {
		slog.Error("state lock failed", "error", err)
		os.Exit(1)
	}
	defer unlock()
	// Bind before starting the decision loop: a failed HTTP bind must never leave
	// an invisible controller performing mutations.
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		slog.Error("listen failed", "error", err)
		os.Exit(1)
	}
	defer listener.Close()
	e, err := engine.New(c, prometheus.New(c.Prometheus.URL), fs, notifier.Webhook{URL: c.Notifier.WebhookURL, Format: c.Notifier.Format})
	if err != nil {
		slog.Error("state initialization failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	app := &server.Server{Engine: e, AuthToken: auth, AllowedHosts: strings.FieldsFunc(os.Getenv("FLAGGUARD_ALLOWED_HOSTS"), func(r rune) bool { return r == ',' }), DemoURL: os.Getenv("DEMO_APP_URL"), DemoToken: os.Getenv("DEMO_CONTROL_TOKEN")}
	srv := &http.Server{Addr: *listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("FlagGuard is listening", "address", *listen, "guardrails", len(c.Guardrails))
	if err = srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		slog.Error("HTTP server stopped", "error", err)
		os.Exit(1)
	}
	stop()
	<-shutdownDone
	<-done // Keep the process/state lock until the active evaluation has stopped.
}
