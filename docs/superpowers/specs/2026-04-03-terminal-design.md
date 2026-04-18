# Phase 3 Design: Terminal (xterm.js + WebSocket)

**Date:** 2026-04-03  
**Status:** Approved  
**Scope:** Phase 3 (single reconnectable terminal per project) + architecture for Phase 3.5 (split panes)

---

## Context

Phase 2 delivered project lifecycle management — users can create projects, start/stop Docker containers, and manage their Anthropic API key. Phase 3 adds the core interaction model: an in-browser terminal connected to each project container, so users can run Claude Code without SSH.

The key design constraint is that sessions must be **reconnectable** — navigating away and back should drop you into the same running shell, not a new one. This drives the central architectural decision: sessions are first-class server-side objects, not disposable per-connection pipes.

---

## Decisions

### Layout: dedicated project page

Clicking a project navigates to `/projects/:id` — a full page with a header bar and a full-height terminal. This gives the terminal maximum real estate, works naturally with browser navigation (back button, bookmarks), and provides space for Phase 3.5's split-pane manager above the terminal.

Alternatives considered:
- **Bottom panel** (dashboard stays visible): less terminal space, more complex layout
- **Full-screen overlay**: same as dedicated page but without a real URL — no benefit

### Architecture: in-memory session registry + ring buffer (Approach B)

Each terminal session is a persistent `docker exec` process with a server-side session ID. WebSocket connections attach and detach freely; the exec process keeps running. On reconnect, the server replays the ring buffer so the user sees recent output.

Alternatives considered:
- **New exec per WebSocket connection**: sessions die on navigation — ruled out
- **tmux as the session manager**: considered seriously. tmux itself is fine (~1MB, no X11 needed) and Claude Code runs inside it without issue. Ruled out for two reasons: (1) tmux uses the alternate screen buffer, which disables xterm.js's native scrollback — mobile users would need `Ctrl-B [` (copy mode) to scroll, which is unusable without a physical keyboard; (2) replaying tmux's ANSI redraw sequences from the ring buffer on reconnect produces garbage. tmux is available to desktop users who want splits — they type `tmux` in the bash session and the container ships a pre-configured `.tmux.conf` (256 color, mouse on). Mobile users stay in raw bash and get native xterm.js scrollback.
- **noVNC (remote desktop)**: pixel-based streaming, heavy bandwidth, no text selection — ruled out
- **Two paths (tmux on desktop, Approach B on mobile)**: device detection is heuristic, ring buffer behaviour diverges, two code surfaces to maintain — ruled out

### Phase 3 vs Phase 3.5

Phase 3 ships a single reconnectable session per project. The backend session registry is designed for multi-session from day one (sessions keyed by their own UUID, project→sessions index maintained). Phase 3 enforces a 1-session-per-project cap that is removed in Phase 3.5.

Phase 3.5 adds split panes using `react-resizable-panels` — multiple xterm.js instances visible simultaneously, each connected to its own session. This matches the iTerm2/Terminator tiling experience. No backend changes needed for Phase 3.5.

### Ring buffer size: configurable via settings

The ring buffer holds the most recent N bytes of terminal output, replayed on reconnect. Default is 512KB — practical for verbose Claude Code sessions (~5000 lines). Stored in the `settings` table (same mechanism as the Anthropic API key) and exposed in the Settings page under a "Terminal" section. Changing the setting affects new sessions only; existing sessions retain their allocated buffer size.

---

## Architecture

### New package: `internal/terminal`

```
internal/
  terminal/
    manager.go    — session registry, ring buffer, create/attach/close
    handler.go    — WebSocket upgrade, I/O pumps, resize handling
    ringbuf.go    — fixed-size circular byte buffer
```

Terminal concerns are isolated from `internal/project`. The `project.Manager` handles container lifecycle; `terminal.Manager` handles session lifecycle. They connect at two seams only:
1. `terminal.Manager` receives the Docker client only (same `dockerer` interface — all 5 Exec methods already declared). It has no reference to `project.Manager`.
2. `project.Manager` holds a `*terminal.Manager` (injected in main after both are constructed) and calls `tm.CloseProjectSessions(projectID)` before stopping or deleting a container.

The session HTTP handler mediates between them: it calls `pm.Get(id)` to verify the project is running and retrieve its container ID, then passes the container ID directly to `tm.CreateSession`. This avoids a circular dependency.

