package engine

import (
	"context"
	"encoding/json"
	"errors"
	"flagguard/internal/config"
	"flagguard/internal/flagsmith"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Querier interface {
	QueryAt(context.Context, string, time.Time) (float64, bool, error)
}
type Flags interface {
	Find(context.Context, string) (flagsmith.FeatureState, error)
	SetEnabled(context.Context, string, bool) (flagsmith.Change, error)
}
type Notify interface {
	Send(context.Context, any) error
}
type RuleState struct {
	ControlQuery string     `json:"control_query,omitempty"`
	ControlValue *float64   `json:"control_value,omitempty"`
	Diagnosis    string     `json:"diagnosis,omitempty"`
	Metric       string     `json:"metric"`
	Query        string     `json:"query"`
	Threshold    float64    `json:"threshold"`
	Comparison   string     `json:"comparison"`
	ForSeconds   float64    `json:"for_seconds"`
	Value        *float64   `json:"value"`
	Since        *time.Time `json:"breach_started_at"`
	Remaining    float64    `json:"remaining_seconds"`
	Error        string     `json:"error,omitempty"`
}
type State struct {
	Recovery       *RecoveryCheck `json:"recovery_probe,omitempty"`
	Control        *ControlState  `json:"control,omitempty"`
	Mode           string         `json:"mode"`
	Name           string         `json:"name"`
	Flag           string         `json:"flag"`
	Variant        string         `json:"variant"`
	Action         string         `json:"action"`
	Status         string         `json:"status"`
	Message        string         `json:"message"`
	Rules          []RuleState    `json:"rules"`
	Samples        *float64       `json:"samples"`
	MinimumSamples float64        `json:"minimum_samples"`
	UpdatedAt      time.Time      `json:"updated_at"`
	FlagEnabled    *bool          `json:"flag_enabled"`
	PrometheusOK   bool           `json:"prometheus_ok"`
	FlagsmithOK    bool           `json:"flagsmith_ok"`
	ResumeAt       *time.Time     `json:"resume_at,omitempty"`
}
type Event struct {
	Recovery  *RecoveryCheck    `json:"recovery_probe,omitempty"`
	Time      time.Time         `json:"time"`
	Kind      string            `json:"kind"`
	Flag      string            `json:"flag"`
	Variant   string            `json:"variant,omitempty"`
	Message   string            `json:"message"`
	Metric    string            `json:"metric,omitempty"`
	Value     *float64          `json:"value,omitempty"`
	Threshold *float64          `json:"threshold,omitempty"`
	Change    *flagsmith.Change `json:"change,omitempty"`
	Evidence  *Evidence         `json:"evidence,omitempty"`
}

// Evidence freezes the decision inputs before later polls or resets change them.
// It deliberately excludes API credentials, webhook URLs, and connection config.
type Evidence struct {
	Observations     []Observation `json:"observations,omitempty"`
	Control          *ControlState `json:"control,omitempty"`
	Mode             string        `json:"mode"`
	EvaluatedAt      time.Time     `json:"evaluated_at"`
	ActionFinishedAt time.Time     `json:"action_finished_at,omitzero"`
	Action           string        `json:"action"`
	ExperimentID     int           `json:"experiment_id,omitempty"`
	PollSeconds      float64       `json:"poll_seconds"`
	SampleQuery      string        `json:"sample_query,omitempty"`
	Samples          *float64      `json:"samples"`
	MinimumSamples   float64       `json:"minimum_samples"`
	Rules            []RuleState   `json:"rules"`
}
type ControlState struct {
	Variant        string   `json:"variant"`
	SampleQuery    string   `json:"sample_query"`
	Samples        *float64 `json:"samples"`
	MinimumSamples float64  `json:"minimum_samples"`
	Ready          bool     `json:"ready"`
	Error          string   `json:"error,omitempty"`
}
type diskState struct {
	Latched     map[string]bool      `json:"latched"`
	Unconfirmed map[string]string    `json:"unconfirmed,omitempty"`
	Events      []Event              `json:"events"`
	ResumeAt    map[string]time.Time `json:"resume_at,omitempty"`
}
type Engine struct {
	observations map[string][]Observation
	mu           sync.RWMutex
	op           sync.Mutex // Serialize a complete evaluation with manual reset.
	cfg          config.Config
	query        Querier
	flags        Flags
	notify       Notify
	states       []State
	events       []Event
	latched      map[string]bool
	unconfirmed  map[string]string
	lastPoll     time.Time
	resumeAt     map[string]time.Time
	storageOK    bool
}

func New(c config.Config, q Querier, f Flags, n Notify) (*Engine, error) {
	if c.Mode == "" {
		c.Mode = "enforce"
	}
	if c.Mode != "enforce" && c.Mode != "shadow" {
		return nil, fmt.Errorf("invalid protection mode")
	}
	e := &Engine{observations: make(map[string][]Observation), cfg: c, query: q, flags: f, notify: n, latched: map[string]bool{}, unconfirmed: map[string]string{}, events: []Event{}, resumeAt: map[string]time.Time{}, storageOK: true}
	if c.StateFile != "" {
		raw, err := os.ReadFile(c.StateFile)
		if err == nil {
			var d diskState
			if err = json.Unmarshal(raw, &d); err != nil {
				return nil, fmt.Errorf("invalid state file: %w", err)
			}
			if d.Latched != nil {
				e.latched = d.Latched
			}
			e.events = d.Events
			if d.ResumeAt != nil {
				e.resumeAt = d.ResumeAt
			}
			if d.Unconfirmed != nil {
				e.unconfirmed = d.Unconfirmed
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	for _, g := range c.Guardrails {
		s := State{Name: g.Name, Flag: g.Flag, Variant: g.Variant, Action: g.Action, Status: "WAITING", Message: "Waiting for the first evaluation", MinimumSamples: g.MinimumSamples}
		s.Mode = c.Mode
		if resume, ok := e.resumeAt[g.Flag]; ok {
			s.ResumeAt = &resume
		}
		if e.latched[g.Flag] {
			s.Status = "ROLLED_BACK"
			s.Message = "Rollback lock restored from disk"
			if g.RecoveryProbe != nil && c.Mode == "enforce" {
				s.Recovery = pendingRecovery(*g.RecoveryProbe, "Waiting for a fresh off-state reading from Flagsmith.")
			}
			if reason := e.unconfirmed[g.Flag]; reason != "" {
				s.Status = "ACTION_REQUIRED"
				s.Message = reason
			}
		}
		for _, r := range g.Rules {
			s.Rules = append(s.Rules, RuleState{Metric: r.Metric, Query: r.Query, Threshold: r.Threshold, Comparison: r.Comparison, ForSeconds: r.For.Seconds(), Remaining: r.For.Seconds()})
		}
		e.states = append(e.states, s)
	}
	return e, nil
}
func (e *Engine) Run(ctx context.Context) {
	e.Evaluate(ctx, time.Now())
	ticker := time.NewTicker(e.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.Evaluate(ctx, time.Now())
		}
	}
}
func clearTimers(s *State) {
	for i := range s.Rules {
		s.Rules[i].Since = nil
		s.Rules[i].Remaining = s.Rules[i].ForSeconds
	}
}
func (e *Engine) Evaluate(ctx context.Context, now time.Time) {
	e.op.Lock()
	defer e.op.Unlock()
	e.mu.RLock()
	states := cloneStates(e.states)
	gap := !e.lastPoll.IsZero() && now.Sub(e.lastPoll) > 3*e.cfg.PollInterval
	e.mu.RUnlock()
	for i, g := range e.cfg.Guardrails {
		s := states[i]
		oldStatus := s.Status
		s.UpdatedAt = now
		s.Message = ""
		s.PrometheusOK = true
		s.FlagsmithOK = true
		if gap {
			clearTimers(&s)
		}
		fs, fe := e.flags.Find(ctx, g.Flag)
		if fe != nil {
			s.FlagsmithOK = false
			s.FlagEnabled = nil
		} else {
			s.FlagEnabled = &fs.Enabled
		}
		samplesReady := true
		if g.SampleQuery != "" {
			v, ok, err := e.query.QueryAt(ctx, g.SampleQuery, now)
			s.Samples = nil
			if err != nil {
				s.PrometheusOK = false
				s.Message = err.Error()
			}
			if ok && err == nil {
				s.Samples = &v
			}
			samplesReady = ok && err == nil && v >= g.MinimumSamples
		}
		anyBreach, missing := false, false
		// Control data is diagnostic only: unavailable control cannot suppress an absolute safety rule.
		s.Control = nil
		if g.Control != nil {
			v, ok, err := e.query.QueryAt(ctx, g.Control.SampleQuery, now)
			s.Control = &ControlState{Variant: g.Control.Variant, SampleQuery: g.Control.SampleQuery, MinimumSamples: g.Control.MinimumSamples, Ready: ok && err == nil && v >= g.Control.MinimumSamples}
			if ok && err == nil {
				s.Control.Samples = &v
			}
			if err != nil {
				s.Control.Error = err.Error()
			}
		}
		trigger := -1
		for j, r := range g.Rules {
			rs := &s.Rules[j]
			v, ok, err := e.query.QueryAt(ctx, r.Query, now)
			rs.Value = nil
			rs.Error = ""
			rs.ControlQuery = r.ControlQuery
			rs.ControlValue = nil
			rs.Diagnosis = ""
			if g.Control != nil {
				cv, cok, cerr := e.query.QueryAt(ctx, r.ControlQuery, now)
				if cok && cerr == nil {
					rs.ControlValue = &cv
				}
				rs.Diagnosis = "INSUFFICIENT_COMPARISON_DATA"
				if ok && err == nil && samplesReady && s.Control.Ready && cok && cerr == nil {
					treatmentBad := (r.Comparison == "gt" && v > r.Threshold) || (r.Comparison == "lt" && v < r.Threshold)
					controlBad := (r.Comparison == "gt" && cv > r.Threshold) || (r.Comparison == "lt" && cv < r.Threshold)
					switch {
					case treatmentBad && controlBad:
						rs.Diagnosis = "SHARED_DEGRADATION"
					case treatmentBad:
						rs.Diagnosis = "TREATMENT_ONLY_BREACH"
					case controlBad:
						rs.Diagnosis = "CONTROL_ONLY_BREACH"
					default:
						rs.Diagnosis = "BOTH_WITHIN_LIMITS"
					}
				}
			}
			if err != nil {
				rs.Error = err.Error()
				s.PrometheusOK = false
			}
			if !ok || err != nil {
				missing = true
				rs.Since = nil
				rs.Remaining = r.For.Seconds()
				continue
			}
			rs.Value = &v
			breach := (r.Comparison == "gt" && v > r.Threshold) || (r.Comparison == "lt" && v < r.Threshold)
			if !samplesReady || fe != nil || !fs.Enabled || e.latched[g.Flag] || (s.ResumeAt != nil && now.Before(*s.ResumeAt)) {
				rs.Since = nil
				rs.Remaining = r.For.Seconds()
				continue
			}
			if breach {
				anyBreach = true
				if rs.Since == nil {
					t := now
					rs.Since = &t
				}
				rs.Remaining = max(0, r.For.Seconds()-now.Sub(*rs.Since).Seconds())
				if rs.Remaining == 0 && trigger < 0 {
					trigger = j
				}
			} else {
				rs.Since = nil
				rs.Remaining = r.For.Seconds()
			}
		}
		eligible := fe == nil && fs.Enabled && !e.latched[g.Flag] && (s.ResumeAt == nil || !now.Before(*s.ResumeAt)) && s.PrometheusOK && samplesReady && !missing
		e.observe(g, s, eligible, anyBreach, gap)
		switch {
		case e.latched[g.Flag]:
			s.Status = "ROLLED_BACK"
			s.Message = "Automatic rollback is locked until you reset"
			if reason := e.unconfirmed[g.Flag]; reason != "" {
				s.Status = "ACTION_REQUIRED"
				s.Message = reason
			}
			if fe == nil && fs.Enabled {
				s.Status = "ACTION_REQUIRED"
				s.Message = "Flag was re-enabled outside FlagGuard; reset required to resume protection"
				if reason := e.unconfirmed[g.Flag]; reason != "" {
					s.Message = reason
				}
			}
		case fe != nil:
			s.Status = "ERROR"
			s.Message = fe.Error()
			clearTimers(&s)
		case !fs.Enabled:
			s.Status = "PAUSED"
			s.Message = "Flag is disabled in Flagsmith"
			clearTimers(&s)
		case s.ResumeAt != nil && now.Before(*s.ResumeAt):
			s.Status = "WARMING_UP"
			s.Message = "Waiting for pre-reset metric samples to leave the query window"
			clearTimers(&s)
		case !s.PrometheusOK:
			s.Status = "ERROR"
			if s.Message == "" {
				s.Message = "Prometheus query failed"
			}
			clearTimers(&s)
		case !samplesReady:
			s.Status = "WAITING"
			s.Message = "Waiting for enough variant traffic"
			clearTimers(&s)
		case missing:
			s.Status = "NO_DATA"
			s.Message = "A rule has no recent finite sample"
			clearTimers(&s)
		case trigger >= 0:
			if e.cfg.Mode == "shadow" {
				s.Status = "SHADOW_TRIGGERED"
				s.Message = "Would roll back: sustained breach confirmed; shadow mode sends no flag changes"
				if oldStatus != "SHADOW_TRIGGERED" {
					r := g.Rules[trigger]
					v := *s.Rules[trigger].Value
					frozen := cloneStates([]State{s})[0]
					e.addEvent(Event{Time: now, Kind: "WOULD_ROLLBACK", Flag: g.Flag, Variant: g.Variant, Message: s.Message, Metric: r.Metric, Value: &v, Threshold: &r.Threshold, Evidence: &Evidence{Observations: e.decisionObservations(g.Flag), Control: frozen.Control, Mode: "shadow", EvaluatedAt: now, Action: g.Action, ExperimentID: g.ExperimentID, PollSeconds: e.cfg.PollInterval.Seconds(), SampleQuery: g.SampleQuery, Samples: frozen.Samples, MinimumSamples: g.MinimumSamples, Rules: frozen.Rules}})
				}
				break
			}
			// Persist the lock BEFORE the write. A crash or ambiguous timeout cannot silently re-arm the flag.
			e.latched[g.Flag] = true
			e.unconfirmed[g.Flag] = "Rollback was interrupted or is unconfirmed. Inspect Flagsmith, then reset."
			if err := e.persist(); err != nil {
				delete(e.latched, g.Flag)
				delete(e.unconfirmed, g.Flag)
				s.Status = "ERROR"
				s.Message = "Cannot persist rollback lock; no change sent"
				break
			}
			change, err := e.flags.SetEnabled(ctx, g.Flag, false)
			frozen := cloneStates([]State{s})[0]
			evidence := &Evidence{Observations: e.decisionObservations(g.Flag), Control: frozen.Control, Mode: "enforce", EvaluatedAt: now, ActionFinishedAt: time.Now(), Action: g.Action, ExperimentID: g.ExperimentID, PollSeconds: e.cfg.PollInterval.Seconds(), SampleQuery: g.SampleQuery, Samples: frozen.Samples, MinimumSamples: g.MinimumSamples, Rules: frozen.Rules}
			r := g.Rules[trigger]
			v := *s.Rules[trigger].Value
			if err != nil {
				s.Status = "ACTION_REQUIRED"
				s.Message = "Rollback outcome unconfirmed: " + err.Error() + ". Inspect Flagsmith, then reset."
				e.unconfirmed[g.Flag] = s.Message
				e.addEvent(Event{Time: now, Kind: "ROLLBACK_UNCONFIRMED", Flag: g.Flag, Variant: g.Variant, Message: s.Message, Metric: r.Metric, Value: &v, Threshold: &r.Threshold, Evidence: evidence})
			} else {
				delete(e.unconfirmed, g.Flag)
				s.Status = "ROLLED_BACK"
				s.FlagEnabled = &change.After
				s.Message = fmt.Sprintf("%s breached for %s; flag disabled and verified", r.Metric, r.For)
				event := Event{Time: now, Kind: "ROLLBACK", Flag: g.Flag, Variant: g.Variant, Message: s.Message, Metric: r.Metric, Value: &v, Threshold: &r.Threshold, Change: &change, Evidence: evidence}
				e.addEvent(event)
				if e.notify != nil {
					go func() {
						nctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
						defer cancel()
						if err := e.notify.Send(nctx, event); err != nil {
							e.op.Lock()
							defer e.op.Unlock()
							e.addEvent(Event{Time: time.Now(), Kind: "WEBHOOK_ERROR", Flag: g.Flag, Message: err.Error()})
							_ = e.persist()
						}
					}()
				}
			}
			clearTimers(&s)
		case anyBreach:
			s.Status = "BREACHING"
			s.Message = "Threshold exceeded; confirming a sustained breach"
		default:
			s.Status = "HEALTHY"
			s.Message = "All guardrails are within their limits"
			s.ResumeAt = nil
			delete(e.resumeAt, g.Flag)
		}
		if p := g.RecoveryProbe; p != nil && e.cfg.Mode == "enforce" {
			previous := s.Recovery
			if gap {
				previous = nil
			}
			s.Recovery = nil
			if s.Status == "ROLLED_BACK" || s.Status == "ACTION_REQUIRED" || s.Status == "PAUSED" {
				// Start the window at a confirmed off read, never before the rollback write.
				if fe != nil || fs.Enabled {
					s.Recovery = pendingRecovery(*p, "Confirm the flag is off in Flagsmith before testing recovery.")
				} else {
					s.Recovery = e.checkRecovery(ctx, *p, previous, now)
				}
			}
		}
		if oldStatus != s.Status && s.Status != "ROLLED_BACK" && s.Status != "SHADOW_TRIGGERED" {
			e.addEvent(Event{Time: now, Kind: s.Status, Flag: g.Flag, Message: s.Message})
		}
		states[i] = s
	}
	e.mu.Lock()
	e.states = states
	e.lastPoll = now
	e.mu.Unlock()
	if err := e.persist(); err != nil {
		e.addEvent(Event{Time: now, Kind: "STORAGE_ERROR", Message: "Could not save audit history"})
	}
}
func (e *Engine) Reset(ctx context.Context, flag string) error {
	e.op.Lock()
	defer e.op.Unlock()
	if e.cfg.Mode == "shadow" {
		return fmt.Errorf("shadow mode cannot re-enable flags; switch to enforce mode deliberately")
	}
	index := -1
	for i, g := range e.cfg.Guardrails {
		if g.Flag == flag {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("unknown guardrail")
	}
	e.mu.RLock()
	old := e.states[index]
	e.mu.RUnlock()
	if !e.latched[flag] && old.Status != "PAUSED" {
		return fmt.Errorf("reset is only available for a disabled or rollback-locked flag")
	}
	var proof *RecoveryCheck
	if p := e.cfg.Guardrails[index].RecoveryProbe; p != nil {
		fs, err := e.flags.Find(ctx, flag)
		if err != nil || fs.Enabled {
			proof = pendingRecovery(*p, "Confirm the flag is off in Flagsmith before testing recovery.")
		} else {
			previous := old.Recovery
			now := time.Now()
			if now.Sub(e.lastPoll) > 3*e.cfg.PollInterval {
				previous = nil
			}
			proof = e.checkRecovery(ctx, *p, previous, now)
			// The transport may have consumed the remaining freshness margin.
			if proof.Status == "READY" && time.Since(proof.Recent[len(proof.Recent)-1].At) > p.MaxGap {
				proof.Status = "NO_DATA"
				proof.Detail = "The latest probe expired while checking recovery. Wait for a fresh scrape."
			}
		}
		e.mu.Lock()
		e.states[index].Recovery = proof
		e.mu.Unlock()
		if proof.Status != "READY" {
			e.addEvent(Event{Time: time.Now(), Kind: "RECOVERY_HELD", Flag: flag, Message: proof.Detail, Recovery: proof})
			if err := e.persist(); err != nil {
				return fmt.Errorf("recovery held: %s (could not save the attempted reset)", proof.Detail)
			}
			return fmt.Errorf("recovery held: %s", proof.Detail)
		}
	}
	// Persist an uncertainty lock before re-enabling, including PAUSED flags.
	// A timeout or a crash during multi-resource recovery must never re-arm silently.
	previousLock, previousReason := e.latched[flag], e.unconfirmed[flag]
	e.latched[flag] = true
	e.unconfirmed[flag] = "Recovery interrupted or unconfirmed. Inspect Flagsmith before retrying recovery."
	if err := e.persist(); err != nil {
		e.latched[flag] = previousLock
		e.unconfirmed[flag] = previousReason
		return fmt.Errorf("cannot persist recovery intent; no change sent")
	}
	change, err := e.flags.SetEnabled(ctx, flag, true)
	if err != nil {
		e.mu.Lock()
		e.states[index].Status = "ACTION_REQUIRED"
		e.states[index].Message = e.unconfirmed[flag]
		e.mu.Unlock()
		e.addEvent(Event{Time: time.Now(), Kind: "RECOVERY_UNCONFIRMED", Flag: flag, Message: e.unconfirmed[flag]})
		_ = e.persist()
		return fmt.Errorf("recovery outcome unconfirmed; inspect Flagsmith before retrying: %w", err)
	}
	delete(e.latched, flag)
	delete(e.unconfirmed, flag)
	resume := time.Now().Add(e.cfg.Warmup())
	e.resumeAt[flag] = resume
	e.addEvent(Event{Time: time.Now(), Kind: "RESET", Flag: flag, Recovery: proof, Message: fmt.Sprintf("Flag re-enabled and verified; %s metric warm-up", e.cfg.Warmup()), Change: &change})
	if err = e.persist(); err != nil {
		e.latched[flag] = true
		e.unconfirmed[flag] = "Flag enabled but recovery state could not be saved; inspect state storage."
		e.mu.Lock()
		e.states[index].Status = "ACTION_REQUIRED"
		e.states[index].Message = e.unconfirmed[flag]
		e.mu.Unlock()
		return fmt.Errorf("flag enabled but lock could not be cleared; fix state storage before retrying")
	}
	e.mu.Lock()
	s := &e.states[index]
	s.Recovery = nil
	clearTimers(s)
	s.Status = "WARMING_UP"
	s.Message = "Reset verified; waiting for fresh traffic"
	s.ResumeAt = &resume
	s.FlagEnabled = &change.After
	e.mu.Unlock()
	return nil
}

func (e *Engine) addEvent(v Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, v)
	slog.Info("controller event", "kind", v.Kind, "flag", v.Flag, "message", v.Message)
	if len(e.events) > 200 {
		e.events = e.events[len(e.events)-200:]
	}
}
func (e *Engine) persist() (result error) {
	defer func() { e.mu.Lock(); e.storageOK = result == nil; e.mu.Unlock() }()
	if e.cfg.StateFile == "" {
		return nil
	}
	e.mu.RLock()
	raw, err := json.Marshal(diskState{Latched: e.latched, Unconfirmed: e.unconfirmed, Events: e.events, ResumeAt: e.resumeAt})
	e.mu.RUnlock()
	if err != nil {
		return err
	}
	path := e.cfg.StateFile
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	// Sync the parent directory as well as the file so the renamed lock state
	// survives a host crash on supported local filesystems.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func cloneStates(s []State) []State {
	b, _ := json.Marshal(s)
	var out []State
	_ = json.Unmarshal(b, &out)
	return out
}
func (e *Engine) Snapshot() ([]State, []Event) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	// Return independent evidence too: callers cannot mutate persisted history.
	raw, _ := json.Marshal(e.events)
	events := []Event{}
	_ = json.Unmarshal(raw, &events)
	return cloneStates(e.states), events
}

func (e *Engine) PersistenceHealthy() bool { e.mu.RLock(); defer e.mu.RUnlock(); return e.storageOK }
