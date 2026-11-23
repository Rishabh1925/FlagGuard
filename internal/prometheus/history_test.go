package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRangeSamples(t *testing.T) {
	for _, tc := range []struct {
		name, series, warning string
		want                  int
		invalid               bool
	}{
		{"gap and nonfinite", `[{"values":[[100,"0"],[110,"NaN"]]}]`, "", 1, false},
		{"label change", `[{"values":[[100,"3"]]},{"values":[[110,"4"]]}]`, "", 2, false},
		{"empty", `[]`, "", 0, false},
		{"overlap", `[{"values":[[100,"0"]]},{"values":[[100,"2"]]}]`, "", 0, true},
		{"off grid", `[{"values":[[101,"2"]]}]`, "", 0, true},
		{"partial results", `[]`, `,"warnings":["partial response"]`, 0, true},
		{"native histogram", `[{"histograms":[[100,{}]]}]`, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Unix(100, 0).UTC()
			end := start.Add(10 * time.Second)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p := r.URL.Query()
				if r.URL.Path != "/api/v1/query_range" || p.Get("start") != start.Format(time.RFC3339) || p.Get("end") != end.Format(time.RFC3339) || p.Get("step") != "5" || p.Get("query") != "my+query" {
					t.Error("unexpected range request")
				}
				fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":%s}%s}`, tc.series, tc.warning)
			}))
			defer srv.Close()
			vals, err := New(srv.URL).Range(context.Background(), "my+query", start, end, 5*time.Second)
			if (err != nil) != tc.invalid || len(vals) != tc.want {
				t.Fatalf("values %v, error %v", vals, err)
			}
		})
	}
}
func TestInstantUsesSpecifiedEvaluationTime(t *testing.T) {
	at := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("time") != at.Format(time.RFC3339Nano) {
			t.Error("evaluation clock changed")
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"value":[%d,"0"]}]}}`, at.Unix())
	}))
	defer s.Close()
	_, ok, err := New(s.URL).QueryAt(context.Background(), "checkout", at)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
}
