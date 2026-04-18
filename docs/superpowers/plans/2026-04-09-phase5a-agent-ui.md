# Phase 5a: Custom Agent Frontend for OpenCode — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a basic but working custom React frontend that operates OpenCode agents remotely — markdown rendering, streaming, tool call display, permissions, questions — using the OpenCode SDK client.

**Architecture:** The `@opencode-ai/sdk/v2/client` entry point provides a browser-safe typed client with SSE support. A headless core layer (`web/src/lib/agent-core/`) manages state via pure event reducers on top of the SDK. React hooks (`web/src/lib/agent-react/`) adapt the core for React components. UI components render message parts (text→markdown, tool→collapsible card, reasoning→details), permission/question docks, and status indicators. The existing `/api/opencode/*` Go proxy is unchanged.

**Tech Stack:** React 19, TypeScript 5.9, Vite 8, `@opencode-ai/sdk` (v2/client entry), `marked`, `dompurify`

**Key References:**
- Spec: `docs/plans/phase_5a_plan.md`
- Architecture: `docs/architecture/appx_knos_v1.md`
- OpenCode API guide: `/Users/max/misc/pj/misc/opencode/docs/remote_server_and_custom_ui.md`
- OpenCode event reducer pattern: `/Users/max/misc/pj/misc/opencode/packages/app/src/context/global-sync/event-reducer.ts`
- OpenCode SSE setup: `/Users/max/misc/pj/misc/opencode/packages/app/src/context/global-sdk.tsx`

**Styling convention:** Inline styles via `Record<string, React.CSSProperties>`. Use CSS variables from `web/src/index.css` (`var(--bg)`, `var(--surface)`, `var(--border)`, `var(--text)`, `var(--muted)`, `var(--cyan)`, `var(--green)`, `var(--red)`, `var(--yellow)`, `var(--blue)`). Fonts: `'DM Sans'` for UI, `'JetBrains Mono'` for code/labels.

**SDK import rule:** Always `import { ... } from "@opencode-ai/sdk/v2/client"`. Never import from `@opencode-ai/sdk` (pulls Node-only server code, breaks browsers).

---

## File Structure

### New files to create

```
web/src/lib/agent-core/
  types.ts              # Re-export SDK types needed by UI
  reducers.ts           # Pure event→state reducer (SessionState + applyEvent)
  connection.ts         # SSE subscription, heartbeat detection, reconnect

web/src/lib/agent-react/
  useSession.ts         # Session state via useReducer, initial message load
  useEventStream.ts     # SSE lifecycle tied to React component mount/unmount
  usePermissions.ts     # Permission/question respond actions via SDK

web/src/components/
  Markdown.tsx          # marked + DOMPurify, code copy button, streaming-safe
  ToolCallCard.tsx      # Generic collapsible tool card with status badge
  PermissionDock.tsx    # Permission request UI: allow/deny/always
  QuestionDock.tsx      # Question UI: radio options, free text, submit
  StatusBar.tsx         # Agent status + connection health indicators
```

### Existing files to modify

```
web/src/api/opencode.ts                    # Replace raw fetch with SDK client factory
web/vite.config.ts                         # Remove process.env shim
web/src/components/agent/ChatPanel.tsx      # Rewrite with hooks + message parts
web/src/components/agent/SessionList.tsx    # Add delete, titles, status indicator
web/src/pages/Project.tsx                   # Wire docks + status bar into layout
web/package.json                           # Add marked + dompurify dependencies
```

---

## Task 1: Install Dependencies and Fix SDK Import

**Files:**
- Modify: `web/package.json`
- Modify: `web/src/api/opencode.ts`
- Modify: `web/vite.config.ts`

- [ ] **Step 1: Install marked and dompurify**

```bash
cd web && npm install marked dompurify && npm install -D @types/dompurify
```

- [ ] **Step 2: Replace opencode.ts with SDK client factory**

Replace the entire contents of `web/src/api/opencode.ts` with:

```typescript
import { createOpencodeClient, type OpencodeClient } from '@opencode-ai/sdk/v2/client';

export type { OpencodeClient };
export type {
  Session,
  Message,
  UserMessage,
  AssistantMessage,
  Part,
  TextPart,
  ToolPart,
  ReasoningPart,
  ToolState,
  Event,
  EventMessagePartDelta,
  EventMessageUpdated,
  EventMessagePartUpdated,
  EventPermissionAsked,
  EventPermissionReplied,
  EventQuestionAsked,
  EventQuestionReplied,
  EventSessionStatus,
  EventSessionIdle,
  EventSessionCreated,
  EventSessionUpdated,
  EventSessionDeleted,
  EventTodoUpdated,
  PermissionRequest,
  QuestionRequest,
  QuestionInfo,
  QuestionOption,
  Todo,
  SessionStatus,
  FileDiff,
} from '@opencode-ai/sdk/v2/client';

const clients = new Map<string, OpencodeClient>();

/** getClient returns a cached SDK client scoped to a project directory. */
export function getClient(directory: string): OpencodeClient {
  let client = clients.get(directory);
  if (!client) {
    client = createOpencodeClient({
      baseUrl: `${window.location.origin}/api/opencode`,
      directory,
    });
    clients.set(directory, client);
  }
  return client;
}
```

- [ ] **Step 3: Remove process.env shim from vite.config.ts**

Replace the entire contents of `web/vite.config.ts` with:

```typescript
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
})
```

- [ ] **Step 4: Verify build compiles**

```bash
cd /Users/max/misc/pj/appx && task web
```

Expected: Compiles cleanly. No `process.env` or `node:child_process` errors.

Note: there will be TypeScript errors in ChatPanel.tsx and SessionList.tsx because they import old functions from opencode.ts. That's expected — we fix those in later tasks.

- [ ] **Step 5: Commit**

```bash
git add web/package.json web/package-lock.json web/src/api/opencode.ts web/vite.config.ts
git commit -m "feat: switch to OpenCode SDK v2/client browser-safe import

Replace raw fetch wrapper with createOpencodeClient() from the browser-safe
@opencode-ai/sdk/v2/client entry point. Add marked + dompurify deps.
Remove process.env shim that was workaround for wrong SDK import."
```

---

## Task 2: Agent Core — Types and Reducers

**Files:**
- Create: `web/src/lib/agent-core/types.ts`
- Create: `web/src/lib/agent-core/reducers.ts`

- [ ] **Step 1: Create types.ts**

Create `web/src/lib/agent-core/types.ts`:

