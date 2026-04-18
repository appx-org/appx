# Phase 8 Plan: Native Clients — Mobile, Desktop, and Cross-Platform Architecture

**Date:** 2026-04-08
**Status:** Draft
**Scope:** React Native mobile app, Electron desktop app, monorepo restructure with shared packages
**Prerequisites:** Phase 6 (bearer token auth — native clients cannot use browser session cookies)

---

## Vision

Phase 7 gives every user a dedicated server. Phase 8 gives them a native client for every device. The same session, the same projects, the same agent — accessible from a phone or a desktop app with platform-native UX.

The architectural insight that makes this tractable: **separate logic from rendering**. React hooks can be shared across React (web) and React Native (mobile) without modification. The rendering layer — the JSX primitives, the styles, the animations — is the only part that must be written per-platform. In an agent chat UI, that's a small fraction of the total code.

---

## The Core Pattern: Headless Hooks

Every component has two layers:

```
Logic layer   — state, events, API calls, derived values    ← shareable (pure TypeScript)
Render layer  — JSX primitives, styles, animations          ← platform-specific
```

A hook like `useAgentStream` returns `{ messages, thinking, send }`. Web renders it with `<div>` and CSS. Mobile renders it with `<FlatList>` and `StyleSheet`. The hook — which contains all the interesting logic — is written once.

This is the same model used by TanStack Query, Zustand, and Radix UI: behaviour and state are decoupled from presentation. React Native is not a constraint; it's just another renderer.

### What can be shared

| Layer | Shareable |
|---|---|
| API functions (`sessionList`, `sessionPrompt`, `eventSubscribe`) | 100% |
| TypeScript types | 100% |
| Business logic (parsing, formatting, validation) | ~90% |
| Custom hooks (state machines, event subscriptions) | ~80% |
| Design tokens (colour values, spacing scale, font names) | values only |
| Rendered components | 0% |
| Styles | 0% |

The rendered components are the smallest part. Most of what makes the agent UI work — the SSE streaming logic, the session state machine, the file reference parsing — lives in hooks.

---

## Architecture

```
packages/
  api/          ← sessionList, sessionCreate, sessionPrompt, eventSubscribe
                  Pure fetch — works in browser, React Native, and Node.js
  agent/        ← useAgentStream, useSessionManager, useFileReferences
                  Hooks that consume packages/api, return plain state + callbacks
  tokens/       ← colour values, spacing scale, font names (not CSS, not StyleSheet)
                  Consumed differently per platform but values are the same

apps/
  web/          ← React + Vite (appx dashboard, current codebase)
  desktop/      ← Electron shell wrapping apps/web (zero additional UI code)
  mobile/       ← React Native, imports packages/* for logic, writes native UI
```

Tooling: pnpm workspaces + Turborepo. Each package has its own `package.json`; `apps/*` reference `packages/*` by name. `task build` at the root builds all packages in dependency order.

---

## Components

### 1. `packages/api` — Platform-Agnostic API Client

Extract `web/src/api/opencode.ts` into a shared package. No changes to the implementation — it already uses plain `fetch` with no Node.js globals. The TypeScript types are imported with `import type` from `@opencode-ai/sdk`, which is a zero-runtime-cost operation.

The package exports:

```ts
export { sessionList, sessionCreate, sessionPrompt, eventSubscribe }
export type { Session, Event, TextPart }
```

React Native's `fetch` is compatible with the browser's. The SSE streaming via `fetch` + `ReadableStream` also works in React Native (RN 0.73+).

`packages/api/package.json`:

```json
{
  "name": "@appx/api",
  "type": "module",
  "exports": { ".": "./src/index.ts" },
  "peerDependencies": { "@opencode-ai/sdk": "*" }
}
```

### 2. `packages/agent` — Shared Logic Hooks

Three hooks that cover the patterns the user interacts with most:

#### `useAgentStream(sessionId, signal)`

Subscribes to the OpenCode SSE event stream, maintains the message list, and tracks thinking state. Returns everything needed to render a chat interface without knowing what that interface looks like.

