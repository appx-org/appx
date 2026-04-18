# Phase 5a: Custom Agent Frontend for OpenCode

Date: 2026-04-09

## Goal

Build a custom React frontend that operates OpenCode agents remotely through the appx proxy. Replace the current minimal ChatPanel/SessionList/Terminal with a full-featured agent UI that supports the complete OpenCode interaction surface.

This frontend is designed from the start with the headless core pattern so that the same agent interaction logic can be reused in the unified UI (with knos), mobile (React Native), and desktop (Electron). See `docs/architecture/appx_knos_v1.md` for the full architectural context.

**Key reference:** `/Users/max/misc/pj/misc/opencode/docs/remote_server_and_custom_ui.md` — comprehensive guide covering the OpenCode REST/SSE/WebSocket API, SDK usage, core interaction loop, SSE event reference, plugin system, and headless core architecture for sharing code across platforms.

## Context

### What exists today

The current appx frontend (`web/src/`) is a lightweight React SPA:

- **ChatPanel** — Basic message list with streaming. No tool call display, no message parts, no markdown rendering. Raw text only.
- **SessionList** — Minimal session sidebar (create, select). No search, no delete, no fork.
- **Terminal** — xterm.js over WebSocket. Functional but connected to the old appx terminal endpoint, not OpenCode PTY.
- **API client** — Custom raw-fetch wrapper in `web/src/api/opencode.ts` that proxies through the Go backend at `/api/opencode/*`. Was previously using the SDK directly but imported from the wrong entry point (`@opencode-ai/sdk` instead of `@opencode-ai/sdk/v2/client`), which pulled in Node-only server code and broke in the browser. A `process.env` shim was added as a workaround, then the SDK was replaced with raw fetch. The correct fix is to import from `@opencode-ai/sdk/v2/client`.
- **Styling** — Inline styles with CSS variables. Darksynth cyberpunk aesthetic.

### What OpenCode's API provides

OpenCode exposes a full REST + SSE + WebSocket API (50+ endpoints). The appx frontend currently uses a fraction of it. Key capabilities not yet surfaced:

- Tool call visualization (bash, file read/write/edit, search, etc.)
- Permission system (allow/deny/always-allow per tool call)
- Question system (multiple choice, free text)
- File browser and viewer
- Diff viewer (session/turn/git/branch diffs)
- Todo/task tracking
- Context usage metrics and compaction
- Model selection and provider management
- Slash commands
- File/image attachments in prompts
- Revert/undo
- Reasoning/thinking display

### SDK browser compatibility (key discovery)

The `@opencode-ai/sdk` package has separate entry points for different environments:

| Entry point | Browser-safe | What it exports |
|---|---|---|
| `@opencode-ai/sdk` | **No** — imports `node:child_process`, `process.env` | Everything (client + server spawning) |
| `@opencode-ai/sdk/v2/client` | **Yes** — only uses `fetch` + `ReadableStream` | `createOpencodeClient()`, all types, SSE client |
| `@opencode-ai/sdk/v2` | **No** | Both client + server |

OpenCode's own web app exclusively imports from `@opencode-ai/sdk/v2/client`. The SDK client provides:
- Typed REST methods for all 50+ endpoints (`client.session.create()`, `client.session.promptAsync()`, etc.)
- Built-in SSE client with reconnect and `Last-Event-ID` resume
- Generated TypeScript types for all request/response shapes
- Permission, question, PTY, file, config, and command APIs — all typed

This means the headless core does NOT need to reimplement HTTP or SSE transport. The SDK already is the transport layer. The core only needs state management (reducers, event dispatching) on top of the SDK client.

### Architecture: proxy routing

The current `/api/opencode/*` path-prefix proxy works and is kept for this PR. The Go backend strips the prefix and forwards to `http://localhost:4096`. SSE streaming is enabled (`FlushInterval=-1`). The SDK client is configured with `baseUrl: "/api/opencode"` so all requests go through the proxy automatically.

Subdomain routing (`oc.localhost`) is a follow-up PR — it eliminates path rewriting but requires Go router changes and is not needed for a working UI.

The frontend talks to:
- `https://localhost:<port>/api/*` — appx handlers (auth, projects, settings)
- `https://localhost:<port>/api/opencode/*` — proxied to OpenCode (this PR)
- `https://oc.localhost:<port>/*` — direct to OpenCode (follow-up PR)

## Headless Core Architecture

The SDK (`@opencode-ai/sdk/v2/client`) handles all HTTP and SSE transport. The headless core sits on top: it manages state (reducers), event dispatching, and business logic (permission auto-respond, reconnect). React hooks are a thin adapter layer.

```
web/src/lib/
  agent-core/                  # Plain TypeScript, no React dependency
    client.ts                  # createOpencodeClient() wrapper with appx proxy baseUrl
    connection.ts              # SSE subscription via SDK, heartbeat detection, reconnect
    reducers.ts                # Pure event -> state functions
    permissions.ts             # Permission/question queue + auto-respond logic
    types.ts                   # Re-export SDK types for convenience

  agent-react/                 # React hooks wrapping agent-core
    useSession.ts              # Session state (messages, parts, status) via useReducer
    useEventStream.ts          # SSE subscription lifecycle tied to React component
    usePermissions.ts          # Permission/question UI state + respond actions
```

Note: no `session.ts` or `terminal.ts` in agent-core — the SDK client already provides typed methods for session CRUD (`client.session.create()`, `client.session.promptAsync()`, etc.) and PTY management (`client.pty.create()`, WebSocket URL construction). No need to wrap what the SDK already does well.

### Core state shape

```typescript
type SessionState = {
  status: "idle" | "running" | "error"
  messages: Message[]
  pendingPermissions: PermissionRequest[]
  pendingQuestions: QuestionRequest[]
  activeStream: {
    messageID: string
    parts: Record<string, string>
  } | null
  todos: Todo[]
  contextUsage: { used: number; total: number } | null
}
```

### Pure event reducers

```typescript
function applyEvent(state: SessionState, event: BusEvent): SessionState {
  switch (event.type) {
    case "message.part.delta":   // append delta to parts[partID]
    case "message.updated":      // upsert message into messages[]
    case "permission.asked":     // push to pendingPermissions
    case "question.asked":       // push to pendingQuestions
    case "session.idle":         // clear activeStream, set status "idle"
    case "todo.updated":         // update todos
    case "file.edited":          // track changed files
  }
}
```