### Component map

```
cmd/appx/main.go
  ├── project.Manager   (updated: holds *terminal.Manager for cleanup hooks)
  └── terminal.Manager  (new)
        └── docker dockerer         — from main, same client as project.Manager

server.NewRouter()
  ├── POST   /api/projects/:id/sessions        (new, auth-gated)
  ├── GET    /api/projects/:id/sessions        (new, auth-gated)
  ├── DELETE /api/projects/:id/sessions/:sid   (new, auth-gated)
  └── GET    /ws/term/:sessionId               (new, auth-gated, no limitBody)

web/src/
  ├── App.tsx                   — add /projects/:id route
  ├── pages/Project.tsx         (new)
  ├── components/Terminal.tsx   (new)
  └── api/client.ts             — add session API functions
```

### Session struct

```go
type Session struct {
    ID        string
    ProjectID string
    ExecID    string
    conn      types.HijackedResponse  // raw exec stdio pipe
    buf       *RingBuffer             // circular output buffer
    subs      map[chan []byte]struct{} // active WebSocket subscribers
    mu        sync.Mutex
    done      chan struct{}            // closed when session is torn down
}
```

`HijackedResponse` is the moby SDK's wrapper around the raw TCP connection that Docker hands back when you attach to an exec process. It behaves like a `net.Conn` pointing directly into the running shell's stdin/stdout.

### Slow subscriber policy

Subscriber channels are buffered (256 entries). When `pumpOutput` cannot send to a subscriber without blocking (channel full), it closes and removes that subscriber — the WebSocket handler detects the closed channel and sends a close frame. The disconnected client auto-reconnects (see below) and receives the ring buffer replay, so no data is permanently lost. This prevents a single slow consumer from stalling output for all subscribers.

### Client-side reconnection

When the WebSocket closes unexpectedly (network blip, laptop sleep), the client auto-reconnects with exponential backoff: 1s → 2s → 4s → 8s cap. During reconnection, xterm.js shows a translucent "Reconnecting..." overlay. On successful reconnect, the ring buffer replay restores recent output. After 5 consecutive failures, the overlay changes to "Connection lost" with a manual "Reconnect" button. Intentional closes (user typed `exit`, clicked Kill Session) do not trigger auto-reconnect.

---

## Session Lifecycle

### Create

```
POST /api/projects/:id/sessions  (handler calls pm.Get first)

1. Handler calls pm.Get(id) → verify status == running (409 if not), get containerID
2. Handler calls tm.CreateSession(projectID, containerID)
3. Check byProject[projectID] — return existing session if present (Phase 3 cap)
4. ExecCreate(containerID, cmd=/bin/bash, AttachStdin/Stdout/Stderr=true, Tty=true)
5. ExecAttach(execID) → HijackedResponse
6. Allocate RingBuffer(size from settings, default 512KB)
7. Store session in registry (sessions[id] and byProject[projectID])
8. go pumpOutput(session) — reads exec stdout → ring buffer → broadcasts to subs
9. Return {sessionId, createdAt}
```

### Attach (WebSocket connect)

```
GET /ws/term/:sessionId  (upgraded to WebSocket)

1. Auth middleware validates session cookie before upgrade
2. Look up session — close with code 4004 if not found
3. Validate Origin header (CSWSH protection)
4. Create subscriber channel, register in session.subs
5. Replay ring buffer → send as initial burst to WebSocket
6. go inputPump: read WebSocket frames → write to session.conn stdin
7. Receive from subscriber channel → write to WebSocket
8. On WebSocket close: unregister subscriber, session stays alive
```

### Reconnect

Identical to Attach — client reconnects to the same `/ws/term/:sessionId`. Ring buffer replay provides recent context. The exec process is unaware the client disconnected.

### Close (explicit)

```
DELETE /api/projects/:id/sessions/:sessionId

1. Close session.conn        → exec process receives EOF, shell exits
2. Close session.done        → signals pumpOutput goroutine to stop
3. Close all subscriber chans → WebSocket handler goroutines exit cleanly
4. Remove from sessions and byProject maps
```

### Implicit cleanup triggers

