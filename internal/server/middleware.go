package server

import (
	"fmt"
	"net/http"
	"strings"
)

// maxRequestBody is the maximum allowed request body size for API endpoints.
// Prevents memory exhaustion from oversized payloads. All current endpoints
// exchange small JSON objects, so 1 MB is generous.
const maxRequestBody = 1 << 20 // 1 MB

// securityHeaders wraps an HTTP handler to inject standard security headers on
// every response. A strict CSP is applied to all routes. When httpMode is true
// the Strict-Transport-Security header is omitted because the server is running
// over plain HTTP (dev mode) and browsers must not be instructed to upgrade.
//
// baseDomain (when non-empty) enables the dashboard to embed agent-built apps,
// which are served on `<project>.<baseDomain>` subdomains: it is added to the
// CSP frame-src so the project view can preview a running app in an iframe.
// Any port is allowed (`:*`) so the directive works in both dev (custom port)
// and production (implicit 443).
func securityHeaders(next http.Handler, httpMode bool, baseDomain string) http.Handler {
	frameSrc := "'self'"
	if baseDomain != "" {
		scheme := "https"
		if httpMode {
			scheme = "http"
		}
		frameSrc = fmt.Sprintf("'self' %s://*.%s:*", scheme, baseDomain)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !httpMode {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; "+
				"frame-src "+frameSrc+"; "+
				"connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

// limitBody wraps an HTTP handler to enforce a maximum request body size on all
// requests. Requests that exceed the limit receive a 413 error when the handler
// tries to read the body. Applied to the API mux in NewRouter.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		next.ServeHTTP(w, r)
	})
}

// requireJSON rejects state-changing requests (POST, PUT, DELETE) that don't
// have a Content-Type of application/json. This acts as a lightweight CSRF
// defense: HTML forms can only submit as application/x-www-form-urlencoded or
// multipart/form-data, so requiring JSON ensures the request came from
// JavaScript with explicit headers — which triggers CORS preflight on
// cross-origin requests. Combined with SameSite=Lax cookies and the Content-Type=application/json
// requirement (which triggers CORS preflight on cross-origin requests), CSRF attacks against
// state-changing endpoints are impractical. SameSite=Lax was chosen over Strict to
// allow the session cookie to be sent on subdomain navigation for agent-built app routing.
// Applied to the API mux in NewRouter.
func requireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			ct := r.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
