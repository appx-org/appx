package terminal

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// maxMessageSize is the maximum allowed size for a single WebSocket message
// (1 MB). Messages exceeding this limit cause the connection to be closed.
const maxMessageSize = 1024 * 1024

// maxResizeDimension is the upper bound for cols and rows in a resize message.
// Values exceeding this are clamped to this limit.
const maxResizeDimension = 500

// maxReplayBytes is the maximum amount of ring buffer content to send on
// WebSocket reconnect (64 KB). This caps replay bandwidth and prevents rapid
// reconnect loops from amplifying large buffer payloads.
const maxReplayBytes = 64 * 1024

// closeCodeSessionNotFound is a custom WebSocket close code indicating that the
// requested terminal session does not exist.
const closeCodeSessionNotFound = 4004

// terminalIdleTimeout is the maximum time a WebSocket terminal connection can
// remain idle (no input from client) before being closed by the server. The
// ring buffer preserves output so clients that reconnect receive a replay and
// experience no visible interruption. Declared as a variable (not a const) so
// tests can override it to avoid 30-minute waits.
var terminalIdleTimeout = 30 * time.Minute

// resizeMsg is the JSON structure expected in text WebSocket frames for resize
// control messages.
type resizeMsg struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// upgrader is the WebSocket upgrader shared by all handler invocations. It uses
// a custom CheckOrigin function that rejects empty origins and only accepts
// connections whose Origin host matches the request Host.
// upgrader is the WebSocket upgrader shared by all handler invocations. It
// rejects connections with empty or non-matching Origin headers to prevent
// cross-site WebSocket hijacking (CSWSH). The Origin host (scheme-agnostic)
// must match the request Host exactly.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return false
		}
		parsed, err := url.Parse(origin)
		if err != nil {
			return false
		}
		// Compare the Origin host (including port) against the request
		// Host. Both include the port when non-standard, so
		// "localhost:8443" == "localhost:8443" matches correctly.
		return parsed.Host == r.Host
	},
}

// HandleTerminalWS returns an http.HandlerFunc that upgrades an HTTP request to
// a WebSocket connection and bridges it to a terminal session managed by the
// given Manager. The session ID is extracted from the last segment of the URL
// path (e.g. /ws/term/{sessionId}).
//
// Auth is expected to be enforced by outer middleware — this handler does not
// check cookies.
//
// Protocol:
//   - On connect, the ring buffer contents are sent as an initial binary frame.
//   - Binary frames from the client are forwarded to the exec process stdin.
//   - Text frames from the client are parsed as JSON resize messages
//     ({"cols": N, "rows": N}). Invalid JSON is silently ignored. Dimensions
//     <= 0 are rejected; dimensions > 500 are clamped to 500.
//   - Output from the exec process is sent to the client as binary frames.
//   - When the client disconnects, the subscriber is removed but the session
//     remains alive for reconnect.
func HandleTerminalWS(tm *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract session ID from the URL path (last segment).
		path := strings.TrimSuffix(r.URL.Path, "/")
		idx := strings.LastIndex(path, "/")
		if idx == -1 {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		sessionID := path[idx+1:]

		// Look up the session.
		sess := tm.GetSession(sessionID)
		if sess == nil {
			// Upgrade first so we can send a proper close frame.
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				// Upgrade failed (e.g. bad origin) — Upgrade already sent HTTP error.
				return
			}
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(closeCodeSessionNotFound, "session not found"))
			conn.Close()
			return
		}

		// Upgrade the HTTP connection to WebSocket.
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// Upgrade already sent an HTTP error response.
			return
		}
		defer conn.Close()

		conn.SetReadLimit(maxMessageSize)

		// Subscribe to session output.
		ch := tm.Subscribe(sessionID)
		if ch == nil {
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(closeCodeSessionNotFound, "session not found"))
			return
		}
		defer tm.Unsubscribe(sessionID, ch)

		// Replay the ring buffer as the initial binary frame. Always sent (even
		// if empty) so the client knows the connection is established and can
		// distinguish "no previous output" from "still connecting".
		// Cap replay to prevent bandwidth amplification on rapid reconnects.
		replay := tm.ReplayBuffer(sessionID)
		if len(replay) > maxReplayBytes {
			replay = replay[len(replay)-maxReplayBytes:]
		}
		if len(replay) > 0 {
			if err := conn.WriteMessage(websocket.BinaryMessage, replay); err != nil {
				return
			}
		}

		// stopOutput signals the output pump goroutine to exit when the input
		// pump (and therefore the WebSocket connection) is done.
		stopOutput := make(chan struct{})

		// Output pump goroutine: reads from the subscriber channel and writes
		// binary frames to the WebSocket. Exits when stopOutput is closed or the
		// subscriber channel is closed (session teardown).
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case data, ok := <-ch:
					if !ok {
						// Channel closed — session was torn down.
						return
					}
					if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
						return
					}
				case <-stopOutput:
					return
				}
			}
		}()

		// Set the initial read deadline. If no message arrives within
		// terminalIdleTimeout, ReadMessage returns an error and the loop exits,
		// triggering deferred cleanup. The deadline is reset after every
		// successfully received message so active sessions are never cut off.
		conn.SetReadDeadline(time.Now().Add(terminalIdleTimeout))

		// Input pump (main goroutine): reads WebSocket frames and dispatches
		// based on frame type. Binary frames are forwarded to exec stdin; text
		// frames are parsed as JSON resize messages.
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				// Client disconnected, idle timeout exceeded, or other read error.
				break
			}
			// Reset the idle deadline after every received message.
			conn.SetReadDeadline(time.Now().Add(terminalIdleTimeout))

			switch msgType {
			case websocket.BinaryMessage:
				if err := tm.WriteInput(sessionID, data); err != nil {
					log.Printf("terminal: write input to session %s: %v", sessionID, err)
				}

			case websocket.TextMessage:
				var msg resizeMsg
				if err := json.Unmarshal(data, &msg); err != nil {
					// Malformed JSON — silently ignore.
					continue
				}
				if msg.Cols <= 0 || msg.Rows <= 0 {
					// Invalid dimensions — ignore.
					continue
				}
				cols := msg.Cols
				rows := msg.Rows
				if cols > maxResizeDimension {
					cols = maxResizeDimension
				}
				if rows > maxResizeDimension {
					rows = maxResizeDimension
				}
				if err := tm.Resize(sessionID, uint(cols), uint(rows)); err != nil {
					log.Printf("terminal: resize session %s: %v", sessionID, err)
				}
			}
		}

		// Signal the output pump to stop and wait for it to exit.
		close(stopOutput)
		<-done
	}
}