| Trigger | Mechanism |
|---|---|
| User types `exit` | `pumpOutput` detects EOF on exec stdout → calls `CloseSession` automatically |
| Container stopped | `project.Manager.doStop()` calls `tm.CloseProjectSessions(projectID)` before `ContainerStop` |
| Container deleted | `project.Manager.Delete()` calls `tm.CloseProjectSessions(projectID)` before Docker cleanup |
| WebSocket disconnect | Subscriber removed; session stays alive (reconnectable) |
| Server shutdown | Graceful shutdown iterates all sessions and calls `CloseSession` on each |

`CloseSession` is the single cleanup path — all triggers funnel through it. The `done` channel is the coordination primitive: every goroutine associated with a session selects on it and exits cleanly when it is closed.

---

## API

### REST endpoints

All require authenticated session cookie (`appx_session`).

| Method | Path | Success | Error codes |
|--------|------|---------|-------------|
| POST | `/api/projects/:id/sessions` | `201 {sessionId, createdAt}` | 404 (project), 409 (not running), 401 |
| GET | `/api/projects/:id/sessions` | `200 [{sessionId, createdAt}]` | 404, 401 |
| DELETE | `/api/projects/:id/sessions/:sid` | `204` | 404, 401 |

### WebSocket endpoint

```
GET /ws/term/:sessionId
Upgrade: websocket
Cookie: appx_session=...
```

Not under `/api/` and not wrapped in `limitBody`. Added to the main mux with auth middleware only:

```go
mux.Handle("/ws/", a.Middleware(http.HandlerFunc(handleTerminalWS(tm))))
```

### WebSocket message protocol

Two types share the connection — raw bytes for I/O, JSON for control:

| Direction | Type | Format |
|---|---|---|
| client → server | Terminal input | Raw bytes, forwarded directly to exec stdin |
| server → client | Terminal output | Raw bytes, forwarded directly from exec stdout |
| client → server | Resize | JSON text frame: `{"type":"resize","cols":120,"rows":40}` |

Frame types map naturally: **binary frames** carry terminal I/O (zero-copy), **text frames** carry JSON control messages (resize). The server checks the WebSocket message type — `BinaryMessage` is forwarded to exec stdin, `TextMessage` is parsed as JSON for control. This avoids speculative JSON parsing on the hot path.

On resize: server validates cols/rows (reject negatives/zeros, clamp to max 500), then calls `ExecResize(execID, cols, rows)`. Docker sends `SIGWINCH` to the process; xterm.js has already reflowed.

---

## Security

### Authentication
Existing session cookie middleware validates before the WebSocket upgrade. The cookie is present on the HTTP upgrade request identically to any other protected route. No additional auth mechanism needed.

### Transport
All WebSocket connections are WSS (WebSocket over TLS) — already handled by the single-port HTTPS server established in Phase 1.

### Cross-site WebSocket hijacking (CSWSH)
Without Origin validation, a malicious page could open a WebSocket to `/ws/term/:id` using the victim's browser session cookie. The `gorilla/websocket` `Upgrader.CheckOrigin` function is set to validate that the `Origin` header matches the server's host. Connections with missing or non-matching origins are rejected before the upgrade completes.

### Input validation
Resize messages are validated before being passed to `ExecResize`: negative dimensions, zero dimensions, and extreme values (>500 cols/rows) are rejected. Malformed JSON is silently ignored. Oversized WebSocket frames (>1MB) cause the connection to be closed gracefully.

---

## Frontend

### `pages/Project.tsx`

On mount:
1. `GET /api/projects/:id` — load project, render header (name, status badge, back link, Stop/Start/Kill Session buttons)
2. If `status === 'running'`: call `createSession(id)` → render `<Terminal sessionId={...} />`
3. If `status !== 'running'`: render empty state with appropriate control (Start button if stopped, progress indicator if starting)
4. If started from this page: poll until `status === 'running'`, then auto-create session and render terminal

### `components/Terminal.tsx`

```
props: { sessionId: string }

mount:
  1. new Terminal({ cursorBlink: true, theme: cssVarsTheme })
  2. Load FitAddon (fills container), WebLinksAddon (clickable URLs)
  3. terminal.open(containerRef.current)
  4. fitAddon.fit()
  5. ws = new WebSocket(`wss://${host}/ws/term/${sessionId}`)
  6. ws.onmessage(e) → terminal.write(new Uint8Array(e.data))
  7. terminal.onData(data) → ws.send(data)
  8. ResizeObserver on container → fitAddon.fit() → ws.send(JSON resize msg)

