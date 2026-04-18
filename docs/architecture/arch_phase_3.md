# Architecture: In-Browser Terminal (Phase 3)

## Table of Contents

1. [Overview](#overview)
2. [System Map](#system-map)
   - [Component Relationships](#component-relationships)
   - [Data Flow: A Terminal Keystroke](#data-flow-a-terminal-keystroke)
   - [API Endpoints](#api-endpoints)
   - [Session Lifecycle State Machine](#session-lifecycle-state-machine)
   - [External Dependencies](#external-dependencies)
3. [Code Review Guide](#code-review-guide)
   - [Ring Buffer — internal/terminal/ringbuf.go](#ring-buffer)
   - [Execer Interface — internal/terminal/manager.go](#execer-interface)
   - [Session Manager — internal/terminal/manager.go](#session-manager)
   - [Output Pump — internal/terminal/manager.go](#output-pump)
   - [WebSocket Handler — internal/terminal/handler.go](#websocket-handler)
   - [REST Session Handlers — internal/server/terminal_handlers.go](#rest-session-handlers)
   - [Router Updates — internal/server/router.go](#router-updates)
   - [Cleanup Hooks — internal/project/container.go](#cleanup-hooks)
   - [Entry Point Wiring — cmd/appx/main.go](#entry-point-wiring)
   - [Container Image — Dockerfile.project + .tmux.conf](#container-image)
   - [Terminal Buffer Size Setting — settings_handlers.go](#terminal-buffer-size-setting)
   - [Frontend — Terminal.tsx](#frontend-terminal)
   - [Frontend — Project.tsx](#frontend-project)
   - [Frontend — ProjectCard.tsx, App.tsx, Settings.tsx](#frontend-updates)
4. [Testing Guide](#testing-guide)
   - [Automated Test Coverage](#automated-test-coverage)
   - [Manual Verification Checklist](#manual-verification-checklist)
5. [Architecture and Code Pitfalls](#architecture-and-code-pitfalls)
6. [Fixed Pitfalls](#fixed-pitfalls)
7. [TODOs and Future Improvements](#todos-and-future-improvements)

---

## Overview

Phase 3 adds the core interaction model: an in-browser terminal connected to each project's Docker container via WebSocket. This is what transforms "managed Docker containers" into "interactive Claude Code sessions in the browser." After Phase 3, a user can create a project, start its container, and immediately use Claude Code from their browser — no SSH, no local tooling, no port forwarding.

### The problem being solved

After Phase 2, users could create, start, and stop projects, but had no way to interact with the running containers. The dashboard showed project cards with status badges, but clicking on a running project had nowhere to go. The terminal bridges this gap: it gives users a full interactive shell inside their project's container.

### The key design decisions

**Persistent sessions that survive WebSocket disconnects.** A terminal session is not a WebSocket connection — it is a long-lived `docker exec` process with its own stdin/stdout pipes, managed by a server-side registry. WebSocket clients attach and detach freely; the exec process stays alive. When a user refreshes the page, closes their laptop, or loses network briefly, their shell is still running and their output is still being captured. They reconnect and pick up where they left off.

This is the central architectural insight. It means the server has two independent lifetimes to manage: the session (exec process + ring buffer + subscriber registry) and the WebSocket connection (a transient subscriber). The Session outlives individual connections.

**Ring buffer for output replay.** A fixed-size circular buffer (default 512KB, configurable 64-4096KB) captures all exec stdout. On reconnect, the buffer contents are replayed as the first binary WebSocket frame. This gives users visual continuity — they see their recent output, not a blank screen. The buffer is simple and O(1) for both write and read; it is not a scrollback buffer (xterm.js handles that client-side).

**Binary/text frame separation.** WebSocket binary frames carry terminal I/O (zero-copy byte forwarding). Text frames carry JSON control messages (resize). This avoids speculative JSON parsing on the hot path — the handler checks the WebSocket frame type, not the message content. A text frame is always JSON; a binary frame is always raw terminal data.

**Slow subscriber eviction.** Each WebSocket subscriber has a buffered channel (256 entries). If the channel fills up (subscriber is too slow), the subscriber is evicted: their channel is closed, their WebSocket gets a close frame, and they auto-reconnect. This prevents a single slow connection from stalling the output broadcast to all subscribers.

**Phase 3 cap: one session per project.** To keep the implementation simple, `CreateSession` returns the existing session if one already exists for the project. This means a user opening the same project in two browser tabs sees the same terminal. The cap is enforced in `CreateSession` and can be lifted in a future phase.

**Interface segregation for Docker.** The terminal package declares its own `Execer` interface with only the five exec methods. The project package has the full `dockerer` interface with container + exec methods. Both are satisfied by `*dockerclient.Client`. This avoids a dependency from `terminal` → `project` (which would be circular since `project` calls `terminal.CloseProjectSessions` via the `terminalCloser` interface).

### How the pieces fit together

```
Browser                      Server                       Docker
  │                            │                            │
  │  POST /projects/:id/       │                            │
  │  sessions                  │  ExecCreate(bash)          │
  │ ────────────────────────►  │ ──────────────────────────►│
  │  ◄─── 201 {sessionId}     │  ◄─── exec ID              │
  │                            │  ExecAttach                │
  │  WS /ws/term/:sessionId   │ ──────────────────────────►│
  │ ════════════════════════►  │  ◄─── hijacked conn        │
  │  ◄═══ ring buffer replay   │                            │
  │                            │       pumpOutput           │
  │  binary: "ls\n" ══════►   │  ═══► conn.Write ─────────►│
  │                            │                            │
  │  ◄═══ binary: output       │  ◄─── conn.Read ◄─────────│
  │                            │       → ringbuf.Write      │
  │  text: resize  ══════►     │       → broadcast subs     │
  │                            │  ExecResize ──────────────►│
```

The `terminal.Manager` is the session registry. The `terminal.HandleTerminalWS` handler is a stateless function that bridges a WebSocket to a session. The `project.Manager` knows about the terminal manager only through the `terminalCloser` interface — it calls `CloseProjectSessions` before stopping or deleting a container.

### Trade-offs

**No terminal state persistence across server restart.** Sessions are in-memory only — no database table. If the server restarts, all sessions are lost and the user gets a fresh shell. This is acceptable for Phase 3 because: (a) the shell environment is ephemeral anyway (containers use tmpfs for /home/node), (b) persistent sessions would require serializing exec state, which Docker doesn't support, and (c) the ring buffer provides continuity within a server lifetime.

**No multi-session support.** One session per project. A future phase could lift this to support multiple shells, but it requires a session picker UI and changes to the auto-create flow on the project page.

**gorilla/websocket instead of stdlib.** Go 1.26's `net/http` doesn't have native WebSocket support. `gorilla/websocket` is the standard choice — mature, widely used, and actively maintained. The `nhooyr.io/websocket` package is an alternative but gorilla's API is simpler for this use case (message-oriented I/O vs. io.Reader/Writer).

---

## System Map

### Component Relationships

```
cmd/appx/main.go
  │
  ├── terminal.NewManager(docker, bufSize)     [NEW]
  │     ├── Execer (5 exec methods)            [NEW] — subset of Docker API
  │     ├── sessions  map[id]*Session           [NEW]
  │     └── byProject map[projectID]sessions    [NEW]
  │
  ├── pm.SetTerminalManager(tm)                [UPDATED]
  │
  └── server.Run(Config{
          TerminalManager: tm,                 [NEW field]
        })

server.NewRouter(a, pm, tm, webFS)             [UPDATED — added tm parameter]
  │
  ├── /api/projects/{id}/sessions              [NEW endpoints]
  │     POST — handleCreateSession(pm, tm)
  │     GET  — handleListSessions(pm, tm)
  │
  ├── /api/projects/{id}/sessions/{sid}        [NEW endpoint]
  │     DELETE — handleDeleteSession(tm)
  │
  ├── /api/settings/terminal-buffer-size       [NEW endpoints]
  │     GET — handleGetTerminalBufferSize
  │     PUT — handleSetTerminalBufferSize
  │
  └── /ws/term/{sessionId}                     [NEW — outside limitBody]
        a.Middleware → terminal.HandleTerminalWS(tm)

terminal.Manager                               [NEW]
  ├── CreateSession(projectID, containerID)
  │     ExecCreate → ExecStart → ExecAttach → go pumpOutput()
  ├── GetSession / ListSessions
  ├── Subscribe / Unsubscribe                   channel-based pub/sub
  ├── WriteInput(sessionID, data)               → exec stdin
  ├── Resize(sessionID, cols, rows)             → ExecResize
  ├── ReplayBuffer(sessionID)                   → ring buffer snapshot
  ├── CloseSession / CloseProjectSessions / CloseAll
  └── pumpOutput(sess)                          goroutine: exec stdout → ringbuf + subs

terminal.HandleTerminalWS(tm)                  [NEW]
  On upgrade:
    replay ring buffer → subscribe → start output pump goroutine
  Input pump (main goroutine):
    binary frame → WriteInput (exec stdin)
    text frame   → JSON resize → Resize (ExecResize)
  Output pump (goroutine):
    subscriber channel → binary WebSocket frame

project.Manager                                [UPDATED]
  ├── tm terminalCloser                         [NEW field]
  ├── SetTerminalManager()                      [NEW method]
  ├── doStop()    → tm.CloseProjectSessions()  [UPDATED — cleanup hook]
  └── Delete()    → tm.CloseProjectSessions()  [UPDATED — cleanup hook]

web/src/
  ├── pages/Project.tsx                         [NEW] full-page terminal view
  ├── components/Terminal.tsx                   [NEW] xterm.js + WebSocket + reconnect
  ├── components/ProjectCard.tsx               [UPDATED] Open button
  ├── pages/Settings.tsx                       [UPDATED] terminal buffer size
  ├── api/client.ts                            [UPDATED] session + buffer size APIs
  └── App.tsx                                  [UPDATED] /projects/:id route
```

### Data Flow: A Terminal Keystroke

```
1. User presses 'l'
2. xterm.js onData → "l"
3. Terminal.tsx sends binary WebSocket frame: [0x6c]
4. handler.go ReadMessage → BinaryMessage
5. tm.WriteInput(sessionID, [0x6c])
6. sess.conn.Conn.Write([0x6c])  → Docker exec stdin
7. bash echoes 'l' back to stdout
8. pumpOutput reads [0x6c] from sess.conn.Reader
9. sess.buf.Write([0x6c])  → ring buffer
10. for each subscriber: ch <- [0x6c]
11. output pump goroutine: conn.WriteMessage(BinaryMessage, [0x6c])
12. Terminal.tsx ws.onmessage → term.write(new Uint8Array([0x6c]))
13. User sees 'l' on screen
```

### API Endpoints

All endpoints require an authenticated session cookie (`appx_session`).

| Method | Path | Request body | Success response | Error codes |
|--------|------|-------------|-----------------|-------------|
| POST | `/api/projects/{id}/sessions` | — | `201 {sessionId, createdAt}` | 404 (project), 409 (not running), 401 |
| GET | `/api/projects/{id}/sessions` | — | `200 [{sessionId, createdAt}]` | 404 (project), 401 |
| DELETE | `/api/projects/{id}/sessions/{sid}` | — | `204` | 404 (session), 401 |
| GET | `/api/settings/terminal-buffer-size` | — | `200 {value: 512}` | 401 |
| PUT | `/api/settings/terminal-buffer-size` | `{value: N}` | `200 {status: "ok"}` | 400 (out of range 64-4096), 401 |

**WebSocket:** `WSS /ws/term/{sessionId}` — upgraded from HTTPS, auth via session cookie on the upgrade request.

### Session Lifecycle State Machine

```
                       POST /projects/:id/sessions
                       (project must be running)
                              │
                              ▼
  ┌─────────────────────────────────────────────────────────┐
  │                     SESSION ACTIVE                       │
  │                                                         │
  │  ExecCreate → ExecStart → ExecAttach → pumpOutput()    │
  │                                                         │
  │  WebSocket connect ──► Subscribe ──► output pump        │
  │  WebSocket close   ──► Unsubscribe (session stays)     │
  │  WebSocket connect ──► Subscribe ──► replay + pump      │
  │                                                         │
  └──────────┬──────────────┬────────────────┬──────────────┘
             │              │                │
      exec EOF      DELETE session      project Stop/Delete
      (user typed   (Kill Session       (project.Manager
       `exit`)       button)             cleanup hook)
             │              │                │
             ▼              ▼                ▼
        CloseSession    CloseSession    CloseProjectSessions
             │              │                │
             └──────────────┴────────────────┘
                            │
                     close(done)
                     conn.Close()
                     close all sub channels
                     remove from registry
```

### External Dependencies

| Dependency | Version | Purpose |
|------------|---------|---------|
| `github.com/gorilla/websocket` | latest | WebSocket upgrade, frame-typed read/write |
| `@xterm/xterm` | v5+ | Terminal emulator in the browser |
| `@xterm/addon-fit` | v5+ | Auto-resize terminal to container |
| `@xterm/addon-web-links` | v5+ | Clickable URLs in terminal output |

---

## Code Review Guide

### Ring Buffer

**File:** `internal/terminal/ringbuf.go` (63 lines)

A simple circular byte buffer with `Write(p []byte)` and `Bytes() []byte`. The buffer stores the most recent `size` bytes; older data is silently overwritten when the buffer wraps.

**Key decisions:**

- `Write` handles three cases: data larger than buffer (keep only tail), data that wraps around the end, and data that fits without wrapping. The large-write shortcut (`len(p) >= rb.size`) avoids a loop.

- `Bytes` returns a new copy every time. This is intentional: the caller (WebSocket handler) sends the bytes over the network, and the buffer continues to be written by the pump goroutine. A shared reference would be a data race.

- Not safe for concurrent use — documented in the doc comment. The caller (pumpOutput) holds the session mutex when calling `Write`; the WebSocket handler calls `ReplayBuffer` which acquires the session mutex before calling `Bytes`.

**What to verify:**

- The wraparound write logic on lines 34-46. When `copy(rb.buf[rb.w:], p)` copies fewer bytes than `len(p)` (because we hit the end of the buffer), the remaining bytes are copied from the start. Confirm `rb.w = len(p) - n` is correct: `n` bytes were written at the tail, `len(p) - n` bytes wrap to the front, so `rb.w` should point after the wrapped portion.

- The `rb.full` flag is set permanently once the buffer wraps. It is never cleared. This is correct because `Bytes()` uses `full` to decide whether to read `[0, w)` (partial) or `[w, size) + [0, w)` (full). Once the buffer has wrapped at least once, there is always `size` bytes of valid data.

---

### Execer Interface

**File:** `internal/terminal/manager.go:19-29`

```go
type Execer interface {
    ExecCreate(...)
    ExecStart(...)
    ExecAttach(...)
    ExecResize(...)
    ExecInspect(...)
}
```

Exported so that `internal/server/router_test.go` can provide its own `fakeExecer`. This is the seam between the terminal package and Docker: production code passes `*dockerclient.Client`, tests pass a fake with `net.Pipe`.

**What to verify:**

- `ExecInspect` is in the interface but never called in the current implementation. It is included for completeness (the project package's `dockerer` has it) and for future health-checking. This is not dead code — it is interface conformance with the full exec API.

---

### Session Manager

**File:** `internal/terminal/manager.go` (377 lines)

The Manager is the session registry. It is conceptually simple: a mutex-protected map of session ID → Session, plus an index from project ID → sessions.

**Key decisions:**

- **Two-level locking.** The Manager has `m.mu` (protects the session registry maps) and each Session has `sess.mu` (protects `buf` and `subs`). `m.mu` is never held while `sess.mu` is held, avoiding deadlocks. The convention is: look up the session under `m.mu`, then release `m.mu` and lock `sess.mu` for output operations.

- **`closeOnce sync.Once` on Session.** Prevents double-close panics when both `pumpOutput` (on EOF) and an explicit `CloseSession` call race to tear down the same session. The `closeOnce.Do` block closes the `done` channel, the exec connection, and all subscriber channels.

- **Phase 3 single-session cap.** `CreateSession` (line 98-109) checks `m.byProject[projectID]` and returns the first session it finds. There is a TOCTOU window: two concurrent `CreateSession` calls could both pass the cap check and both create sessions. This is acceptable for Phase 3 (single user, one browser tab typically) but should be tightened if the cap becomes a correctness requirement.

- **ExecStart before ExecAttach.** The implementation calls `ExecStart` then `ExecAttach`. Some Docker SDK examples do these in the opposite order. Both orderings work for TTY exec, but Start-then-Attach matches the moby/moby integration tests and ensures the process is running before we try to attach.

**What to verify:**

- `CloseSession` (line 222) removes from both maps, then calls `closeOnce.Do`. The map removal under `m.mu` prevents concurrent `GetSession` from returning a session that is mid-teardown. The subscriber channel `close()` calls happen under `sess.mu`, which means `pumpOutput` will see the closed channel on its next broadcast iteration — but `pumpOutput` doesn't hold `sess.mu` across the `Read` call, only during the broadcast, so this is safe.

- `WriteInput` (line 284) writes to `sess.conn.Conn.Write`. This is the raw net.Conn underlying the hijacked response. Writing while `pumpOutput` is reading from `sess.conn.Reader` is safe because TCP connections support concurrent read and write on different goroutines.

---

### Output Pump

**File:** `internal/terminal/manager.go:340-376`

The `pumpOutput` goroutine runs for the lifetime of each session. It reads from the exec stdout (the `HijackedResponse.Reader` — a `*bufio.Reader` over the raw connection), writes to the ring buffer, and broadcasts to all subscriber channels.

```
pumpOutput:
  loop:
    n, err := sess.conn.Reader.Read(buf)
    if n > 0:
      data = copy(buf[:n])
      sess.mu.Lock()
      sess.buf.Write(data)
      for ch := range sess.subs:
        select { case ch <- data: default: evict(ch) }
      sess.mu.Unlock()
    if err != nil:
      CloseSession(sess.ID)
      return
```

**Key decisions:**

- **Non-blocking send with eviction.** The `select { case ch <- data: default: close(ch); delete(...) }` pattern ensures a slow subscriber cannot block the pump. The evicted subscriber's WebSocket handler will see the closed channel and send a close frame. The client auto-reconnects and gets the ring buffer replay.

- **32KB read buffer.** Allocated once per session. Terminal output is typically small chunks, but a large `cat` or log dump can produce bursts. 32KB is generous enough to avoid excessive read syscalls without wasting memory.

- **Single copy per read.** The `data := make([]byte, n); copy(data, buf[:n])` ensures each subscriber gets its own copy. Without this, all subscribers would share the same buffer, which is overwritten on the next Read.

**What to verify:**

- On EOF (exec process exits), `m.CloseSession(sess.ID)` is called, which closes the `done` channel and all subscriber channels. This is the cleanup path for `exit` or shell death. Confirm this does not deadlock: `CloseSession` acquires `m.mu` then `sess.closeOnce` then `sess.mu`. `pumpOutput` does not hold any of these when it calls `CloseSession`.

- Non-EOF errors (line 367-369) are silently swallowed (`_ = err`). The session is still closed. This means a transient network error to the Docker daemon kills the session. For a future improvement, the error could be logged or the exec process re-attached, but for Phase 3 this is acceptable — the user reconnects and creates a new session.

---

### WebSocket Handler

**File:** `internal/terminal/handler.go` (194 lines)

`HandleTerminalWS` returns an `http.HandlerFunc` that bridges a WebSocket connection to a terminal session. The session ID is extracted from the URL path (last segment of `/ws/term/{sessionId}`).

**Key decisions:**

- **Origin validation.** The `CheckOrigin` function on the `websocket.Upgrader` (lines 35-52) rejects empty origins and accepts only origins whose host matches the request Host. This prevents cross-site WebSocket hijacking (CSWSH). The comparison strips the scheme from the Origin header to compare hosts directly.

- **Session-not-found handling.** When the session ID is invalid, the handler upgrades the connection anyway, then sends a close frame with custom code 4004. This is because `gorilla/websocket` requires a successful upgrade before close frames can be sent. The alternative (returning HTTP 404 before upgrade) would leave the WebSocket client with a generic connection error instead of a meaningful close code.

- **Two-goroutine I/O pump.** The input pump runs in the main goroutine (reads from WebSocket), and the output pump runs in a separate goroutine (reads from subscriber channel, writes to WebSocket). A `stopOutput` channel coordinates shutdown: when the input pump exits (client disconnect), it closes `stopOutput` and waits for the output pump to finish (`<-done`). This prevents goroutine leaks.

- **Frame-type dispatch.** Binary frames are forwarded to exec stdin via `tm.WriteInput`. Text frames are parsed as JSON `{cols, rows}` resize messages. Malformed JSON is silently ignored (the connection stays open). Resize dimensions are validated: <= 0 is rejected, > 500 is clamped.

- **Ring buffer always replayed.** On connect, the ring buffer contents are sent as the first binary message, even if empty. This serves as a "connection established" signal — the client knows the session is live once it receives the first message.

**What to verify:**

- The `defer conn.Close()` on line 103 and the output pump's `defer conn.Close()` create a double-close. This is safe because `gorilla/websocket.Conn.Close()` is idempotent.

- The `stopOutput` channel prevents a goroutine leak that would occur if the output pump blocked on `ch` after the input pump exited. Without it, the output pump would block forever because `Unsubscribe` removes the channel from the subscriber set but does not close it. Closing `stopOutput` gives the `select` an exit path.

- The session is never closed by the WebSocket handler. Disconnecting only calls `Unsubscribe`. The session lives on until: exec EOF, explicit DELETE, or project Stop/Delete. This is the "persistent session" design.

---

### REST Session Handlers

**File:** `internal/server/terminal_handlers.go` (104 lines)

Three handlers following the existing closure pattern.

**`handleCreateSession` (POST /api/projects/{id}/sessions):**
1. Looks up the project via `pm.Get(id)`.
2. Checks `proj.Status == StatusRunning` — returns 409 if not.
3. Calls `tm.CreateSession(proj.ID, proj.ContainerID)`.
4. Returns 201 with `{sessionId, createdAt}`.

The handler is the bridge between project.Manager (which knows about project status and container IDs) and terminal.Manager (which manages exec sessions). It does not import the terminal package's internal types — only `*terminal.Manager` and `*terminal.Session`.

**`handleListSessions` (GET /api/projects/{id}/sessions):**
Verifies the project exists, then returns `tm.ListSessions(id)` as a JSON array of `{sessionId, createdAt}` objects.

**`handleDeleteSession` (DELETE /api/projects/{id}/sessions/{sid}):**
Looks up the session via `tm.GetSession(sid)`, returns 404 if nil, otherwise calls `tm.CloseSession(sid)` and returns 204.

**What to verify:**

- `handleCreateSession` uses `proj.ContainerID` to create the exec process. If the container was deleted externally between the status check and `ExecCreate`, the session creation will fail with a Docker error, returning 500 to the client. This is a narrow race and acceptable.

- `handleDeleteSession` uses `r.PathValue("sid")` — note the different path variable name from `r.PathValue("id")` used in other handlers. The route is registered as `DELETE /api/projects/{id}/sessions/{sid}`, so both are available on the same request.

---

### Router Updates

**File:** `internal/server/router.go` (89 lines)

`NewRouter` now accepts `*terminal.Manager` as a fourth parameter. Three changes:

1. **Session routes added to `api` mux.** These go through `limitBody` and auth middleware like all other `/api/` routes.

2. **WebSocket route outside `limitBody`.** The `/ws/` route is registered directly on the outer `mux` with only auth middleware. Terminal WebSocket connections are long-lived binary streams — applying the 1MB body limit would break them. The placement is critical: it must be after `/api/` (so API routes take priority) and before `/` (so the SPA handler doesn't swallow it).

3. **Terminal buffer size settings routes** added alongside the existing API key settings routes.

**What to verify:**

- The WebSocket route pattern `/ws/` is a prefix match. Any path starting with `/ws/` will be handled by the terminal WebSocket handler. Currently only `/ws/term/{sessionId}` is used, but if future phases add other WebSocket endpoints (e.g. `/ws/logs/`), they would all go to the same handler. The handler extracts the session ID from the last path segment, so `/ws/anything/sessionId` would work — verify this is not a security concern (it's not, because the session ID lookup validates the session exists).

---

### Cleanup Hooks

**File:** `internal/project/container.go` (changes across multiple functions)

The project.Manager needs to close terminal sessions before stopping or deleting a container. This creates a dependency: `project` → `terminal`. To avoid a circular import (since `terminal` imports `dockerclient` types that `project` also uses), the dependency is expressed as an interface:

```go
type terminalCloser interface {
    CloseProjectSessions(projectID string)
}
```

The interface is declared in the project package. The `terminal.Manager` satisfies it. The connection is made in `main.go` via `pm.SetTerminalManager(tm)`.

**Changes to existing methods:**

- `doStop()` calls `m.tm.CloseProjectSessions(proj.ID)` before `ContainerStop`. This ensures WebSocket connections receive a clean close frame before the container goes away.

- `Delete()` calls `m.tm.CloseProjectSessions(proj.ID)` before Docker resource cleanup. Same reasoning.

Both calls are guarded by `m.tm != nil` so existing tests (which don't set a terminal manager) continue to pass.

**What to verify:**

- `doStop` runs in a background goroutine. If the user deletes the project while it is stopping, both `doStop` and `Delete` will call `CloseProjectSessions`. This is safe because `CloseSession` is idempotent (guarded by `closeOnce`), and `CloseProjectSessions` handles an empty project gracefully.

---

### Entry Point Wiring

**File:** `cmd/appx/main.go`

Three additions:

1. **Buffer size from settings.** Reads `terminal_buffer_size` from the settings table (default 512KB). The value is read once at startup and used for all new sessions. Changing the setting via the API takes effect only after a server restart for the Manager's `bufSize` — but the PUT handler stores it in the DB, so it persists. Note: there is no hot-reload of the buffer size. New sessions after a setting change still use the old Manager-level default until restart.

2. **terminal.NewManager(docker, bufSizeKB*1024).** The `*dockerclient.Client` from Phase 2 satisfies both the project `dockerer` interface and the terminal `Execer` interface. A single Docker client is shared.

3. **pm.SetTerminalManager(tm).** Injects the terminal manager into the project manager for cleanup hooks.

**What to verify:**

- The buffer size validation in main.go (`n >= 64 && n <= 4096`) mirrors the validation in the PUT handler. If the DB contains an out-of-range value (e.g. manually inserted), it is silently ignored and the default 512KB is used.

---

### Container Image

**Files:** `internal/project/Dockerfile.project`, `internal/project/.tmux.conf`

The Dockerfile gains `tmux` in the apt-get install line and a `COPY .tmux.conf /home/node/.tmux.conf`. The tmux config enables 256-color and true-color terminal support, mouse mode, and 1-based window numbering.

The `.tmux.conf` is embedded via `go:embed` alongside the Dockerfile. The `createBuildContext` function was updated to accept and tar both files into the Docker build context.

**Why tmux?** Desktop users who want split panes can type `tmux` in their terminal. The config makes it work correctly with xterm.js (true color passthrough, mouse events). This is a low-cost addition that significantly improves the power-user experience.

---

### Terminal Buffer Size Setting

**File:** `internal/server/settings_handlers.go` (new handlers: `handleGetTerminalBufferSize`, `handleSetTerminalBufferSize`)

Follows the existing settings handler pattern. The buffer size is stored as the string value of an integer in the `settings` table under key `terminal_buffer_size`.

GET returns `{value: 512}` (the default) if the key doesn't exist. PUT validates `64 <= value <= 4096` and stores it. The setting affects new sessions only — the current Manager's `bufSize` is set at startup and not hot-reloaded.

---

### Frontend — Terminal

**File:** `web/src/components/Terminal.tsx` (326 lines)

The most complex frontend component. Wraps xterm.js with WebSocket connectivity, auto-reconnect, resize handling, and mobile copy/paste.

**Architecture — single `useEffect` for lifecycle:**

The component uses one primary `useEffect` (keyed on `sessionId`) that:
1. Creates the xterm.js Terminal instance with the darksynth theme.
2. Loads FitAddon and WebLinksAddon.
3. Opens the terminal in the container div.
4. Defines a `connect()` function that creates a WebSocket and wires events.
5. Sets up keystroke forwarding (`term.onData` → binary WS frame).
6. Sets up a ResizeObserver that calls `fitAddon.fit()` and sends JSON resize messages.
7. Calls `connect()` to initiate the WebSocket.
8. Returns a cleanup function that sets `intentionalClose = true`, disposes everything.

**Reconnection logic:**

- On unexpected close (not code 1000 or 4004), increment retry counter.
- Schedule reconnect with exponential backoff: `min(1000 * 2^retries, 8000)` ms.
- After 5 failures, show "Connection lost" + manual Reconnect button.
- The manual Reconnect button resets the retry counter and calls `connect()` via `reconnectRef`.

**`onSessionEnd` ref pattern:**

The `onSessionEnd` callback is stored in a ref (`onSessionEndRef`) to avoid making the main `useEffect` depend on it. Without this, changing `onSessionEnd` (e.g. when the parent re-renders) would tear down and rebuild the entire terminal. The ref ensures the latest callback is always called without re-triggering the effect.

**Mobile copy/paste:**

Floating buttons appear on touch devices (`'ontouchstart' in window`). Copy uses `navigator.clipboard.writeText(term.getSelection())`. Paste uses `navigator.clipboard.readText()` and sends the text as a binary WS frame. Both require a user gesture (button tap), satisfying mobile clipboard permission requirements.

**What to verify:**

- `TextEncoder` is used to convert string data to binary for WS send (lines 159, 218). This is correct — xterm.js `onData` gives strings, but the WebSocket binary protocol expects `ArrayBuffer`.

- The `ResizeObserver` fires on mount (when the terminal container first gets a size) and on every subsequent resize. Each fire sends a resize message to the server. If the user is resizing their browser window, this could generate many resize messages in rapid succession. Consider whether debouncing is needed — in practice, `ExecResize` is cheap (sends SIGWINCH) so rapid-fire resizes are not harmful.

---

### Frontend — Project

**File:** `web/src/pages/Project.tsx` (437 lines)

Full-page view for a project with an embedded terminal. Uses `useParams<{ id: string }>()` to get the project ID from the URL.

**Lifecycle flow:**

1. On mount, fetch the project via `getProject(id)`.
2. If `running`: call `createSession(id)` → set session state → render `<Terminal sessionId={...} />`.
3. If `starting`: start a 2-second polling interval. On each poll, check status. When `running`, clear interval, create session.
4. If `stopped`/`error`: show empty state with Start button.
5. If session ends (exec exit or kill): show "Session ended" + "New Session" button.

**Header controls:**

- Back button (←) → navigate to dashboard.
- Project name + status dot.
- Kill Session (visible when session active) → calls `deleteSession`, clears session state.
- Stop (visible when project running) → calls `stopProject`, clears session.
- Logout → standard logout flow.

**What to verify:**

- The polling logic duplicates between `useEffect` init and `handleStart`. Both create intervals with the same logic. This is not DRY but is pragmatically correct — extracting a shared "poll until running" function would need to manage cleanup carefully. The duplication is limited and clear.

- When `handleStop` is called while a session is active, the session is cleared client-side (`setSession(null)`) but `CloseProjectSessions` on the server handles the actual cleanup when the container stops. The client doesn't need to explicitly delete the session before stopping — the server-side cleanup hook handles it.

---

### Frontend Updates

**`web/src/components/ProjectCard.tsx`:** Added an "Open" button (outline-green style) that appears alongside the Stop button when a project is running. Clicking it navigates to `/projects/{project.id}`.

**`web/src/App.tsx`:** Added route `<Route path="/projects/:id" element={<Project />} />`.

**`web/src/pages/Settings.tsx`:** Added a "Terminal" section below the API key card with a buffer size input (number, min 64, max 4096), a "KB" label, and a Save button. Client-side validation matches the server's range check.

**`web/src/api/client.ts`:** Added `Session` interface and five new functions: `createSession`, `listSessions`, `deleteSession`, `getTerminalBufferSize`, `setTerminalBufferSize`.

---

## Testing Guide

### Automated Test Coverage

#### `internal/terminal/ringbuf_test.go`

6 tests covering: basic write/read, wraparound, size limits, multiple small writes, empty read, oversized single write. 100% coverage of the ring buffer logic.

#### `internal/terminal/manager_test.go`

11 tests using `fakeDocker` (single pipe pair) and `multiFakeDocker` (multiple pipe pairs for multi-project tests).

- CreateSession: success, returns existing (cap test)
- CloseSession: removes from registry, closes done channel
- CloseProjectSessions: only closes the target project
- ListSessions: returns correct sessions, empty for nonexistent project
- PumpOutput: data reaches subscriber channel and ring buffer
- SessionExitClosesSession: EOF on exec stdout → session cleaned up
- CloseAll: shuts down all sessions
- WriteInput: data reaches exec stdin (via net.Pipe synchronous read)
- Resize: ExecResize called with correct dimensions
- Unsubscribe: channel removed from subscriber set

**Notable:** `fakeDocker` uses `net.Pipe` for synchronous I/O. This means writes block until the other side reads. The `WriteInput` test uses a concurrent reader goroutine to avoid deadlock. This is a testing pattern worth understanding before modifying these tests.

#### `internal/terminal/handler_test.go`

13 tests using `httptest.Server` with auth middleware. These require network access (for the test HTTP server).

Functional tests (7): Unauthenticated (401), SessionNotFound (close code 4004), InputForwarded, OutputReceived, RingBufferReplayed, ResizeForwarded, SessionSurvivesDisconnect.

Security tests (6): WrongOrigin (rejected), MissingOrigin (rejected), ResizeNegativeDimensions (ignored), ResizeZeroDimensions (ignored), ResizeExtremeDimensions (clamped to 500), ResizeMalformedJSON (ignored, connection stays alive).

**Notable:** These tests use `httptest.NewServer` which requires network listen access. In sandboxed environments, they may need the sandbox override.

#### `internal/server/router_test.go`

10 new tests added alongside the existing 34.

Helper: `fakeExecer` implements `terminal.Execer` with `net.Pipe`. `createRunningProject` is a test helper that creates a project and forces it to `running` via direct DB update (since the test uses nil Docker, which can't actually start containers).

Session endpoint tests (6): CreateSession_Running (201), CreateSession_Stopped (409), CreateSession_Unauthenticated (401), ListSessions_Success, DeleteSession_Success (204), DeleteSession_NotFound (404).

Buffer size setting tests (4): GetTerminalBufferSize_Default (512), SetTerminalBufferSize (roundtrip), SetTerminalBufferSize_TooSmall (400), SetTerminalBufferSize_TooLarge (400).

#### Test gaps worth noting

- No test for `Resize` with a nonexistent session (returns error, but not tested).
- No integration test that verifies the full path: create session → WebSocket → type → output appears. The unit tests cover each step individually.
- `fakeExecer` in router_test.go leaks the server side of `net.Pipe` (discarded in `ExecAttach`). Acceptable for tests but would cause resource warnings in long test runs.
- The buffer size setting PUT handler doesn't update the Manager's bufSize at runtime — no test verifies this gap because it is by design.

### Manual Verification Checklist

```
[ ] 1. Fresh start: rm -rf data/ && ./appx -port 8443
       → Server starts normally

[ ] 2. Create + start project: Dashboard → New Project → "term-test" → Start
       → Wait for "running" status

[ ] 3. Open terminal: Click "Open" on the running project card
       → Navigates to /projects/:id
       → Terminal renders with cursor, shell prompt appears ($ or #)

[ ] 4. Type commands: type "ls" + Enter
       → Output appears in terminal, command works correctly

[ ] 5. Resize: drag browser window smaller/larger
       → Terminal reflows to fill the available space
       → No rendering glitches or scrollbar issues

[ ] 6. Reconnect on refresh: press F5 or Ctrl+R
       → Page reloads, session reconnects
       → Recent output (from ring buffer) is visible immediately

[ ] 7. Navigate away and back: click ← to go to Dashboard, then Open again
       → Same session reconnects, output preserved

[ ] 8. Exit shell: type "exit" + Enter
       → WebSocket closes cleanly (code 1000 or 4004)
       → Page shows "Session ended" with "New Session" button

[ ] 9. New session after exit: click "New Session"
       → Fresh shell prompt appears

[ ] 10. Kill Session: while terminal is active, click "Kill Session" in header
        → Terminal closes, "Session ended" state shown

[ ] 11. Stop from project page: start a new session, then click "Stop" in header
        → Terminal closes, page shows "Project is stopped" with Start button

[ ] 12. Start from project page: click "Start"
        → Page shows "Starting...", then auto-creates session when running

[ ] 13. Wrong origin (security): using curl or a script, attempt to open a WebSocket
        to /ws/term/:id with Origin: https://evil.com
        → Connection rejected (403 before upgrade)

[ ] 14. Unauthenticated WebSocket: attempt WS connect without session cookie
        → 401 Unauthorized

[ ] 15. Buffer size setting: go to Settings → Terminal section
        → Shows current buffer size (default 512 KB)
        → Change to 1024, click Save → success message
        → Verify persists across page refresh

[ ] 16. tmux: in terminal, type "tmux" → split pane works (Ctrl+b %)
        → Mouse scrolling works, pane selection works

[ ] 17. Graceful shutdown: while terminal is active, send SIGINT to server
        → Terminal closes cleanly (WebSocket close frame received)
        → Server logs "Server stopped"

[ ] 18. Project delete: from Dashboard, delete a project that has an active session
        → Session closes cleanly before Docker resources are removed
```

---

## Architecture and Code Pitfalls

### Pitfall 1 — TOCTOU in CreateSession single-session cap

**Location:** `internal/terminal/manager.go:98-109`

**The problem:** The cap check (line 102-107) releases `m.mu` before creating the exec process. Two concurrent `CreateSession` calls for the same project could both pass the cap check and both create exec processes. The second one would overwrite the first in the `byProject` map, orphaning the first session's goroutine and exec process.

**Why it matters:** Low severity for Phase 3 (single user). Medium if multi-tab use becomes common. The orphaned session would consume resources until the exec process exits or the server restarts.

**What a fix looks like:** Hold `m.mu` through the entire `CreateSession` call, or use a per-project lock. The trade-off is that Docker API calls would be made under the lock, which could block other session operations during the 10+ second exec create timeout.

---

### Pitfall 2 — Buffer size not hot-reloaded

**Location:** `cmd/appx/main.go:115-121`, `internal/server/settings_handlers.go:91-109`

**The problem:** The terminal Manager's `bufSize` is set once at startup from the DB setting. The PUT handler updates the DB but not the Manager. New sessions created after a PUT still use the old buffer size.

**Why it matters:** Low severity. The user changes the setting and expects it to take effect immediately. Instead, they must restart the server. The Settings UI says "Affects new sessions only," but even new sessions don't get the new size until restart.

**What a fix looks like:** Add a `SetBufferSize(int)` method to the Manager that updates `m.bufSize` under the lock. Call it from the PUT handler. Existing sessions retain their old buffers; new sessions use the new size.

---

### Pitfall 3 — Exec errors silently swallowed in pumpOutput

**Location:** `internal/terminal/manager.go:366-369`

**The problem:** When `sess.conn.Reader.Read` returns a non-EOF error (e.g. Docker daemon disconnected), the error is assigned to `_` and the session is closed. No log message is emitted for non-EOF errors.

**Why it matters:** Low severity. Debugging session drops requires adding temporary logging. A transient Docker API issue could kill sessions without any trace in the server logs.

**What a fix looks like:** `log.Printf("terminal session %s: pump read error: %v", sess.ID, err)` before the `CloseSession` call.

---

### Pitfall 4 — ResizeObserver can fire rapidly

**Location:** `web/src/components/Terminal.tsx:170-177`

**The problem:** The `ResizeObserver` callback calls `fitAddon.fit()` and sends a resize message on every observation. During a window drag, this can fire dozens of times per second, sending a JSON message each time.

**Why it matters:** Low severity. `ExecResize` is cheap (sends SIGWINCH to the process), and the WebSocket can handle high-frequency small messages. But it does generate unnecessary network traffic.

**What a fix looks like:** Debounce the ResizeObserver callback with a 100ms delay. `fitAddon.fit()` should still be called immediately (for visual responsiveness), but the resize message can be debounced.

---

### Pitfall 5 — fakeExecer leaks net.Pipe server side

**Location:** `internal/server/router_test.go:38-40`

**The problem:** `ExecAttach` creates a `net.Pipe` and discards the server side (`_`). The discarded `net.Conn` is never closed. In tests with many session creates, this accumulates unclosed connections.

**Why it matters:** Low severity — test-only issue. Each test creates a fresh `fakeExecer`, and Go's test cleanup handles resource release. But it could cause confusing test failures in long-running test suites.

**What a fix looks like:** Track created pipes in the `fakeExecer` and close them in a `t.Cleanup`.

---

## Fixed Pitfalls

> **Problem:** WebSocket output pump goroutine leaked when the client disconnected. The output pump goroutine blocked on `range ch` indefinitely because `Unsubscribe` removes the channel from the subscriber set but does not close it (closing it would cause a panic in the broadcast loop if pumpOutput tries to send to a closed channel).
> **Fix:** Added a `stopOutput` channel. The input pump closes it on exit. The output pump selects on both the subscriber channel and `stopOutput`, exiting when either fires. The input pump then waits for `<-done` before returning, ensuring clean goroutine teardown.

> **Problem:** `createBuildContext` only included the Dockerfile in the tar archive. Adding the `.tmux.conf` to the container image required updating the build context.
> **Fix:** Changed `createBuildContext` to accept a variadic set of named files (Dockerfile + .tmux.conf), each added as a tar entry. The function signature changed from `createBuildContext(dockerfile []byte)` to `createBuildContext(dockerfile, tmuxConf []byte)`.

> **Problem:** The `Execer` interface was initially unexported (`execer`), preventing `internal/server/router_test.go` from creating test doubles.
> **Fix:** Exported the interface as `Execer` so that other packages' test files can implement it.

> **Problem:** `ExecStart` was called before `ExecAttach`, starting the exec process with no reader attached. The process would run, produce output to nobody, and the subsequent `ExecAttach` would either hang or miss output.
> **Fix:** Removed the `ExecStart` call entirely. `ExecAttach` both attaches the hijacked connection and starts the exec process in one operation.

> **Problem:** `CreateSession` used `context.Background()` with no timeout. If Docker was slow or unresponsive, the API request would block indefinitely, causing the frontend to hang in "Connecting..." state.
> **Fix:** Added `context.WithTimeout(context.Background(), 30*time.Second)` around the exec create + attach calls.

> **Problem:** Claude Code's TUI hung silently because the exec process had no `TERM` environment variable and a 0x0 initial PTY size.
> **Fix:** Added `Env: []string{"TERM=xterm-256color"}` and `ConsoleSize: {Height: 24, Width: 80}` to `ExecCreateOptions`.

> **Problem:** Container resource limits (1 GB RAM, 256 PIDs, 50 MB noexec tmpfs) were too restrictive for Claude Code, which needs 4 GB RAM, spawns many subprocesses, and writes executable files to `~/.claude/`.
> **Fix:** Raised memory to 4 GB, PIDs to 512, `/home/node` tmpfs to 500 MB with exec allowed, `/tmp` to 200 MB with exec allowed.

> **Problem:** Project.tsx had no render branch for `status === 'running' && !session && !sessionEnded` (the brief gap between `fetchProject` resolving and `initSession` completing). Users saw "Unknown state".
> **Fix:** Added a "Connecting..." state for running projects without an active session.

---

## TODOs and Future Improvements

### Explicit TODOs in Code

- `cmd/appx/main.go:100` — `// TODO: start with Anthropic key but be flexible to add other Coding Agent providers in the future (Codex, OpenCode, Gemini etc)`. Pre-existing from Phase 2, not a Phase 3 concern.

### Known Limitations (Deliberate Trade-offs)

- **One session per project.** The single-session cap simplifies the UI (no session picker) and the server (no session-ID routing complexity). Lifting it requires a session list UI and changes to `CreateSession` to stop auto-returning existing sessions.

- **Sessions are in-memory only.** No database table, no recovery across server restarts. This is inherent to the Docker exec model — exec processes are tied to the server process and cannot be serialized.

- **Buffer size not hot-reloaded.** The Manager uses the buffer size from startup. See Pitfall 2.

- **No scrollback sync.** The ring buffer replays the most recent N bytes on reconnect, but xterm.js maintains its own scrollback buffer client-side. If the ring buffer is smaller than xterm's scrollback, some output is lost on reconnect. Increasing the ring buffer to 2-4MB would mitigate this at the cost of memory per session.

### AI Coding Agent Compatibility (Discovered During Phase 3)

#### The TUI Problem

Modern AI coding agents (Claude Code, OpenCode) use rich TUI frameworks that require "modern terminal emulators" — they rely on terminal capabilities beyond what a basic Docker exec PTY provides through a WebSocket bridge. Both agents fail to render their interactive TUI through our xterm.js + WebSocket + Docker exec pipeline, even though xterm.js supports most terminal features.

**What works:**
- Shell commands (ls, git, vim, tmux) — all render correctly through our terminal
- Non-interactive agent commands (`opencode run "say hello"`) — executes successfully and returns output
- The Anthropic API key is correctly injected and the agent can make API calls

**What doesn't work:**
- Interactive TUI mode for both Claude Code and OpenCode — the TUI either hangs, silently exits, or fails to render
- Both agents list specific "modern terminal emulators" (WezTerm, Alacritty, Ghostty, Kitty) as prerequisites

**Root cause:** These TUI frameworks likely use advanced terminal protocols (Kitty graphics protocol, synchronized output, etc.) or perform terminal capability detection that fails in a Docker exec PTY. The issue is not specific to our WebSocket bridge — `docker exec -it` from iTerm2 also fails.

#### Claude Code Specific Issues

Claude Code is no longer a simple npm package. The npm package (`@anthropic-ai/claude-code`) installs a `cli.js` bootstrapper that downloads a **native binary** on first run. Additional issues discovered:

- The native installer (`install.sh`) does not support `CLAUDE_INSTALL_DIR` — it always installs to `$HOME/.claude/`
- The npm bootstrapper writes the native binary to `~/.claude/` on first run — ephemeral on tmpfs, re-downloaded every container restart
- Requires at least 4 GB RAM per the docs
- Silently fails with no error output when the TUI can't initialize

#### OpenCode (Current Solution)

OpenCode is an open-source, provider-agnostic AI coding agent distributed as a single static binary (~50 MB). It's installed at Docker image build time — no first-run download needed.

**What works today:**
- `opencode run "message"` — non-interactive mode, executes AI tasks and returns output
- `opencode serve` — headless server mode with API
- `opencode web` — starts server with web UI (unexplored, see Phase 5 TODOs)

**Current Dockerfile approach:** `curl -fsSL https://opencode.ai/install | bash` at build time, then copy the binary to `/usr/local/bin/opencode`. The install script downloads a platform-specific binary from GitHub releases.

#### Container Resource Changes

| Constraint | Original (Phase 2) | Current | Rationale |
|-----------|-------------------|---------|-----------|
| Memory | 1 GB | 4 GB | Claude Code minimum; ceiling not reservation |
| PID limit | 256 | 512 | Agents spawn many subprocesses |
| `/home/node` tmpfs | 50 MB, `noexec`, root-owned | 500 MB, exec, uid=1000 | Agent config/state, executable scripts |
| `/tmp` tmpfs | 100 MB, `noexec` | 200 MB, exec allowed | Larger temp space for agent operations |

**Memory limit is a ceiling, not a reservation**: Docker memory limits don't pre-allocate memory. Setting 4 GB per container doesn't mean each container uses 4 GB — idle containers use ~50 MB (`sleep infinity`). The limit exists as a safety cap.

#### Exec Environment Requirements

| Setting | Why |
|---------|-----|
| `TERM=xterm-256color` | TUI apps need color and cursor control |
| `ConsoleSize{80,24}` | Initial PTY size must be non-zero; real size arrives via ExecResize |
| No `ExecStart` before `ExecAttach` | `ExecAttach` both attaches and starts the exec; calling `ExecStart` first hangs |
| `uid=1000,gid=1000` on `/home/node` tmpfs | Docker tmpfs defaults to root ownership; agent needs writable home dir |

### Prerequisites for Phase 4 (Reverse Proxy)

- The router already serves the SPA on `/` and API on `/api/`. Phase 4 adds `/apps/{name}/*` for reverse proxy to containers. The route pattern doesn't conflict with any Phase 3 routes.
- The project's `Port` field (stored but unused in Phases 2-3) becomes the target port for the reverse proxy.
- WebSocket proxying for apps (distinct from terminal WebSocket) will need its own route under `/apps/{name}/ws/*`.

### Phase 5+ TODOs

**OpenCode `serve` / `web` Integration**: OpenCode has a client/server architecture with `opencode serve` (headless API server) and `opencode web` (web UI). Instead of fighting the TUI-in-terminal battle, we could:
- Run `opencode serve --port N` in the container on project start
- Reverse-proxy the OpenCode web UI through appx at `/apps/:name/opencode/`
- Get a proper coding agent web UI without any terminal emulation issues
- This is the most promising path to a fully interactive coding agent experience
- Needs investigation: what API does `opencode serve` expose? Can we embed the web UI? What ports does it use?

**Shared Agent Binary Volume**: Instead of baking OpenCode (or Claude Code) into the Docker image at build time, install once into a shared Docker volume and mount read-only into every container. Benefits:
- Decouples agent updates from image rebuilds
- Reduces image size
- All containers share one installation
- Needs: a one-time installer container, volume lifecycle management, update mechanism in the UI

**Claude Code Revisit**: If/when Claude Code fixes Docker exec PTY compatibility, reconsider as an option alongside OpenCode. The native installer approach would need the shared volume solution since the npm bootstrapper re-downloads on every container restart (tmpfs is ephemeral). Alternatively, Claude Code's Agent SDK provides a programmatic interface that sidesteps the TUI problem entirely.

**Configurable Container Resources**: The 4 GB / 512 PID / 2 CPU limits are hardcoded in `container.go`. Allow per-project resource configuration via the API and Settings UI. Users with smaller hosts can reduce limits for lightweight projects; power users can increase for heavy workloads.

**Container Resource Separation**: Consider separating the "project workspace" container (lightweight, just files + tools) from the "agent runtime" (heavy, 4 GB, spawns many processes). The workspace idles cheaply; the agent runtime spins up only when the user opens a terminal. This would dramatically reduce memory usage for users with many projects.

**Hot-reload Buffer Size**: Add `SetBufferSize(int)` to `terminal.Manager` so the PUT handler can update the buffer size at runtime without a server restart. New sessions would use the new size immediately.
