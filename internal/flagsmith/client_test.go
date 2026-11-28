package flagsmith

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDisableAndReadBack(t *testing.T) {
	enabled := true
	writes := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Api-Key secret" {
			t.Error("wrong authentication")
		}
		if r.Method == "PATCH" {
			writes++
			var body map[string]bool
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["enabled"] {
				t.Error("incorrect mutation payload")
			}
			enabled = false
			w.Write([]byte(`{}`))
			return
		}
		fmt.Fprintf(w, `[{"id":7,"enabled":%v,"feature":{"id":2,"name":"checkout_flow"}}]`, enabled)
	}))
	defer s.Close()
	c := New(s.URL, "secret", "env")
	change, err := c.SetEnabled(context.Background(), "checkout_flow", false)
	if err != nil {
		t.Fatal(err)
	}
	if !change.Before || change.After || writes != 1 {
		t.Fatal("incorrect change")
	}
	_, err = c.SetEnabled(context.Background(), "checkout_flow", false)
	if err != nil || writes != 1 {
		t.Fatal("idempotent disable wrote twice")
	}
}

func TestExperimentRollbackDisablesOverrideAndDefault(t *testing.T) {
	defaultEnabled, rolloutEnabled := true, true
	var writes []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/experiments/") {
			if r.Method == "PATCH" {
				writes = append(writes, "rollout")
				var p map[string]any
				json.NewDecoder(r.Body).Decode(&p)
				rolloutEnabled = p["enabled"].(bool)
				if p["rollout_percentage"] != float64(100) || p["feature_state_value"] == nil || p["multivariate_feature_state_values"] == nil {
					t.Error("rollout allocation was not preserved")
				}
				w.Write([]byte(`{}`))
				return
			}
			fmt.Fprintf(w, `{"feature":{"id":2},"status":"running","experiment_rollout":{"enabled":%v,"rollout_percentage":100,"feature_state_value":{"type":"string","value":"control"},"multivariate_feature_state_values":[{"multivariate_feature_option":9,"percentage_allocation":50}]}}`, rolloutEnabled)
			return
		}
		if r.Method == "PATCH" {
			writes = append(writes, "default")
			var p map[string]bool
			json.NewDecoder(r.Body).Decode(&p)
			defaultEnabled = p["enabled"]
			w.Write([]byte(`{}`))
			return
		}
		fmt.Fprintf(w, `[{"id":7,"enabled":%v,"feature":{"id":2,"name":"checkout_flow"}}]`, defaultEnabled)
	}))
	defer s.Close()
	c := New(s.URL, "secret", "env")
	c.Experiments = map[string]int{"checkout_flow": 1}
	change, err := c.SetEnabled(context.Background(), "checkout_flow", false)
	if err != nil {
		t.Fatal(err)
	}
	if rolloutEnabled || defaultEnabled || change.ExperimentID != 1 || len(writes) != 2 || writes[0] != "rollout" {
		t.Fatalf("rollback missed experiment override: %+v", writes)
	}
	if !change.DefaultBefore || change.DefaultAfter || change.RolloutBefore == nil || !*change.RolloutBefore || change.RolloutAfter == nil || *change.RolloutAfter {
		t.Fatalf("missing verified per-resource changes: %+v", change)
	}
	_, err = c.SetEnabled(context.Background(), "checkout_flow", true)
	if err != nil {
		t.Fatal(err)
	}
	if !rolloutEnabled || !defaultEnabled {
		t.Fatal("reset did not restore rollout")
	}
}
func TestRejectUnverifiedChange(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			w.Write([]byte(`{}`))
			return
		}
		w.Write([]byte(`[{"id":1,"enabled":true,"feature":{"name":"checkout_flow"}}]`))
	}))
	defer s.Close()
	if _, err := New(s.URL, "secret", "env").SetEnabled(context.Background(), "checkout_flow", false); err == nil {
		t.Fatal("accepted unapplied change")
	}
}
func TestPaginationNeverLeaksCredential(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[],"next":"https://example.com/steal"}`))
	}))
	defer s.Close()
	if _, err := New(s.URL, "secret", "env").Find(context.Background(), "checkout_flow"); err == nil {
		t.Fatal("accepted unsafe pagination")
	}
}
func TestEnvironmentStateExcludesOverrides(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":1,"enabled":true,"identity":4,"feature":{"name":"checkout_flow"}},{"id":2,"enabled":true,"feature_segment":7,"feature":{"name":"checkout_flow"}},{"id":3,"enabled":true,"identity":null,"feature_segment":null,"feature":{"name":"checkout_flow"}}]`))
	}))
	defer s.Close()
	v, err := New(s.URL, "secret", "env").Find(context.Background(), "checkout_flow")
	if err != nil || v.ID != 3 {
		t.Fatalf("wrong state %v: %v", v, err)
	}
}

func TestNumericFeatureReference(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("feature_name") != "checkout_flow" {
			t.Fatal("missing exact feature filter")
		}
		w.Write([]byte(`{"count":1,"next":null,"results":[{"id":1,"feature":8,"enabled":true,"identity":null,"feature_segment":null}]}`))
	}))
	defer s.Close()
	state, err := New(s.URL, "secret", "env").Find(context.Background(), "checkout_flow")
	if err != nil || state.Feature.ID != 8 {
		t.Fatalf("numeric feature: %+v %v", state, err)
	}
}

func TestReadBackRequiresExplicitSettingOnOriginalState(t *testing.T) {
	for name, response := range map[string]string{
		"missing setting":     `[{"id":1,"feature":7}]`,
		"null setting":        `[{"id":1,"feature":7,"enabled":null}]`,
		"replacement state":   `[{"id":2,"feature":7,"enabled":false}]`,
		"replacement feature": `[{"id":1,"feature":8,"enabled":false}]`,
	} {
		t.Run(name, func(t *testing.T) {
			wrote := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPatch:
					wrote = true
					fmt.Fprint(w, "{}")
				case wrote:
					fmt.Fprint(w, response)
				default:
					fmt.Fprint(w, `[{"id":1,"feature":7,"enabled":true}]`)
				}
			}))
			defer server.Close()
			_, err := New(server.URL, "token", "env").SetEnabled(context.Background(), "checkout_flow", false)
			if !wrote || err == nil {
				t.Fatalf("expected an unverified read-back after the write; wrote=%v err=%v", wrote, err)
			}
		})
	}
}