```typescript
import type {
  Message,
  Part,
  PermissionRequest,
  QuestionRequest,
  Todo,
} from '@opencode-ai/sdk/v2/client';

/** SessionState holds all UI-relevant state for one active session. */
export interface SessionState {
  messages: Message[];
  parts: Record<string, Part[]>;
  status: 'idle' | 'running' | 'error';
  pendingPermissions: PermissionRequest[];
  pendingQuestions: QuestionRequest[];
  todos: Todo[];
  error: string | null;
}

export const initialSessionState: SessionState = {
  messages: [],
  parts: {},
  status: 'idle',
  pendingPermissions: [],
  pendingQuestions: [],
  todos: [],
  error: null,
};
```

- [ ] **Step 2: Create reducers.ts**

Create `web/src/lib/agent-core/reducers.ts`:

```typescript
import type { Event, Message, Part } from '@opencode-ai/sdk/v2/client';
import type { SessionState } from './types';

/** binarySearch finds the index of an item by id in a sorted array. Returns -1 if not found. */
function findIndex(arr: { id: string }[], id: string): number {
  for (let i = 0; i < arr.length; i++) {
    if (arr[i].id === id) return i;
  }
  return -1;
}

/** upsertById replaces an existing item or appends a new one. */
function upsertById<T extends { id: string }>(arr: T[], item: T): T[] {
  const idx = findIndex(arr, item.id);
  if (idx >= 0) {
    const next = [...arr];
    next[idx] = item;
    return next;
  }
  return [...arr, item];
}

/** removeById returns a new array without the item matching id. */
function removeById<T extends { id: string }>(arr: T[], id: string): T[] {
  return arr.filter((x) => x.id !== id);
}

/** applyEvent is a pure reducer that applies an SSE event to session state. */
export function applyEvent(state: SessionState, event: Event): SessionState {
  switch (event.type) {
    case 'message.updated': {
      const msg = event.properties.info as Message;
      return {
        ...state,
        messages: upsertById(state.messages, msg),
      };
    }

    case 'message.removed': {
      const { messageID } = event.properties;
      const { [messageID]: _, ...restParts } = state.parts;
      return {
        ...state,
        messages: state.messages.filter((m) => m.id !== messageID),
        parts: restParts,
      };
    }

    case 'message.part.updated': {
      const part = event.properties.part as Part;
      const msgId = part.messageID;
      const existing = state.parts[msgId] ?? [];
      return {
        ...state,
        parts: {
          ...state.parts,
          [msgId]: upsertById(existing, part),
        },
      };
    }

    case 'message.part.removed': {
      const { messageID, partID } = event.properties;
      const existing = state.parts[messageID];
      if (!existing) return state;
      return {
        ...state,
        parts: {
          ...state.parts,
          [messageID]: removeById(existing, partID),
        },
      };
    }

    case 'message.part.delta': {
      const { messageID, partID, field, delta } = event.properties;
      const existing = state.parts[messageID];
      if (!existing) return state;
      const idx = findIndex(existing, partID);
      if (idx < 0) return state;
      const part = { ...existing[idx] } as Record<string, unknown>;
      part[field] = ((part[field] as string) ?? '') + delta;
      const next = [...existing];
      next[idx] = part as Part;
      return {
        ...state,
        parts: { ...state.parts, [messageID]: next },
      };
    }

    case 'session.status': {
      const { status } = event.properties;
      return {
        ...state,
        status: status.type === 'busy' ? 'running' : 'idle',
      };
    }

    case 'session.idle': {
      return { ...state, status: 'idle' };
    }

    case 'session.error': {
      const err = event.properties.error;
      return {
        ...state,
        status: 'error',
        error: err ? JSON.stringify(err) : 'Unknown error',
      };
    }

    case 'permission.asked': {
      const perm = event.properties;
      return {
        ...state,
        pendingPermissions: [...state.pendingPermissions, perm],
      };
    }

    case 'permission.replied': {
      const { requestID } = event.properties;
      return {
        ...state,
        pendingPermissions: state.pendingPermissions.filter(
          (p) => p.id !== requestID,
        ),
      };
    }

    case 'question.asked': {
      const q = event.properties;
      return {
        ...state,
        pendingQuestions: [...state.pendingQuestions, q],
      };
    }

    case 'question.replied':
    case 'question.rejected': {
      const { requestID } = event.properties;
      return {
        ...state,
        pendingQuestions: state.pendingQuestions.filter(
          (q) => q.id !== requestID,
        ),
      };
    }

    case 'todo.updated': {
      return {
        ...state,
        todos: event.properties.items,
      };
    }

    default:
      return state;
  }
}
```

- [ ] **Step 3: Verify build compiles**

```bash
cd /Users/max/misc/pj/appx && task web
```

Expected: Compiles (ignoring errors in ChatPanel/SessionList from old imports).

- [ ] **Step 4: Commit**

```bash
git add web/src/lib/agent-core/
git commit -m "feat: add agent-core types and event reducer

Pure TypeScript with no React dependency. SessionState holds messages, parts,
permissions, questions, todos. applyEvent reducer handles all SSE event types
needed for P0: message CRUD, part deltas, permissions, questions, session status."
```

---

## Task 3: Agent Core — SSE Connection

**Files:**
- Create: `web/src/lib/agent-core/connection.ts`

- [ ] **Step 1: Create connection.ts**

Create `web/src/lib/agent-core/connection.ts`:

```typescript
import type { Event, OpencodeClient } from '@opencode-ai/sdk/v2/client';

export type ConnectionStatus = 'disconnected' | 'connecting' | 'connected';

export interface ConnectionOptions {
  client: OpencodeClient;
  onEvent: (event: Event) => void;
  onStatusChange: (status: ConnectionStatus) => void;
}

const HEARTBEAT_TIMEOUT_MS = 15_000;

/**
 * createConnection starts an SSE event stream and dispatches events.
 * Handles heartbeat detection and auto-reconnect with exponential backoff.
 * Returns a cleanup function to stop the connection.
 */
export function createConnection(opts: ConnectionOptions): () => void {
  let stopped = false;
  let heartbeatTimer: ReturnType<typeof setTimeout> | undefined;
  let currentAbort: AbortController | undefined;

  const clearHeartbeat = () => {
    if (heartbeatTimer !== undefined) {
      clearTimeout(heartbeatTimer);
      heartbeatTimer = undefined;
    }
  };

  const resetHeartbeat = () => {
    clearHeartbeat();
    heartbeatTimer = setTimeout(() => {
      // No events in 15s — force reconnect
      currentAbort?.abort();
    }, HEARTBEAT_TIMEOUT_MS);
  };

  const run = async () => {
    let retryDelay = 3_000;

    while (!stopped) {
      opts.onStatusChange('connecting');
      currentAbort = new AbortController();

      try {
        const result = await opts.client.event.subscribe(
          {},
          { signal: currentAbort.signal },
        );

        opts.onStatusChange('connected');
        resetHeartbeat();
        retryDelay = 3_000;

        for await (const event of result.stream) {
          if (stopped) break;
          resetHeartbeat();
          opts.onEvent(event as Event);
        }
      } catch (e) {
        if (stopped) break;
        // AbortError from heartbeat timeout or manual stop — just reconnect
        if (e instanceof DOMException && e.name === 'AbortError') {
          continue;
        }
        // Network error — wait before retry
        opts.onStatusChange('connecting');
        await new Promise((r) => setTimeout(r, retryDelay));
        retryDelay = Math.min(retryDelay * 1.5, 30_000);
      }
    }

    clearHeartbeat();
    opts.onStatusChange('disconnected');
  };

  run();

  return () => {
    stopped = true;
    clearHeartbeat();
    currentAbort?.abort();
  };
}
```