## Feature Inventory

### P0 — Required for basic agent operation

Without these, you can't use agents remotely.

| # | Feature | Description | OpenCode API |
|---|---------|-------------|--------------|
| 1 | **Markdown rendering** | Agent responses are markdown with code blocks. Syntax highlighting, copy-to-clipboard | Client-side (marked + highlight.js or shiki) |
| 2 | **Chat with streaming** | Send prompts, see tokens arrive incrementally via SSE. Message parts (text, tool calls, reasoning) rendered per type | `POST /session/:id/prompt_async` + `GET /event` (`message.part.delta`, `message.updated`) |
| 3 | **Tool call display** | Collapsible cards showing what the agent is doing: bash commands, file reads, edits, searches. Status indicator (pending/running/complete/error). Tool result content | `message.part.delta` events with tool-call parts |
| 4 | **Permission handling** | Agent pauses for approval before running commands. Dock with tool description, allow/deny/always-allow buttons | `permission.asked` event → `POST /session/:id/permission/:permID` |
| 5 | **Question handling** | Agent asks questions (multiple choice radio/checkbox, or free text). Answer UI with submit | `question.asked` event → `POST /question/:id` |
| 6 | **Session management** | Create, list, select, delete sessions. Session title display. Active session indicator | `POST/GET/DELETE /session` |
| 7 | **Agent status indicator** | Idle / running / error badge. Disable prompt input while running. Show when safe to send next prompt | `session.status` + `session.idle` events |
| 8 | **Connection health** | Is the SSE stream alive? Heartbeat detection (10s interval). Auto-reconnect with backoff. Visual indicator | `server.heartbeat` event, reconnect on miss |
| 9 | **Error display** | Tool errors (inline in tool card), session errors (banner), connection errors (status bar) | `message.updated` with error state, tool error parts |

### P1 — Important for productive use

You can operate without these but the experience is significantly worse.

| # | Feature | Description | OpenCode API |
|---|---------|-------------|--------------|
| 10 | **Terminal (PTY)** | Shell access to project directory via xterm.js. Multiple tabs. Resize. Reconnect on disconnect | `POST /pty` + `WS /pty/:id/connect` |
| 11 | **File browser** | Navigate project file tree. Directory expand/collapse. File icons | `GET /file/file?path=` |
| 12 | **File viewer** | View file contents with syntax highlighting and line numbers | `GET /file/file/content?path=` |
| 13 | **Diff viewer** | See what the agent changed. Unified or split diff. Per-turn and per-session diffs | `GET /session/:id/diff`, `session.diff` event |
| 14 | **Todo/task display** | Agent's task list with status (pending/in_progress/completed/cancelled). Progress counter | `todo.updated` event |
| 15 | **Context usage meter** | Token count vs context window. Visual bar. Warning when approaching limit | Session metadata |
| 16 | **Model selection** | Switch models per-session. Provider + model picker | `GET /config/providers`, `PATCH /config` |
| 17 | **File/image attachments** | Attach files or paste images into prompts. Pills/chips showing attached context. Remove button | Request body `content` array with file/image parts |
| 18 | **Revert/undo** | Undo agent's last set of changes. Revert dock with list of available reverts | `POST /session/:id/revert`, `POST /session/:id/unrevert` |
| 19 | **Followup suggestions** | Agent suggests next actions. Clickable suggestions that populate the prompt | Followup parts in message content |
| 20 | **Provider setup** | Configure API keys for Claude/OpenAI/etc. OAuth flows. Connection status per provider | `PUT /auth/:providerID`, `GET /provider` |
| 21 | **Slash commands** | `/compact`, `/clear`, custom commands. Autocomplete popover triggered by `/` | `GET /command` + `POST /session/:id/command` |
| 22 | **Context compaction** | Trigger context summarization when window fills. Status indicator during compaction | `POST /session/:id/summarize`, `session.compacted` event |
| 23 | **Reasoning/thinking display** | Show agent's thinking process in collapsible sections. Optional summary mode | Reasoning parts in message content |

### P2 — Polish and power-user features

| # | Feature | Description |
|---|---------|-------------|
| 24 | **Session search** | Search across sessions by title or content |
| 25 | **Session forking** | Fork conversation at a specific message point |
| 26 | **Session sharing** | Generate shareable link to a session |
| 27 | **Keyboard shortcuts** | Command palette (Cmd+Shift+P), terminal toggle, navigation |
| 28 | **@ file mentions** | Reference files by name in prompts with autocomplete |
| 29 | **Prompt history** | Up/down arrow through previous prompts |
| 30 | **Line comments on diffs** | Annotate specific lines in agent's changes |
| 31 | **Drag-and-drop files** | Drop files into prompt as attachments |
| 32 | **Notification system** | Toast notifications, turn-complete sounds |
| 33 | **Theme switching** | Light/dark/auto theme support |
| 34 | **MCP server management** | Enable/disable MCP servers, view status and tools |
| 35 | **Git integration** | Branch diff, working directory status, file change indicators |
| 36 | **File search** | Search within files, ripgrep across project |

### P3 — Defer

| # | Feature | Why defer |
|---|---------|-----------|
| 37 | **Workspace/project drag-and-drop** | Power user organization — not needed for remote operation |
| 38 | **Custom provider setup** | Custom API endpoints, model definitions — niche use case |
| 39 | **i18n** | Multi-language — English-first is fine |
| 40 | **Deep linking** | URL-based navigation to specific sessions — nice but not essential |
| 41 | **Animated visual effects** | Text shimmer, spring physics, animated numbers — cosmetic polish |
| 42 | **Line comment threading** | Comment replies on diffs — code review feature, not agent operation |

## Current State vs. Required

| Feature | Current appx | Gap |
|---------|-------------|-----|
| Chat with streaming | Basic (raw text, no parts) | Rewrite with message parts, markdown, tool display |
| Terminal | Working (xterm.js) | Reconnect to OpenCode PTY instead of old appx endpoint |
| Session list | Minimal (create, select) | Add delete, search, titles, status indicators |
| Permission handling | None | Build from scratch |
| Question handling | None | Build from scratch |
| Tool call display | None | Build from scratch |
| File browser/viewer | None | Build from scratch |
| Diff viewer | None | Build from scratch |
| Todo display | None | Build from scratch |
| Model selection | None | Build from scratch |
| Provider setup | API key only | Expand to full provider management |
| Markdown rendering | None | Build from scratch |
| Context usage | None | Build from scratch |
| Revert/undo | None | Build from scratch |
| Slash commands | None | Build from scratch |
| Connection health | None | Build from scratch |

