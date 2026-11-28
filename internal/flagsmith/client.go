package flagsmith

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	URL, Token, Environment string
	HTTP                    *http.Client
	Experiments             map[string]int
}
type FeatureRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (f *FeatureRef) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '{' {
		type plain FeatureRef
		return json.Unmarshal(raw, (*plain)(f))
	}
	return json.Unmarshal(raw, &f.ID)
}

type FeatureState struct {
	ID             int             `json:"id"`
	Enabled        bool            `json:"enabled"`
	Feature        FeatureRef      `json:"feature"`
	Identity       json.RawMessage `json:"identity"`
	Segment        json.RawMessage `json:"feature_segment"`
	DefaultEnabled bool            `json:"-"`
	Rollout        map[string]any  `json:"-"`
}

// A missing or null enabled field is not evidence that the flag is off.
func (s *FeatureState) UnmarshalJSON(data []byte) error {
	type plain FeatureState
	var wire struct {
		plain
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Enabled == nil {
		return fmt.Errorf("feature state is missing its enabled setting")
	}
	*s = FeatureState(wire.plain)
	s.Enabled = *wire.Enabled
	return nil
}

type Change struct {
	ID            int   `json:"feature_state_id"`
	Before        bool  `json:"before_enabled"`
	After         bool  `json:"after_enabled"`
	ExperimentID  int   `json:"experiment_id,omitempty"`
	DefaultBefore bool  `json:"default_before_enabled"`
	DefaultAfter  bool  `json:"default_after_enabled"`
	RolloutBefore *bool `json:"rollout_before_enabled,omitempty"`
	RolloutAfter  *bool `json:"rollout_after_enabled,omitempty"`
}

func New(raw, token, env string) *Client {
	return &Client{URL: strings.TrimRight(raw, "/"), Token: token, Environment: env, HTTP: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) request(ctx context.Context, method, path string, payload any) ([]byte, error) {
	if c.Token == "" || c.Environment == "" {
		return nil, fmt.Errorf("setup required: set FLAGSMITH_ADMIN_TOKEN and FLAGSMITH_ENV_KEY")
	}
	var buf bytes.Buffer
	if payload != nil {
		if e := json.NewEncoder(&buf).Encode(payload); e != nil {
			return nil, e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, c.URL+path, &buf)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Api-Key "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return nil, fmt.Errorf("Flagsmith unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Flagsmith HTTP %d (check token permissions and environment)", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}
func (c *Client) Find(ctx context.Context, flag string) (FeatureState, error) {
	path := "/environments/" + url.PathEscape(c.Environment) + "/featurestates/?page_size=100&feature_name=" + url.QueryEscape(flag)
	var found *FeatureState
	for page := 0; page < 100; page++ {
		raw, e := c.request(ctx, "GET", path, nil)
		if e != nil {
			return FeatureState{}, e
		}
		var states []FeatureState
		next := ""
		if e = json.Unmarshal(raw, &states); e != nil {
			var p struct {
				Results []FeatureState `json:"results"`
				Next    *string        `json:"next"`
			}
			if e = json.Unmarshal(raw, &p); e != nil {
				return FeatureState{}, fmt.Errorf("invalid feature states response")
			}
			states = p.Results
			if p.Next != nil {
				next = *p.Next
			}
		}
		for _, s := range states {
			// Numeric feature IDs are resolved by the server's exact feature_name filter.
			if s.Feature.Name == "" && s.Feature.ID > 0 {
				s.Feature.Name = flag
			}
			if s.Feature.Name == flag && (len(s.Identity) == 0 || string(s.Identity) == "null") && (len(s.Segment) == 0 || string(s.Segment) == "null") {
				if found != nil {
					return FeatureState{}, fmt.Errorf("multiple environment states match %s", flag)
				}
				copy := s
				found = &copy
			}
		}
		if next == "" {
			if found == nil {
				return FeatureState{}, fmt.Errorf("flag %q not found in environment", flag)
			}
			found.DefaultEnabled = found.Enabled
			if id := c.Experiments[flag]; id > 0 {
				raw, err := c.request(ctx, "GET", fmt.Sprintf("/environments/%s/experiments/%d/", url.PathEscape(c.Environment), id), nil)
				if err != nil {
					return FeatureState{}, err
				}
				var exp struct {
					Feature struct {
						ID int `json:"id"`
					} `json:"feature"`
					Status  string         `json:"status"`
					Rollout map[string]any `json:"experiment_rollout"`
				}
				if json.Unmarshal(raw, &exp) != nil || exp.Feature.ID != found.Feature.ID || exp.Rollout == nil {
					return FeatureState{}, fmt.Errorf("experiment does not match flag or has no rollout")
				}
				if exp.Status != "running" && exp.Status != "paused" {
					return FeatureState{}, fmt.Errorf("experiment is not running or paused")
				}
				rolloutEnabled, ok := exp.Rollout["enabled"].(bool)
				if !ok {
					return FeatureState{}, fmt.Errorf("invalid experiment rollout")
				}
				found.Rollout = exp.Rollout
				found.Enabled = found.Enabled || rolloutEnabled
			}
			return *found, nil
		}
		// Never send an admin credential to a pagination URL supplied by another host.
		base, _ := url.Parse(c.URL + path)
		u, e := url.Parse(next)
		if e != nil {
			return FeatureState{}, fmt.Errorf("invalid pagination URL")
		}
		u = base.ResolveReference(u)
		root, _ := url.Parse(c.URL)
		if u.Scheme != root.Scheme || u.Host != root.Host || !strings.HasPrefix(u.Path, root.Path+"/environments/") {
			return FeatureState{}, fmt.Errorf("unsafe pagination URL")
		}
		path = strings.TrimPrefix(u.Path, root.Path) + "?" + u.RawQuery
	}
	return FeatureState{}, fmt.Errorf("too many feature-state pages")
}
func (c *Client) SetEnabled(ctx context.Context, flag string, enabled bool) (Change, error) {
	before, e := c.Find(ctx, flag)
	if e != nil {
		return Change{}, e
	}
	change := Change{ID: before.ID, Before: before.Enabled, After: enabled, ExperimentID: c.Experiments[flag], DefaultBefore: before.DefaultEnabled}
	// An experiment has a segment override. Disable it as well as the default state.
	if before.Rollout != nil {
		v := before.Rollout["enabled"].(bool)
		change.RolloutBefore = &v
		payload := map[string]any{"enabled": enabled, "rollout_percentage": before.Rollout["rollout_percentage"], "feature_state_value": before.Rollout["feature_state_value"], "multivariate_feature_state_values": before.Rollout["multivariate_feature_state_values"]}
		if before.Rollout["enabled"] != enabled {
			if _, e = c.request(ctx, "PATCH", fmt.Sprintf("/environments/%s/experiments/%d/rollout/", url.PathEscape(c.Environment), change.ExperimentID), payload); e != nil {
				return change, e
			}
		}
	}
	if before.DefaultEnabled != enabled {
		_, e = c.request(ctx, "PATCH", fmt.Sprintf("/environments/%s/featurestates/%d/", url.PathEscape(c.Environment), before.ID), map[string]bool{"enabled": enabled})
		if e != nil {
			return change, e
		}
	}
	after, e := c.Find(ctx, flag)
	if e != nil {
		return change, fmt.Errorf("read-back verification: %w", e)
	}
	if after.ID != before.ID || after.Feature.ID != before.Feature.ID {
		return change, fmt.Errorf("read-back resolved a different feature state")
	}
	if after.DefaultEnabled != enabled || (after.Rollout != nil && after.Rollout["enabled"] != enabled) {
		return change, fmt.Errorf("Flagsmith did not apply the change; check change approvals")
	}
	change.DefaultAfter = after.DefaultEnabled
	if after.Rollout != nil {
		v := after.Rollout["enabled"].(bool)
		change.RolloutAfter = &v
	}
	return change, nil
}