- [ ] **Step 2: Verify build compiles**

```bash
cd /Users/max/misc/pj/appx && task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/lib/agent-core/connection.ts
git commit -m "feat: add SSE connection with heartbeat and auto-reconnect

Uses SDK's event.subscribe() for SSE streaming. 15s heartbeat timeout
triggers reconnect. Exponential backoff (3s-30s) on network errors.
Returns cleanup function for React useEffect teardown."
```

---

## Task 4: Agent React Hooks

**Files:**
- Create: `web/src/lib/agent-react/useSession.ts`
- Create: `web/src/lib/agent-react/useEventStream.ts`
- Create: `web/src/lib/agent-react/usePermissions.ts`

- [ ] **Step 1: Create useEventStream.ts**

Create `web/src/lib/agent-react/useEventStream.ts`:

```typescript
import { useEffect, useRef, useState, useCallback } from 'react';
import type { Event, OpencodeClient } from '@opencode-ai/sdk/v2/client';
import {
  createConnection,
  type ConnectionStatus,
} from '../agent-core/connection';

/**
 * useEventStream manages an SSE connection to OpenCode, dispatching events
 * via the provided callback. Reconnects automatically on heartbeat timeout.
 */
export function useEventStream(
  client: OpencodeClient | null,
  onEvent: (event: Event) => void,
): ConnectionStatus {
  const [status, setStatus] = useState<ConnectionStatus>('disconnected');
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  useEffect(() => {
    if (!client) return;

    const stop = createConnection({
      client,
      onEvent: (e) => onEventRef.current(e),
      onStatusChange: setStatus,
    });

    return stop;
  }, [client]);

  return status;
}
```

- [ ] **Step 2: Create useSession.ts**

Create `web/src/lib/agent-react/useSession.ts`:

```typescript
import { useReducer, useEffect, useCallback, useMemo } from 'react';
import type { Event, OpencodeClient, Part } from '@opencode-ai/sdk/v2/client';
import { applyEvent } from '../agent-core/reducers';
import { initialSessionState, type SessionState } from '../agent-core/types';
import { useEventStream } from './useEventStream';
import type { ConnectionStatus } from '../agent-core/connection';
import { getClient } from '../../api/opencode';

function reducer(state: SessionState, event: Event): SessionState {
  return applyEvent(state, event);
}

export interface UseSessionResult {
  state: SessionState;
  connectionStatus: ConnectionStatus;
  sendPrompt: (text: string) => Promise<void>;
  abort: () => Promise<void>;
}

/**
 * useSession provides full session state for a given session and project.
 * Connects to SSE, loads initial messages, and exposes prompt/abort actions.
 */
export function useSession(
  sessionId: string | null,
  projectDir: string,
): UseSessionResult {
  const [state, dispatch] = useReducer(reducer, initialSessionState);

  const client = useMemo(
    () => (projectDir ? getClient(projectDir) : null),
    [projectDir],
  );

  const connectionStatus = useEventStream(client, dispatch);

  // Load initial messages when session changes
  useEffect(() => {
    if (!client || !sessionId) return;
    let cancelled = false;

    (async () => {
      try {
        const res = await client.session.messages({ sessionID: sessionId });
        if (cancelled) return;
        const data = res.data as Array<{
          info: import('@opencode-ai/sdk/v2/client').Message;
          parts: Part[];
        }>;
        if (!data) return;
        // Dispatch synthetic events to populate state
        for (const item of data) {
          dispatch({
            type: 'message.updated',
            properties: { sessionID: sessionId, info: item.info },
          } as Event);
          for (const part of item.parts) {
            dispatch({
              type: 'message.part.updated',
              properties: { sessionID: sessionId, part, time: Date.now() },
            } as Event);
          }
        }
      } catch (e) {
        console.error('Failed to load messages:', e);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [client, sessionId]);

  const sendPrompt = useCallback(
    async (text: string) => {
      if (!client || !sessionId) return;
      await client.session.promptAsync({
        sessionID: sessionId,
        parts: [{ type: 'text', text }],
      });
    },
    [client, sessionId],
  );

  const abort = useCallback(async () => {
    if (!client || !sessionId) return;
    await client.session.abort({ sessionID: sessionId });
  }, [client, sessionId]);

  return { state, connectionStatus, sendPrompt, abort };
}
```

- [ ] **Step 3: Create usePermissions.ts**

Create `web/src/lib/agent-react/usePermissions.ts`:

```typescript
import { useCallback } from 'react';
import type { OpencodeClient, QuestionAnswer } from '@opencode-ai/sdk/v2/client';

/**
 * usePermissions provides actions to respond to permission and question requests.
 */
export function usePermissions(client: OpencodeClient | null) {
  const respondPermission = useCallback(
    async (requestID: string, reply: 'once' | 'always' | 'reject') => {
      if (!client) return;
      await client.permission.reply({ requestID, reply });
    },
    [client],
  );

  const answerQuestion = useCallback(
    async (requestID: string, answers: QuestionAnswer[]) => {
      if (!client) return;
      await client.question.reply({ requestID, answers });
    },
    [client],
  );

  const rejectQuestion = useCallback(
    async (requestID: string) => {
      if (!client) return;
      await client.question.reject({ requestID });
    },
    [client],
  );

  return { respondPermission, answerQuestion, rejectQuestion };
}
```

- [ ] **Step 4: Verify build compiles**

```bash
cd /Users/max/misc/pj/appx && task web
```

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/agent-react/
git commit -m "feat: add React hooks for session state, SSE, and permissions

useSession: manages SessionState via useReducer + applyEvent, loads initial
messages, exposes sendPrompt (promptAsync) and abort actions.
useEventStream: SSE lifecycle tied to component mount/unmount.
usePermissions: permission reply and question answer/reject via SDK."
```

---

## Task 5: Markdown Component

**Files:**
- Create: `web/src/components/Markdown.tsx`

- [ ] **Step 1: Create Markdown.tsx**

Create `web/src/components/Markdown.tsx`:

```typescript
import { useMemo, useRef, useEffect } from 'react';
import { marked } from 'marked';
import DOMPurify from 'dompurify';

