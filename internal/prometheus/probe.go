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

type ProbeReader interface {
	ProbeWindow(context.Context, string, time.Duration, time.Time) ([]ProbeSample, error)
}

type ProbeSample struct {
	At     time.Time `json:"at"`
	Passed bool      `json:"passed"`
}

// ProbeWindow reads actual scrape timestamps. A stepped range query can repeat
// a single scrape at several evaluation times during Prometheus lookback.
func (c *Client) ProbeWindow(ctx context.Context, selector string, window time.Duration, at time.Time) ([]ProbeSample, error) {
	params := url.Values{"query": {fmt.Sprintf("%s[%dms]", selector, window.Milliseconds())}, "time": {at.UTC().Format(time.RFC3339Nano)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/v1/query?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach Prometheus")
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body); err != nil || body.Status != "success" || body.Data.ResultType != "matrix" || len(body.Warnings) > 0 {
		return nil, fmt.Errorf("incomplete probe response")
	}
	if len(body.Data.Result) == 0 {
		return nil, nil
	}
	if len(body.Data.Result) != 1 {
		return nil, fmt.Errorf("probe selector must identify one series")
	}
	series := body.Data.Result[0]
	if len(series.Histograms) > 0 {
		return nil, fmt.Errorf("expected binary probe samples")
	}
	samples := make([]ProbeSample, 0, len(series.Values))
	for _, pair := range series.Values {
		var stamp json.Number
		var raw string
		if len(pair) != 2 || json.Unmarshal(pair[0], &stamp) != nil || json.Unmarshal(pair[1], &raw) != nil {
			return nil, fmt.Errorf("invalid probe sample")
		}
		sec, err := stamp.Float64()
		if err != nil {
			return nil, fmt.Errorf("invalid probe timestamp")
		}
		t := time.UnixMilli(int64(math.Round(sec * 1000))).UTC()
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || (v != 0 && v != 1) || t.After(at) || t.Before(at.Add(-window).Truncate(time.Millisecond)) || (len(samples) > 0 && !samples[len(samples)-1].At.Before(t)) {
			return nil, fmt.Errorf("invalid binary sample or scrape timestamp")
		}
		samples = append(samples, ProbeSample{At: t, Passed: v == 1})
	}
	return samples, nil
}
