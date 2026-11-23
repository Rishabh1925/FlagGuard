package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeWindowUsesRawScrapesAndRejectsAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		valid        bool
	}{
		{"raw", `[{"values":[[82.25,"0"],[96.75,"1"]]}]`, true},
		{"two targets", `[{"values":[[82,"1"]]},{"values":[[96,"1"]]}]`, false},
		{"duplicate", `[{"values":[[82,"1"],[82,"1"]]}]`, false},
		{"future", `[{"values":[[102,"1"]]}]`, false},
		{"not binary", `[{"values":[[90,"NaN"]]}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/query" || r.URL.Query().Get("query") != "probe_success{}[20000ms]" {
					t.Error("expected one raw range vector")
				}
				fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":%s}}`, tc.result)
			}))
			defer srv.Close()
			samples, err := New(srv.URL).ProbeWindow(context.Background(), "probe_success{}", 20*time.Second, time.Unix(100, 0))
			if tc.valid {
				if err != nil || len(samples) != 2 || samples[0].Passed || !samples[1].Passed || samples[1].At.UnixMilli() != 96750 {
					t.Fatalf("samples=%v err=%v", samples, err)
				}
			} else if err == nil {
				t.Fatal("accepted ambiguous data")
			}
		})
	}
}
