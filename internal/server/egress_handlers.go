package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/neuromaxer/appx/internal/egress"
)

// handleGetEgressLog returns the handler for GET /api/egress/log.
// Accepts query params "limit" (default 50, max 200) and "offset" (default 0).
// Returns {"entries": [...], "total": N}. Auth required.
func handleGetEgressLog(es *egress.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		offset := 0

		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		entries, total, err := es.ListLog(limit, offset)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]any{
			"entries": entries,
			"total":   total,
		})
	}
}

// handleGetAllowlist returns the handler for GET /api/egress/allowlist.
// Returns {"entries": ["host:port", ...]}. Auth required.
func handleGetAllowlist(es *egress.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"entries": es.GetAllowlist(),
		})
	}
}

// handleSetAllowlist returns the handler for PUT /api/egress/allowlist.
// Expects {"entries": ["host:port", ...]}. Rejects empty lists and invalid
// host:port pairs. Returns {"status": "ok"} on success. Auth required.
func handleSetAllowlist(es *egress.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Entries []string `json:"entries"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		if len(req.Entries) == 0 {
			http.Error(w, "allowlist must not be empty", http.StatusBadRequest)
			return
		}

		for _, entry := range req.Entries {
			host, portStr, err := net.SplitHostPort(entry)
			if err != nil || host == "" {
				http.Error(w, "invalid entry (must be host:port): "+entry, http.StatusBadRequest)
				return
			}
			if _, err := strconv.Atoi(portStr); err != nil {
				http.Error(w, "invalid port in entry: "+entry, http.StatusBadRequest)
				return
			}
			if host == "localhost" || strings.HasSuffix(host, ".localhost") {
				http.Error(w, "loopback addresses may not be added to the allowlist: "+entry, http.StatusBadRequest)
				return
			}
			if ip := net.ParseIP(host); ip != nil {
				if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
					http.Error(w, "internal addresses may not be added to the allowlist: "+entry, http.StatusBadRequest)
					return
				}
			}
		}

		if err := es.SetAllowlist(req.Entries); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]string{"status": "ok"})
	}
}

// handleGetEgressPending returns the handler for GET /api/egress/pending.
// Returns {"requests": [...]} — the list of pending egress permission requests
// from the agent. Auth required.
func handleGetEgressPending(ep *egress.PendingRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"requests": ep.List(),
		})
	}
}

// handleApproveEgressRequest returns the handler for POST /api/egress/pending/{id}/approve.
// Approves the request, adding the host:port to the persistent allowlist.
// Returns {"status": "ok"} on success. Auth required.
func handleApproveEgressRequest(ep *egress.PendingRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := ep.Resolve(id, true); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	}
}

// handleDenyEgressRequest returns the handler for POST /api/egress/pending/{id}/deny.
// Denies the request. The agent receives {"allowed": false}.
// Returns {"status": "ok"} on success. Auth required.
func handleDenyEgressRequest(ep *egress.PendingRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := ep.Resolve(id, false); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	}
}