interface MarkdownProps {
  text: string;
}

/** Markdown renders a markdown string as sanitized HTML with copy buttons on code blocks. */
export default function Markdown({ text }: MarkdownProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  const html = useMemo(() => {
    if (!text) return '';
    const raw = marked.parse(text, { async: false }) as string;
    return DOMPurify.sanitize(raw);
  }, [text]);

  // Add copy buttons to code blocks after render
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const pres = container.querySelectorAll('pre');
    pres.forEach((pre) => {
      if (pre.querySelector('[data-copy-btn]')) return;
      const btn = document.createElement('button');
      btn.setAttribute('data-copy-btn', '');
      btn.textContent = 'Copy';
      Object.assign(btn.style, copyBtnStyle);
      btn.addEventListener('click', () => {
        const code = pre.querySelector('code');
        const text = code?.textContent ?? pre.textContent ?? '';
        navigator.clipboard.writeText(text).then(() => {
          btn.textContent = 'Copied!';
          setTimeout(() => {
            btn.textContent = 'Copy';
          }, 2000);
        });
      });
      pre.style.position = 'relative';
      pre.appendChild(btn);
    });
  }, [html]);

  return (
    <div
      ref={containerRef}
      style={styles.container}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

const copyBtnStyle: Partial<CSSStyleDeclaration> = {
  position: 'absolute',
  top: '6px',
  right: '6px',
  background: 'var(--surface)',
  border: '1px solid var(--border)',
  color: 'var(--muted)',
  borderRadius: '3px',
  padding: '2px 8px',
  fontSize: '10px',
  cursor: 'pointer',
  fontFamily: "'JetBrains Mono', monospace",
};

const styles: Record<string, React.CSSProperties> = {
  container: {
    fontSize: 13,
    lineHeight: 1.6,
    color: 'var(--text)',
    fontFamily: "'DM Sans', sans-serif",
    wordBreak: 'break-word',
    overflowWrap: 'break-word',
  },
};

// Global markdown styles — inject once
const styleId = 'appx-markdown-styles';
if (typeof document !== 'undefined' && !document.getElementById(styleId)) {
  const style = document.createElement('style');
  style.id = styleId;
  style.textContent = `
    .appx-markdown p { margin: 0 0 8px 0; }
    .appx-markdown p:last-child { margin-bottom: 0; }
    .appx-markdown pre {
      background: var(--surface);
      border: 1px solid var(--border);
      border-radius: 4px;
      padding: 12px;
      overflow-x: auto;
      margin: 8px 0;
      position: relative;
    }
    .appx-markdown code {
      font-family: 'JetBrains Mono', monospace;
      font-size: 12px;
    }
    .appx-markdown :not(pre) > code {
      background: var(--surface);
      border: 1px solid var(--border);
      border-radius: 3px;
      padding: 1px 5px;
      font-size: 12px;
    }
    .appx-markdown ul, .appx-markdown ol { margin: 4px 0; padding-left: 20px; }
    .appx-markdown li { margin: 2px 0; }
    .appx-markdown a { color: var(--cyan); text-decoration: none; }
    .appx-markdown a:hover { text-decoration: underline; }
    .appx-markdown h1, .appx-markdown h2, .appx-markdown h3 {
      margin: 12px 0 6px 0;
      color: var(--text);
    }
    .appx-markdown blockquote {
      border-left: 3px solid var(--border);
      margin: 8px 0;
      padding: 4px 12px;
      color: var(--muted);
    }
    .appx-markdown table { border-collapse: collapse; margin: 8px 0; }
    .appx-markdown th, .appx-markdown td {
      border: 1px solid var(--border);
      padding: 6px 10px;
      font-size: 12px;
    }
    .appx-markdown th { background: var(--surface); }
  `;
  document.head.appendChild(style);
}
```

Update the container div to use the class. Replace the `return` in the component:

Actually, the styles are injected globally with `.appx-markdown` prefix. Update the container div:

```typescript
  return (
    <div
      ref={containerRef}
      className="appx-markdown"
      style={styles.container}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
```

- [ ] **Step 2: Verify build compiles**

```bash
cd /Users/max/misc/pj/appx && task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/components/Markdown.tsx
git commit -m "feat: add Markdown component with sanitization and code copy buttons

Uses marked for parsing, DOMPurify for XSS sanitization. Injects copy-to-clipboard
buttons on code blocks. Global CSS styles for code, tables, lists, blockquotes.
Styled with appx CSS variables."
```

---

## Task 6: ToolCallCard Component

**Files:**
- Create: `web/src/components/ToolCallCard.tsx`

- [ ] **Step 1: Create ToolCallCard.tsx**

Create `web/src/components/ToolCallCard.tsx`:

```typescript
import { useState } from 'react';
import type { ToolPart } from '@opencode-ai/sdk/v2/client';

interface ToolCallCardProps {
  part: ToolPart;
}

/** ToolCallCard renders a collapsible card for a tool call with status indicator. */
export default function ToolCallCard({ part }: ToolCallCardProps) {
  const { tool, state } = part;
  const status = state.status;
  const isRunning = status === 'running';
  const isError = status === 'error';
  const isCompleted = status === 'completed';

  const [open, setOpen] = useState(isRunning || isError);

  const title =
    (status === 'completed' || status === 'running') && state.title
      ? state.title
      : tool;

  const statusColor = isError
    ? 'var(--red)'
    : isRunning
      ? 'var(--yellow)'
      : isCompleted
        ? 'var(--green)'
        : 'var(--muted)';

  const statusLabel = isError
    ? 'error'
    : isRunning
      ? 'running'
      : isCompleted
        ? 'done'
        : 'pending';

  return (
    <div style={styles.card}>
      <button style={styles.header} onClick={() => setOpen(!open)}>
        <span style={styles.toolName}>{title}</span>
        <span style={{ ...styles.statusBadge, color: statusColor }}>
          {isRunning && <span style={styles.spinner}>⟳</span>}
          {statusLabel}
        </span>
        <span style={styles.toggle}>{open ? '▾' : '▸'}</span>
      </button>
      {open && (
        <div style={styles.body}>
          {isError && (
            <pre style={styles.errorOutput}>
              {(state as { error: string }).error}
            </pre>
          )}
          {isCompleted && (
            <pre style={styles.output}>
              {(state as { output: string }).output || '(no output)'}
            </pre>
          )}
          {isRunning && (
            <span style={styles.runningText}>Running...</span>
          )}
          {status === 'pending' && (
            <span style={styles.runningText}>Pending...</span>
          )}
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  card: {
    border: '1px solid var(--border)',
    borderRadius: 4,
    overflow: 'hidden',
    margin: '4px 0',
  },
  header: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    width: '100%',
    padding: '8px 12px',
    background: 'var(--surface)',
    border: 'none',
    cursor: 'pointer',
    textAlign: 'left' as const,
  },
  toolName: {
    flex: 1,
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
    color: 'var(--text)',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap' as const,
  },
  statusBadge: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    letterSpacing: '0.05em',
    display: 'flex',
    alignItems: 'center',
    gap: 4,
  },
  spinner: {
    display: 'inline-block',
    animation: 'spin 1s linear infinite',
  },
  toggle: {
    fontSize: 10,
    color: 'var(--muted)',
  },
  body: {
    padding: '8px 12px',
    borderTop: '1px solid var(--border)',
    background: 'var(--bg)',
  },
  output: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--text)',
    margin: 0,
    whiteSpace: 'pre-wrap' as const,
    wordBreak: 'break-word' as const,
    maxHeight: 300,
    overflowY: 'auto' as const,
    lineHeight: 1.4,
  },
  errorOutput: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--red)',
    margin: 0,
    whiteSpace: 'pre-wrap' as const,
    wordBreak: 'break-word' as const,
    maxHeight: 200,
    overflowY: 'auto' as const,
  },
  runningText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
  },
};
```

- [ ] **Step 2: Add spinner keyframe to index.css**

Add to the end of `web/src/index.css`:

```css
@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}
```

- [ ] **Step 3: Verify build compiles**

```bash
cd /Users/max/misc/pj/appx && task web
```

- [ ] **Step 4: Commit**

```bash
git add web/src/components/ToolCallCard.tsx web/src/index.css
git commit -m "feat: add ToolCallCard collapsible component

