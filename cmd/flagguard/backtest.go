package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"flagguard/internal/backtest"
	"flagguard/internal/config"
	"flagguard/internal/prometheus"
)

func runBacktest(c config.Config, suite, window, until, save, replay, format, expect, gapFlag, deadline string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if format != "json" && format != "text" {
		return fmt.Errorf("-format requires json or text")
	}
	if suite != "" {
		report, err := backtest.RunSuite(ctx, c, suite)
		if err != nil {
			return err
		}
		if format == "text" {
			err = report.WriteText(os.Stdout)
		} else {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			err = enc.Encode(report)
		}
		if err != nil {
			return err
		}
		if report.Passed != len(report.Cases) {
			return fmt.Errorf("policy suite failed")
		}
		return nil
	}
	var trace backtest.Trace
	if replay != "" {
		var err error
		trace, err = backtest.ReadTrace(replay)
		if err != nil {
			return err
		}
	} else {
		d, err := time.ParseDuration(window)
		if err != nil {
			return err
		}
		end := time.Now()
		if until != "" {
			end, err = time.Parse(time.RFC3339Nano, until)
			if err != nil || end.After(time.Now()) {
				return fmt.Errorf("-until must be an RFC3339 timestamp in the past")
			}
		}
		trace, err = backtest.Capture(ctx, c, prometheus.New(c.Prometheus.URL), end, d)
		if err != nil {
			return err
		}
	}
	if gapFlag != "" {
		report, err := backtest.CheckGaps(ctx, c, trace, gapFlag)
		if err != nil {
			return err
		}
		var verdict error
		if deadline != "" {
			d, err := time.ParseDuration(deadline)
			if err != nil || d < 0 || d > report.End.Sub(report.Start) {
				return fmt.Errorf("-require-by must be a duration within the trace window")
			}
			verdict = report.RequireBy(d)
		}
		if format == "text" {
			err = report.WriteText(os.Stdout)
		} else {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			err = enc.Encode(report)
		}
		if err != nil {
			return err
		}
		return verdict
	}
	report, err := backtest.Replay(ctx, c, trace)
	if err != nil {
		return err
	}
	if save != "" {
		b, err := json.MarshalIndent(trace, "", "  ")
		if err != nil {
			return err
		}
		f, err := os.OpenFile(save, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(append(b, '\n'))
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			os.Remove(save)
			return fmt.Errorf("could not finish writing trace %s", save)
		}
	}
	if format == "text" {
		if err := report.WriteText(os.Stdout); err != nil {
			return err
		}
	} else {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	}
	if expect != "" {
		return report.Check(expect)
	}
	return nil
}
