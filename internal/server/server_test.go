package server

import (
	"encoding/json"
	"flagguard/internal/config"
	"flagguard/internal/engine"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReportExcludesConnectionCredentials(t *testing.T) {
	c := config.Config{}
	c.Flagsmith.AdminToken = "private-admin-token"
	c.Flagsmith.EnvironmentKey = "private-environment-key"
	c.Notifier.WebhookURL = "https://example.test/private-webhook"
	e, err := engine.New(c, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := (&Server{Engine: e}).Handler()
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost/api/report", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("report unavailable")
	}
	var report map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{c.Flagsmith.AdminToken, c.Flagsmith.EnvironmentKey, c.Notifier.WebhookURL} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("report exposed connection credentials")
		}
	}
	if report["schema_version"] != float64(1) {
		t.Fatal("missing report version")
	}
}

func TestCrossOriginMutationsRejected(t *testing.T) {
	s := (&Server{}).Handler()
	for _, tc := range []struct{ origin, content string }{{"https://untrusted.example", "application/json"}, {"", "text/plain"}} {
		r := httptest.NewRequest(http.MethodPost, "http://localhost:8085/api/reset/checkout_flow", strings.NewReader("{}"))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", tc.content)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("unsafe request got %d", w.Code)
		}
	}
}
func TestMalformedChaosPayloadRejected(t *testing.T) {
	s := (&Server{}).Handler()
	r := httptest.NewRequest(http.MethodPost, "http://localhost/api/demo/chaos", strings.NewReader(`{"enabled":"yes"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("malformed payload got %d", w.Code)
	}
}

func TestDashboardAssetsAndPolicy(t *testing.T) {
	s := (&Server{}).Handler()
	for path, content := range map[string]string{"/": "text/html", "/workspace": "text/html", "/app.js": "text/javascript", "/app.css": "text/css", "/landing.css": "text/css", "/landing.js": "text/javascript", "/exhibit/switch.webp": "image/webp", "/exhibit/recorder.webp": "image/webp", "/exhibit/checkout.json": "application/json"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), content) || w.Body.Len() == 0 {
			t.Fatalf("asset unavailable: %s", path)
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self';") {
			t.Fatal("scripts must be same-origin")
		}
		if path == "/" && !strings.Contains(w.Body.String(), "id=\"proof-title\"") {
			t.Fatal("root must serve the landing page")
		}
		if path == "/workspace" && (!strings.Contains(w.Body.String(), `id="guards"`) || !strings.Contains(w.Body.String(), `id="chaos"`)) {
			t.Fatal("workspace must contain guardrails and demo controls")
		}
	}
}

func TestAuthenticationAndHostBoundary(t *testing.T) {
	e, _ := engine.New(config.Config{}, nil, nil, nil)
	token := strings.Repeat("a", 32)
	handler := (&Server{Engine: e, AuthToken: token, AllowedHosts: []string{"localhost", "guard.example"}}).Handler()
	for _, tc := range []struct {
		path, host, auth string
		want             int
	}{
		{"/", "localhost", "", 200}, {"/healthz", "localhost", "", 200},
		{"/exhibit/checkout.json", "localhost", "", 200}, {"/exhibit/other.json", "localhost", "", 401},
		{"/workspace", "localhost", "", 401}, {"/api/report", "localhost", "Bearer wrong", 401},
		{"/api/report", "localhost", "Bearer " + token, 200},
		{"/api/status", "rebind.example", "Bearer " + token, 403},
		{"/readyz", "guard.example", "Bearer " + token, 503},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+tc.path, nil)
		r.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s host %s: got %d want %d", tc.path, tc.host, w.Code, tc.want)
		}
	}
	r := httptest.NewRequest("GET", "http://localhost/workspace", nil)
	r.SetBasicAuth("operator", token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("browser authentication rejected")
	}
	r.SetBasicAuth("wrong-user", token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("unexpected operator accepted")
	}
}