unmount:
  9. ws.close()        ← session stays alive on server
  10. terminal.dispose()
```

The xterm.js theme maps to CSS variables from `index.css` — `--bg-primary` for background, `--text-primary` for foreground, `--accent` for cursor — keeping the cyberpunk aesthetic consistent.

### `api/client.ts` additions

```ts
createSession(projectId: string): Promise<{ sessionId: string; createdAt: string }>
listSessions(projectId: string): Promise<Session[]>
deleteSession(projectId: string, sessionId: string): Promise<void>
```

### npm dependencies

```
@xterm/xterm
@xterm/addon-fit       — resize terminal to fill container
@xterm/addon-web-links — make URLs in output clickable
```

(`xterm` (v4) packages referenced in the original plan are superseded by `@xterm/` (v5+) scoped packages.)

### Go dependencies

```
github.com/gorilla/websocket — WebSocket upgrade + read/write with message type awareness
```

### `App.tsx`

Add one route:
```tsx
<Route path="/projects/:id" element={<Project />} />
```

### `ProjectCard.tsx`

Add "Open" button navigating to `/projects/:id`, enabled only when `status === 'running'`.

### `Dockerfile.project` update

Add tmux with a pre-configured `.tmux.conf`:
```dockerfile
RUN apt-get install -y tmux
COPY .tmux.conf /home/node/.tmux.conf
```

`.tmux.conf` ships with: `set -g default-terminal "screen-256color"`, `set -as terminal-overrides ",xterm*:Tc"` (true color passthrough), `set -g mouse on` (touch-friendly). No status bar customisation needed — users who want splits type `tmux`; the config just makes it work correctly when they do.

### Terminal buffer size setting (Settings page)

A "Terminal" section is added below the existing "API Key" section on the Settings page. It contains a single numeric input labelled "Terminal buffer size (KB)" with a default of 512. The value is stored via `PUT /api/settings/terminal-buffer-size` (same pattern as the API key). Validation: minimum 64 KB, maximum 4096 KB. The setting affects new sessions only — a note below the input states this.

New API additions:
```ts
getTerminalBufferSize(): Promise<{ value: number }>   // GET /api/settings/terminal-buffer-size
setTerminalBufferSize(kb: number): Promise<void>        // PUT /api/settings/terminal-buffer-size
```

### Mobile copy/paste

`Terminal.tsx` shows a floating "Copy" button when `terminal.hasSelection()` is true, calling `navigator.clipboard.writeText(terminal.getSelection())`. A "Paste" button calls `navigator.clipboard.readText()` and writes to the WebSocket. Both require a user gesture (button tap), which satisfies mobile browser clipboard permission requirements.

---

## Testing

### `internal/terminal/manager_test.go`

Uses a `fakeDocker` (new, in this package) with Exec methods implemented as configurable stubs.

| Test | Covers |
|---|---|
| `TestCreateSession_Success` | Session created, exec started, stored in registry |
| `TestCreateSession_ProjectNotRunning` | 409 if container not running |
| `TestCreateSession_ReturnsExisting` | Phase 3 cap: second call returns same session |
| `TestCloseSession` | Goroutines exit, registry cleared, exec conn closed |
| `TestCloseProjectSessions` | All sessions for a project closed |
| `TestRingBuffer_WriteRead` | Wraparound correct, replay returns last N bytes |
| `TestRingBuffer_SizeRespected` | Oldest bytes overwritten when full |

### `internal/terminal/handler_test.go`

Uses `net/http/httptest` + `gorilla/websocket` test client.

| Test | Covers |
|---|---|
| `TestWS_Unauthenticated` | No cookie → 401 before upgrade |
| `TestWS_SessionNotFound` | Unknown sessionId → close code 4004 |
| `TestWS_InputForwarded` | Client bytes → exec stdin |
| `TestWS_OutputReceived` | Exec stdout → client |
| `TestWS_RingBufferReplayed` | Reconnect receives buffered output first |
| `TestWS_ResizeForwarded` | JSON resize → `ExecResize` called with correct dims |
| `TestWS_SessionSurvivesDisconnect` | Disconnect + reconnect → same session, no new exec |
| `TestWS_ExecExitClosesWS` | Exec EOF → WebSocket close frame sent |

### Security tests

| Test | Covers |
|---|---|
| `TestWS_WrongOrigin` | `Origin: https://evil.com` → rejected (CSWSH) |
| `TestWS_CorrectOrigin` | Server's own origin → accepted |
| `TestWS_MissingOrigin` | No Origin header → rejected |
| `TestWS_ResizeNegativeDimensions` | `cols: -1` → rejected, no ExecResize call |
| `TestWS_ResizeZeroDimensions` | `cols: 0` → rejected |
| `TestWS_ResizeExtremeDimensions` | `cols: 999999` → clamped to 500 |
| `TestWS_ResizeMalformedJSON` | `{"type":"resize","cols":"abc"}` → ignored, no crash |
| `TestWS_OversizedMessage` | Frame >1MB → connection closed gracefully |
| `TestSessionID_IsUUID` | Created sessionId matches UUID v4 format |
| `TestCreateSession_LimitEnforced` | Can't exceed 1 session per project in Phase 3 |

