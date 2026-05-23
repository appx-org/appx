package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/neuromaxer/appx/internal/auth"
)

// handleChangePassword returns the handler for PUT /api/settings/password. It
// requires the current password for re-authentication, sets the new password,
// and invalidates all existing sessions (forcing re-login on all devices).
// Returns 400 if the new password is too short, 401 if the current password is
// wrong. This route is behind auth middleware.
func handleChangePassword(a *auth.Auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		ok, err := a.Store.CheckPassword(req.CurrentPassword)
		if err != nil || !ok {
			http.Error(w, "current password is incorrect", http.StatusUnauthorized)
			return
		}

		if err := a.Store.SetPassword(req.NewPassword); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		a.Store.DeleteAllSessions()
		log.Printf("settings: password changed, all sessions invalidated")

		// Create a fresh session for the current user so they stay logged in.
		token, err := a.Store.CreateSession()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		a.SetSessionCookie(w, token)
		writeJSON(w, map[string]string{"status": "ok"})
	}
}

// handleGetTerminalBufferSize returns the handler for GET /api/settings/terminal-buffer-size.
// It responds with {"value": N} where N is the buffer size in KB. Defaults to
// 512 if not set. This route is behind auth middleware.
func handleGetTerminalBufferSize(store *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		val, _ := store.GetSetting("terminal_buffer_size")
		size := 512 // default
		if val != "" {
			if n, err := strconv.Atoi(val); err == nil {
				size = n
			}
		}
		writeJSON(w, map[string]int{"value": size})
	}
}

// handleGetConfig returns the handler for GET /api/config. It exposes server
// runtime configuration that the frontend needs at startup. Auth required.
func handleGetConfig(baseDomain string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{
			"baseDomain": baseDomain,
		})
	}
}

// handleSetTerminalBufferSize returns the handler for PUT /api/settings/terminal-buffer-size.
// It expects a JSON body with {"value": N} where N is the buffer size in KB
// (min 64, max 4096). This route is behind auth middleware.
func handleSetTerminalBufferSize(store *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Value int `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.Value < 64 || req.Value > 4096 {
			http.Error(w, "value must be between 64 and 4096", http.StatusBadRequest)
			return
		}
		if err := store.SetSetting("terminal_buffer_size", strconv.Itoa(req.Value)); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	}
}