## Implementation Steps (This PR)

Goal: basic but working custom OpenCode UI — markdown rendering, tool call display, permissions, questions, streaming. No Go backend changes. No subdomain routing change.

### Step 1: Switch to SDK client (fix the browser import)

Replace the raw fetch wrapper in `opencode.ts` with the SDK client using the correct browser-safe import.

**Files to change:**
- `web/src/api/opencode.ts` — replace with SDK client factory
- `web/vite.config.ts` — remove the `process.env` shim (no longer needed)

**Critical design choice: one client per project directory.** The SDK's `directory` config sets `x-opencode-directory` on all requests via an interceptor. Since we have multiple projects, we create a client per active project. A factory function handles this.

```typescript
// web/src/api/opencode.ts — new implementation
import { createOpencodeClient } from "@opencode-ai/sdk/v2/client"

// Re-export types from the browser-safe entry point
export type { Session, Event } from "@opencode-ai/sdk/v2/client"

/** createClient returns a typed OpenCode SDK client scoped to a project directory. */
export function createClient(directory: string) {
  return createOpencodeClient({
    baseUrl: `${window.location.origin}/api/opencode`,
    directory,
    // No auth header — appx session cookie handles auth via proxy
  })
}
```

**Verify:** `task web` compiles without errors. No `process.env` or Node-only import warnings.

### Step 2: Build agent-core (reducers + connection)

Create `web/src/lib/agent-core/` with pure TypeScript, no React dependency.

**Files to create:**

`web/src/lib/agent-core/types.ts` — Re-export SDK types needed by the UI:
```typescript
export type { Session, Message, Part, TextPart, ToolPart, ReasoningPart,
  PermissionRequest, QuestionRequest, Todo, Event } from "@opencode-ai/sdk/v2/client"
```

`web/src/lib/agent-core/reducers.ts` — Pure event→state reducer:
- Define `SessionState` shape (messages, parts, status, pendingPermissions, pendingQuestions, todos)
- Switch-based `applyEvent()` handling: `message.part.delta`, `message.updated`, `message.part.updated`, `session.status`, `session.idle`, `permission.asked`, `permission.replied`, `question.asked`, `question.replied`, `todo.updated`
- Binary search for O(log n) part/message lookups during delta accumulation

`web/src/lib/agent-core/connection.ts` — SSE subscription wrapper:
- Use SDK's built-in SSE client (`client.event.subscribe()` or equivalent)
- Heartbeat detection (15s timeout → reconnect)
- Event batching (coalesce within 16ms frame before dispatching)
- Expose `subscribe(callback)` / `unsubscribe()` interface

### Step 3: Build agent-react hooks

Create `web/src/lib/agent-react/` with React hooks wrapping the core.

**Files to create:**

`web/src/lib/agent-react/useSession.ts`:
- Takes `sessionID` + `projectDir`
- Uses `useReducer` with `applyEvent` from agent-core
- Loads initial messages via `client.session.messages()`
- Returns `SessionState` (messages, parts, status, permissions, questions, todos)

`web/src/lib/agent-react/useEventStream.ts`:
- Takes `sessionID` (optional — if null, subscribes to global events)
- Manages SSE connection lifecycle tied to component mount/unmount
- Dispatches events into the reducer from `useSession`
- Returns connection status: `"connected" | "reconnecting" | "disconnected"`

`web/src/lib/agent-react/usePermissions.ts`:
- Takes pending permissions/questions from session state
- Exposes `respond(permissionID, "allow" | "deny")` and `answer(questionID, value)` actions
- Calls SDK methods: `client.session.permission()`, `client.question.reply()`

### Step 4: Markdown component

Create `web/src/components/Markdown.tsx`.

- Install `marked` + `DOMPurify` (add to package.json)
- Parse markdown with `marked`, sanitize with `DOMPurify`
- Walk rendered DOM to find `<pre>` elements, add copy-to-clipboard button
- For streaming: split at incomplete code fences so completed text above isn't re-parsed
- Style code blocks with CSS variables from `index.css`

### Step 5: Tool call card component

Create `web/src/components/ToolCallCard.tsx`.

- Generic collapsible card — no per-tool specialization in this PR
- Header: tool name (`part.tool`) + status badge (pending/running/completed/error)
- Collapsed by default when completed, open when running
- Body: for completed tools, show `part.state.output` in `<pre>` block; for errors, show `part.state.error` in red
- Spinner/loading indicator while status is `"running"`

### Step 6: Rewrite ChatPanel

Replace `web/src/components/agent/ChatPanel.tsx` with the new component architecture.

- Use `useSession` + `useEventStream` hooks
- **Critical ordering: SSE must be connected before first prompt.** The OpenCode docs warn: "Always open the event stream first so you don't miss early events." The `useEventStream` hook connects on mount; prompts can only be sent after connection is established.
- Group messages into turns (user + assistant linked via `parentID`)
- For each assistant message, render parts by type:
  - `"text"` → `<Markdown text={part.text} />`
  - `"tool"` → `<ToolCallCard part={part} />`
  - `"reasoning"` → collapsible `<details>` with thinking text
- Prompt input: `<textarea>` that calls `client.session.promptAsync()` on submit (**not** `client.session.message()` — async returns 204, response streams via SSE)
- **Abort button:** When agent is running, show a stop button that calls `client.session.abort()`. Without this, users can't cancel a runaway agent.
- Disable prompt input while agent status is `"running"`
- Auto-scroll to bottom on new content

### Step 7: Permission and question docks

Create `web/src/components/PermissionDock.tsx` and `web/src/components/QuestionDock.tsx`.

**PermissionDock:**
- Renders when `pendingPermissions.length > 0`
- Shows tool name + input description
- Three buttons: Deny, Allow Always, Allow Once
- Calls `usePermissions().respond()` on click

**QuestionDock:**
- Renders when `pendingQuestions.length > 0`
- For multiple-choice: radio buttons with options
- For free text: text input
- Submit button calls `usePermissions().answer()`

Both docks render between the message list and the prompt input (the "dock" area).

### Step 8: Status bar + session management

- Add status bar below prompt showing: agent status badge (idle/running/error) + connection indicator (green dot / yellow reconnecting / red disconnected)
- Enhance SessionList: add delete button, show session title (not just ID), show active indicator

