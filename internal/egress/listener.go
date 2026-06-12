package egress

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

// InternalAddr is the default listen address for the agent-facing egress
// request endpoint. Bound to localhost only — no auth needed.
const InternalAddr = "127.0.0.1:9081"

// InternalPort is the internal listener's TCP port (see ProxyPort).
const InternalPort = "9081"

// ListenAndServeInternal starts the internal HTTP listener on the default
// loopback address. See ListenAndServeInternalAddr for the configurable form.
func ListenAndServeInternal(registry *PendingRegistry) error {
	return ListenAndServeInternalAddr(registry, InternalAddr)
}

// ListenAndServeInternalAddr starts the internal HTTP listener that accepts
// egress permission requests from the agent on the given address. In container
// mode the bind host is the docker bridge gateway so the in-container agent can
// reach it via host.docker.internal. Blocks until the listener is closed.
func ListenAndServeInternalAddr(registry *PendingRegistry, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("egress internal listener: %w", err)
	}
	log.Printf("Egress internal listener on %s", addr)
	srv := &http.Server{
		Handler:           newInternalHandler(registry),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.Serve(ln)
}

// newInternalHandler returns the HTTP handler for the internal listener.
// Only POST /egress/request is accepted.
func newInternalHandler(registry *PendingRegistry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /egress/request", handleEgressRequest(registry))
	return mux
}

// handleEgressRequest handles POST /egress/request from the agent. Creates a
// pending request and blocks until the user approves, denies, or the request
// times out (60s). Returns {"allowed": bool, "timeout": bool}.
func handleEgressRequest(registry *PendingRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Host   string `json:"host"`
			Port   int    `json:"port"`
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.Host == "" || body.Port == 0 {
			http.Error(w, "host and port are required", http.StatusBadRequest)
			return
		}

		log.Printf("egress: access requested for %s:%d (%s)", body.Host, body.Port, body.Reason)

		req, ch := registry.Add(body.Host, body.Port, body.Reason)
		_ = req

		// Block until resolved or timeout.
		timer := time.NewTimer(registry.timeout)
		defer timer.Stop()

		var allowed, timeout bool
		select {
		case result, ok := <-ch:
			if ok {
				allowed = result
			}
		case <-timer.C:
			timeout = true
			// Clean up the expired request.
			registry.Resolve(req.ID, false)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{
			"allowed": allowed,
			"timeout": timeout,
		})
	}
}