Generic tool call display with status badge (pending/running/done/error),
collapsible output, and error display. Auto-opens when running or errored."
```

---

## Task 7: Permission and Question Docks

**Files:**
- Create: `web/src/components/PermissionDock.tsx`
- Create: `web/src/components/QuestionDock.tsx`

- [ ] **Step 1: Create PermissionDock.tsx**

Create `web/src/components/PermissionDock.tsx`:

```typescript
import type { PermissionRequest } from '@opencode-ai/sdk/v2/client';

interface PermissionDockProps {
  permission: PermissionRequest;
  onRespond: (requestID: string, reply: 'once' | 'always' | 'reject') => void;
}

/** PermissionDock shows a permission request with allow/deny actions. */
export default function PermissionDock({
  permission,
  onRespond,
}: PermissionDockProps) {
  return (
    <div style={styles.dock}>
      <div style={styles.header}>
        <span style={styles.icon}>⚠</span>
        <span style={styles.title}>Permission Required</span>
      </div>
      <div style={styles.info}>
        <span style={styles.label}>Tool:</span>
        <span style={styles.value}>{permission.permission}</span>
      </div>
      {permission.patterns.length > 0 && (
        <div style={styles.patterns}>
          {permission.patterns.map((p, i) => (
            <code key={i} style={styles.pattern}>
              {p}
            </code>
          ))}
        </div>
      )}
      <div style={styles.actions}>
        <button
          style={styles.denyBtn}
          onClick={() => onRespond(permission.id, 'reject')}
        >
          Deny
        </button>
        <button
          style={styles.alwaysBtn}
          onClick={() => onRespond(permission.id, 'always')}
        >
          Allow Always
        </button>
        <button
          style={styles.allowBtn}
          onClick={() => onRespond(permission.id, 'once')}
        >
          Allow Once
        </button>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  dock: {
    background: 'var(--surface)',
    border: '1px solid var(--yellow)',
    borderRadius: 6,
    padding: '12px 16px',
    margin: '0 20px 8px',
  },
  header: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    marginBottom: 8,
  },
  icon: { fontSize: 14, color: 'var(--yellow)' },
  title: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    letterSpacing: '0.05em',
    color: 'var(--yellow)',
    fontWeight: 500,
  },
  info: {
    display: 'flex',
    gap: 6,
    marginBottom: 6,
    fontSize: 12,
  },
  label: {
    color: 'var(--muted)',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
  },
  value: {
    color: 'var(--text)',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
  },
  patterns: {
    display: 'flex',
    flexWrap: 'wrap' as const,
    gap: 4,
    marginBottom: 10,
  },
  pattern: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 3,
    padding: '2px 6px',
    color: 'var(--text)',
  },
  actions: {
    display: 'flex',
    gap: 8,
    justifyContent: 'flex-end',
  },
  denyBtn: {
    background: 'transparent',
    border: '1px solid var(--red)',
    color: 'var(--red)',
    borderRadius: 4,
    padding: '5px 14px',
    fontSize: 11,
    cursor: 'pointer',
  },
  alwaysBtn: {
    background: 'transparent',
    border: '1px solid var(--green)',
    color: 'var(--green)',
    borderRadius: 4,
    padding: '5px 14px',
    fontSize: 11,
    cursor: 'pointer',
  },
  allowBtn: {
    background: 'var(--blue)',
    border: 'none',
    color: '#fff',
    borderRadius: 4,
    padding: '5px 14px',
    fontSize: 11,
    fontWeight: 500,
    cursor: 'pointer',
  },
};
```

- [ ] **Step 2: Create QuestionDock.tsx**

Create `web/src/components/QuestionDock.tsx`:

```typescript
import { useState } from 'react';
import type {
  QuestionRequest,
  QuestionAnswer,
} from '@opencode-ai/sdk/v2/client';

interface QuestionDockProps {
  question: QuestionRequest;
  onAnswer: (requestID: string, answers: QuestionAnswer[]) => void;
  onReject: (requestID: string) => void;
}

