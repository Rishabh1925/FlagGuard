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
	"strings"
	"time"
)

type Client struct {
	URL  string
	HTTP *http.Client
}

func New(raw string) *Client {
	return &Client{strings.TrimRight(raw, "/"), &http.Client{Timeout: 8 * time.Second}}
}
func (c *Client) Query(ctx context.Context, q string) (float64, bool, error) {
	return c.QueryAt(ctx, q, time.Now())
}

func (c *Client) QueryAt(ctx context.Context, q string, at time.Time) (float64, bool, error) {
	params := url.Values{"query": {q}, "time": {at.UTC().Format(time.RFC3339Nano)}}
	req, e := http.NewRequestWithContext(ctx, "GET", c.URL+"/api/v1/query?"+params.Encode(), nil)
	if e != nil {
		return 0, false, e
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return 0, false, fmt.Errorf("Prometheus unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, false, fmt.Errorf("Prometheus HTTP %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body); e != nil {
		return 0, false, fmt.Errorf("invalid Prometheus response: %w", e)
	}
	if body.Status != "success" || body.Data.ResultType != "vector" {
		return 0, false, fmt.Errorf("PromQL must return a successful instant vector")
	}
	if len(body.Data.Result) == 0 {
		return 0, false, nil
	}
	if len(body.Data.Result) != 1 {
		return 0, false, fmt.Errorf("PromQL returned multiple series; aggregate to one value")
	}
	pair := body.Data.Result[0].Value
	if len(pair) != 2 {
		return 0, false, fmt.Errorf("invalid Prometheus sample")
	}
	var timestamp float64
	var s string
	if json.Unmarshal(pair[0], &timestamp) != nil || json.Unmarshal(pair[1], &s) != nil {
		return 0, false, fmt.Errorf("invalid Prometheus sample")
	}
	age := float64(time.Now().Unix()) - timestamp
	if age > 30 || age < -30 {
		return 0, false, nil
	}
	v, e := strconv.ParseFloat(s, 64)
	if e != nil {
		return 0, false, fmt.Errorf("invalid numeric sample")
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false, nil
	}
	return v, true, nil
}
