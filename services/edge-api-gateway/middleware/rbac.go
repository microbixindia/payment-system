package middleware

import (
	"net/http"
)

// RequireScope creates a middleware restricting access unless the caller's JWT has the specified scope.
func RequireScope(requiredScope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawScopes := r.Context().Value(ScopesContextKey)
			if rawScopes == nil {
				http.Error(w, "Forbidden: Missing authorization context", http.StatusForbidden)
				return
			}

			scopes, ok := rawScopes.([]string)
			if !ok {
				http.Error(w, "Forbidden: Invalid authorization context", http.StatusForbidden)
				return
			}

			hasScope := false
			for _, s := range scopes {
				if s == requiredScope || s == "admin:all" {
					hasScope = true
					break
				}
			}

			if !hasScope {
				http.Error(w, "Forbidden: Insufficient privileges (scope '"+requiredScope+"' required)", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
