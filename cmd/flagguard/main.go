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
	suite := flag.String("suite", "", "check a policy against a local JSON suite of labeled traces")
	window := flag.String("backtest", "", "replay a duration of Prometheus history with no flag or state writes, e.g. 10m")
	until := flag.String("until", "", "end of the replay window in RFC3339 (defaults to now)")
	replay := flag.String("replay", "", "evaluate a saved backtest trace offline")
	save := flag.String("save-trace", "", "write the historical inputs to a new JSON file for offline replay")
	expect := flag.String("expect", "", "backtest assertion: flag=metric or flag=none (requires complete coverage for none)")
	format := flag.String("format", "json", "backtest output: json or text")
	gapCheck := flag.String("gap-check", "", "stress one flag by removing each confirmation poll in turn from a saved replay")
	requireBy := flag.String("require-by", "", "fail the gap check unless every scenario decides by this duration from trace start")
	flag.Parse()
	given := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { given[f.Name] = true })
	historical := given["backtest"] || given["replay"] || given["suite"]
	var optionError string
	switch {
	case given["gap-check"] && (!given["replay"] || *gapCheck == "" || given["expect"]):
		optionError = "-gap-check requires a flag name and -replay; use -require-by for its deadline"
	case given["require-by"] && (!given["gap-check"] || *requireBy == ""):
		optionError = "-require-by needs -gap-check and a duration from trace start"
	case given["suite"] && (*suite == "" || given["replay"] || given["backtest"] || given["expect"]):
		optionError = "-suite needs a manifest path and uses the expectations in that file"
	case given["backtest"] && *window == "" || given["replay"] && *replay == "":
		optionError = "supply a duration for -backtest or a trace path for -replay"
	case given["backtest"] && given["replay"]:
		optionError = "choose either -backtest or -replay"
	case historical && (*check || given["mode"]):
		optionError = "historical evaluation cannot be combined with -check or -mode"
	case !given["backtest"] && (given["until"] || given["save-trace"]):
		optionError = "-until and -save-trace require -backtest"
	case !historical && (given["expect"] || given["format"]):
		optionError = "-expect and -format require historical evaluation"
	case given["expect"] && *expect == "":
		optionError = "-expect requires flag=metric or flag=none"
	}
	if optionError != "" {
		slog.Error(optionError)
		os.Exit(2)
	}
	if !historical {
		if err := runtimeguard.LoadSecrets("FLAGSMITH_ADMIN_TOKEN", "FLAGSMITH_ENV_KEY", "FLAGGUARD_AUTH_TOKEN", "WEBHOOK_URL"); err != nil {
			slog.Error("secret configuration failed", "error", err)
			os.Exit(1)
		}
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
	if historical {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
		if err := runBacktest(c, *suite, *window, *until, *save, *replay, *format, *expect, *gapCheck, *requireBy); err != nil {
			slog.Error("backtest failed", "error", err)
			os.Exit(1)
		}
		return
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
	metrics := prometheus.New(c.Prometheus.URL)
	e, err := engine.New(c, metrics, fs, notifier.Webhook{URL: c.Notifier.WebhookURL, Format: c.Notifier.Format})
	if err != nil {
		slog.Error("state initialization failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	app := &server.Server{Engine: e, History: metrics, AuthToken: auth, AllowedHosts: strings.FieldsFunc(os.Getenv("FLAGGUARD_ALLOWED_HOSTS"), func(r rune) bool { return r == ',' }), DemoURL: os.Getenv("DEMO_APP_URL"), DemoToken: os.Getenv("DEMO_CONTROL_TOKEN")}
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