```ts
export function useAgentStream(sessionId: string) {
  const [messages, setMessages] = useState<Message[]>([]);
  const [thinking, setThinking] = useState(false);

  useEffect(() => {
    const abort = new AbortController();
    (async () => {
      for await (const event of eventSubscribe(abort.signal)) {
        if (event.type === 'session.status') {
          setThinking(event.properties.status.role === 'assistant');
        }
        if (event.type === 'message.part.updated') {
          // upsert message by id
        }
      }
    })();
    return () => abort.abort();
  }, [sessionId]);

  const send = useCallback((text: string) => sessionPrompt(sessionId, text), [sessionId]);

  return { messages, thinking, send };
}
```

Web renders `thinking` as a pulsing CSS animation. Mobile renders it as a React Native `Animated` spinner. Same data, different pixels.

#### `useSessionManager(projectDir)`

Manages the session list for a project: fetching, creating, and tracking the active session.

```ts
export function useSessionManager(projectDir: string) {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [activeId, setActiveId] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    const data = await sessionList(projectDir);
    setSessions(data);
  }, [projectDir]);

  useEffect(() => { refresh(); }, [refresh]);

  const create = async () => {
    const s = await sessionCreate(projectDir);
    setSessions(prev => [...prev, s]);
    setActiveId(s.id);
  };

  return { sessions, activeId, setActiveId, create, refresh };
}
```

Web renders this as a sidebar list with a `+ New` button. Mobile renders it as a bottom sheet or drawer. Same hook, both platforms.

#### `useFileReferences(sessionId)`

Tracks files mentioned in the agent conversation (created, edited, read). Returns a deduplicated list for the file reference panel.

```ts
export function useFileReferences(sessionId: string) {
  const [files, setFiles] = useState<FileReference[]>([]);

  useEffect(() => {
    const abort = new AbortController();
    (async () => {
      for await (const event of eventSubscribe(abort.signal)) {
        if (event.type !== 'file.edited') continue;
        if (event.properties.sessionID !== sessionId) continue;
        setFiles(prev => upsertFile(prev, event.properties));
      }
    })();
    return () => abort.abort();
  }, [sessionId]);

  return { files };
}
```

### 3. `packages/tokens` — Design Tokens

Colour values and spacing constants, exported as plain JavaScript objects. No CSS variables, no `StyleSheet.create` — just values. Each platform consumes them in its own way.

```ts
// packages/tokens/src/index.ts
export const colors = {
  bg:      '#0a0b0f',
  surface: '#12141a',
  text:    '#e2e4ed',
  muted:   '#5a5f72',
  green:   '#3ddc84',
  cyan:    '#22d3ee',
  blue:    '#3b82f6',
  red:     '#f87171',
  border:  '#1e2130',
} as const;

export const spacing = {
  xs: 4, sm: 8, md: 12, lg: 16, xl: 24,
} as const;

export const fontFamilies = {
  mono: "'JetBrains Mono', monospace",  // web
  monoNative: 'JetBrains Mono',         // React Native
  sans: "'DM Sans', sans-serif",
  sansNative: 'DM-Sans',
} as const;
```

Web continues to use CSS variables from `index.css` (derived from the same values). Mobile passes these values directly to `StyleSheet.create`.

### 4. `apps/desktop` — Electron Shell

Electron runs Chromium. `apps/web` runs inside it with zero modifications. The desktop app is a thin shell:

```
apps/desktop/
  main.js        ← Electron main process: creates BrowserWindow, loads apps/web
  package.json   ← electron dependency
  build/         ← electron-builder config for .dmg / .exe / .AppImage packaging
```

`main.js` in its entirety:

```js
const { app, BrowserWindow } = require('electron');

app.whenReady().then(() => {
  const win = new BrowserWindow({ width: 1280, height: 800 });
  win.loadURL('http://localhost:8443');  // or the user's appx.app URL
});
```

No UI code. No component sharing problem. Web and desktop are the same codebase.

If offline support or deeper OS integration is needed later (system tray, native notifications, local file picker), those can be added to `main.js` without touching the React components.

### 5. `apps/mobile` — React Native App

The mobile app imports hooks from `packages/agent` and types from `packages/api`, and renders them with React Native primitives. It does not import anything from `apps/web`.