### Step 9: Verify end-to-end

1. `task build` — compiles cleanly
2. Run `./appx`, create a project, open it
3. Create a session, send a prompt
4. Verify: markdown renders, tool calls show as cards, streaming works
5. Trigger a permission (e.g., agent tries to run bash) — verify dock appears, allow/deny works
6. Verify connection reconnects after simulated disconnect

## Follow-up PRs

### PR 2: Subdomain routing
- Switch Go router from `/api/opencode/*` path prefix to `oc.localhost` subdomain
- Update SDK client `baseUrl` to `https://oc.localhost:${port}`
- Update TLS cert SANs to include `oc.localhost`
- Configure OpenCode with `--cors https://localhost:<port>` for cross-origin browser requests from appx origin

### PR 3: Terminal + file browser
- Connect xterm.js to OpenCode PTY endpoint
- File tree with lazy loading
- File viewer with syntax highlighting
- Side panel layout (chat left, files/terminal right)

### PR 4: Extended UI
- Todo dock, revert dock, followup suggestions
- Diff viewer (session/turn diffs)
- Model selection + provider setup
- Slash commands, reasoning display
- Context usage meter

### PR 5: Polish
- Session search, forking, sharing
- Keyboard shortcuts, @ mentions, prompt history
- Theme switching, notifications
- Dead code cleanup (old container columns, process.env shim commit)

## Key OpenCode SSE Events

Events the headless core must handle:

| Event | Phase | Handler |
|-------|-------|---------|
| `message.part.delta` | A | Append streaming delta to active message part |
| `message.updated` | A | Upsert completed/updated message |
| `session.status` | A | Update agent status (running/idle/error) |
| `session.idle` | A | Clear active stream, enable prompt input |
| `permission.asked` | A | Push to pending permissions queue |
| `permission.replied` | A | Remove from pending permissions |
| `question.asked` | A | Push to pending questions queue |
| `question.replied` | A | Remove from pending questions |
| `server.heartbeat` | A | Reset reconnect timer |
| `server.connected` | A | Mark connection as established |
| `file.edited` | B | Track changed files for diff viewer |
| `session.diff` | B | Update session diff data |
| `todo.updated` | B | Update todo list state |
| `session.compacted` | B | Update context usage, refresh messages |
| `pty.created` / `pty.exited` | B | Terminal tab lifecycle |
| `session.created` / `session.updated` / `session.deleted` | B | Session list updates |
| `vcs.branch.updated` | C | Git branch change indicator |
| `mcp.tools.changed` | C | MCP tool list refresh |

## OpenCode Interaction Loop

The core interaction pattern that the headless core implements:

```
1. Create session          POST /session  (with x-opencode-directory)
2. Subscribe to SSE        GET /event?sessionID=...  (open BEFORE sending prompt)
3. Send prompt             POST /session/:id/prompt_async  (returns 204)
4. Stream response         SSE: message.part.delta events (render incrementally)
5. Handle permissions      permission.asked → show dock → POST /session/:id/permission/:id
6. Handle questions        question.asked → show dock → POST /question/:id
7. Message complete        message.updated (status: completed)
8. Agent idle              session.idle → enable prompt input, safe to send next
9. Repeat from 3
```

## Dependencies

| Package | Purpose | Status |
|---------|---------|--------|
| `@opencode-ai/sdk` | SDK client + types (import from `/v2/client` only!) | Already in package.json |
| `marked` | Markdown parsing | **Add in this PR** |
| `dompurify` + `@types/dompurify` | HTML sanitization for rendered markdown | **Add in this PR** |
| `@xterm/xterm` etc. | Terminal emulator | Already in package.json (PR 3) |

Note: syntax highlighting for code blocks can use `marked-highlight` + `highlight.js` or be deferred to a follow-up. Basic code blocks render fine without highlighting via `<pre><code>` styling with CSS variables.

## Implementation Details

### Key Data Shapes (from `@opencode-ai/sdk/v2/client`)

These types are imported from the browser-safe SDK entry point. They define the contract between the SSE stream and the UI. **Import types from `@opencode-ai/sdk/v2/client`, never from `@opencode-ai/sdk`.**

**Messages:**

```typescript
type UserMessage = {
  id: string
  sessionID: string
  role: "user"
  time: { created: number }
  summary?: { title?: string; body?: string; diffs: FileDiff[] }
  agent: string
  model: { providerID: string; modelID: string; variant?: string }
}

type AssistantMessage = {
  id: string
  sessionID: string
  role: "assistant"
  time: { created: number; completed?: number }
  error?: ProviderAuthError | UnknownError
  parentID: string          // links to user message ID
  modelID: string
  providerID: string
  cost: number
  tokens: {
    total?: number
    input: number
    output: number
    reasoning: number
    cache: { read: number; write: number }
  }
}
```

**Message parts** (each message contains multiple parts):

```typescript
type TextPart = {
  id: string; sessionID: string; messageID: string
  type: "text"
  text: string              // markdown content, streamed via deltas
  time?: { start: number; end?: number }
}

type ToolPart = {
  id: string; sessionID: string; messageID: string
  type: "tool"
  callID: string
  tool: string              // "bash", "read", "edit", "write", "glob", "grep", etc.
  state: ToolState
}

type ToolState =
  | { status: "pending"; input: Record<string, unknown>; raw: string }
  | { status: "running"; input: Record<string, unknown>; title?: string; time: { start: number } }
  | { status: "completed"; input: Record<string, unknown>; output: string; title: string;
      metadata: Record<string, unknown>; time: { start: number; end: number } }
  | { status: "error"; input: Record<string, unknown>; error: string; time: { start: number; end: number } }

type ReasoningPart = {
  id: string; sessionID: string; messageID: string
  type: "reasoning"
  text: string
  time: { start: number; end?: number }
}

type FileDiff = {
  file: string
  before: string
  after: string
  additions: number
  deletions: number
  status?: "added" | "deleted" | "modified"
}
```

### SSE Event Payload Structure

Every SSE event has the shape `{ type: string, properties: object }`. The `message.part.delta` event is the most critical — it's how streaming works:

```typescript
// Delta event — arrives many times per second during streaming
type MessagePartDeltaEvent = {
  type: "message.part.delta"
  properties: {
    messageID: string
    partID: string
    field: string       // "text" for TextPart, "output" for ToolPart, etc.
    delta: string       // text chunk to append
  }
}
```

