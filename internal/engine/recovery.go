package engine

import (
	"context"
	"flagguard/internal/config"
	"flagguard/internal/prometheus"
	"time"
)

type RecoveryCheck struct {
	Status         string                   `json:"status"`
	Detail         string                   `json:"detail"`
	Selector       string                   `json:"selector"`
	WindowSeconds  float64                  `json:"window_seconds"`
	MaxGapSeconds  float64                  `json:"max_gap_seconds"`
	MinimumSamples int                      `json:"minimum_samples"`
	Observed       int                      `json:"observed"`
	Passed         int                      `json:"passed"`
	StartedAt      time.Time                `json:"started_at,omitzero"`
	CheckedAt      time.Time                `json:"checked_at,omitzero"`
	Recent         []prometheus.ProbeSample `json:"recent,omitempty"`
}

func pendingRecovery(p config.RecoveryProbe, detail string) *RecoveryCheck {
	return &RecoveryCheck{Status: "WAITING", Detail: detail, Selector: p.Selector(), WindowSeconds: p.Window.Seconds(), MaxGapSeconds: p.MaxGap.Seconds(), MinimumSamples: p.MinimumSamples}
}

func (e *Engine) checkRecovery(ctx context.Context, p config.RecoveryProbe, previous *RecoveryCheck, now time.Time) *RecoveryCheck {
	check := pendingRecovery(p, "Collecting a full window of probes while the flag is off.")
	check.CheckedAt = now
	check.StartedAt = now
	if previous != nil && !previous.StartedAt.IsZero() && !previous.StartedAt.After(now) {
		check.StartedAt = previous.StartedAt
	}
	reader, ok := e.query.(prometheus.ProbeReader)
	if !ok {
		check.Status = "NO_DATA"
		check.Detail = "Raw probe samples are unavailable."
		return check
	}
	samples, err := reader.ProbeWindow(ctx, check.Selector, p.Window, now)
	if err != nil {
		check.Status = "NO_DATA"
		check.Detail = err.Error()
		return check
	}
	first := now.Add(-p.Window)
	for len(samples) > 0 && (samples[0].At.Before(check.StartedAt) || !samples[0].At.After(first)) {
		samples = samples[1:]
	}
	check.Observed = len(samples)
	check.Recent = samples[max(0, len(samples)-16):]
	last := first
	gap := false
	for _, s := range samples {
		if s.At.Sub(last) > p.MaxGap {
			gap = true
		}
		last = s.At
		if s.Passed {
			check.Passed++
		}
	}
	switch {
	case check.Passed < check.Observed:
		check.Status = "FAILING"
		check.Detail = "The candidate failed a probe. Fix the candidate and wait for a clean window."
	case now.Sub(check.StartedAt) < p.Window:
	case check.Observed < p.MinimumSamples || gap || now.Sub(last) > p.MaxGap:
		check.Status = "NO_DATA"
		check.Detail = "Probe coverage is incomplete. Check the candidate probe and scrape cadence."
	default:
		check.Status = "READY"
		check.Detail = "The candidate passed the full probe window. An operator can now re-enable the flag."
	}
	return check
}
