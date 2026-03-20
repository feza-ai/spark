package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestAuth_ValidToken(t *testing.T) {
	m := NewAuthMiddleware("secret-token")
	handler := m.Wrap(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/pods", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestAuth_InvalidToken(t *testing.T) {
	m := NewAuthMiddleware("secret-token")
	handler := m.Wrap(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/pods", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
	body := rec.Body.String()
	if body != `{"error":"unauthorized"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestAuth_MissingHeader(t *testing.T) {
	m := NewAuthMiddleware("secret-token")
	handler := m.Wrap(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/pods", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
	body := rec.Body.String()
	if body != `{"error":"unauthorized"}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestAuth_HealthzExempt(t *testing.T) {
	m := NewAuthMiddleware("secret-token")
	handler := m.Wrap(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestAuth_MetricsExempt(t *testing.T) {
	m := NewAuthMiddleware("secret-token")
	handler := m.Wrap(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestAuth_Disabled(t *testing.T) {
	m := NewAuthMiddleware("")
	handler := m.Wrap(okHandler())

	tests := []struct {
		name string
		path string
	}{
		{"root", "/"},
		{"pods", "/pods"},
		{"healthz", "/healthz"},
		{"metrics", "/metrics"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
		})
	}
}
