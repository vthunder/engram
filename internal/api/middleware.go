package api

import (
	"net/http"
)

// authMiddleware validates the X-API-Key header against the configured key.
// If apiKey is empty, auth is disabled (useful for local-only setups).
func authMiddleware(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if apiKey == "" {
				next.ServeHTTP(w, r)
				return
			}
			if r.Header.Get("X-API-Key") != apiKey {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing API key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