/** QuestionDock shows an agent question with radio/text options and submit. */
export default function QuestionDock({
  question,
  onAnswer,
  onReject,
}: QuestionDockProps) {
  const [answers, setAnswers] = useState<string[][]>(
    question.questions.map(() => []),
  );

  const handleSelect = (qIdx: number, label: string, multiple: boolean) => {
    setAnswers((prev) => {
      const next = [...prev];
      if (multiple) {
        const current = next[qIdx];
        next[qIdx] = current.includes(label)
          ? current.filter((l) => l !== label)
          : [...current, label];
      } else {
        next[qIdx] = [label];
      }
      return next;
    });
  };

  const handleSubmit = () => {
    onAnswer(question.id, answers);
  };

  const hasAnswer = answers.some((a) => a.length > 0);

  return (
    <div style={styles.dock}>
      {question.questions.map((q, qIdx) => (
        <div key={qIdx} style={styles.questionBlock}>
          {q.header && (
            <div style={styles.header}>{q.header}</div>
          )}
          <div style={styles.questionText}>{q.question}</div>
          <div style={styles.options}>
            {q.options.map((opt) => (
              <button
                key={opt.label}
                style={
                  answers[qIdx].includes(opt.label)
                    ? styles.optionSelected
                    : styles.option
                }
                onClick={() =>
                  handleSelect(qIdx, opt.label, q.multiple ?? false)
                }
              >
                <span style={styles.optionLabel}>{opt.label}</span>
                {opt.description && (
                  <span style={styles.optionDesc}>{opt.description}</span>
                )}
              </button>
            ))}
          </div>
        </div>
      ))}
      <div style={styles.actions}>
        <button
          style={styles.rejectBtn}
          onClick={() => onReject(question.id)}
        >
          Dismiss
        </button>
        <button
          style={styles.submitBtn}
          onClick={handleSubmit}
          disabled={!hasAnswer}
        >
          Submit
        </button>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  dock: {
    background: 'var(--surface)',
    border: '1px solid var(--cyan)',
    borderRadius: 6,
    padding: '12px 16px',
    margin: '0 20px 8px',
  },
  questionBlock: { marginBottom: 10 },
  header: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    letterSpacing: '0.05em',
    color: 'var(--cyan)',
    marginBottom: 4,
  },
  questionText: {
    fontSize: 13,
    color: 'var(--text)',
    marginBottom: 8,
  },
  options: {
    display: 'flex',
    flexDirection: 'column' as const,
    gap: 4,
  },
  option: {
    display: 'flex',
    flexDirection: 'column' as const,
    gap: 2,
    padding: '8px 12px',
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    cursor: 'pointer',
    textAlign: 'left' as const,
  },
  optionSelected: {
    display: 'flex',
    flexDirection: 'column' as const,
    gap: 2,
    padding: '8px 12px',
    background: 'var(--cyan-dim)',
    border: '1px solid var(--cyan)',
    borderRadius: 4,
    cursor: 'pointer',
    textAlign: 'left' as const,
  },
  optionLabel: {
    fontSize: 12,
    color: 'var(--text)',
    fontWeight: 500,
  },
  optionDesc: {
    fontSize: 11,
    color: 'var(--muted)',
  },
  actions: {
    display: 'flex',
    gap: 8,
    justifyContent: 'flex-end',
    marginTop: 8,
  },
  rejectBtn: {
    background: 'transparent',
    border: '1px solid var(--border)',
    color: 'var(--muted)',
    borderRadius: 4,
    padding: '5px 14px',
    fontSize: 11,
    cursor: 'pointer',
  },
  submitBtn: {
    background: 'var(--blue)',
    border: 'none',
    color: '#fff',
    borderRadius: 4,
    padding: '5px 14px',
    fontSize: 11,
    fontWeight: 500,
    cursor: 'pointer',
  },
};
```

- [ ] **Step 3: Verify build compiles**

```bash
cd /Users/max/misc/pj/appx && task web
```

- [ ] **Step 4: Commit**

```bash
git add web/src/components/PermissionDock.tsx web/src/components/QuestionDock.tsx
git commit -m "feat: add PermissionDock and QuestionDock components

PermissionDock: shows tool name, patterns, deny/allow-always/allow-once buttons.
QuestionDock: renders options as selectable buttons, supports multiple choice,
dismiss and submit actions."
```

---

## Task 8: StatusBar Component

**Files:**
- Create: `web/src/components/StatusBar.tsx`

- [ ] **Step 1: Create StatusBar.tsx**

Create `web/src/components/StatusBar.tsx`:

```typescript
import type { ConnectionStatus } from '../lib/agent-core/connection';

interface StatusBarProps {
  agentStatus: 'idle' | 'running' | 'error';
  connectionStatus: ConnectionStatus;
}

/** StatusBar shows agent status and SSE connection health. */
export default function StatusBar({
  agentStatus,
  connectionStatus,
}: StatusBarProps) {
  const agentColor =
    agentStatus === 'running'
      ? 'var(--yellow)'
      : agentStatus === 'error'
        ? 'var(--red)'
        : 'var(--green)';

  const connColor =
    connectionStatus === 'connected'
      ? 'var(--green)'
      : connectionStatus === 'connecting'
        ? 'var(--yellow)'
        : 'var(--red)';

  return (
    <div style={styles.bar}>
      <div style={styles.item}>
        <span style={{ ...styles.dot, background: agentColor }} />
        <span style={styles.label}>
          {agentStatus === 'running'
            ? 'Agent running'
            : agentStatus === 'error'
              ? 'Agent error'
              : 'Agent idle'}
        </span>
      </div>
      <div style={styles.item}>
        <span style={{ ...styles.dot, background: connColor }} />
        <span style={styles.label}>
          {connectionStatus === 'connected'
            ? 'Connected'
            : connectionStatus === 'connecting'
              ? 'Reconnecting...'
              : 'Disconnected'}
        </span>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  bar: {
    display: 'flex',
    gap: 16,
    padding: '6px 20px',
    borderTop: '1px solid var(--border)',
    background: 'var(--bg)',
  },
  item: {
    display: 'flex',
    alignItems: 'center',
    gap: 6,
  },
  dot: {
    width: 6,
    height: 6,
    borderRadius: '50%',
    flexShrink: 0,
  },
  label: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--muted)',
  },
};
```

- [ ] **Step 2: Verify build compiles**

```bash
cd /Users/max/misc/pj/appx && task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/components/StatusBar.tsx
git commit -m "feat: add StatusBar with agent status and connection health"
```

---

## Task 9: Rewrite ChatPanel

**Files:**
- Modify: `web/src/components/agent/ChatPanel.tsx`

- [ ] **Step 1: Rewrite ChatPanel.tsx**

Replace the entire contents of `web/src/components/agent/ChatPanel.tsx` with:

```typescript
import { useState, useRef, useEffect, useMemo } from 'react';
import type {
  Message,
  UserMessage,
  AssistantMessage,
  Part,
  TextPart,
  ToolPart,
  ReasoningPart,
} from '@opencode-ai/sdk/v2/client';
import { useSession } from '../../lib/agent-react/useSession';
import { usePermissions } from '../../lib/agent-react/usePermissions';
import { getClient } from '../../api/opencode';
import Markdown from '../Markdown';
import ToolCallCard from '../ToolCallCard';
import PermissionDock from '../PermissionDock';
import QuestionDock from '../QuestionDock';
import StatusBar from '../StatusBar';

interface Turn {
  user: UserMessage;
  assistants: AssistantMessage[];
}

function groupIntoTurns(messages: Message[]): Turn[] {
  const users = messages.filter((m): m is UserMessage => m.role === 'user');
  return users.map((user) => ({
    user,
    assistants: messages.filter(
      (m): m is AssistantMessage =>
        m.role === 'assistant' && m.parentID === user.id,
    ),
  }));
}

