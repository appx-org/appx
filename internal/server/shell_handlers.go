package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/neuromaxer/appx/internal/project"
	"github.com/neuromaxer/appx/internal/terminal"
)

// shellUpgrader is the gorilla WebSocket upgrader for shell connections.
// CheckOrigin is handled by the auth middleware upstream.
var shellUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// shellCreateResponse is the body returned by the shell-create endpoints.
type shellCreateResponse struct {
	ID string `json:"id"`
}

// shellResizeRequest is the body for PUT /api/shell/{id}.
type shellResizeRequest struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// handleShellCreate handles POST /api/shell. It spawns a local PTY running the
// appx user's login shell in that user's home directory and returns the session
// ID. This is the server terminal; project terminals use the project-scoped
// route below. The body is ignored — the working directory is deliberately not
// caller-controlled, so an authenticated request cannot open a shell at an
// arbitrary host path. Requires authentication.
func handleShellCreate(lm *terminal.LocalManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := lm.Create("")
		if err != nil {
			log.Printf("shell create: %v", err)
			http.Error(w, "failed to create shell", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, shellCreateResponse{ID: sess.ID})
	}
}

// ProjectShellConfig tells the project-shell handler how to reach a project's
// files. In container mode the agent owns the workspace inside the outer
// container, so a project terminal must exec into it; in a co-located
// deployment the files are on the host and a plain PTY works.
type ProjectShellConfig struct {
	// Container, when non-nil, selects container mode: open the shell with
	// `docker exec -w <workspace>/<name>`.
	Container *terminal.ContainerShell
}

// handleProjectShellCreate handles POST /api/projects/{id}/shell. It opens a
// shell rooted in the given project's directory and returns the session ID;
// resize and I/O then go through the shared /api/shell/{id} routes.
//
// The project is addressed by ID and the directory is derived server-side, so
// unlike the old flow (which took a caller-supplied cwd) the caller cannot
// choose the path. Returns 404 for an unknown project, 501 when neither a
// container nor a host project directory is available, and 502 when the
// container exec cannot be started. Requires authentication.
func handleProjectShellCreate(pm *project.Manager, lm *terminal.LocalManager, cfg ProjectShellConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		proj, err := pm.Store.Get(r.PathValue("id"))
		if err != nil {
			if errors.Is(err, project.ErrNotFound) {
				http.Error(w, "project not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		var sess *terminal.LocalSession
		if cfg.Container != nil {
			spec, serr := cfg.Container.CommandFor(proj.Name)
			if serr != nil {
				log.Printf("project shell %q: %v", proj.Name, serr)
				http.Error(w, "project terminal unavailable", http.StatusNotImplemented)
				return
			}
			sess, err = lm.CreateCommand(spec)
		} else {
			dir := pm.ProjectDir(proj.Name)
			if dir == "" {
				http.Error(w, "project terminal unavailable: no container configured and no host project directory",
					http.StatusNotImplemented)
				return
			}
			sess, err = lm.Create(dir)
		}
		if err != nil {
			// A failed container exec (container stopped, shell missing) is an
			// upstream problem, not a bad request.
			log.Printf("project shell %q: %v", proj.Name, err)
			http.Error(w, "failed to open project shell", http.StatusBadGateway)
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
