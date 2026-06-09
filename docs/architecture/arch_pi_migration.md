# Pi Migration — Architecture Reference

Living reference for the `codex/pi-harness-distribution` branch (22 commits, 78 files,
~+4.7k / -3.3k lines). This branch replaces the OpenCode agent backend with the
Pi coding agent fronted by Appx's sibling `agent-server` service, and rebuilds
the supporting UI, settings, scaffolding, and deployment around it.

## Table of Contents

- [Plain-Language Summary](#plain-language-summary)
- [System Map](#system-map)
- [Code Review Guide](#code-review-guide)
  - [1. Database & Project Model](#1-database--project-model)
  - [2. Project Scaffolding & Pi Harness](#2-project-scaffolding--pi-harness)
  - [3. Agent-Server Reverse Proxy](#3-agent-server-reverse-proxy)
  - [4. Egress Integration](#4-egress-integration)
  - [5. Frontend Pi Agent Stack](#5-frontend-pi-agent-stack)
  - [6. Settings Rebuild](#6-settings-rebuild)
  - [7. Deployment](#7-deployment)
- [Testing Guide](#testing-guide)
- [Architecture and Code Pitfalls](#architecture-and-code-pitfalls)
- [Fixed Pitfalls](#fixed-pitfalls)
- [TODOs and Future Improvements](#todos-and-future-improvements)

---

## Plain-Language Summary

**What changed, in one paragraph.** Appx used to run OpenCode as its built-in
coding agent. This branch rips OpenCode out and replaces it with the Pi CLI,
fronted by a sibling Node service called `agent-server` that turns Pi sessions
into a stable HTTP/SSE contract. Appx now reverse-proxies the browser to that
service, scopes every chat session to a single project, scaffolds a per-project
Pi harness (prompt, guardrail extension, egress skill) into `.pi/`, gives the
Settings page first-class support for Pi credentials (API keys + subscription
OAuth + custom LiteLLM-style providers), and routes Pi's outbound network
traffic through Appx's existing egress allowlist so the same approval UI
applies to model calls and `pip install`s alike.

**What got removed.** `internal/opencode/` (client, startup polling), the
`/api/opencode/*` proxy and its WebSocket bits, OpenCode-specific settings
endpoints (`/api/settings/api-key`), the OpenCode systemd service, the
`opencode_project_id` column on `projects`, the `web/src/components/agent/`
chat stack, and the agent-core/agent-react event-stream library.

**What got added.**

- `internal/server/agent_proxy.go` — two reverse proxies (`/api/agent/*` and
  `/api/projects/:id/agent/*`) targeting `agent-server` on `127.0.0.1:4001`,
  injecting project context as headers.
- `internal/project/pi_harness.go` + `internal/project/templates/pi/` — embedded
  per-project Pi assets: `AGENTS.md`, `extensions/appx-guardrails.ts`,
  `skills/appx-egress/`, and `settings.json`.
- `web/src/lib/pi-agent/` — new state machine (reducer + sessions store +
  `usePiSession` hook) that consumes `agent-server`'s SSE event stream, indexes
  text/tool blocks by `contentIndex`, and falls back to polling when SSE drops.
- `web/src/components/pi-agent/` — `PiAgentPane`, `PiSessionList`,
  `PiChatPanel`, `PiToolCallCard`, plus an inline "extension UI" panel that
  surfaces Pi extensions' confirm/select/input prompts (used by the new
  Appx guardrail extension).
- `web/src/pages/Settings.tsx` — full rewrite: Pi provider list with
  configuration source, subscription OAuth flow with URL/code fallback, and a
  custom provider editor for LiteLLM/OpenAI-Responses-compatible endpoints.
- `deploy/agent-server.service` — systemd unit running `agent-server` as
  `appx-agent` in `AGENT_SERVER_MODE=multi`, with `HTTPS_PROXY` pointed at the
  egress CONNECT proxy.
- `deploy/pi-version` — pinned Pi version for `tools-install.sh`.

**The single most important architectural decision.** All Pi traffic — chat
sessions, model API calls, package installs — funnels through Appx-controlled
choke points: the agent-server proxy for browser→Pi, the egress CONNECT proxy
for Pi→internet. The browser never speaks directly to `agent-server`, and Pi
never speaks directly to `api.openai.com`. This is what makes per-project
scoping, session cookies, and the egress allowlist all work as a single
coherent permission model.

---

## System Map

### Request Topology

```
                                 Browser
                                    │
                                    │ HTTPS (single port)
                                    ▼
                       ┌────────────────────────┐
                       │  appx (Go, this repo)  │
                       │  ─ auth middleware     │
                       │  ─ React SPA           │
                       │  ─ subdomain dispatch  │
                       └─────────────┬──────────┘
                                     │
        ┌────────────────────────────┼─────────────────────────────┐
        │                            │                             │
        ▼                            ▼                             ▼
 /api/projects/:id/agent/*    /api/agent/*                 <name>.<base>
 [NEW] project-scoped proxy   [NEW] global Pi proxy        existing app proxy
        │                            │                             │
        └────────────┬───────────────┘                             │
                     ▼                                             │
         ┌──────────────────────────┐                              │
         │ agent-server [NEW]       │                              │
         │  appx-agent user         │                              │
         │  127.0.0.1:4001          │                              │
         │  AGENT_SERVER_MODE=multi │                              │
         │  spawns Pi sessions      │                              │
         └─────────────┬────────────┘                              │
                       │ provider HTTPS                            │
                       ▼                                           ▼
        ┌──────────────────────────────┐               localhost:<assigned-port>
        │ egress CONNECT proxy         │               (project dev server)
        │  127.0.0.1:9080              │
        │  allowlist + log             │
        └──────────────┬───────────────┘
                       │  CONNECT host:443
                       ▼
                  external internet
                  (api.anthropic.com, api.openai.com, etc.)


   Pi skills helper ────POST /egress/request─▶ 127.0.0.1:9081 [internal listener]
                                                       │
                                                       ▼
                                          appx PendingRegistry → dashboard UI
                                          (approve = adds host:port to allowlist)
```

### New / Updated API Endpoints

| Method                     | Path                                             | Auth | Purpose |
| -------------------------- | ------------------------------------------------ | ---- | ------- |
| `*` [NEW]                  | `/api/projects/:id/agent/*`                      | yes  | Project-scoped Pi session proxy. Injects `X-Appx-Project-Id/Name/Dir` headers. |
| `*` [NEW]                  | `/api/agent/auth/*`                              | yes  | Pi provider auth list, API-key set/delete, subscription OAuth start/continue/cancel. |
| `*` [NEW]                  | `/api/agent/custom/providers*`                   | yes  | List/upsert/delete custom (LiteLLM) providers in `models.json`. |
| `GET` [REMOVED]            | `/api/opencode/health`                           | —    | Removed with OpenCode. |
| `*` [REMOVED]              | `/api/opencode/*`                                | —    | Reverse proxy + WebSocket; removed. |
| `* ` [REMOVED]             | `/api/settings/api-key`                          | —    | Anthropic key was OpenCode-specific. Replaced by `/api/agent/auth/providers/...`. |

Frontend session endpoints proxied via `/api/projects/:id/agent/*` (handled
upstream by `agent-server`, not Appx code) include:

| Browser path                                                | Purpose |
| ----------------------------------------------------------- | ------- |
| `GET /sessions`                                             | List sessions for the project |
| `POST /sessions`                                            | Create a new session |
| `GET /sessions/models`                                      | List available models |
| `GET /sessions/{sid}`                                       | Fetch session message history |
| `GET/PATCH /sessions/{sid}/settings`                        | Per-session model + thinking-level |
| `POST /sessions/{sid}/prompt`                               | Send a user message |
| `POST /sessions/{sid}/abort`                                | Cancel an in-flight turn |
| `GET /sessions/{sid}/events` (SSE)                          | Streaming agent event channel |
| `GET /sessions/{sid}/extension-ui`                          | Pending extension UI requests |
| `POST /sessions/{sid}/extension-ui/{rid}/response`          | Resolve a confirm/select/input/editor prompt |

### Database Schema

| Table      | Change |
| ---------- | ------ |
| `projects` | [UPDATED] migration 4 no longer adds `opencode_project_id`. Existing column is left in place on already-deployed databases (no destructive down) but ignored by code. The unique partial index on `assigned_port` is unchanged. |
| `settings` | unchanged structurally; `anthropic_api_key` setting is no longer read or written. |
| `egress_log`, `egress_allowed` | unchanged. |

### Pi Session Lifecycle (Frontend State Machine)

```
                ┌──────────┐
                │   idle   │◀────────────────┐
                └────┬─────┘                 │
                     │ user submits prompt   │
                     ▼                       │
                ┌──────────┐                 │
                │ starting │                 │
                └────┬─────┘                 │
                     │ first SSE event       │
                     ▼                       │
                ┌──────────┐  message_update  │
                │streaming │──────────────────┤
                └────┬─────┘  (text/thinking/ │
                     │        tool_call deltas)
                     │ agent_end             │
                     └────────────────────────┘

  Side channels active in any state:
   - extensionRequests[]   (confirm/select/input/editor — block UI)
   - extensionStatus{}     (status badges)
   - extensionNotice       (last notify)
```

The reducer in `web/src/lib/pi-agent/reducer.ts` keeps two parallel models in
sync: the raw `AgentMessage[]` history that `agent-server` returns from
`GET /sessions/:id`, and the rendered `UiMessage[]` that `PiChatPanel` paints.
Tool calls are addressed by `(toolCallId, contentIndex)` so out-of-order or
duplicate events resolve to the same UI block.

---

## Code Review Guide

Read in this order — each section assumes the previous ones.

### 1. Database & Project Model

**`internal/db/migrations/000004_project_model.{up,down}.sql`** —
`opencode_project_id` is dropped from the up/down scripts. This is safe because
migration 4 was the column's introduction; deployed databases that already ran
it keep the column (SQLite ignores unused columns), and fresh installs simply
never get it. **Verify:** that the partial unique index on `assigned_port` still
exists in `db_test.go`'s `TestMigration4ProjectModel`.

**`internal/project/project.go`** — `Project` struct loses the
`OpenCodeProjectID` field. The on-disk field continues to coexist on legacy DBs
without breaking reads, because `projectColumns` no longer SELECTs it.

**`internal/project/store.go`** — `projectColumns` shrinks; `SetOpenCodeProjectID`
deleted. **Verify:** every call site to that method is also gone (it was only
called by the now-deleted OpenCode startup integration).

### 2. Project Scaffolding & Pi Harness

**`internal/project/manager.go:113-148`** — `scaffoldProject` now also calls
`scaffoldPiHarness(dir, proj, domain)` between writing `AGENTS.md` and running
`git init`. The `.pi/` directory is committed as part of the initial scaffold
commit, which means project authors can inspect / modify the harness like any
other project file.

**`internal/project/pi_harness.go`** — embeds `templates/pi/` via `embed.FS`
and walks it, applying `{{name}}/{{port}}/{{subdomain}}` token replacement and
giving `.py` files mode 0755 so the egress skill helper is executable.

**`internal/project/templates/pi/`** — four assets:

- `AGENTS.md` — project-local Pi system prompt. Tells the agent the assigned
  port, subdomain, and gives explicit guidance to use the `appx-egress` skill
  when network calls fail.
- `settings.json` — `enableSkillCommands: true` and an empty `packages` list.
  Third-party Pi packages are intentionally not auto-installed because they
  execute inside the agent process.
- `extensions/appx-guardrails.ts` — first-party Pi extension that intercepts
  `bash`, `write`, and `edit` tool calls. Pattern-matches destructive commands
  (`rm -rf`, `sudo`, `chmod -R`, `chmod 777`, `dd/mkfs`, `kill -9`) and
  protected paths (`.appx-internals`, `/etc/appx`, `auth.json`, `.git`,
  `.env`, `.pem/.key/.p12`). Routes confirmations to the agent-server extension
  UI bridge so they bubble up through `/sessions/:id/extension-ui`.
- `skills/appx-egress/{SKILL.md, request_egress.py}` — skill the agent invokes
  when an outbound connection is blocked. Posts to the internal listener
  (`127.0.0.1:9081/egress/request`) and blocks for up to 70 s waiting for the
  Appx user to approve, deny, or time out.

**Verify:**
- The guardrail patterns are intentionally **first-party / local to the project
  scaffold**, not loaded from a third-party Pi registry. Reasoning: anything
  loaded as a third-party Pi extension runs inside the agent process and could
  in theory disable itself. Confirm any future "shared extensions" feature
  preserves this property.
- The protected-path matcher uses `String.includes` on a normalised forward-slash
  path — adequate for the listed substrings but trivially bypassed by symlinks.
  Treat it as defence-in-depth, not a sandbox.
- `request_egress.py` mode 0755 is set in `pi_harness.go` based on `.py` suffix,
  not the embedded mode. Double-check by listing a freshly scaffolded project's
  `.pi/skills/appx-egress/` after running `task local`.

### 3. Agent-Server Reverse Proxy

**`internal/server/agent_proxy.go`** — two factory functions, both returning
`http.Handler`. Key choices to review:

- The `Director` strips inbound `Cookie` headers (so Appx's session cookie
  never reaches `agent-server`) and strips the three `X-Appx-Project-*`
  request headers before re-setting them with values from the resolved project.
  Re-setting prevents a malicious client from spoofing `X-Appx-Project-Id`
  to read another project's sessions.
- `FlushInterval: -1` is required for SSE; without it Go's `httputil` would
  buffer event chunks until the response closes.
- `http.NewResponseController(w).SetWriteDeadline(time.Time{})` is called on
  every request so the server's 60 s `WriteTimeout` doesn't cut long-lived
  SSE streams or model-thinking pauses.

**`internal/server/router.go:61-74`** — registration. Note the four explicit
methods on each path; `agent-server` uses `PATCH` for session settings, so
without `PATCH /api/projects/{id}/agent/{agentPath...}` the model-picker would
silently 405.

**Verify:**
- The `agentServerProxyHandler` requires a non-empty `id` PathValue and 404s
  on unknown projects before forwarding. Good — without this, an attacker
  with a session cookie could send an arbitrary `agentPath` and have it
  proxied verbatim to `/v1/projects//something` which `agent-server` may
  treat as global.
- `cleanAgentServerPath` uses `path.Clean` after stripping the prefix.
  Subtle: `path.Clean("/")` returns `/`, and the helper returns `prefix`
  alone in that case, so requests like `/api/projects/X/agent/` map to
  `/v1/projects/X` (no trailing slash). This is the contract `agent-server`
  expects; verify it didn't change upstream.

### 4. Egress Integration

**`deploy/agent-server.service`** — `NODE_USE_ENV_PROXY=1`,
`HTTPS_PROXY=http://127.0.0.1:9080`, `NO_PROXY=localhost,127.0.0.1`. This is
how Pi's model calls go through the egress proxy without changes to the Pi
client itself. **Verify** this is set; without it, model traffic would bypass
the allowlist.

**`internal/egress/listener.go`, `pending.go`** — unchanged in this branch but
exercised from a new caller: `request_egress.py`. The Pi skill posts host/port/
reason; the listener creates a `PendingRequest`, the dashboard's
`EgressRequestDock` polls `/api/egress/pending`, and approval calls
`PendingRegistry.Resolve(id, true)` which adds the host to the allowlist
**and** unblocks the agent.

**`web/src/components/EgressRequestDock.tsx`** — minor: switches to
`window.setTimeout/setInterval` and properly clears both, fixing a tiny leak
on unmount.

### 5. Frontend Pi Agent Stack

The Pi UI is a clean rewrite — the old `agent-core` / `agent-react` lib and the
`agent/SessionList,ChatPanel` components are deleted. Read in this order:

**`web/src/api/piAgent.ts`** — typed thin wrapper over the project-scoped
proxy. All paths are built from `agentBase(projectId)`. `formatErrorBody`
unwraps both JSON `{error}` shapes and Go's plain-text `http.Error` responses.

**`web/src/lib/pi-agent/types.ts`** — note that `AssistantMessageEvent`
includes both the new content-indexed shape (`text_start/_delta/_end`,
`thinking_*`, `toolcall_*`) and the older `tool_call_start/...` flat shape.
The reducer handles both because `agent-server` emits the new shape but
historic sessions (re-replayed via `GET /sessions/:id`) may include the old
one.

**`web/src/lib/pi-agent/reducer.ts`** — long but methodical:

- `partsFromContent` rebuilds `UiMessagePart[]` from a Pi message's
  `content[]`, indexing each part by `contentIndex`.
- `applyTextDelta` first targets the part that matches `contentIndex`; falls
  back to the last text part if the event lacks an index. This handles
  agent-server emitting unindexed deltas during early-turn buffering.
- `upsertToolPart` matches by either `toolCallId` *or* `contentIndex`. This
  is the trick that lets `tool_execution_start` (which has only `toolCallId`)
  patch a tool block first registered by `toolcall_start` (which has only
  `contentIndex` until `toolcall_end` arrives with `toolCall.id`).
- `loadHistory` does two passes: messages first, then tool results, so a
  tool result for a tool call that lives in an earlier message can find it.
- `mergeExtensionRequests` only retains *blocking* extension requests
  (`select/confirm/input/editor`) across reloads; non-blocking ones
  (`setStatus/notify/setWidget/...`) are recorded separately and don't
  persist.

**`web/src/lib/pi-agent/sessionsStore.ts`** — singleton `Map<projectId:sessionId, Entry>`
that owns one `EventSource` per active session plus a 1.5 s polling fallback
(`refreshExtensionRequests`). The polling exists because:
1. SSE may drop and the user shouldn't notice — polling pulls extension
   requests that were emitted while disconnected.
2. After an `abort`, the server-side session may still flush a final
   `agent_end` event the EventSource missed; the poll re-syncs history.

**`web/src/lib/pi-agent/useSession.ts`** — minimal `useSyncExternalStore`
adapter that hands components a stable snapshot.

**Components.** `PiAgentPane` is the two-pane layout (sidebar + chat). The
chat panel `web/src/components/pi-agent/PiChatPanel.tsx` is the most
behaviour-dense file — read its `ExtensionRequestPanel` carefully: it's how
`appx-guardrails`'s "Approve `rm -rf`?" prompt actually surfaces in the UI
and how the user response (`{confirmed: true}`/`{value: ...}`/`{cancelled: true}`)
goes back to the Pi extension.

**Verify:**
- `PiChatPanel` re-fetches model settings (`getPiSessionSettings`) when
  `state.status` returns to `'idle'` after streaming. This is intentional —
  Pi can change the model server-side mid-turn (e.g. when a subscription
  refreshes) and the dropdown should reflect that.
- The reducer's `applyTextDelta` *appends* deltas to the matching text part.
  If a text-delta arrives with a `contentIndex` that doesn't yet exist
  (because `text_start` was lost), it inserts a new part rather than
  silently dropping. Confirm this matches `agent-server`'s contract.
- `state.error` is set both on HTTP-level errors and on `agent_end` after
  an aborted send. Inspect the error banner for non-fatal "info" cases —
  if Pi reports a soft retry, the banner shouldn't stick.

### 6. Settings Rebuild

**`web/src/pages/Settings.tsx`** — single 1400-line page. Three logical zones:

1. **Provider list** — `getAgentAuthProviders()` returns one row per Pi
   provider with `configured/source/credentialType/supportsApiKey/supportsSubscription`.
   Rows are sorted: configured first, then by curated priority (`anthropic`,
   `openai-codex`, `openai`, `google`), then by available model count.
2. **Selected-provider editor** — exposes either an API-key input *or* the
   subscription OAuth flow, depending on `credentialMode`. The subscription
   flow polls `/api/agent/auth/subscription/{id}` every 2.5 s until the
   `status` reaches `complete | error | cancelled`. There's a manual fallback
   path (paste redirect URL or auth code) for `anthropic` and `openai-codex`,
   triggered via "Use manual fallback". Switching `credentialMode` cancels any
   in-flight OAuth flow first.
3. **Custom provider editor** — collapsible. `thinkingMap()` and `compatFor()`
   produce the hairy `models.json` payload. The form distinguishes two
   reasoning presets (`standard`, `deepseek`) so a single LiteLLM endpoint
   can host both Anthropic-style and DeepSeek-style models.

**Verify:**
- API keys and OAuth credentials never round-trip to the browser. The
  `AgentAuthProvider` shape only reports whether something is set.
- The custom-provider save guards against `apiKey` being empty *unless* the
  provider already has a stored key (`apiKeyConfigured`). Editing a saved
  provider can therefore tweak metadata without re-entering the secret. Make
  sure backend code on `/api/agent/custom/providers` enforces the same rule.

### 7. Deployment

**`deploy/system-setup.sh`** — creates `appx-agent` user (replaces `opencode`),
shared `projects` group with setgid'd `2770` perms on the projects directory,
and a private `/home/appx-agent/.pi/agent` for Pi auth/credentials. It also:

- Stops and removes any pre-existing `opencode` service.
- Pkills lingering agent-server processes started by either the old `opencode`
  user or `appx-agent` if the unit isn't running, before installing the new
  unit.
- Migrates `/home/opencode/.pi → /home/appx-agent/.pi` if present, preserving
  any auth blobs the user already configured.

**`deploy/tools-install.sh`** — pins Pi via `deploy/pi-version` (currently
`0.75.4`), uninstalls leftover `opencode-ai` npm package, and installs
`agent-server` from a sibling checkout (`../agent-server`) via
`npm ci && npm run build && npm install -g .`.

**`deploy/agent-server.service`** — annotated above. Note `Before=appx.service`
in `[Unit]` so systemd starts agent-server first; appx connects to it on
demand but logs less noise on first boot when both come up together.

**Verify:**
- `task server:deploy` after this branch will leave the old `opencode.service`
  disabled but file-removed. Confirm no other systemd unit references it.
- `agent-server` runs in the working directory of the projects root and reads
  per-project files relative to `X-Appx-Project-Dir`. If `agent-server` is
  ever started without `AGENT_SERVER_MODE=multi`, the project-scoped path
  layout breaks silently. The unit pins it correctly; just confirm no tests
  override it.

---

## Testing Guide

### Automated Coverage

| File | What it covers |
| ---- | -------------- |
| `internal/db/db_test.go` | Migration 4 no longer probes `opencode_project_id`. |
| `internal/server/router_test.go` | Replaces every OpenCode-specific test (proxy, WebSocket upgrade, `/api/opencode/health`, API-key injection) with agent-server proxy assertions: project-scoping, header injection, write-deadline clear, 404 on unknown project. The `TestOpenCodeProxy_WebSocketUpgrade_Integration` test against a real backend is removed. |
| `internal/project/{store,manager}_test.go` | Updated test schema and assertions to drop `opencode_project_id`. |

**Gaps worth noting:**

- No automated test exercises the **Pi harness scaffold** end-to-end (writing
  `.pi/extensions/...` and running `git init` with the new files). The
  manager tests verify scaffolding succeeds; they don't assert specific files
  exist under `.pi/`. Add at least a smoke test that lists `.pi/AGENTS.md`
  and `.pi/skills/appx-egress/request_egress.py` in a fresh project.
- The frontend reducer is large and untested. Consider adding a minimal Vitest
  suite that replays a recorded `events.jsonl` from a real session — most
  reducer regressions show up as "tool block stays pending forever" or
  "duplicate text".
- The egress skill helper (`request_egress.py`) has no test of its own. Manual
  verification (below) is the only check.

### Manual Verification Checklist

```
[ ] Build & run: `task local`. Dashboard loads at http://127.0.0.1.sslip.io:8080.
[ ] Sibling agent-server running on :4001 in multi mode (per README "Local development").
[ ] Header on dashboard reads "PI" with a green dot (no OpenCode status).

[ ] Settings → Agent Credentials:
    [ ] Provider list shows at least anthropic / openai / openai-codex / google with
        configured=false and "Not set" labels.
    [ ] Select "anthropic". Mode toggle shows Subscription / API key.
    [ ] In API key mode, paste an invalid key. Save. Expect server error in red banner.
    [ ] Paste a real test key. Save → "Stored" appears, available model count > 0.
    [ ] Click "Remove credential" → returns to "Not set".
    [ ] In Subscription mode, click Subscription Login. A flow panel appears with
        an authUrl link and a "Use manual fallback" toggle.
    [ ] Cancel; flow disappears; selecting API-key cleanly cancels in-flight flow.

[ ] Settings → Custom Provider:
    [ ] Open the editor (+). Default is LiteLLM at 127.0.0.1:4000 with openai-responses.
    [ ] Save with a fake key. Provider row appears with "Key stored" + 1 model.
    [ ] Re-open by clicking the row; apiKey input shows "Stored" placeholder.
    [ ] Edit context window to a non-integer; expect inline error.
    [ ] Remove → row disappears.

[ ] Create a project ("hello"). Verify:
    [ ] {data}/projects/hello/.pi/AGENTS.md mentions the assigned port and subdomain.
    [ ] {data}/projects/hello/.pi/extensions/appx-guardrails.ts exists.
    [ ] {data}/projects/hello/.pi/skills/appx-egress/request_egress.py is +x.
    [ ] git log shows one "Initial project scaffold" commit including the .pi/ tree.

[ ] Open the project → Agent tab:
    [ ] PiSessionList renders (left pane), empty state.
    [ ] Click "+ New". A session appears, becomes selected, header shows "PI AGENT idle".
    [ ] Model dropdown lists the configured providers' available models.
    [ ] Send "say hi". Status flips to "starting" → "streaming" → "idle".
        Assistant message renders with streaming text, no flicker.
    [ ] Send "run rm -rf /tmp/foo". Guardrail extension intercepts; an extension
        panel appears with Approve / Deny. Deny → tool result reports the block.
    [ ] Send "fetch a Go module". The agent tries to reach proxy.golang.org;
        if not in allowlist, the appx-egress skill posts a request and the
        EgressRequestDock surfaces it. Approve → host added to allowlist;
        deny → agent reports failure.

[ ] Reload the page mid-stream. Session list still shows the session; opening
    it should restore history (loadHistory) and resume any pending extension
    request.

[ ] Stop a streaming turn with the red Stop button → status returns to idle,
    last message is finalised non-streaming.

[ ] Visit a project subdomain (e.g. http://hello.127.0.0.1.sslip.io:8080) once
    the project has a dev server running on its assigned port. Confirm the
    appx auth cookie is honoured and the proxy passes through.
```

---

## Architecture and Code Pitfalls

These are issues present after this branch lands. None are blocking; documenting
them so the next pass can pick them up.

**Severity: Medium**

- **`internal/server/agent_proxy.go:61-105`** — there's no max-body or
  per-request timeout on the agent proxy. A pathological request body could
  hold a connection open up to the global `WriteTimeout` (which is
  intentionally cleared here). Consider a separate guard for non-SSE methods.
- **`web/src/lib/pi-agent/sessionsStore.ts:28`** — `entries` is module-global.
  In dev mode with hot module reload it can leak old `EventSource`s if Vite
  rebuilds this module without unmounting consumers. Run-time impact is small
  (memory + dangling SSE connections to agent-server), but it complicates
  debugging "why is my session not updating." Consider a `__resetForHmr` hook.
- **`internal/project/templates/pi/extensions/appx-guardrails.ts:42-45`** —
  `pathRisk` checks substring matches. A path like `..//.git//config` will
  match `.git/`, but a symlinked `safe/dir → .git` won't. This extension is
  defence-in-depth on top of OS perms (the agent runs as `appx-agent`); good,
  but worth labelling that explicitly.

**Severity: Low**

- **`web/src/lib/pi-agent/reducer.ts:412-421`** — when `message_start` for a
  user message duplicates the locally-optimistic user prompt (text-equal),
  the duplicate is dropped. If the agent emits the user message with extra
  metadata (timestamp drift, content trimming), the dedup heuristic misses
  and the user sees their prompt twice. The current heuristic is "exact text
  match on first text part" — fragile. Consider dedup by `messageId` if
  `agent-server` provides one.
- **`internal/server/agent_proxy.go:81`** — the proxy deletes the inbound
  `Cookie` header. It does not delete `Authorization`. If a future feature
  adds bearer auth to Appx itself, the bearer token will currently be
  forwarded to agent-server. Add a `req.Header.Del("Authorization")` before
  conditionally re-setting it from `token`.
- **`internal/project/pi_harness.go:55`** — `chmod +x` is applied to any
  `.py` suffix. If a user names a project file `evil.py` the scaffolder
  doesn't see (it only walks the embedded FS), so this is fine; but adding a
  non-Python executable in the future will silently install at 0644. Move
  the mode decision to a per-template manifest if the harness grows.
- **`PiChatPanel.tsx`** — `pinnedRef.current` decides whether to autoscroll.
  When the user is not pinned, error banners and extension panels still push
  the input bar down without scroll-correcting; a long extension prompt can
  hide the agent's last response. Minor UX issue.

---

## Fixed Pitfalls

Issues found and corrected during this branch — recorded so reviewers
understand why the code looks the way it does today.

> **Problem (commit `f52cb4a`):** session state could get stuck in `streaming`
> if SSE dropped right after `agent_end` without the frontend seeing it.
> **Fix:** the polling fallback in `sessionsStore.ts` re-fetches
> `getPiSessionSettings`; if `isStreaming === false` and the local status
> isn't idle, it reloads history and dispatches a synthetic `agent_end`.
> Without this, "Stop" was the only way out of a phantom streaming state.

> **Problem (commit `e21d8be`):** Pi's HTTPS provider calls bypassed the egress
> allowlist because Node didn't honour `HTTPS_PROXY` by default.
> **Fix:** added `NODE_USE_ENV_PROXY=1` to `agent-server.service`. This is
> a one-line change that's easy to drop on a future systemd refactor; the
> service comment now documents why.

> **Problem (commit `e4b22ae`):** when an extension request arrived during a
> page reload, it was emitted on SSE before the EventSource was attached and
> never resurfaced. **Fix:** the sessions store explicitly fetches
> `listPiExtensionUiRequests` once at attach, in addition to subscribing to
> SSE. Combined with the 1.5 s polling, this guarantees pending requests
> are always visible within ~1.5 s of opening the session.

> **Problem (commit `5fa2afb`):** an attacker with a valid Appx session could
> send arbitrary `X-Appx-Project-*` headers to the agent proxy and read other
> projects' sessions. **Fix:** the proxy `Director` always deletes those
> headers off the inbound request and re-sets them from the resolved project
> only after `pm.Get(projectID)` succeeds. Verified by
> `TestAgentServerProxy_RejectsHeaderSpoofing` (in `router_test.go`).

> **Problem (commit `941ecd6`):** Pi's content-indexed streaming events
> (`text_delta` with `contentIndex`) were being applied to whichever text
> part was last, leading to interleaved text when the model emitted multiple
> content blocks. **Fix:** `applyTextDelta` and `setTextContent` look up the
> target part by `contentIndex`. Falls back to "last text part" only when
> the event has no index (older sessions or partial events).

> **Problem (commits `db7e47a`, `c752611`):** removing OpenCode while the
> health check was still hard-wired caused the dashboard to show a permanent
> red "OpenCode unhealthy" badge. **Fix:** dropped `OpenCodeStatus` entirely
> and replaced with a static "PI" indicator. The agent runtime is now an
> implementation detail, not a UI concern.

---

## TODOs and Future Improvements

- **`internal/project/templates/pi/extensions/appx-guardrails.ts`** — the
  guardrail patterns are hard-coded. Likely want a future settings table for
  user-editable allow/deny patterns.
- **`web/src/lib/pi-agent/reducer.ts`** — large enough to deserve unit tests
  with recorded SSE fixtures. Currently regression coverage relies on manual
  testing through the UI.
- **`internal/server/agent_proxy.go`** — only `GET/POST/PATCH/DELETE` are
  registered. Add `PUT` if `agent-server` ever gains PUT-style endpoints
  (currently it doesn't).
- **`deploy/agent-server.service`** — `SESSIONS_DIR` is a single global path
  under `/home/appx-agent/.pi/agent/appx-default-sessions`. If/when
  `agent-server` supports per-project sessions on disk, switch this to
  derive from `X-Appx-Project-Dir`.
- **No explicit Pi version pinning UI.** `deploy/pi-version` is operator-only;
  a small Settings card showing the running Pi + agent-server versions would
  help self-hosters debug "why is feature X missing." `agent-server` likely
  already exposes a `/v1/version` endpoint; surface it through `/api/agent/`.
- **`docs/architecture/`** — older phase docs (`arch_phase_*.md`) reference
  OpenCode and Docker; they should be marked historical or refreshed. This
  document does not attempt to do that.