**Delta accumulation pattern** (from OpenCode's event-reducer.ts): Find the part by ID using binary search, then concatenate the delta to the existing field value:

```typescript
// Simplified from OpenCode's implementation
case "message.part.delta": {
  const { messageID, partID, field, delta } = event.properties
  const parts = state.parts[messageID]
  const index = binarySearch(parts, partID, (p) => p.id)
  if (index < 0) break
  const part = { ...parts[index] }
  part[field] = (part[field] ?? "") + delta
  // ... update state immutably
}
```

> **Ref:** `opencode/packages/app/src/context/global-sync/event-reducer.ts:255-271`

### Implementation: SSE Connection (connection.ts)

**With the SDK client**, the SSE connection is simpler than building from raw fetch. The SDK's generated client includes SSE support. However, we still need to wrap it with heartbeat detection and event dispatching.

**Pattern from OpenCode:** Infinite reconnect loop with heartbeat detection. The server sends `server.heartbeat` every 10s. If no event arrives within 15s, abort and reconnect.

```typescript
// connection.ts — wraps SDK's SSE with heartbeat + reconnect + dispatch
import { client } from "./client"

const HEARTBEAT_TIMEOUT_MS = 15_000
const FLUSH_FRAME_MS = 16  // ~60fps batching

export function createConnection(opts: {
  sessionID?: string
  onEvent: (event: BusEvent) => void
  onStatusChange: (status: "connected" | "reconnecting" | "disconnected") => void
}) {
  let abort = new AbortController()
  let heartbeatTimer: ReturnType<typeof setTimeout>

  const resetHeartbeat = () => {
    clearTimeout(heartbeatTimer)
    heartbeatTimer = setTimeout(() => {
      abort.abort()
      abort = new AbortController()
      reconnect()
    }, HEARTBEAT_TIMEOUT_MS)
  }

  async function connect() {
    opts.onStatusChange("reconnecting")
    let retryDelay = 3_000
    while (!abort.signal.aborted) {
      try {
        // Use fetch-based SSE (SDK's EventSource or raw fetch to /event)
        const url = opts.sessionID
          ? `/api/opencode/event?sessionID=${opts.sessionID}`
          : `/api/opencode/event`
        const res = await fetch(url, { credentials: "include", signal: abort.signal })
        const reader = res.body!.pipeThrough(new TextDecoderStream()).getReader()
        opts.onStatusChange("connected")
        resetHeartbeat()
        retryDelay = 3_000 // reset on success
        // ... parse SSE, call opts.onEvent(), resetHeartbeat() on each event
      } catch {
        opts.onStatusChange("reconnecting")
        await new Promise(r => setTimeout(r, retryDelay))
        retryDelay = Math.min(retryDelay * 1.5, 30_000)
      }
    }
  }
  // ...
}
```

**Event batching** (from OpenCode): Coalesce events within a 16ms frame (60fps) before dispatching to reducers. This prevents thrashing during high-frequency delta events. Can be deferred — dispatch directly first, add batching if there are performance issues.

> **Ref:** `opencode/packages/app/src/context/global-sdk.tsx:48-96` — FLUSH_FRAME_MS = 16, STREAM_YIELD_MS = 8, double-buffer swap pattern

### Implementation: Event Reducers (reducers.ts)

**Pattern from OpenCode:** Switch-based dispatcher. All arrays kept sorted by ID for O(log n) lookups via binary search before mutation. Immutable updates (spread + replace).

Key reducer cases to implement in Phase A:

```typescript
function applyEvent(state: SessionState, event: BusEvent): SessionState {
  switch (event.type) {
    case "message.part.delta": {
      // Append delta string to part[field]. Most frequent event.
      // Use binary search to find part by ID in sorted array.
      // Concatenate: part[field] = (part[field] ?? "") + delta
      break
    }
    case "message.updated": {
      // Upsert message. If exists (binary search by ID), replace. Otherwise insert sorted.
      // This fires when a message completes, errors, or metadata changes.
      break
    }
    case "message.part.updated": {
      // Upsert full part object. Fires when a tool call completes (status changes to "completed").
      // Insert into parts[messageID] sorted by ID.
      break
    }
    case "session.status": {
      // Update status map: state.sessionStatus[sessionID] = { type: "idle" | "busy" }
      break
    }
    case "session.idle": {
      // Clear activeStream, set status "idle", enable prompt input.
      break
    }
    case "permission.asked": {
      // Push to state.pendingPermissions[sessionID]. Pause UI, show dock.
      break
    }
    case "permission.replied": {
      // Remove from pendingPermissions by permission ID.
      break
    }
    case "question.asked": {
      // Push to state.pendingQuestions[sessionID]. Show question dock.
      break
    }
    case "question.replied": {
      // Remove from pendingQuestions by question ID.
      break
    }
    case "todo.updated": {
      // Replace state.todos[sessionID] with event payload.
      break
    }
    case "session.created": {
      // Add session to list, sorted by time_updated DESC.
      break
    }
    case "session.updated": {
      // Update session metadata (title, timestamps).
      break
    }
    case "session.deleted": {
      // Remove session from list.
      break
    }
  }
  return state
}
```

> **Ref:** `opencode/packages/app/src/context/global-sync/event-reducer.ts` — full 359-line reducer with all event types

### Implementation: Markdown Rendering

**Pattern from OpenCode:** `marked` for parsing → `DOMPurify` for sanitization → code block enhancement (copy button, language label).

For streaming markdown, OpenCode splits content into blocks: if the last block is an incomplete code fence, it's rendered separately so that completed markdown above isn't re-parsed every delta.

```typescript
// Simplified from OpenCode's markdown-stream.ts
function splitStreamingMarkdown(text: string): string[] {
  const lastFence = text.lastIndexOf("```")
  if (lastFence === -1) return [text]
  const afterFence = text.slice(lastFence)
  const closingFence = afterFence.indexOf("```", 3)
  if (closingFence !== -1) return [text]  // fence is closed, render as one block
  // Incomplete fence — split so completed markdown above is cached
  return [text.slice(0, lastFence), afterFence]
}
```

**Caching:** LRU cache (max 200 entries) keyed by content hash. Each block cached independently so partial updates only re-render the streaming tail.

**Code block enhancement:** After rendering, walk DOM to find `<pre>` elements, wrap each in a container, add a copy button with clipboard API and 2s "Copied!" feedback.

> **Ref:** `opencode/packages/ui/src/components/markdown.tsx:10-16` (cache), `120-182` (code block enhancement), `252-286` (streaming split)

### Implementation: Tool Call Display

**Pattern from OpenCode:** A registry maps tool names to renderer components. Each tool type gets a specialized renderer. Unknown tools fall back to a generic display.

```typescript
// Registry pattern from OpenCode's message-part.tsx
const TOOL_RENDERERS: Record<string, React.FC<ToolPartProps>> = {
  bash:        BashTool,
  read:        ReadTool,
  edit:        EditTool,
  write:       WriteTool,
  glob:        GlobTool,
  grep:        GrepTool,
  apply_patch: PatchTool,
  web_fetch:   WebFetchTool,
  task:        TaskTool,
  // ... more tools
}

