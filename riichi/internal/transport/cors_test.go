package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSDisabledWhenOriginEmpty(t *testing.T) {
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), "	")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("CORS headers should be absent, got %q", got)
	}
}

func TestCORSPreflight(t *testing.T) {
	called := false
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}), "*")

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/games", nil)
	req.Header.Set("Origin", "null")
	req.Header.Set("Access-Control-Request-Headers", "authorization")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called {
		t.Fatal("OPTIONS preflight should not reach the wrapped handler")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("allow-origin=%q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("allow-headers should be set for Authorization preflight")
	}
}

func TestCORSPassesThroughNormalRequest(t *testing.T) {
	called := false
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}), "http://127.0.0.1:8098")

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusOK {
		t.Fatalf("normal request should pass through, called=%v code=%d", called, rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:8098" {
		t.Fatalf("allow-origin=%q", got)
	}
}
