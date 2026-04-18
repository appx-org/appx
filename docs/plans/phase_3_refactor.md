# Phase 3 Refactor: Alignment with OpenCode Mental Model

**Date:** 2026-04-05
**Context:** Phase 3 was built around the assumption that Claude Code's TUI would run inside Docker exec through our WebSocket terminal. That assumption failed — the terminal is now a shell-access tool, and the AI agent UI will be OpenCode's web mode proxied through appx in Phase 4.

This doc identifies what to clean up, what to prepare for Phase 4, and what to leave alone.

---

## 1. Dead Code Removal

### `claudeCodeVolume` constant
**File:** `internal/project/container.go:26-30`

```go
const claudeCodeVolume = "appx-claude-code"
```

Never used. Was added speculatively for a shared Claude Code volume that was never implemented. Remove it.

### `ExecStart` in `Execer` interface
**File:** `internal/terminal/manager.go:25`

`ExecStart` is declared in the `Execer` interface but never called — we removed the call when we fixed the exec lifecycle bug. It's also implemented in `fakeDocker` (manager_test.go:61), `multiFakeDocker` (manager_test.go:391), and `fakeExecer` (router_test.go:31).

**Decision:** Remove from interface and all fakes. It's dead code that misleads readers into thinking we call it. The `dockerer` interface in `project/container.go` still has it (for container exec operations in the future), and `*dockerclient.Client` still satisfies both interfaces regardless.

---

## 2. Stale Comments and References

All "Claude Code" references in code comments should be updated to reflect reality. The terminal serves shell access; the AI agent is OpenCode.

| File | Line | Current | Should be |
|------|------|---------|-----------|
| `container.go:293` | `// 4 GB — Claude Code requires at least 4 GB` | `// 4 GB — generous ceiling for AI agent operations and builds` |
| `container.go:294` | `// Claude Code spawns many subprocesses` | `// AI agents and build tools spawn many subprocesses` |
| `container.go:314` | `The node user running Claude Code needs no capabilities` | `The node user needs no capabilities` |
| `container.go:322` | `Claude Code stores config/state in ~/.claude/` | `AI agents and tools store config/state in the home directory` |
| `manager.go:115` | `applications like Claude Code that need color and cursor control` | `TUI applications (vim, tmux, etc.) that need color and cursor control` |
| `Settings.tsx:109` | `Required for Claude Code in project containers` | `Required for AI agents (OpenCode) in project containers` |

---

## 3. Project.tsx Polling Duplication

**File:** `web/src/pages/Project.tsx`

The "poll until running then create session" logic is duplicated between the `useEffect` init (line 78-96) and `handleStart` (line 121-136). Both create an interval, check status, clear on running, create session.

**Refactor:** Extract a `pollUntilRunning(projectId)` function that returns a cleanup function. Both call sites become one-liners. This also makes the code easier to extend in Phase 4 when we need to poll for OpenCode web readiness after the container starts.

---

## 4. Phase 4 Preparation

### Store container IP on start

**File:** `internal/project/store.go`, `internal/project/container.go`

Phase 4's reverse proxy needs the container's internal IP to forward requests. Currently we don't store it. Two options:

**Option A (recommended): Resolve at proxy time.** Call `docker.ContainerInspect` when the proxy needs the IP. Cache it in memory (invalidate on stop/restart). No schema change needed.

**Option B: Store in DB.** Add a `container_ip` column to the projects table, populate in `SetRunning`. The IP is available from `ContainerInspect` right after start.

Recommendation: **Option A for now** — keeps the schema unchanged. The proxy can call ContainerInspect on first request per project and cache it. The project.Manager already has the Docker client. Add a `ContainerIP(id string) (string, error)` method that inspects and returns the IP.

### Project model: no changes needed yet

The `Port` field already exists on Project and is stored in the DB. Phase 4 uses it as the proxy target for user apps. OpenCode's port (4096) is a constant, not per-project config. No schema changes needed.

---

## 5. Things to Leave Alone

### Memory limit (4 GB)
Keep it. Even though OpenCode's binary is lighter than Claude Code, the container still runs Node.js builds, git operations, and the OpenCode web server. 4 GB as a ceiling is reasonable and doesn't cost anything when idle.

### PID limit (512)
Keep it. OpenCode web server + Node.js dev server + build tools + shell = many processes.

### tmpfs sizes and permissions
Keep `/home/node` at 500 MB with exec and uid=1000. Keep `/tmp` at 200 MB with exec. These are reasonable for development work. The exec permission is needed for shell scripts and build tools, not just AI agents.

### Ring buffer + session manager
The shell terminal benefits from persistent sessions and reconnect replay. This infrastructure is solid and serves its purpose well. No changes.

### Terminal.tsx
The xterm.js component is well-built — reconnect, resize, mobile copy/paste all work. In Phase 4 it becomes the "Terminal" tab alongside the "Agent" tab. No changes needed.

### Terminal buffer size setting
Still useful for shell session replay. No changes.

### TERM=xterm-256color and ConsoleSize in exec
Still needed for vim, tmux, and other TUI tools in the shell. Not agent-specific.

---

## 6. Nice-to-Have (Low Priority)

### Buffer size hot-reload
The PUT handler stores the new value in the DB but the Manager still uses the startup value. Add `SetBufferSize(int)` to Manager, call from the PUT handler. Low priority — rarely changed.

### Debounce resize in Terminal.tsx
ResizeObserver fires rapidly during window drag. The fitAddon.fit() call should stay immediate (visual), but the WebSocket resize message could be debounced (100ms). Low priority — ExecResize is cheap.

---

## Summary

| Category | Item | Effort | Priority |
|----------|------|--------|----------|
| Dead code | Remove `claudeCodeVolume` constant | 1 min | High |
| Dead code | Remove `ExecStart` from `Execer` + fakes | 5 min | High |
| Comments | Update 6 stale "Claude Code" references | 5 min | High |
| Refactor | Extract poll-until-running in Project.tsx | 15 min | Medium |
| Phase 4 prep | Add `ContainerIP()` method to Manager | 15 min | Medium |
| Polish | Buffer size hot-reload | 10 min | Low |
| Polish | Debounce resize messages | 5 min | Low |

Total: ~1 hour of focused work for the High + Medium items.
