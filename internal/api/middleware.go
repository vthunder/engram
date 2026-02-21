package api

import (
	"net/http"
	"strings"
)

// authMiddleware validates the Authorization: Bearer header against the configured key.
// If apiKey is empty, auth is disabled (useful for local-only setups).
func authMiddleware(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if apiKey == "" {
				next.ServeHTTP(w, r)
				return
			}
			token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token != apiKey {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing API key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
