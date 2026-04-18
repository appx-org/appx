package auth

import (
	"net/http"
)

// CookieConfig controls the attributes of the appx_session cookie.
type CookieConfig struct {
	Domain string // e.g. ".localhost" or ".user.appx.app" (leading dot)
	Secure bool   // false in --http mode, true otherwise
}

// Auth provides HTTP authentication middleware and cookie management.
// It wraps a Store to validate session tokens from incoming requests.
type Auth struct {
	Store  *Store
	Cookie CookieConfig
}

// New creates an Auth instance backed by the given session/password store.
// Cookie.Secure defaults to true; callers may override it for HTTP dev mode.
func New(store *Store) *Auth {
	return &Auth{
		Store: store,
		Cookie: CookieConfig{
			Secure: true,
		},
	}
}

// AuthRequiredHeader is set on 401 responses from the auth middleware so that
// API clients can distinguish an appx session expiry from an OpenCode backend
// error. The frontend redirects to /login when it sees this header on a 401.
const AuthRequiredHeader = "X-Appx-Auth"

// Middleware returns an HTTP middleware that enforces authentication.
// It reads the "appx_session" cookie, validates it against the sessions table,
// and returns 401 Unauthorized (with X-Appx-Auth: required) if the session is
// missing or invalid. Public routes (e.g. POST /api/login) must be registered
// outside this middleware.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("appx_session")
		if err != nil {
			w.Header().Set(AuthRequiredHeader, "required")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		if !a.Store.ValidSession(cookie.Value) {
			w.Header().Set(AuthRequiredHeader, "required")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// SetSessionCookie writes an HttpOnly "appx_session" cookie to the response.
// Called after successful login to establish the user's session. The Domain
// and Secure attributes are taken from Cookie (configured at startup) so that
// the cookie is shared across subdomains (e.g. project.localhost) and omits the
// Secure flag in HTTP dev mode. SameSite=Lax allows the cookie to be sent on
// top-level navigations from subdomain to dashboard while still blocking CSRF.
func (a *Auth) SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "appx_session",
		Value:    token,
		Path:     "/",
		Domain:   a.Cookie.Domain,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.Cookie.Secure,
		MaxAge:   int(sessionDuration.Seconds()),
	})
}
