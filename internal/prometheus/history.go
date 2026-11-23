package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Range returns finite samples keyed by evaluation time in milliseconds.
// A change of labels between steps is fine, but two values at a step are ambiguous.
func (c *Client) Range(ctx context.Context, query string, start, end time.Time, step time.Duration) (map[int64]float64, error) {
	if step < time.Millisecond || step%time.Millisecond != 0 {
		return nil, fmt.Errorf("expected a positive millisecond step")
	}
	params := url.Values{
		"query": {query}, "start": {start.Format(time.RFC3339Nano)}, "end": {end.Format(time.RFC3339Nano)},
		"step": {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/v1/query_range?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Prometheus range request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prometheus HTTP %d", resp.StatusCode)
	}
	var body struct {
		Status   string
		Warnings []string
		Data     struct {
			ResultType string
			Result     []struct {
				Values     [][]json.RawMessage
				Histograms []json.RawMessage
			}
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("invalid range response: %w", err)
	}
	if body.Status != "success" || body.Data.ResultType != "matrix" || body.Data.Result == nil {
		return nil, fmt.Errorf("expected a successful Prometheus range matrix")
	}
	if len(body.Warnings) > 0 {
		return nil, fmt.Errorf("Prometheus returned a range warning: %s", body.Warnings[0])
	}
	out := make(map[int64]float64)
	seen := make(map[int64]bool)
	for _, series := range body.Data.Result {
		if len(series.Histograms) > 0 {
			return nil, fmt.Errorf("expected a numeric query; reduce native histograms to one value")
		}
		for _, pair := range series.Values {
			var seconds float64
			var raw string
			if len(pair) != 2 || json.Unmarshal(pair[0], &seconds) != nil || json.Unmarshal(pair[1], &raw) != nil {
				return nil, fmt.Errorf("invalid range sample")
			}
			ms := int64(math.Round(seconds * 1000))
			if ms < start.UnixMilli() || ms > end.UnixMilli() || (ms-start.UnixMilli())%step.Milliseconds() != 0 {
				return nil, fmt.Errorf("off-grid sample at %d", ms)
			}
			if seen[ms] {
				return nil, fmt.Errorf("multiple series at %s; aggregate to one value", time.UnixMilli(ms).UTC())
			}
			seen[ms] = true
			v, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid range value")
			}
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				out[ms] = v
			}
		}
	}
	return out, nil
}
