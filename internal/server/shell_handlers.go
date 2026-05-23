package server

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/neuromaxer/appx/internal/terminal"
)

// shellUpgrader is the gorilla WebSocket upgrader for shell connections.
// CheckOrigin is handled by the auth middleware upstream.
var shellUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// shellCreateRequest is the body for POST /api/shell.
type shellCreateRequest struct {
	// Cwd is the working directory for the shell. Defaults to the server's
	// working directory if empty.
	Cwd string `json:"cwd"`
}

// shellCreateResponse is the body returned by POST /api/shell.
type shellCreateResponse struct {
	ID string `json:"id"`
}

// shellResizeRequest is the body for PUT /api/shell/{id}.
type shellResizeRequest struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// handleShellCreate handles POST /api/shell. It spawns a new local PTY shell
// session and returns its ID. The optional cwd field controls the working
// directory; if absent the server process's working directory is used.
// Requires authentication.
func handleShellCreate(lm *terminal.LocalManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req shellCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		sess, err := lm.Create(req.Cwd)
		if err != nil {
			log.Printf("shell create: %v", err)
			http.Error(w, "failed to create shell", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, shellCreateResponse{ID: sess.ID})
	}
}

// handleShellResize handles PUT /api/shell/{id}. It resizes the PTY of the
// given session via SIGWINCH. Called by the browser on terminal resize.
// Requires authentication.
func handleShellResize(lm *terminal.LocalManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req shellResizeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Cols == 0 || req.Rows == 0 {
			http.Error(w, "cols and rows must be > 0", http.StatusBadRequest)
			return
		}
		if err := lm.Resize(id, req.Cols, req.Rows); err != nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleShellConnect handles GET /api/shell/{id}/connect. It upgrades to a
// WebSocket and proxies raw terminal I/O between the browser and the PTY.
//
// Protocol:
//   - Text frames from client → stdin of the shell
//   - Binary frames from server → stdout/stderr of the shell
//
// Requires authentication.
func handleShellConnect(lm *terminal.LocalManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if lm.GetSession(id) == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

		// Disable write deadline — WebSocket connections are long-lived.
		http.NewResponseController(w).SetWriteDeadline(time.Time{})

		conn, err := shellUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("shell ws upgrade: %v", err)
			return
		}
		defer conn.Close()

		// Replay buffered output so reconnecting clients see history.
		if replay := lm.ReplayBuffer(id); len(replay) > 0 {
			conn.WriteMessage(websocket.BinaryMessage, replay)
		}

		ch := lm.Subscribe(id)
		if ch == nil {
			return
		}
		defer lm.Unsubscribe(id, ch)

		done := lm.Done(id)

		// output pump: subscriber channel → WebSocket binary frames
		go func() {
			for {
				select {
				case chunk, ok := <-ch:
					if !ok {
						conn.Close()
						return
					}
					if err := conn.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
						return
					}
				case <-done:
					conn.Close()
					return
				}
			}
		}()

		// input pump: WebSocket text frames → PTY stdin
		conn.SetReadLimit(1 << 20)
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// Accept both text and binary input frames.
			if msgType == websocket.TextMessage || msgType == websocket.BinaryMessage {
				if err := lm.Write(id, data); err != nil {
					return
				}
			}
		}
	}
}
