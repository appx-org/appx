package server

import (
	"encoding/json"
	"log"
	"net"
	"net/http"

	"github.com/neuromaxer/appx/internal/auth"
)

// clientIP returns the remote address of the request for logging purposes.
// Uses r.RemoteAddr directly (not X-Forwarded-For) to avoid log spoofing.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// handleLogin returns the handler for POST /api/login. It reads a JSON body
// with a "password" field, validates it against the stored bcrypt hash, and on
// success creates a session and sets the appx_session cookie. This route is
// public (not behind auth middleware) and rate-limited.
func handleLogin(a *auth.Auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		ok, err := a.Store.CheckPassword(req.Password)
		if err != nil || !ok {
			log.Printf("auth: login failed from %s", clientIP(r))
			http.Error(w, "invalid password", http.StatusUnauthorized)
			return
		}

		token, err := a.Store.CreateSession()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Printf("auth: login succeeded from %s", clientIP(r))
		a.SetSessionCookie(w, token)
		writeJSON(w, map[string]string{"status": "ok"})
	}
}

// handleLogout returns the handler for DELETE /api/session. It deletes the
// session from the database and clears the appx_session cookie. This route
// is behind auth middleware.
func handleLogout(a *auth.Auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("auth: logout from %s", clientIP(r))
		cookie, err := r.Cookie("appx_session")
		if err == nil {
			a.Store.DeleteSession(cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "appx_session",
			Value:    "",
			Path:     "/",
			Domain:   a.Cookie.Domain,
			HttpOnly: true,
			Secure:   a.Cookie.Secure,
			MaxAge:   -1,
		})
		writeJSON(w, map[string]string{"status": "ok"})
	}
}
