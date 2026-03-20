package api

import (
	"net/http"
	"strings"
)

// AuthMiddleware validates bearer tokens on incoming HTTP requests.
type AuthMiddleware struct {
	token string
}

// NewAuthMiddleware creates an AuthMiddleware with the given expected token.
// If token is empty, all requests are passed through without authentication.
func NewAuthMiddleware(token string) *AuthMiddleware {
	return &AuthMiddleware{token: token}
}

// Wrap returns an http.Handler that checks the Authorization header before
// calling next. Requests to /healthz and /metrics are exempt. If no token
// is configured (empty string), all requests pass through.
func (a *AuthMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.token == "" {
			next.ServeHTTP(w, r)
			return
		}

		if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		if strings.TrimPrefix(auth, "Bearer ") != a.token {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		next.ServeHTTP(w, r)
	})
}