### `internal/server/router_test.go` additions

| Test | Covers |
|---|---|
| `TestCreateSession_Running` | 201 + sessionId |
| `TestCreateSession_Stopped` | 409 |
| `TestCreateSession_Unauthenticated` | 401 |
| `TestListSessions` | 200 + array |
| `TestDeleteSession_Success` | 204 |
| `TestDeleteSession_NotFound` | 404 |

### Manual verification checklist

```
[ ] Navigate to /projects/:id for a running project
    → Terminal renders, cursor visible, shell prompt appears
[ ] Type a command (ls) → output correct
[ ] Resize browser window → terminal reflows to fill container
[ ] Run claude → Claude Code starts and is interactive
[ ] Navigate to Dashboard and back
    → Session reconnects, recent output replayed, same shell ($$ matches)
[ ] Type exit → WebSocket closes, page shows "Session ended" state
[ ] Click "Kill Session" → same result as exit
[ ] Stop container from project page → terminal closes cleanly
[ ] Navigate to stopped project → empty state with Start button
[ ] Start from project page → polls until running, auto-opens terminal
[ ] Change terminal buffer size in Settings
    → Existing session unaffected; new session uses new size
```

---

## Caveats and Known Limitations

**Ring buffer size affects new sessions only.** Changing the buffer size setting takes effect for sessions created after the change. Existing sessions retain their allocated buffer size until closed and reopened.

**Sessions die on server restart.** The session registry is in-memory. When appx restarts, all session state (IDs, ring buffers, exec connections) is lost. The exec processes also exit (they receive EOF on stdin when the pipe closes). The container keeps running — files and backgrounded processes are preserved — but the interactive shell session is gone. A new session must be created on next page load.

**No idle session timeout in Phase 3.** A session with no active WebSocket subscribers stays alive indefinitely (holding the exec process and ring buffer in memory). For the single-user single-session-per-project model of Phase 3 this is acceptable. Phase 3.5 should introduce a configurable idle timeout to reclaim resources from abandoned sessions.

**No scrollback beyond the ring buffer.** The ring buffer holds the most recent 512KB of output (configurable). Output older than that is gone. For a full scrollback history, disk-backed output logging would be required — deferred to a future phase.

---

## Future: Phase 3.5

### Phase 3.5a — Multiple sessions (tab bar)

The simplest multi-session extension. Works on all devices. No backend changes needed beyond removing the 1-session-per-project cap.

1. Remove the 1-session-per-project cap in `terminal.Manager.CreateSession`
2. Tab bar on the project page showing active sessions, "+" button to open a new one
3. Per-tab close button → `deleteSession`

Desktop users who prefer tmux still use it; this gives mobile and tablet users a native way to manage multiple sessions without keyboard shortcuts.

### Phase 3.5b — Split panes (tablet-focused)

Builds on Phase 3.5a. Targeted at iPad/tablet users: large screen, touch-friendly, where tmux keyboard shortcuts are impractical.

1. `react-resizable-panels` split-pane layout on the project page
2. "Split horizontal / Split vertical" controls to divide the terminal area
3. Each pane renders its own `<Terminal>` connected to its own `sessionId`
4. Per-pane close button → `deleteSession`
5. Disabled on small screens (phones) — single terminal only

Desktop users who prefer tmux splits are not the target; this is for touch devices with enough screen real estate. Desktop users who want web UI splits get it as a bonus.

The session registry, ring buffer, and WebSocket protocol are unchanged for both phases.
