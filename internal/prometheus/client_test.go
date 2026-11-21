package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQueryResponses(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		ok, wantErr  bool
	}{{"finite", `[{"value":[%d,"0.125"]}]`, true, false}, {"empty", `[]`, false, false}, {"nan", `[{"value":[%d,"NaN"]}]`, false, false}, {"infinity", `[{"value":[%d,"+Inf"]}]`, false, false}, {"multiple", `[{"value":[%d,"1"]},{"value":[0,"2"]}]`, false, true}, {"malformed", `[{"value":[%d]}]`, false, true}, {"stale", `[{"value":[1,"1"]}]`, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				result := tc.result
				if tc.name != "empty" && tc.name != "stale" {
					result = fmt.Sprintf(result, time.Now().Unix())
				}
				fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":%s}}`, result)
			}))
			defer s.Close()
			_, ok, err := New(s.URL).Query(context.Background(), "sum(test)")
			if ok != tc.ok || (err != nil) != tc.wantErr {
				t.Fatalf("ok=%v error=%v", ok, err)
			}
		})
	}
}