function renderPart(part: Part) {
  switch (part.type) {
    case 'text':
      return <Markdown key={part.id} text={(part as TextPart).text} />;
    case 'tool':
      return <ToolCallCard key={part.id} part={part as ToolPart} />;
    case 'reasoning':
      return (
        <details key={part.id} style={partStyles.reasoning}>
          <summary style={partStyles.reasoningSummary}>Thinking...</summary>
          <pre style={partStyles.reasoningText}>
            {(part as ReasoningPart).text}
          </pre>
        </details>
      );
    default:
      return null;
  }
}

/** ChatPanel renders the full agent conversation for a session. Uses the
 *  headless core hooks for SSE streaming, state management, and actions. */
export default function ChatPanel({
  sessionId,
  projectDir,
}: {
  sessionId: string;
  projectDir: string;
}) {
  const { state, connectionStatus, sendPrompt, abort } = useSession(
    sessionId,
    projectDir,
  );
  const client = useMemo(
    () => (projectDir ? getClient(projectDir) : null),
    [projectDir],
  );
  const { respondPermission, answerQuestion, rejectQuestion } =
    usePermissions(client);

  const [input, setInput] = useState('');
  const [sending, setSending] = useState(false);
  const bottomRef = useRef<HTMLDivElement>(null);

  const turns = useMemo(() => groupIntoTurns(state.messages), [state.messages]);
  const isRunning = state.status === 'running';

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [state.messages, state.parts]);

  const handleSend = async () => {
    const text = input.trim();
    if (!text || sending || isRunning) return;
    setInput('');
    setSending(true);
    try {
      await sendPrompt(text);
    } catch (e) {
      console.error('Failed to send prompt:', e);
    } finally {
      setSending(false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  return (
    <div style={styles.container}>
      {/* Messages */}
      <div style={styles.messages}>
        {turns.length === 0 && (
          <div style={styles.empty}>
            <span style={styles.emptyText}>Send a prompt to start</span>
          </div>
        )}
        {turns.map((turn) => (
          <div key={turn.user.id} style={styles.turn}>
            {/* User message */}
            <div style={styles.userMsg}>
              <span style={styles.msgRole}>YOU</span>
              {state.parts[turn.user.id]?.map(renderPart) ?? (
                <span style={styles.userText}>
                  {(state.parts[turn.user.id]?.find(
                    (p) => p.type === 'text',
                  ) as TextPart | undefined)?.text ?? ''}
                </span>
              )}
            </div>
            {/* Assistant messages */}
            {turn.assistants.map((asst) => (
              <div key={asst.id} style={styles.assistantMsg}>
                <span style={styles.msgRole}>AGENT</span>
                {(state.parts[asst.id] ?? []).map(renderPart)}
                {asst.error && (
                  <div style={styles.msgError}>
                    {JSON.stringify(asst.error)}
                  </div>
                )}
              </div>
            ))}
          </div>
        ))}
        <div ref={bottomRef} />
      </div>

      {/* Docks */}
      {state.pendingPermissions.map((perm) => (
        <PermissionDock
          key={perm.id}
          permission={perm}
          onRespond={respondPermission}
        />
      ))}
      {state.pendingQuestions.map((q) => (
        <QuestionDock
          key={q.id}
          question={q}
          onAnswer={answerQuestion}
          onReject={rejectQuestion}
        />
      ))}

      {/* Error banner */}
      {state.error && <div style={styles.errorBanner}>{state.error}</div>}

      {/* Input */}
      <div style={styles.inputBar}>
        <textarea
          style={styles.input}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder={isRunning ? 'Agent is working...' : 'Send a message...'}
          rows={1}
          disabled={sending || isRunning}
        />
        {isRunning ? (
          <button style={styles.abortBtn} onClick={abort}>
            Stop
          </button>
        ) : (
          <button
            style={styles.sendBtn}
            onClick={handleSend}
            disabled={sending || !input.trim()}
          >
            {sending ? '...' : 'Send'}
          </button>
        )}
      </div>

      {/* Status bar */}
      <StatusBar
        agentStatus={state.status}
        connectionStatus={connectionStatus}
      />
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    flex: 1,
    display: 'flex',
    flexDirection: 'column',
    minHeight: 0,
    overflow: 'hidden',
  },
  messages: {
    flex: 1,
    overflowY: 'auto',
    padding: '16px 20px',
    display: 'flex',
    flexDirection: 'column',
    gap: 16,
  },
  empty: {
    flex: 1,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
  },
  emptyText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
    color: 'var(--muted)',
  },
  turn: {
    display: 'flex',
    flexDirection: 'column',
    gap: 8,
  },
  userMsg: {
    padding: '10px 14px',
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    alignSelf: 'flex-end',
    maxWidth: '80%',
  },
  assistantMsg: {
    padding: '10px 14px',
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    alignSelf: 'flex-start',
    maxWidth: '90%',
    display: 'flex',
    flexDirection: 'column',
    gap: 6,
  },
  msgRole: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 9,
    letterSpacing: '0.1em',
    color: 'var(--muted)',
    display: 'block',
    marginBottom: 4,
  },
  userText: {
    fontSize: 13,
    color: 'var(--text)',
    whiteSpace: 'pre-wrap' as const,
  },
  msgError: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--red)',
    padding: '6px 8px',
    background: 'var(--red-dim)',
    borderRadius: 3,
  },
  errorBanner: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--red)',
    padding: '6px 20px',
    background: 'var(--red-dim)',
  },
  inputBar: {
    display: 'flex',
    gap: 8,
    padding: '12px 20px',
    borderTop: '1px solid var(--border)',
    background: 'var(--surface)',
  },
  input: {
    flex: 1,
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '10px 12px',
    fontSize: 13,
    color: 'var(--text)',
    outline: 'none',
    resize: 'none' as const,
    fontFamily: "'DM Sans', sans-serif",
    lineHeight: 1.4,
  },
  sendBtn: {
    background: 'var(--blue)',
    border: 'none',
    color: '#fff',
    borderRadius: 4,
    padding: '10px 18px',
    fontSize: 13,
    fontWeight: 500,
    cursor: 'pointer',
    alignSelf: 'flex-end',
  },
  abortBtn: {
    background: 'var(--red)',
    border: 'none',
    color: '#fff',
    borderRadius: 4,
    padding: '10px 18px',
    fontSize: 13,
    fontWeight: 500,
    cursor: 'pointer',
    alignSelf: 'flex-end',
  },
};

const partStyles: Record<string, React.CSSProperties> = {
  reasoning: {
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '4px 8px',
    margin: '4px 0',
  },
  reasoningSummary: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
    cursor: 'pointer',
  },
  reasoningText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
    margin: '6px 0 0',
    whiteSpace: 'pre-wrap' as const,
    lineHeight: 1.4,
  },
};
```

- [ ] **Step 2: Verify build compiles**

```bash
cd /Users/max/misc/pj/appx && task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/components/agent/ChatPanel.tsx
git commit -m "feat: rewrite ChatPanel with hooks, markdown, tool cards, and docks

