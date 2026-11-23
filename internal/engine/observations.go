package engine

import (
	"time"

	"flagguard/internal/config"
)

// Observation keeps the original timer input and metric values in policy order:
// optional sample gate, optional control gate, then each rule and its optional control.
type Observation struct {
	At     time.Time  `json:"at"`
	Values []*float64 `json:"values"`
}

const maxIncidentPolls = 256

// Save the last healthy poll and subsequent uninterrupted eligible breach.
// Overlong episodes use the existing historical export path.
func (e *Engine) observe(g config.Guardrail, s State, eligible, breaching, gap bool) {
	if !eligible || gap || !breaching {
		delete(e.observations, g.Flag)
	}
	if !eligible {
		return
	}
	window := e.observations[g.Flag]
	if len(window) > maxIncidentPolls {
		return
	}
	if len(window) == maxIncidentPolls {
		e.observations[g.Flag] = append(window, Observation{})
		return
	}
	p := Observation{At: s.UpdatedAt}
	if g.SampleQuery != "" {
		p.Values = append(p.Values, s.Samples)
	}
	if g.Control != nil {
		p.Values = append(p.Values, s.Control.Samples)
	}
	for _, r := range s.Rules {
		p.Values = append(p.Values, r.Value)
		if r.ControlQuery != "" {
			p.Values = append(p.Values, r.ControlValue)
		}
	}
	e.observations[g.Flag] = append(window, p)
}

func (e *Engine) decisionObservations(flag string) []Observation {
	window := e.observations[flag]
	if len(window) > maxIncidentPolls {
		return nil
	}
	// Once retained in an event, the slice must stay immutable.
	return append([]Observation(nil), window...)
}
