package middleware

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

// APIKey guards the data endpoints. An empty key disables the check, which is
// convenient in development and dangerous anywhere else, so it says so loudly
// once at startup rather than silently letting the world in.
func APIKey(key string) func(http.Handler) http.Handler {
	if key == "" {
		slog.Warn("API_KEY is not set: the API is unauthenticated")
		return func(next http.Handler) http.Handler { return next }
	}

	want := []byte(key)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := presentedKey(r)

			// Constant-time compare: a byte-by-byte comparison leaks the key
			// one character at a time to anyone willing to time the responses.
			if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func presentedKey(r *http.Request) string {
	if k := r.Header.Get("X-API-Key"); k != "" {
		return k
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		if token, ok := strings.CutPrefix(auth, "Bearer "); ok {
			return token
		}
	}
	return ""
}