Uses useSession for state + SSE, usePermissions for responding. Renders
message parts by type: text->Markdown, tool->ToolCallCard, reasoning->details.
Sends via promptAsync (not message). Has abort button, permission dock,
question dock, status bar. SSE connects on mount before any prompt is sent."
```

---

## Task 10: Update SessionList

**Files:**
- Modify: `web/src/components/agent/SessionList.tsx`

- [ ] **Step 1: Rewrite SessionList.tsx**

Replace the entire contents of `web/src/components/agent/SessionList.tsx` with:

```typescript
import { useState, useEffect, useCallback, useMemo } from 'react';
import { getClient } from '../../api/opencode';
import type { Session } from '@opencode-ai/sdk/v2/client';

/** SessionList displays OpenCode sessions for a project directory.
 *  Allows creating, selecting, and deleting sessions. */
export default function SessionList({
  projectDir,
  activeSessionId,
  onSelectSession,
}: {
  projectDir: string;
  activeSessionId: string | null;
  onSelectSession: (id: string) => void;
}) {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState('');

  const client = useMemo(
    () => (projectDir ? getClient(projectDir) : null),
    [projectDir],
  );

  const fetchSessions = useCallback(async () => {
    if (!client) return;
    try {
      const res = await client.session.list({});
      setSessions((res.data ?? []) as Session[]);
    } catch {
      // OpenCode may not be running yet
    }
  }, [client]);

  useEffect(() => {
    fetchSessions();
  }, [fetchSessions]);

  const handleCreate = async () => {
    if (!client) return;
    setCreating(true);
    setError('');
    try {
      const res = await client.session.create({});
      const session = res.data as Session | undefined;
      if (session?.id) {
        await fetchSessions();
        onSelectSession(session.id);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to create session');
    } finally {
      setCreating(false);
    }
  };

  const handleDelete = async (e: React.MouseEvent, sessionId: string) => {
    e.stopPropagation();
    if (!client) return;
    try {
      await client.session.delete({ sessionID: sessionId });
      setSessions((prev) => prev.filter((s) => s.id !== sessionId));
    } catch (err) {
      console.error('Failed to delete session:', err);
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <span style={styles.title}>SESSIONS</span>
        <button
          style={styles.createBtn}
          onClick={handleCreate}
          disabled={creating}
        >
          {creating ? '...' : '+ New'}
        </button>
      </div>
      {error && <div style={styles.error}>{error}</div>}
      <div style={styles.list}>
        {sessions.length === 0 ? (
          <span style={styles.emptyText}>No sessions yet</span>
        ) : (
          sessions.map((s) => (
            <button
              key={s.id}
              style={
                s.id === activeSessionId ? styles.itemActive : styles.item
              }
              onClick={() => onSelectSession(s.id)}
            >
              <div style={styles.itemRow}>
                <span style={styles.itemTitle}>
                  {s.title || 'Untitled'}
                </span>
                <button
                  style={styles.deleteBtn}
                  onClick={(e) => handleDelete(e, s.id)}
                  title="Delete session"
                >
                  ×
                </button>
              </div>
              <span style={styles.itemId}>{s.id.slice(0, 8)}</span>
            </button>
          ))
        )}
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    display: 'flex',
    flexDirection: 'column',
    borderRight: '1px solid var(--border)',
    width: 220,
    flexShrink: 0,
  },
  header: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: '12px 14px',
    borderBottom: '1px solid var(--border)',
  },
  title: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    letterSpacing: '0.1em',
    color: 'var(--muted)',
  },
  createBtn: {
    background: 'transparent',
    border: '1px solid rgba(61,220,132,0.35)',
    color: 'var(--green)',
    borderRadius: 4,
    padding: '3px 10px',
    fontSize: 11,
    cursor: 'pointer',
  },
  error: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--red)',
    padding: '6px 14px',
  },
  list: { flex: 1, overflowY: 'auto' },
  emptyText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
    padding: '16px 14px',
    display: 'block',
  },
  item: {
    display: 'flex',
    flexDirection: 'column',
    gap: 2,
    width: '100%',
    padding: '10px 14px',
    background: 'transparent',
    border: 'none',
    borderBottom: '1px solid var(--border)',
    cursor: 'pointer',
    textAlign: 'left' as const,
  },
  itemActive: {
    display: 'flex',
    flexDirection: 'column',
    gap: 2,
    width: '100%',
    padding: '10px 14px',
    background: 'var(--surface-hover)',
    border: 'none',
    borderBottom: '1px solid var(--border)',
    borderLeft: '2px solid var(--cyan)',
    cursor: 'pointer',
    textAlign: 'left' as const,
  },
  itemRow: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  itemTitle: {
    fontSize: 12,
    color: 'var(--text)',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap' as const,
    flex: 1,
  },
  deleteBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    fontSize: 14,
    cursor: 'pointer',
    padding: '0 4px',
    lineHeight: 1,
  },
  itemId: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--muted)',
  },
};
```

- [ ] **Step 2: Verify build compiles**

```bash
cd /Users/max/misc/pj/appx && task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/components/agent/SessionList.tsx
git commit -m "feat: update SessionList with SDK client, delete, and active indicator

Uses SDK client.session.list/create/delete instead of raw fetch.
Adds delete button per session. Shows title and active indicator."
```

---

## Task 11: Full Build and Verification

**Files:**
- No new files — verification only

- [ ] **Step 1: Run full build**

```bash
cd /Users/max/misc/pj/appx && task build
```

Expected: Compiles cleanly. No TypeScript errors.

- [ ] **Step 2: Run linter**

```bash
cd /Users/max/misc/pj/appx && task lint
```

Fix any lint issues.

- [ ] **Step 3: Run tests**

```bash
cd /Users/max/misc/pj/appx && task test
```

All existing Go tests should pass (no Go changes in this PR).

- [ ] **Step 4: Manual smoke test**

Start the server and verify the UI works:

1. Run `./appx` (or `./appx --http -port 8080` for dev mode)
2. Navigate to a project
3. Create a session via the SessionList
4. Send a prompt — verify:
   - Tokens stream in as markdown (not raw text)
   - Tool calls appear as collapsible cards with status badges
   - Permission dock appears when agent needs approval
   - Abort button shows while agent is running
   - Status bar shows agent status + connection health
   - Session can be deleted from the list

- [ ] **Step 5: Final commit if any fixes were needed**

```bash
git add -A
git commit -m "fix: address build/lint issues from agent UI integration"
```