Authentication uses bearer tokens (Phase 6). The app stores the token in the OS keychain via `react-native-keychain` and passes it in `Authorization: Bearer` headers through `packages/api`'s fetch calls.

Key screens and the hooks they consume:

| Screen | Hook |
|---|---|
| Session list | `useSessionManager(projectDir)` |
| Agent chat | `useAgentStream(sessionId)` |
| File references | `useFileReferences(sessionId)` |
| Settings | Direct API calls via `packages/api` |

Navigation: React Navigation (stack + bottom tabs). No web routing.

---

## Monorepo Structure

```
appx/                          ← git root
  packages/
    api/
      src/
        index.ts               ← sessionList, sessionCreate, sessionPrompt, eventSubscribe
      package.json
      tsconfig.json
    agent/
      src/
        useAgentStream.ts
        useSessionManager.ts
        useFileReferences.ts
        index.ts
      package.json
      tsconfig.json
    tokens/
      src/
        index.ts
      package.json
      tsconfig.json
  apps/
    web/                       ← current codebase (web/src/** → apps/web/src/**)
      src/
        api/opencode.ts        ← thin re-export of @appx/api (for backward compat during migration)
        components/agent/      ← renders @appx/agent hooks with web primitives
      package.json
      vite.config.ts
    desktop/
      main.js
      package.json
    mobile/
      src/
        screens/
        components/            ← React Native UI, uses @appx/agent hooks
      package.json
      metro.config.js
  pnpm-workspace.yaml
  turbo.json
  Taskfile.yml                 ← updated with build:web, build:desktop, build:mobile tasks
```

---

## Migration Path (Web → Monorepo)

The migration does not need to happen all at once. The intermediate step: keep the current `web/` layout, but extract the packages in place.

**Step 1** — Extract `packages/api`: move `web/src/api/opencode.ts` to `packages/api/src/index.ts`. Add a re-export shim at the old path so no other file changes. Build still works.

**Step 2** — Extract `packages/agent`: move hooks out of components into `packages/agent/src/`. Components now import from `@appx/agent`. No behaviour change.

**Step 3** — Move `web/` to `apps/web/`. Update `go:embed` path in `cmd/appx/main.go`. Update `Taskfile.yml`.

**Step 4** — Add `apps/desktop/` and `apps/mobile/` as new packages.

Each step is independently shippable. Phase 8 does not require completing all four before any value is delivered.

---

## What Does NOT Change

- The Go backend — no changes required. Native clients use bearer token auth (Phase 6) and the same REST/SSE API.
- The OpenCode proxy at `/api/opencode/*` — mobile and desktop hit the same endpoints as the web app.
- The subdomain routing for agent-built apps — accessible from any platform's browser.
- The appx dashboard's visual design — web components are not touched by the mobile work.

---

## Open Questions

1. **React Native SSE compatibility** — `eventSubscribe` uses `fetch` + `ReadableStream`. React Native 0.73+ includes the Fetch API with streaming support, but behaviour with long-lived SSE connections on mobile (background state, network handoff) needs testing. Fallback: use a polling endpoint for mobile if SSE proves unreliable.

2. **Bearer token storage on mobile** — `react-native-keychain` is the standard approach. Verify it works on both iOS (Keychain) and Android (Keystore) before committing to it.

3. **Electron distribution** — `electron-builder` can produce `.dmg`, `.exe`, and `.AppImage`. Auto-update via `electron-updater` needs a signed binary and an update server. For Phase 8, manual downloads from GitHub releases are sufficient.

4. **Offline mode** — mobile users may have intermittent connectivity. The current architecture requires a live connection to the appx server. Local caching of session history (SQLite via `react-native-mmkv`) is a future concern but the hook interfaces do not need to change to support it.

5. **Push notifications** — agent completion events could trigger native push notifications on mobile. This requires a notification service (APNs/FCM) and a webhook from appx to that service. Out of scope for Phase 8 but worth designing the hook return types to include a `status` field that a future notification layer can consume.

6. **Server URL configuration** — the mobile app needs to know the user's appx server URL. Options: QR code scan from the web dashboard, manual entry, or auto-discovery via the Phase 7 control plane if the user signed up for hosted service. Simplest first: manual entry + QR code.
