package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/neuromaxer/appx/internal/auth"
	"github.com/neuromaxer/appx/internal/opencode"
)

// handleOpenCodeHealth returns the handler for GET /api/opencode/health. It
// calls the OpenCode health endpoint and returns {"healthy": true/false}.
// Used by the dashboard to show the OpenCode server status. Auth required.
func handleOpenCodeHealth(oc *opencode.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if oc == nil {
			writeJSON(w, map[string]bool{"healthy": false})
			return
		}
		healthy := oc.HealthCheck() == nil
		writeJSON(w, map[string]bool{"healthy": healthy})
	}
}

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

// handleGetAPIKeyStatus returns the handler for GET /api/settings/api-key. It
// responds with {"set": true} if an Anthropic API key is stored in the settings
// table, or {"set": false} otherwise. The actual key value is never exposed via
// this endpoint. This route is behind auth middleware.
func handleGetAPIKeyStatus(store *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		val, _ := store.GetSetting("anthropic_api_key")
		writeJSON(w, map[string]bool{"set": val != ""})
	}
}

// handleSetAPIKey returns the handler for PUT /api/settings/api-key. It stores
// the Anthropic API key in the settings table and, if an OpenCode client is
// available, injects the key into the running OpenCode server via SetAuth.
// Returns 400 if the key is empty. This route is behind auth middleware.
//
// Security note: the key is stored in plaintext in the SQLite database. This is
// acceptable for a self-hosted single-user deployment where database access
// implies full system access. Future work may add at-rest encryption.
func handleSetAPIKey(store *auth.Store, oc *opencode.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.Key == "" {
			http.Error(w, "key is required", http.StatusBadRequest)
			return
		}

		if err := store.SetSetting("anthropic_api_key", req.Key); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if oc != nil {
			if err := oc.SetAuth("anthropic", req.Key); err != nil {
				log.Printf("settings: failed to inject key into OpenCode: %v", err)
			} else if err := oc.DisposeAll(); err != nil {
				log.Printf("settings: failed to reload OpenCode instances: %v", err)
			}
		}

		log.Printf("settings: API key updated")
		writeJSON(w, map[string]string{"status": "ok"})
	}
}

// handleDeleteAPIKey returns the handler for DELETE /api/settings/api-key. It
// removes the Anthropic API key from the settings table. Returns 200 on success
// (idempotent). The oc parameter is accepted for interface consistency with
// handleSetAPIKey but is not used on delete. This route is behind auth middleware.
func handleDeleteAPIKey(store *auth.Store, oc *opencode.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.DeleteSetting("anthropic_api_key"); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		log.Printf("settings: API key deleted")
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
// runtime configuration that the frontend needs at startup — currently the
// baseDomain so the SPA can construct correct subdomain URLs regardless of
// deployment mode. Auth required.
func handleGetConfig(baseDomain string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"baseDomain": baseDomain})
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