function ToolCallCard({ part }: { part: ToolPart }) {
  const Renderer = TOOL_RENDERERS[part.tool] ?? GenericTool
  return <Renderer part={part} />
}
```

**Collapsible card structure** (from BasicTool):
- Header: icon + title (with shimmer animation while running) + status badge + collapse toggle
- Body: tool-specific content, lazy-rendered on expand for heavy tools (edit/patch diffs)
- Status flow: pending → running (with `TextShimmer` on title) → completed/error

**Tool-specific rendering:**

| Tool | Title | Content when completed |
|------|-------|----------------------|
| `bash` | "Shell" + command description | `<pre><code>` with ANSI-stripped output + copy button |
| `read` | "Read" + filename | List of loaded files with checkmark icons |
| `edit` | "Edit" + filename | Diff with +/- counts, deferred render (lazy on expand) |
| `write` | "Write" + filename | Full file content display, deferred |
| `glob`/`grep` | Tool name + pattern | Markdown-rendered output |
| `web_fetch` | "Fetch" + clickable URL | Extracted links |
| `task` | Agent name (color-coded) | Spinner while running, link to child session |

**Context grouping:** Consecutive context-gathering tools (read, glob, grep, list) are grouped into a single collapsible section showing summary counts ("3 files read, 2 searches"). Each individual tool is nested inside.

> **Ref:** `opencode/packages/ui/src/components/message-part.tsx:1530-2347` (tool renderers), `opencode/packages/ui/src/components/basic-tool.tsx:44-242` (collapsible card)

### Implementation: Message Timeline & Turns

**Pattern from OpenCode:** Messages are grouped into "turns" — a user message plus all associated assistant messages (linked via `parentID`). Each turn renders the user prompt, then all assistant parts.

```typescript
// Turn grouping from session-turn.tsx
function groupIntoTurns(messages: Message[]): Turn[] {
  const userMessages = messages.filter(m => m.role === "user")
  return userMessages.map(user => ({
    user,
    assistants: messages.filter(m =>
      m.role === "assistant" && m.parentID === user.id
    )
  }))
}
```

**Part rendering dispatch** (from message-part.tsx): Each part has a `type` field. A mapping dispatches to the correct renderer:

| Part type | Renderer |
|-----------|----------|
| `"text"` | Markdown component |
| `"tool"` | Tool registry lookup → specialized card |
| `"reasoning"` | Collapsible thinking section |

> **Ref:** `opencode/packages/ui/src/components/session-turn.tsx:140-532`, `opencode/packages/ui/src/components/message-part.tsx:1212-1227` (PART_MAPPING dispatcher)

### Implementation: Permission Dock

**Pattern from OpenCode:** A dock slides up from the composer area when a permission is pending. Shows tool name, description, file patterns. Three buttons: Deny, Allow Always, Allow Once.

```
+--------------------------------------------------+
| ⚠ Permission Required                            |
| Tool: bash                                        |
| Description: Run shell command                    |
| Pattern: git status                               |
|                                                    |
| [Deny]     [Allow Always]     [Allow Once]        |
+--------------------------------------------------+
```

**Auto-respond logic** (from permission-auto-respond.ts): Checks a persisted `autoAccept` map with key hierarchy:
1. `base64(directory)/sessionID` — most specific
2. `sessionID` — session-level
3. `base64(directory)/*` — directory-level

If "Allow Always" was clicked, future permissions for the same scope auto-respond without showing the dock.

**Deduplication:** Track last response time per permission ID with 60-minute TTL to prevent duplicate auto-responses.

> **Ref:** `opencode/packages/app/src/pages/session/composer/session-permission-dock.tsx:8-74`, `opencode/packages/app/src/context/permission-auto-respond.ts:1-51`, `opencode/packages/app/src/context/permission.tsx:1-277`

### Implementation: Question Dock

**Pattern from OpenCode:** Multi-question form with tab-based navigation. Questions can have multiple-choice options (radio/checkbox) or free text.

State shape:
```typescript
{
  tab: number           // current question index
  answers: string[][]   // selected option IDs per question
  custom: string[]      // custom text per question
  customOn: boolean[]   // whether custom input is active
}
```

Keyboard navigation: ArrowUp/Down to move focus, Enter to select, Meta+Enter to advance to next question, Escape to reject.

OpenCode caches question state per request ID so users can navigate away and return.

> **Ref:** `opencode/packages/app/src/pages/session/composer/session-question-dock.tsx:61-568`

### Implementation: Terminal (PTY)

**Pattern from OpenCode:** Create PTY session via REST, connect via WebSocket, handle binary + JSON control frames.

```typescript
// 1. Create PTY
const pty = await client.pty.create({
  body: { shell: "default" },
  headers: { "x-opencode-directory": projectDir }
})

// 2. Connect WebSocket
const ws = new WebSocket(
  `wss://oc.localhost:${port}/pty/${pty.id}/connect?directory=${encodeURIComponent(projectDir)}`
)
ws.binaryType = "arraybuffer"

// 3. Handle messages
ws.onmessage = (e) => {
  const data = new Uint8Array(e.data)
  if (data[0] === 0) {
    // Control frame: JSON metadata after first byte
    const meta = JSON.parse(new TextDecoder().decode(data.slice(1)))
    // meta.cursor tracks position in byte stream
  } else {
    // Terminal output — write to xterm.js
    terminal.write(data)
  }
}

// 4. Send input
terminal.onData((data) => ws.send(data))

// 5. Resize
terminal.onResize(({ cols, rows }) => {
  client.pty.update({ path: { ptyID: pty.id }, body: { size: { cols, rows } } })
})
```

**Resize debouncing:** OpenCode uses dual-throttle — a 100ms debounce prevents rapid API calls during drag resizing.

**Multiple terminals:** A store tracks `{ all: PTY[], active: string }`. Listen for `pty.exited` SSE events to remove closed terminals. Auto-create first terminal when panel opens. Auto-close panel when last terminal removed.

**Persistence:** Serialize terminal buffer + cursor position on cleanup. Restore on reconnect by replaying from saved cursor position via `?cursor=N` query parameter.

> **Ref:** `opencode/packages/app/src/components/terminal.tsx:192-627`, `opencode/packages/app/src/context/terminal.tsx:133-250`, `opencode/packages/app/src/pages/session/terminal-panel.tsx:24-323`

### Implementation: File Browser

**Pattern from OpenCode:** Lazy-loading tree. Only load directory contents when expanded. Recursive component.

```typescript
// API call for directory listing
const files = await client.file.list({ query: { path: directoryPath } })
// Returns: FileNode[] with { name, path, type: "file" | "directory", ignored }
```

**Lazy loading triggers:**
1. Root directory loaded on mount (level 0)
2. Subdirectory loaded on first expand
3. Results cached in store — no refetch on collapse/re-expand

**Modified file indicators:** Accept a `kinds: Map<string, "add" | "del" | "mix">` prop from diff data. Show colored badges on modified files, colored dots on directories containing changes.

**Depth limit:** MAX_DEPTH = 128 to prevent circular symlink recursion.

> **Ref:** `opencode/packages/app/src/components/file-tree.tsx:259-500`

### Implementation: File Viewer

**Pattern from OpenCode:** Uses `@pierre/diffs` for virtualized file rendering with syntax highlighting. For our simpler case, `shiki` + a scrollable `<pre>` with line numbers is sufficient for Phase B.

```typescript
// API call
const content = await client.file.read({ query: { path: filePath } })
// Returns file content as string
```

**Virtualization threshold:** OpenCode virtualizes files > 500KB. We can start without virtualization and add it if needed.

> **Ref:** `opencode/packages/ui/src/components/file.tsx:47-92`

### Implementation: Diff Viewer

**Pattern from OpenCode:** Multiple diff modes — per-turn (changes in one agent response), per-session (all changes), git working directory, branch comparison. Diff data comes from the API and SSE events.

```typescript
// Per-session diff
const diffs = await client.session.diff({ path: { sessionID } })
// Returns: FileDiff[] — each with before/after content, additions/deletions

// Per-turn diffs come from message.summary.diffs on AssistantMessage
```

**Diff rendering:** OpenCode uses `@pierre/diffs` for unified/split views. For our implementation, a simpler approach: use `diff` library (npm) to compute unified diff from before/after strings, render with syntax highlighting and +/- line coloring.

**DiffChanges summary bar:** Shows +N/-M counts with a proportional 5-block SVG bar (green for additions, red for deletions, gray for neutral).

> **Ref:** `opencode/packages/ui/src/components/diff-changes.tsx:3-110`, `opencode/packages/app/src/pages/session/review-tab.tsx`, `opencode/packages/ui/src/components/session-review.tsx`

### Implementation: Todo Dock

**Pattern from OpenCode:** Dock at bottom of composer showing agent's task list. Checkboxes with strikethrough for completed items. Progress counter "3/7".

```
+--------------------------------------------------+
| Tasks  3/7                              [collapse] |
| ☑ Set up database schema          (strikethrough)  |
| ☑ Create API endpoints            (strikethrough)  |
| ☑ Write tests for auth            (strikethrough)  |
| ● Implement file upload           (in progress)    |
| ○ Add error handling                               |
| ○ Write documentation                              |
| ○ Deploy to staging                                |
+--------------------------------------------------+
```

State comes from `todo.updated` SSE events. Todo statuses: `pending`, `in_progress`, `completed`, `cancelled`.

> **Ref:** `opencode/packages/app/src/pages/session/composer/session-todo-dock.tsx:43-268`

### Implementation: Composer (Prompt Input)

**Pattern from OpenCode:** Content-editable div with mode switching (normal/shell). Supports slash commands, @ mentions, file pills, image paste.

**Submit flow** (from submit.ts):
1. Extract text from editor
2. Build request parts array: `[textPart, ...fileParts, ...contextParts, ...imageParts]`
3. For file attachments: URL format `file://{path}?start={line}&end={line}`
4. For images: include dataURL
5. Check for slash command (`/compact`, etc.) → route to `client.session.command()` instead
6. Otherwise: `client.session.promptAsync({ path: { sessionID }, body: { content: parts } })`

**Context pills:** Attached files show as small chips below the input. Each shows file icon + filename + optional line range + remove button. Horizontal scrolling if many.

**Image paste:** Listen for `paste` event, check `clipboardData.items` for `image/*` MIME types, convert to data URL, show thumbnail preview below input.

**Slash command popover:** Triggered when input starts with `/`. Fetch available commands from `GET /command`, filter by typed text, show in floating popover above input. Merge built-in commands with custom/MCP commands.

> **Ref:** `opencode/packages/app/src/components/prompt-input/submit.ts:91-574`, `opencode/packages/app/src/components/prompt-input/context-items.tsx:18-88`, `opencode/packages/app/src/components/prompt-input/image-attachments.tsx:20-61`, `opencode/packages/app/src/components/prompt-input/slash-popover.tsx:36-141`

### Implementation: Provider & Model Selection

**API calls:**
```typescript
// List providers with connection status
const providers = await client.provider.list()
// Returns: { id, name, models[], connected: boolean }[]

// Set API key for a provider
await client.auth.set({ path: { providerID: "anthropic" }, body: { key: "sk-..." } })

// Get model/provider config
const config = await client.config.providers()
// Returns: providers with default model selections

// Update config (change model)
await client.config.update({ body: { model: { default: "claude-sonnet-4-6" } } })
```

**Model selector UI:** Dropdown in session header showing current model. Grouped by provider. Shows cost indicator and "latest" badge where applicable.

> **Ref:** `opencode/packages/app/src/components/settings-providers.tsx`, `opencode/packages/app/src/components/dialog-select-model.tsx`

## OpenCode Source Reference Index

Quick lookup for each feature's implementation in the OpenCode codebase. All paths relative to `/Users/max/misc/pj/misc/opencode/`.

### Core Architecture

| Component | Path | Lines | Notes |
|-----------|------|-------|-------|
| Event reducer | `packages/app/src/context/global-sync/event-reducer.ts` | 1-359 | Switch-based, binary search indexing |
| Bus event types | `packages/opencode/src/bus/bus-event.ts` | 1-40 | Zod-validated event definitions |
| SSE client | `packages/sdk/js/src/v2/gen/core/serverSentEvents.gen.ts` | 78-172 | fetch + TextDecoderStream + reconnect |
| Global SDK (SSE setup) | `packages/app/src/context/global-sdk.tsx` | 48-202 | Event batching, heartbeat, reconnect loop |
| Permission auto-respond | `packages/app/src/context/permission-auto-respond.ts` | 1-51 | Key hierarchy, session lineage walk |
| Permission context | `packages/app/src/context/permission.tsx` | 1-277 | Store, respond(), toggleAutoAccept() |
| Binary search utility | `packages/util/src/binary.ts` | — | O(log n) sorted array operations |
| Persist utility | `packages/app/src/utils/persist.ts` | — | Versioned storage, LRU eviction |

### UI Components

| Component | Path | Lines | Notes |
|-----------|------|-------|-------|
| Message part dispatcher | `packages/ui/src/components/message-part.tsx` | 1212-1227 | PART_MAPPING registry |
| Tool renderers (all) | `packages/ui/src/components/message-part.tsx` | 1530-2347 | Per-tool: bash, read, edit, write, glob, grep, patch, fetch, task, question, skill |
| BasicTool (collapsible card) | `packages/ui/src/components/basic-tool.tsx` | 44-242 | Icon + title + status + collapsible body |
| Markdown | `packages/ui/src/components/markdown.tsx` | 1-330 | marked + DOMPurify + morphdom + LRU cache |
| Session turn | `packages/ui/src/components/session-turn.tsx` | 140-532 | Turn grouping, diff summary, auto-scroll |
| Diff changes bar | `packages/ui/src/components/diff-changes.tsx` | 3-110 | +N/-M with SVG bar chart |
| Session review (diff viewer) | `packages/ui/src/components/session-review.tsx` | — | Unified/split diff, line comments |
| Tool status title | `packages/ui/src/components/tool-status-title.tsx` | 24-138 | Animated active→done transition |
| Tool count summary | `packages/ui/src/components/tool-count-summary.tsx` | 11-52 | Pluralized count labels |
| Tool error card | `packages/ui/src/components/tool-error-card.tsx` | — | Error message + stack trace |
| File viewer | `packages/ui/src/components/file.tsx` | 47-241 | Virtualized, syntax highlighted |
| File icon | `packages/ui/src/components/file-icon.tsx` | — | Language-specific icons |

### App Features

| Component | Path | Lines | Notes |
|-----------|------|-------|-------|
| Terminal | `packages/app/src/components/terminal.tsx` | 192-627 | Ghostty, WebSocket, cursor persistence |
| Terminal panel (tabs) | `packages/app/src/pages/session/terminal-panel.tsx` | 24-323 | Multi-tab, drag reorder |
| Terminal context | `packages/app/src/context/terminal.tsx` | 133-250 | Store, pty.exited listener |
| File tree | `packages/app/src/components/file-tree.tsx` | 259-500 | Lazy loading, depth limit, modified indicators |
| Prompt input | `packages/app/src/components/prompt-input.tsx` | 102-1592 | Content-editable, mode switch, @ mentions |
| Submit handler | `packages/app/src/components/prompt-input/submit.ts` | 91-574 | Part assembly, slash commands, shell mode |
| Context items (pills) | `packages/app/src/components/prompt-input/context-items.tsx` | 18-88 | File chips with remove, line ranges |
| Image attachments | `packages/app/src/components/prompt-input/image-attachments.tsx` | 20-61 | Thumbnail preview, remove button |
| Slash command popover | `packages/app/src/components/prompt-input/slash-popover.tsx` | 36-141 | Command list, search, keybind display |
| Prompt history | `packages/app/src/components/prompt-input/history.ts` | — | Up/down arrow, max 100 entries |
| Permission dock | `packages/app/src/pages/session/composer/session-permission-dock.tsx` | 8-74 | Deny / Allow Always / Allow Once |
| Question dock | `packages/app/src/pages/session/composer/session-question-dock.tsx` | 61-568 | Multi-question tabs, radio/checkbox, custom input |
| Todo dock | `packages/app/src/pages/session/composer/session-todo-dock.tsx` | 43-268 | Checkbox list, progress counter |
| Revert dock | `packages/app/src/pages/session/composer/session-revert-dock.tsx` | — | Revert list, restore buttons |
| Followup dock | `packages/app/src/pages/session/composer/session-followup-dock.tsx` | — | Suggested next actions |
| Review tab (diffs) | `packages/app/src/pages/session/review-tab.tsx` | — | Turn/session/git/branch diff modes |
| Side panel | `packages/app/src/pages/session/session-side-panel.tsx` | — | File tree + review + context tabs |
| Session header | `packages/app/src/components/session/session-header.tsx` | — | Title, model selector, actions |
| Model selector | `packages/app/src/components/dialog-select-model.tsx` | — | Provider+model picker, cost display |
| Provider settings | `packages/app/src/components/settings-providers.tsx` | — | API key input, OAuth, status |
| MCP dialog | `packages/app/src/components/dialog-select-mcp.tsx` | — | Server list, enable/disable |
| Status popover | `packages/app/src/components/status-popover.tsx` | — | Connection + MCP + provider status |

## Notes

- **SDK import rule:** Always import from `@opencode-ai/sdk/v2/client`, never from `@opencode-ai/sdk`. The bare import pulls in Node-only server code (`node:child_process`, `process.env`) that breaks in browsers.
- The existing darksynth cyberpunk aesthetic (CSS variables in `index.css`) is preserved. All new components use the same `var(--*)` pattern.
- OpenCode's existing app uses Solid.js with 180+ custom UI components. We are not porting those — we build React equivalents using the same data/event patterns but our own component library.
- The headless core (`agent-core`) is extracted to a standalone package when knos unified UI or mobile work begins. Until then it lives in `web/src/lib/`.
- All OpenCode source references above are for understanding patterns, not for copying code. The Solid.js reactivity model (createStore, produce, reconcile) translates to React's `useReducer` + immutable spread updates.
- The `process.env` shim in `vite.config.ts` (commit `c83fe5e`) should be removed when switching to the correct SDK import — it was a workaround for the wrong entry point.
