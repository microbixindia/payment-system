package middleware

import (
	"net/http"
	"path"
	"strings"
)

var forbiddenFileExtensions = []string{
	".env", ".git", ".sql", ".json", ".yaml", ".yml", ".sh", ".lock",
}

// ZeroTrustSecurityMiddleware applies defensive HTTP security headers,
// blocks path traversal attempts, and prevents sensitive file inspection.
func ZeroTrustSecurityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Enforce Enterprise HTTP Security Headers
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("X-XSS-Protection", "1; mode=block")

		// 2. Zero-Trust Path Traversal Defense & Normalization
		rawURL := r.URL.Path
		cleaned := strings.ToLower(strings.TrimSpace(rawURL))

		// Check for directory traversal sequences or null bytes
		if strings.Contains(cleaned, "..") ||
			strings.Contains(cleaned, "/.") ||
			strings.Contains(cleaned, "\\") ||
			strings.Contains(cleaned, "%00") {
			http.Error(w, "Forbidden: Security policy violation", http.StatusForbidden)
			return
		}

		normalized := path.Clean(cleaned)
		if strings.Contains(normalized, "..") {
			http.Error(w, "Forbidden: Path traversal detected", http.StatusForbidden)
			return
		}

		// Check forbidden extensions
		for _, ext := range forbiddenFileExtensions {
			if strings.HasSuffix(normalized, ext) {
				http.Error(w, "Forbidden: Resource extension restricted", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}
