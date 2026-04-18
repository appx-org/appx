# Phase 5 Step 7: Frontend Adaptation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Frontend testing is manual per CLAUDE.md conventions -- verify with `task web`, `task build`, `task lint`, and describe what you checked visually.

**Goal:** Rewrite the React frontend for the de-Docker architecture. Replace container lifecycle UI (start/stop/reset) with a simplified project model (name + auto-assigned port + app health). Build agent interaction directly into the appx SPA using the OpenCode SDK (`@opencode-ai/sdk`). Add egress log viewer and allowlist editor. Terminal rewired to OpenCode PTY via `/api/opencode/pty/:id/connect`.

**Architecture:** The frontend communicates with two APIs through one origin:
- `/api/*` -- appx REST API (auth, project CRUD, settings, egress)
- `/api/opencode/*` -- reverse proxy to OpenCode server on localhost:4096 (sessions, events, permissions, PTY)

Agent interaction is built INTO the appx SPA using the OpenCode TypeScript SDK. OpenCode's own web UI is never served to the browser. React components are designed as reusable modules for future mobile/desktop apps.

**Tech Stack:** React 19, TypeScript 5.9, Vite 8, `@opencode-ai/sdk`, `@xterm/xterm`, react-router-dom 7. Inline styles via `Record<string, React.CSSProperties>`, darksynth cyberpunk aesthetic, CSS variables from `index.css`.

**Reference:** See `docs/plans/phase_5_plan.md` (Step 7), `docs/analysis/refactors/de-docker-refactor.md` (F2, Q4), and `docs/guides/style-guide.md` for the full palette.

---

## Task 1: Install OpenCode SDK and update dependencies

- [ ] **Step 1: Install `@opencode-ai/sdk`**

```bash
cd web && npm install @opencode-ai/sdk
```

- [ ] **Step 2: Verify package.json updated**

```bash
grep opencode web/package.json
```

Expected: `"@opencode-ai/sdk": "^X.Y.Z"` in dependencies.

- [ ] **Step 3: Verify build still works**

```bash
task web
```

- [ ] **Step 4: Commit**

```bash
git add web/package.json web/package-lock.json
git commit -m "deps: add @opencode-ai/sdk to frontend dependencies"
```

---

## Task 2: Update API client -- remove Docker endpoints, add new types

Remove container lifecycle functions and add types for the new architecture: projects with `assignedPort` and `appRunning`, OpenCode health, egress logs, and allowlist.

**File:** `web/src/api/client.ts`

- [ ] **Step 1: Replace the full API client**

```typescript
const BASE = '/api';

/**
 * request is the shared HTTP client for all API calls. It prepends the /api
 * base path, sets JSON content-type, and throws on non-2xx responses with the
 * error body as the message. All exported API functions delegate to this.
 */
async function request<T>(path: string, opts?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    ...opts,
    headers: { 'Content-Type': 'application/json', ...opts?.headers },
  });
  if (!res.ok) {
    if (res.status === 401) {
      window.location.href = '/login';
      throw new Error('Unauthorized');
    }
    throw new Error(await res.text());
  }
  if (res.status === 204 || res.status === 202) return undefined as T;
  return res.json();
}

/** Ends the current session by calling DELETE /api/session. */
export function logout() {
  return request<{ status: string }>('/session', { method: 'DELETE' });
}

/** Authenticates with the server via POST /api/login. On success the server
 *  sets an httpOnly session cookie. */
export function login(password: string) {
  return request<{ status: string }>('/login', {
    method: 'POST',
    body: JSON.stringify({ password }),
  });
}

/** A project as returned by the API. Post de-Docker: no container lifecycle,
 *  projects have an auto-assigned port and an app health indicator. */
export interface Project {
  id: string;
  name: string;
  assignedPort: number;
  appRunning: boolean;
  openCodeProjectId?: string;
  createdAt: string;
}

/** Fetches the list of all projects via GET /api/projects. Includes app health status. */
export function getProjects() {
  return request<Project[]>('/projects');
}

/** Fetches a single project by ID via GET /api/projects/:id. */
export function getProject(id: string) {
  return request<Project>(`/projects/${id}`);
}

/** Creates a new project via POST /api/projects. Port is auto-assigned by the backend.
 *  Returns the created project with its assigned port. */
export function createProject(name: string) {
  return request<Project>('/projects', {
    method: 'POST',
    body: JSON.stringify({ name }),
  });
}

/** Deletes a project and its directory via DELETE /api/projects/:id. */
export function deleteProject(id: string) {
  return request<void>(`/projects/${id}`, { method: 'DELETE' });
}

/** Checks whether an Anthropic API key is configured via GET /api/settings/api-key. */
export function getApiKeyStatus() {
  return request<{ set: boolean }>('/settings/api-key');
}

/** Stores an Anthropic API key via PUT /api/settings/api-key. */
export function setApiKey(key: string) {
  return request<{ status: string }>('/settings/api-key', {
    method: 'PUT',
    body: JSON.stringify({ key }),
  });
}

/** Removes the stored Anthropic API key via DELETE /api/settings/api-key. */
export function deleteApiKey() {
  return request<{ status: string }>('/settings/api-key', { method: 'DELETE' });
}

/** Gets the terminal buffer size setting in KB via GET /api/settings/terminal-buffer-size. */
export function getTerminalBufferSize() {
  return request<{ value: number }>('/settings/terminal-buffer-size');
}

/** Sets the terminal buffer size in KB via PUT /api/settings/terminal-buffer-size. */
export function setTerminalBufferSize(value: number) {
  return request<{ status: string }>('/settings/terminal-buffer-size', {
    method: 'PUT',
    body: JSON.stringify({ value }),
  });
}

/** OpenCode server health status as reported by GET /api/opencode-health. */
export interface OpenCodeHealth {
  healthy: boolean;
}

/** Checks if the OpenCode server is reachable via GET /api/opencode-health. */
export function getOpenCodeHealth() {
  return request<OpenCodeHealth>('/opencode-health');
}

/** A single egress log entry as returned by the API. */
export interface EgressLogEntry {
  id: string;
  timestamp: string;
  destination: string;
  port: number;
  status: 'allowed' | 'blocked';
}

/** Fetches paginated egress log via GET /api/egress/log. */
export function getEgressLog(offset = 0, limit = 50) {
  return request<EgressLogEntry[]>(`/egress/log?offset=${offset}&limit=${limit}`);
}

/** Fetches the current egress allowlist via GET /api/egress/allowlist. */
export function getEgressAllowlist() {
  return request<{ entries: string[] }>('/egress/allowlist');
}

/** Updates the egress allowlist via PUT /api/egress/allowlist. */
export function setEgressAllowlist(entries: string[]) {
  return request<{ status: string }>('/egress/allowlist', {
    method: 'PUT',
    body: JSON.stringify({ entries }),
  });
}
```

- [ ] **Step 2: Verify build**

```bash
task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/api/client.ts
git commit -m "api: rewrite client for de-Docker architecture (drop container lifecycle, add egress/health)"
```

---

## Task 3: Create OpenCode SDK client wrapper

Create a thin wrapper around the OpenCode SDK that initializes the client pointing at `/api/opencode` (appx proxies to localhost:4096). This module is the data layer for all agent interaction components.

**File:** `web/src/api/opencode.ts` (new)

- [ ] **Step 1: Create the OpenCode client module**

```typescript
import { createOpencodeClient } from '@opencode-ai/sdk/v2';

/**
 * opencode is the singleton OpenCode SDK client. All agent interaction
 * components use this client to communicate with the OpenCode server
 * through appx's reverse proxy at /api/opencode. The proxy strips the
 * prefix and forwards to localhost:4096, with appx auth middleware in front.
 */
export const opencode = createOpencodeClient({
  baseUrl: '/api/opencode',
});

/** OpenCode session as returned by the SDK. */
export interface OCSession {
  id: string;
  title: string;
  projectID: string;
  createdAt: string;
}

/** OpenCode event from the SSE stream. */
export interface OCEvent {
  type: string;
  properties: Record<string, unknown>;
}

/** Permission request from OpenCode requiring user approval. */
export interface OCPermissionRequest {
  id: string;
  sessionID: string;
  tool: string;
  input: Record<string, unknown>;
}
```

- [ ] **Step 2: Verify build**

```bash
task web
```

Note: If the SDK export path `@opencode-ai/sdk/v2` does not resolve, check the SDK's package.json exports and adjust the import. The SDK may export from the root as `@opencode-ai/sdk` with a named `createOpencodeClient` export. Adjust accordingly:

```typescript
// Alternative if /v2 subpath doesn't exist:
import { createOpencodeClient } from '@opencode-ai/sdk';
```

- [ ] **Step 3: Commit**

```bash
git add web/src/api/opencode.ts
git commit -m "feat: add OpenCode SDK client wrapper for agent interaction"
```

---

## Task 4: Rewrite ProjectCard for de-Docker architecture

Replace the container lifecycle buttons (Start/Stop/Reset) with a simplified card showing name, assigned port, app health status, and subdomain link.

**File:** `web/src/components/ProjectCard.tsx`

- [ ] **Step 1: Replace the full component**

```tsx
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import type { Project } from '../api/client';
import { deleteProject } from '../api/client';

/** ProjectCard renders a single project as a card with app health status,
 *  assigned port, subdomain link, and delete control. The left border is
 *  color-coded: green when the app is running, muted when not started. */
export default function ProjectCard({
  project,
  onUpdate,
  index,
}: {
  project: Project;
  onUpdate: () => void;
  index: number;
}) {
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  const [loading, setLoading] = useState(false);

  const statusClr = project.appRunning ? 'var(--green)' : 'var(--muted)';
  const statusLabel = project.appRunning ? 'RUNNING' : 'NOT STARTED';

  const handleDelete = async () => {
    setLoading(true);
    try {
      await deleteProject(project.id);
    } catch (e) {
      alert(e instanceof Error ? e.message : 'Failed to delete');
    } finally {
      setLoading(false);
      setConfirming(false);
      onUpdate();
    }
  };

  /** Builds the app subdomain URL for this project. Uses the current
   *  window location to determine protocol and port. */
  const subdomainUrl = (() => {
    const proto = window.location.protocol;
    const port = window.location.port;
    const portSuffix = port ? `:${port}` : '';
    return `${proto}//${project.name}.localhost${portSuffix}/`;
  })();

  return (
    <div
      data-card="project"
      style={{
        ...styles.card,
        borderLeft: `2px solid ${statusClr}`,
        animation: 'fadeSlideIn 0.3s ease both',
        animationDelay: `${index * 50}ms`,
      }}
    >
      <div style={styles.header}>
        <span style={styles.name}>{project.name}</span>
        <span style={styles.statusWrap}>
          <span style={{ ...styles.dot, background: statusClr }} />
          <span style={{ ...styles.statusText, color: statusClr }}>
            {statusLabel}
          </span>
        </span>
      </div>

      <div style={styles.meta}>
        <span style={styles.port}>:{project.assignedPort}</span>
        {project.appRunning && (
          <a
            href={subdomainUrl}
            target="_blank"
            rel="noopener noreferrer"
            style={styles.subdomainLink}
          >
            {project.name}.localhost
          </a>
        )}
      </div>

      <div style={styles.actions}>
        <button
          data-btn="outline-green"
          style={styles.outlineGreenBtn}
          onClick={() => navigate(`/projects/${project.id}`)}
        >
          Open
        </button>

        <div style={styles.deleteArea}>
          {confirming ? (
            <span style={styles.confirmGroup}>
              <span style={styles.confirmText}>Delete all data?</span>
              <button
                data-btn="text-red"
                style={{ ...styles.textBtn, color: 'var(--muted)' }}
                onClick={handleDelete}
                disabled={loading}
              >
                Yes
              </button>
              <button
                data-btn="text"
                style={styles.textBtn}
                onClick={() => setConfirming(false)}
              >
                No
              </button>
            </span>
          ) : (
            <button
              data-btn="text-red"
              style={{ ...styles.textBtn, color: 'var(--muted)' }}
              onClick={() => setConfirming(true)}
              disabled={loading}
            >
              Delete
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  card: {
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderLeft: '2px solid var(--subtle)',
    borderRadius: 4,
    padding: '16px 18px',
    display: 'flex',
    flexDirection: 'column',
    gap: 10,
  },
  header: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  name: {
    fontSize: 14,
    fontWeight: 500,
    color: 'var(--text)',
  },
  statusWrap: {
    display: 'flex',
    alignItems: 'center',
    gap: 6,
  },
  dot: {
    width: 7,
    height: 7,
    borderRadius: '50%',
    flexShrink: 0,
  },
  statusText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    textTransform: 'uppercase' as const,
    letterSpacing: '0.07em',
  },
  meta: {
    display: 'flex',
    alignItems: 'center',
    gap: 12,
  },
  port: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
  },
  subdomainLink: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--cyan)',
    textDecoration: 'none',
  },
  actions: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    marginTop: 2,
  },
  outlineGreenBtn: {
    background: 'transparent',
    border: '1px solid rgba(61,220,132,0.35)',
    color: 'var(--green)',
    borderRadius: 4,
    padding: '4px 14px',
    fontSize: 12,
    fontWeight: 500,
    cursor: 'pointer',
  },
  textBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    padding: '4px 8px',
    fontSize: 12,
    cursor: 'pointer',
  },
  deleteArea: {
    marginLeft: 'auto',
  },
  confirmGroup: {
    display: 'flex',
    alignItems: 'center',
    gap: 4,
  },
  confirmText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
    marginRight: 2,
  },
};
```

- [ ] **Step 2: Verify build**

```bash
task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/components/ProjectCard.tsx
git commit -m "ui: rewrite ProjectCard for de-Docker model (health status, subdomain link, no lifecycle)"
```

---

## Task 5: Rewrite CreateProjectModal -- name only, no port field

Port is auto-assigned by the backend. The modal only needs a name input.

**File:** `web/src/components/CreateProjectModal.tsx`

- [ ] **Step 1: Replace the full component**

```tsx
import { useState } from 'react';
import { createProject } from '../api/client';

/** CreateProjectModal renders a modal overlay with a form to create a new
 *  project. Only a name is required -- the backend auto-assigns a port from
 *  the 10000-10999 range. Validates the name as a valid slug on the client. */
export default function CreateProjectModal({
  onCreated,
  onClose,
}: {
  onCreated: () => void;
  onClose: () => void;
}) {
  const [name, setName] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const nameValid = /^[a-z][a-z0-9-]{0,61}[a-z0-9]$/.test(name) || (name.length === 2 && /^[a-z][a-z0-9]$/.test(name));

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!nameValid) return;

    setSubmitting(true);
    setError('');
    try {
      await createProject(name);
      onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create project');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div data-overlay style={styles.overlay} onClick={onClose}>
      <div style={styles.modal} onClick={e => e.stopPropagation()}>
        <h2 style={styles.title}>New Project</h2>
        <form onSubmit={handleSubmit}>
          <label style={styles.label}>
            <span style={styles.labelText}>NAME</span>
            <input
              style={styles.input}
              type="text"
              value={name}
              onChange={e => setName(e.target.value.toLowerCase())}
              placeholder="my-app"
              autoFocus
            />
            {name && !nameValid && (
              <span style={styles.hint}>Lowercase letters, numbers, hyphens. 2-63 chars.</span>
            )}
          </label>
          <p style={styles.portNote}>
            A unique port will be assigned automatically.
          </p>
          {error && <div style={styles.error}>{error}</div>}
          <div style={styles.actions}>
            <button
              type="button"
              data-btn="text"
              style={styles.cancelBtn}
              onClick={onClose}
            >
              Cancel
            </button>
            <button
              type="submit"
              data-btn="primary"
              style={styles.createBtn}
              disabled={!nameValid || submitting}
            >
              {submitting ? 'Creating...' : 'Create'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  overlay: {
    position: 'fixed',
    inset: 0,
    background: 'rgba(0,0,0,0.75)',
    backdropFilter: 'blur(4px)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    zIndex: 100,
  },
  modal: {
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderRadius: 6,
    padding: '24px 28px',
    width: 360,
    maxWidth: '90vw',
  },
  title: {
    margin: '0 0 22px',
    fontSize: 15,
    fontWeight: 500,
    color: 'var(--text)',
  },
  label: {
    display: 'flex',
    flexDirection: 'column',
    gap: 7,
    marginBottom: 10,
  },
  labelText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    letterSpacing: '0.1em',
    color: 'var(--muted)',
  },
  input: {
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '8px 12px',
    fontSize: 13,
    color: 'var(--text)',
    outline: 'none',
    width: '100%',
  },
  hint: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--red)',
  },
  portNote: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--muted)',
    margin: '0 0 18px',
    lineHeight: 1.5,
  },
  error: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--red)',
    marginBottom: 14,
    padding: '7px 10px',
    background: 'var(--red-dim)',
    border: '1px solid rgba(255,107,107,0.2)',
    borderRadius: 4,
  },
  actions: {
    display: 'flex',
    justifyContent: 'flex-end',
    alignItems: 'center',
    gap: 4,
    marginTop: 22,
  },
  cancelBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    padding: '7px 14px',
    fontSize: 13,
    cursor: 'pointer',
  },
  createBtn: {
    background: 'var(--blue)',
    border: 'none',
    color: '#fff',
    borderRadius: 4,
    padding: '7px 20px',
    fontSize: 13,
    fontWeight: 500,
    cursor: 'pointer',
  },
};
```

- [ ] **Step 2: Verify build**

```bash
task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/components/CreateProjectModal.tsx
git commit -m "ui: simplify CreateProjectModal -- name only, port auto-assigned"
```

---

## Task 6: Create OpenCode health status indicator component

A small component that polls the OpenCode health endpoint and shows a status dot in the dashboard header.

**File:** `web/src/components/OpenCodeStatus.tsx` (new)

- [ ] **Step 1: Create the component**

```tsx
import { useState, useEffect, useRef } from 'react';
import { getOpenCodeHealth } from '../api/client';

/** POLL_INTERVAL is how often we check OpenCode server health, in milliseconds. */
const POLL_INTERVAL = 10000;

/** OpenCodeStatus renders a small health indicator for the OpenCode server.
 *  It polls GET /api/opencode-health every 10 seconds and shows a colored dot
 *  with a label: green "OPENCODE" when healthy, red "OPENCODE" when down. */
export default function OpenCodeStatus() {
  const [healthy, setHealthy] = useState<boolean | null>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    const check = () => {
      getOpenCodeHealth()
        .then(res => setHealthy(res.healthy))
        .catch(() => setHealthy(false));
    };

    check();
    pollRef.current = setInterval(check, POLL_INTERVAL);

    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, []);

  const color = healthy === null
    ? 'var(--muted)'
    : healthy
      ? 'var(--green)'
      : 'var(--red)';

  return (
    <span style={styles.wrapper}>
      <span style={{ ...styles.dot, background: color }} />
      <span style={{ ...styles.label, color }}>OPENCODE</span>
    </span>
  );
}

const styles: Record<string, React.CSSProperties> = {
  wrapper: {
    display: 'flex',
    alignItems: 'center',
    gap: 5,
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
    letterSpacing: '0.07em',
  },
};
```

- [ ] **Step 2: Verify build**

```bash
task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/components/OpenCodeStatus.tsx
git commit -m "ui: add OpenCode server health status indicator component"
```

---

## Task 7: Rewrite Dashboard -- simplified project list with health indicator

Remove transitional state polling (no more starting/stopping). Add OpenCode health status to the header.

**File:** `web/src/pages/Dashboard.tsx`

- [ ] **Step 1: Replace the full page**

```tsx
import { useState, useEffect, useCallback, useRef } from 'react';
import { useNavigate } from 'react-router-dom';
import { getProjects, logout, type Project } from '../api/client';
import ProjectCard from '../components/ProjectCard';
import CreateProjectModal from '../components/CreateProjectModal';
import OpenCodeStatus from '../components/OpenCodeStatus';

/** POLL_INTERVAL is how often the dashboard refreshes project list to pick up
 *  app health changes, in milliseconds. */
const POLL_INTERVAL = 10000;

/** Dashboard is the main authenticated page. It fetches and displays the list of
 *  projects with app health status. Polls every 10 seconds to detect app
 *  start/stop on assigned ports. Shows OpenCode server health in the header.
 *  Redirects to /login on 401. */
export default function Dashboard() {
  const navigate = useNavigate();
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [showCreate, setShowCreate] = useState(false);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchProjects = useCallback(() => {
    getProjects()
      .then(setProjects)
      .catch(() => {
        window.location.href = '/login';
      })
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchProjects();
    pollRef.current = setInterval(fetchProjects, POLL_INTERVAL);
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [fetchProjects]);

  return (
    <div style={styles.container}>
      <header style={styles.header}>
        <div style={styles.headerLeft}>
          <span style={styles.wordmark}>APPX</span>
          <OpenCodeStatus />
        </div>
        <div style={styles.headerActions}>
          <button
            data-btn="new-project"
            style={styles.newProjectBtn}
            onClick={() => setShowCreate(true)}
          >
            + New Project
          </button>
          <span style={styles.separator}>|</span>
          <button
            data-btn="text-nav"
            style={styles.navBtn}
            onClick={() => navigate('/settings')}
          >
            Settings
          </button>
          <button
            data-btn="text-nav"
            style={styles.navBtn}
            onClick={() => navigate('/egress')}
          >
            Egress
          </button>
          <button
            data-btn="text-nav"
            style={{ ...styles.navBtn, color: 'var(--muted)' }}
            onClick={() => logout().then(() => { window.location.href = '/login'; })}
          >
            Logout
          </button>
        </div>
      </header>

      <main style={styles.main}>
        {loading ? (
          <div style={styles.grid}>
            {[0, 1, 2].map(i => (
              <div key={i} style={styles.skeleton} />
            ))}
          </div>
        ) : projects.length === 0 ? (
          <div style={styles.empty}>
            <p style={styles.emptyTitle}>No projects</p>
            <p style={styles.emptyHint}>Click + New Project to get started</p>
          </div>
        ) : (
          <div style={styles.grid}>
            {projects.map((p, i) => (
              <ProjectCard key={p.id} project={p} onUpdate={fetchProjects} index={i} />
            ))}
          </div>
        )}
      </main>

      {showCreate && (
        <CreateProjectModal
          onCreated={() => {
            setShowCreate(false);
            fetchProjects();
          }}
          onClose={() => setShowCreate(false)}
        />
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    minHeight: '100vh',
  },
  header: {
    borderBottom: '1px solid var(--border)',
    padding: '14px 24px',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  headerLeft: {
    display: 'flex',
    alignItems: 'center',
    gap: 16,
  },
  wordmark: {
    fontFamily: "'DM Sans', sans-serif",
    fontSize: 14,
    fontWeight: 500,
    letterSpacing: '0.35em',
    color: 'var(--text)',
  },
  headerActions: {
    display: 'flex',
    alignItems: 'center',
    gap: 4,
  },
  newProjectBtn: {
    background: 'transparent',
    border: '1px solid var(--border)',
    color: 'var(--text)',
    borderRadius: 4,
    padding: '5px 14px',
    fontSize: 13,
    fontWeight: 400,
    cursor: 'pointer',
  },
  separator: {
    color: 'var(--subtle)',
    fontSize: 14,
    padding: '0 6px',
    userSelect: 'none' as const,
  },
  navBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    padding: '5px 10px',
    fontSize: 13,
    cursor: 'pointer',
  },
  main: {
    padding: '28px 24px',
    maxWidth: 1080,
    margin: '0 auto',
  },
  grid: {
    display: 'grid',
    gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))',
    gap: 12,
  },
  skeleton: {
    background: 'linear-gradient(90deg, var(--surface) 25%, var(--surface-hover) 50%, var(--surface) 75%)',
    backgroundSize: '200% 100%',
    animation: 'shimmer 1.4s infinite',
    borderRadius: 4,
    height: 120,
    border: '1px solid var(--border)',
  },
  empty: {
    textAlign: 'center' as const,
    padding: '80px 0',
  },
  emptyTitle: {
    fontFamily: "'DM Sans', sans-serif",
    fontSize: 20,
    color: 'var(--muted)',
    margin: '0 0 10px',
    fontWeight: 400,
  },
  emptyHint: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
    color: 'var(--subtle)',
    margin: 0,
  },
};
```

- [ ] **Step 2: Verify build**

```bash
task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/pages/Dashboard.tsx
git commit -m "ui: rewrite Dashboard with health polling and OpenCode status indicator"
```

---

## Task 8: Create agent interaction components

Build the core agent interaction UI as reusable components. These use the OpenCode SDK client from Task 3 to create sessions, send prompts, display real-time events, and handle permission requests.

### Step 1: Session list component

**File:** `web/src/components/agent/SessionList.tsx` (new)

- [ ] **Step 1a: Create the component**

```tsx
import { useState, useEffect, useCallback } from 'react';
import { opencode } from '../../api/opencode';

/** SessionListItem represents a session in the list. */
interface SessionListItem {
  id: string;
  title: string;
  createdAt: string;
}

/** SessionList displays the list of OpenCode sessions for a given project
 *  directory. It allows creating new sessions and selecting an existing one.
 *  Designed as a reusable module for web, mobile, and desktop apps. */
export default function SessionList({
  projectDir,
  activeSessionId,
  onSelectSession,
}: {
  projectDir: string;
  activeSessionId: string | null;
  onSelectSession: (sessionId: string) => void;
}) {
  const [sessions, setSessions] = useState<SessionListItem[]>([]);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState('');

  const fetchSessions = useCallback(async () => {
    try {
      const res = await opencode.session.list({
        headers: { 'x-opencode-directory': projectDir },
      });
      const items: SessionListItem[] = (res.data ?? []).map((s: Record<string, unknown>) => ({
        id: s.id as string,
        title: (s.title as string) || 'Untitled',
        createdAt: s.createdAt as string,
      }));
      setSessions(items);
    } catch {
      setError('Failed to load sessions');
    }
  }, [projectDir]);

  useEffect(() => {
    fetchSessions();
  }, [fetchSessions]);

  const handleCreate = async () => {
    setCreating(true);
    setError('');
    try {
      const res = await opencode.session.create({
        body: {},
        headers: { 'x-opencode-directory': projectDir },
      });
      const newId = (res.data as Record<string, unknown>).id as string;
      await fetchSessions();
      onSelectSession(newId);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to create session');
    } finally {
      setCreating(false);
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <span style={styles.title}>SESSIONS</span>
        <button
          data-btn="outline-green"
          style={styles.createBtn}
          onClick={handleCreate}
          disabled={creating}
        >
          {creating ? 'Creating...' : '+ New'}
        </button>
      </div>

      {error && <div style={styles.error}>{error}</div>}

      <div style={styles.list}>
        {sessions.length === 0 ? (
          <span style={styles.emptyText}>No sessions yet</span>
        ) : (
          sessions.map(s => (
            <button
              key={s.id}
              style={s.id === activeSessionId ? styles.itemActive : styles.item}
              onClick={() => onSelectSession(s.id)}
            >
              <span style={styles.itemTitle}>{s.title}</span>
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
    overflow: 'hidden',
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
    fontWeight: 500,
    cursor: 'pointer',
  },
  error: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--red)',
    padding: '6px 14px',
  },
  list: {
    flex: 1,
    overflowY: 'auto',
  },
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
  itemTitle: {
    fontSize: 12,
    color: 'var(--text)',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap' as const,
  },
  itemId: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--muted)',
  },
};
```

- [ ] **Step 1b: Verify build**

```bash
task web
```

### Step 2: Chat panel component

**File:** `web/src/components/agent/ChatPanel.tsx` (new)

- [ ] **Step 2a: Create the component**

```tsx
import { useState, useEffect, useRef, useCallback } from 'react';
import { opencode } from '../../api/opencode';

/** ChatMessage represents a single message in the conversation. */
interface ChatMessage {
  id: string;
  role: 'user' | 'assistant';
  content: string;
  timestamp: string;
}

/** ChatPanel renders the agent conversation for a given session. It subscribes
 *  to the OpenCode SSE event stream for real-time updates and provides a prompt
 *  input for sending messages. Designed as a reusable module. */
export default function ChatPanel({
  sessionId,
  projectDir,
}: {
  sessionId: string;
  projectDir: string;
}) {
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState('');
  const [sending, setSending] = useState(false);
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState('');
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const abortRef = useRef<AbortController | null>(null);

  /** Scrolls the message list to the bottom. */
  const scrollToBottom = useCallback(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, []);

  useEffect(() => {
    scrollToBottom();
  }, [messages, scrollToBottom]);

  /** Subscribe to SSE events for this session. */
  useEffect(() => {
    const controller = new AbortController();
    abortRef.current = controller;

    const subscribe = async () => {
      try {
        const res = await opencode.event.subscribe({
          query: { sessionID: sessionId },
          headers: { 'x-opencode-directory': projectDir },
          signal: controller.signal,
        });

        setStreaming(true);

        for await (const event of res.stream) {
          const evt = event as Record<string, unknown>;
          const type = evt.type as string;

          if (type === 'message.part.text' || type === 'message.complete') {
            const props = evt.properties as Record<string, unknown>;
            const content = (props?.content as string) ?? '';
            const role = (props?.role as string) === 'user' ? 'user' as const : 'assistant' as const;
            const msgId = (props?.id as string) ?? crypto.randomUUID();

            setMessages(prev => {
              const existing = prev.find(m => m.id === msgId);
              if (existing) {
                return prev.map(m =>
                  m.id === msgId ? { ...m, content } : m
                );
              }
              return [...prev, {
                id: msgId,
                role,
                content,
                timestamp: new Date().toISOString(),
              }];
            });
          }
        }
      } catch (e) {
        if (!controller.signal.aborted) {
          setError(e instanceof Error ? e.message : 'Event stream failed');
        }
      } finally {
        setStreaming(false);
      }
    };

    subscribe();

    return () => {
      controller.abort();
    };
  }, [sessionId, projectDir]);

  /** Sends a prompt to the active session. */
  const handleSend = async () => {
    if (!input.trim() || sending) return;

    const text = input.trim();
    setInput('');
    setSending(true);
    setError('');

    // Optimistic: add user message immediately
    const userMsg: ChatMessage = {
      id: crypto.randomUUID(),
      role: 'user',
      content: text,
      timestamp: new Date().toISOString(),
    };
    setMessages(prev => [...prev, userMsg]);

    try {
      await opencode.session.prompt({
        params: { sessionID: sessionId },
        body: { parts: [{ type: 'text', text }] },
        headers: { 'x-opencode-directory': projectDir },
      });
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to send prompt');
    } finally {
      setSending(false);
    }
  };

  /** Handles Enter key to send prompt. */
  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.messages}>
        {messages.length === 0 && !streaming && (
          <div style={styles.empty}>
            <span style={styles.emptyText}>Send a prompt to start</span>
          </div>
        )}
        {messages.map(msg => (
          <div
            key={msg.id}
            style={msg.role === 'user' ? styles.userMsg : styles.assistantMsg}
          >
            <span style={styles.msgRole}>
              {msg.role === 'user' ? 'YOU' : 'AGENT'}
            </span>
            <pre style={styles.msgContent}>{msg.content}</pre>
          </div>
        ))}
        <div ref={messagesEndRef} />
      </div>

      {error && <div style={styles.error}>{error}</div>}

      <div style={styles.inputBar}>
        <textarea
          style={styles.input}
          value={input}
          onChange={e => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Send a message..."
          rows={1}
          disabled={sending}
        />
        <button
          data-btn="primary"
          style={styles.sendBtn}
          onClick={handleSend}
          disabled={sending || !input.trim()}
        >
          {sending ? '...' : 'Send'}
        </button>
      </div>
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
    gap: 12,
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
    maxWidth: '80%',
  },
  msgRole: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 9,
    letterSpacing: '0.1em',
    color: 'var(--muted)',
    display: 'block',
    marginBottom: 4,
  },
  msgContent: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
    color: 'var(--text)',
    margin: 0,
    whiteSpace: 'pre-wrap' as const,
    wordBreak: 'break-word' as const,
    lineHeight: 1.5,
  },
  error: {
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
};
```

- [ ] **Step 2b: Verify build**

```bash
task web
```

### Step 3: Permission handler component

**File:** `web/src/components/agent/PermissionPrompt.tsx` (new)

- [ ] **Step 3a: Create the component**

```tsx
import { useState } from 'react';
import { opencode } from '../../api/opencode';

/** PermissionRequest describes a pending tool permission from the agent. */
interface PermissionRequest {
  id: string;
  sessionID: string;
  tool: string;
  input: Record<string, unknown>;
}

/** PermissionPrompt renders a permission request from the OpenCode agent,
 *  allowing the user to approve or reject the action. Supports "once" (approve
 *  this instance) and "reject" responses. Designed as a reusable module. */
export default function PermissionPrompt({
  request,
  onResolved,
}: {
  request: PermissionRequest;
  onResolved: () => void;
}) {
  const [responding, setResponding] = useState(false);

  const handleReply = async (reply: 'once' | 'reject') => {
    setResponding(true);
    try {
      await opencode.permission.reply({
        body: { requestID: request.id, reply },
      });
      onResolved();
    } catch {
      // Permission may have timed out or been resolved elsewhere
      onResolved();
    } finally {
      setResponding(false);
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <span style={styles.label}>PERMISSION REQUEST</span>
      </div>
      <div style={styles.body}>
        <span style={styles.tool}>{request.tool}</span>
        <pre style={styles.inputPreview}>
          {JSON.stringify(request.input, null, 2)}
        </pre>
      </div>
      <div style={styles.actions}>
        <button
          data-btn="outline-green"
          style={styles.approveBtn}
          onClick={() => handleReply('once')}
          disabled={responding}
        >
          Approve
        </button>
        <button
          data-btn="outline-red"
          style={styles.rejectBtn}
          onClick={() => handleReply('reject')}
          disabled={responding}
        >
          Reject
        </button>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    background: 'var(--surface)',
    border: '1px solid var(--yellow)',
    borderRadius: 4,
    margin: '8px 20px',
    overflow: 'hidden',
  },
  header: {
    padding: '8px 14px',
    borderBottom: '1px solid var(--border)',
    background: 'var(--yellow-dim)',
  },
  label: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    letterSpacing: '0.1em',
    color: 'var(--yellow)',
  },
  body: {
    padding: '10px 14px',
  },
  tool: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
    color: 'var(--text)',
    fontWeight: 500,
  },
  inputPreview: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
    margin: '6px 0 0',
    whiteSpace: 'pre-wrap' as const,
    wordBreak: 'break-word' as const,
    maxHeight: 120,
    overflowY: 'auto',
    lineHeight: 1.4,
  },
  actions: {
    display: 'flex',
    gap: 8,
    padding: '8px 14px',
    borderTop: '1px solid var(--border)',
  },
  approveBtn: {
    background: 'transparent',
    border: '1px solid rgba(61,220,132,0.35)',
    color: 'var(--green)',
    borderRadius: 4,
    padding: '4px 14px',
    fontSize: 12,
    fontWeight: 500,
    cursor: 'pointer',
  },
  rejectBtn: {
    background: 'transparent',
    border: '1px solid rgba(255,107,107,0.35)',
    color: 'var(--red)',
    borderRadius: 4,
    padding: '4px 14px',
    fontSize: 12,
    fontWeight: 500,
    cursor: 'pointer',
  },
};
```

- [ ] **Step 3b: Verify build**

```bash
task web
```

### Step 4: Create agent directory barrel export

**File:** `web/src/components/agent/index.ts` (new)

- [ ] **Step 4a: Create the barrel export**

```typescript
/** Barrel export for agent interaction components. These are designed as
 *  reusable modules that can be shared across web, mobile (React Native),
 *  and desktop (Electron/Tauri) apps. */
export { default as SessionList } from './SessionList';
export { default as ChatPanel } from './ChatPanel';
export { default as PermissionPrompt } from './PermissionPrompt';
```

- [ ] **Step 4b: Verify build**

```bash
task web
```

- [ ] **Step 4c: Commit all agent components**

```bash
git add web/src/components/agent/ web/src/api/opencode.ts
git commit -m "feat: add agent interaction components (SessionList, ChatPanel, PermissionPrompt)"
```

---

## Task 9: Rewrite Project page -- agent + terminal tabs using SDK

Replace the old Project page (Docker-dependent lifecycle, iframe agent) with a new version that uses the OpenCode SDK for agent interaction and wires the terminal to OpenCode PTY.

**File:** `web/src/pages/Project.tsx`

- [ ] **Step 1: Replace the full page**

```tsx
import { useState, useEffect, useCallback } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { getProject, logout, type Project as ProjectType } from '../api/client';
import { SessionList, ChatPanel } from '../components/agent';
import Terminal from '../components/Terminal';

/** Project is the full-page project view with tabbed Agent/Terminal interface.
 *  The Agent tab uses the OpenCode SDK for session management and chat. The
 *  Terminal tab connects to OpenCode's PTY via WebSocket. No container lifecycle
 *  -- OpenCode is always running as a systemd service. */
export default function Project() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();

  const [project, setProject] = useState<ProjectType | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [activeTab, setActiveTab] = useState<'agent' | 'terminal'>('agent');
  const [activeSessionId, setActiveSessionId] = useState<string | null>(null);

  /** Fetches the project data. Redirects to /login on auth failure. */
  const fetchProject = useCallback(async () => {
    if (!id) return;
    try {
      const p = await getProject(id);
      setProject(p);
      setError('');
    } catch (e) {
      if (e instanceof Error && e.message.includes('401')) {
        window.location.href = '/login';
      } else {
        setError(e instanceof Error ? e.message : 'Failed to load project');
      }
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    fetchProject();
  }, [fetchProject]);

  /** Derives the project directory path used for OpenCode SDK headers.
   *  This matches the directory where appx scaffolded the project. */
  const projectDir = project ? `/home/opencode/projects/${project.name}` : '';

  /** Builds the app subdomain URL for this project. */
  const subdomainUrl = project ? (() => {
    const proto = window.location.protocol;
    const port = window.location.port;
    const portSuffix = port ? `:${port}` : '';
    return `${proto}//${project.name}.localhost${portSuffix}/`;
  })() : '';

  if (loading) {
    return (
      <div style={styles.container}>
        <div style={styles.centered}>
          <span style={styles.statusLabel}>Loading...</span>
        </div>
      </div>
    );
  }

  if (error || !project) {
    return (
      <div style={styles.container}>
        <div style={styles.centered}>
          <span style={styles.errorText}>{error || 'Project not found'}</span>
          <button
            data-btn="outline-green"
            style={styles.actionBtn}
            onClick={() => navigate('/')}
          >
            Back to Dashboard
          </button>
        </div>
      </div>
    );
  }

  return (
    <div style={styles.container}>
      <header style={styles.header}>
        <div style={styles.headerLeft}>
          <button style={styles.backBtn} onClick={() => navigate('/')} aria-label="Back to dashboard">
            &#8592;
          </button>
          <span style={styles.projectName}>{project.name}</span>
          <span style={styles.portLabel}>:{project.assignedPort}</span>
          {project.appRunning && (
            <span style={styles.appBadge}>
              <span style={styles.appDot} />
              APP RUNNING
            </span>
          )}
        </div>
        <div style={styles.headerActions}>
          {project.appRunning && (
            <a
              href={subdomainUrl}
              target="_blank"
              rel="noopener noreferrer"
              style={styles.appLink}
            >
              Open App
            </a>
          )}
          <button
            data-btn="text-nav"
            style={styles.navBtn}
            onClick={() => logout().then(() => { window.location.href = '/login'; })}
          >
            Logout
          </button>
        </div>
      </header>

      <div style={styles.tabBar}>
        <button
          style={activeTab === 'agent' ? styles.tabActive : styles.tab}
          onClick={() => setActiveTab('agent')}
        >
          Agent
        </button>
        <button
          style={activeTab === 'terminal' ? styles.tabActive : styles.tab}
          onClick={() => setActiveTab('terminal')}
        >
          Terminal
        </button>
      </div>

      <div style={styles.main}>
        {activeTab === 'agent' ? (
          <div style={styles.agentLayout}>
            <SessionList
              projectDir={projectDir}
              activeSessionId={activeSessionId}
              onSelectSession={setActiveSessionId}
            />
            {activeSessionId ? (
              <ChatPanel
                sessionId={activeSessionId}
                projectDir={projectDir}
              />
            ) : (
              <div style={styles.centered}>
                <span style={styles.statusLabel}>Select or create a session</span>
              </div>
            )}
          </div>
        ) : (
          <Terminal projectDir={projectDir} />
        )}
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    height: '100vh',
    display: 'flex',
    flexDirection: 'column',
    overflow: 'hidden',
  },
  header: {
    borderBottom: '1px solid var(--border)',
    padding: '10px 16px',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    flexShrink: 0,
  },
  headerLeft: {
    display: 'flex',
    alignItems: 'center',
    gap: 10,
  },
  backBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    fontSize: 18,
    cursor: 'pointer',
    padding: '0 4px',
    lineHeight: 1,
  },
  projectName: {
    fontSize: 14,
    fontWeight: 500,
    color: 'var(--text)',
  },
  portLabel: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
  },
  appBadge: {
    display: 'flex',
    alignItems: 'center',
    gap: 5,
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    letterSpacing: '0.07em',
    color: 'var(--green)',
  },
  appDot: {
    width: 6,
    height: 6,
    borderRadius: '50%',
    background: 'var(--green)',
    flexShrink: 0,
  },
  headerActions: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
  },
  appLink: {
    padding: '4px 12px',
    fontSize: 12,
    color: 'var(--cyan)',
    textDecoration: 'none',
    border: '1px solid var(--border)',
    borderRadius: 4,
  },
  navBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    padding: '5px 10px',
    fontSize: 12,
    cursor: 'pointer',
  },
  tabBar: {
    display: 'flex',
    gap: 4,
    padding: '8px 16px',
    borderBottom: '1px solid var(--border)',
    background: 'var(--bg)',
    flexShrink: 0,
  },
  tab: {
    padding: '6px 16px',
    cursor: 'pointer',
    border: '1px solid transparent',
    borderRadius: 4,
    fontSize: 13,
    color: 'var(--muted)',
    background: 'transparent',
  },
  tabActive: {
    padding: '6px 16px',
    cursor: 'pointer',
    border: '1px solid var(--border)',
    borderRadius: 4,
    fontSize: 13,
    color: 'var(--text)',
    background: 'var(--surface)',
  },
  main: {
    flex: 1,
    display: 'flex',
    minHeight: 0,
  },
  agentLayout: {
    flex: 1,
    display: 'flex',
    minHeight: 0,
  },
  centered: {
    flex: 1,
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 16,
  },
  statusLabel: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 13,
    color: 'var(--muted)',
    letterSpacing: '0.04em',
  },
  errorText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
    color: 'var(--red)',
    maxWidth: 400,
    textAlign: 'center' as const,
    lineHeight: 1.5,
  },
  actionBtn: {
    background: 'transparent',
    border: '1px solid rgba(61,220,132,0.35)',
    color: 'var(--green)',
    borderRadius: 4,
    padding: '6px 20px',
    fontSize: 12,
    fontWeight: 500,
    cursor: 'pointer',
  },
};
```

- [ ] **Step 2: Verify build**

```bash
task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/pages/Project.tsx
git commit -m "ui: rewrite Project page with SDK-based agent and OpenCode PTY terminal"
```

---

## Task 10: Rewire Terminal component to OpenCode PTY

The terminal now connects to OpenCode's PTY endpoint through the appx proxy instead of the old Docker-exec WebSocket.

**File:** `web/src/components/Terminal.tsx`

- [ ] **Step 1: Update the Terminal component**

Change the props from `sessionId` to `projectDir`. The WebSocket URL changes from `/ws/term/:id` to `/api/opencode/pty/:id/connect`. First, create the PTY session via the OpenCode API, then connect the WebSocket.

```tsx
import { useEffect, useRef, useState, useCallback } from 'react';
import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebLinksAddon } from '@xterm/addon-web-links';
import '@xterm/xterm/css/xterm.css';

/** Maximum number of reconnect attempts before showing a permanent failure overlay. */
const MAX_RETRIES = 5;

/** Base delay in milliseconds for exponential backoff reconnect. */
const BASE_DELAY = 1000;

/** Cap on reconnect delay in milliseconds. */
const MAX_DELAY = 8000;

/** WebSocket close codes that indicate intentional closure (no reconnect). */
const INTENTIONAL_CODES = [1000, 4004];

interface TerminalProps {
  projectDir: string;
}

/** Terminal renders an xterm.js terminal connected to OpenCode's PTY endpoint.
 *  It creates a PTY session via POST /api/opencode/pty, then connects a
 *  WebSocket to /api/opencode/pty/:id/connect. Handles auto-reconnect with
 *  exponential backoff, resize events, and mobile copy/paste. */
export default function Terminal({ projectDir }: TerminalProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<XTerm | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectRef = useRef<() => void>(() => {});

  const [connected, setConnected] = useState(false);
  const [reconnecting, setReconnecting] = useState(false);
  const [failed, setFailed] = useState(false);
  const [initializing, setInitializing] = useState(true);
  const [hasSelection, setHasSelection] = useState(false);
  const [isMobile] = useState(() => 'ontouchstart' in window);

  useEffect(() => {
    if (!containerRef.current) return;

    let intentionalClose = false;
    let retries = 0;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    let ptyId: string | null = null;

    const term = new XTerm({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: "'JetBrains Mono', monospace",
      theme: {
        background: '#060c0e',
        foreground: '#e2f4f8',
        cursor: '#00e5ff',
        selectionBackground: 'rgba(0, 229, 255, 0.25)',
        black: '#0d1214',
        red: '#ff6b6b',
        green: '#3ddc84',
        yellow: '#f5c518',
        blue: '#0369a1',
        magenta: '#c084fc',
        cyan: '#00e5ff',
        white: '#e2f4f8',
        brightBlack: '#1a2c30',
        brightRed: '#ff8a8a',
        brightGreen: '#5ce89d',
        brightYellow: '#f7d24a',
        brightBlue: '#0284c7',
        brightMagenta: '#d4a5fd',
        brightCyan: '#33ecff',
        brightWhite: '#ffffff',
      },
    });

    const fitAddon = new FitAddon();
    const webLinksAddon = new WebLinksAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(webLinksAddon);

    termRef.current = term;

    term.open(containerRef.current);
    fitAddon.fit();

    /** Creates a PTY session via the OpenCode API. */
    async function createPty(): Promise<string> {
      const res = await fetch('/api/opencode/pty', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'x-opencode-directory': projectDir,
        },
        body: JSON.stringify({}),
      });
      if (!res.ok) throw new Error(`PTY create failed: ${res.status}`);
      const data = await res.json();
      return data.id;
    }

    /** Opens a WebSocket to the PTY endpoint. */
    function connectWs(id: string) {
      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const url = `${proto}//${window.location.host}/api/opencode/pty/${id}/connect`;
      const ws = new WebSocket(url);
      ws.binaryType = 'arraybuffer';
      wsRef.current = ws;

      ws.onopen = () => {
        retries = 0;
        setConnected(true);
        setReconnecting(false);
        setFailed(false);

        fitAddon.fit();
        const { cols, rows } = term;
        ws.send(JSON.stringify({ type: 'resize', cols, rows }));
      };

      ws.onmessage = (ev) => {
        if (ev.data instanceof ArrayBuffer) {
          term.write(new Uint8Array(ev.data));
        } else {
          term.write(ev.data);
        }
      };

      ws.onclose = (ev) => {
        setConnected(false);

        if (intentionalClose || INTENTIONAL_CODES.includes(ev.code)) {
          return;
        }

        if (retries >= MAX_RETRIES) {
          setReconnecting(false);
          setFailed(true);
          return;
        }

        const delay = Math.min(BASE_DELAY * Math.pow(2, retries), MAX_DELAY);
        retries += 1;
        setReconnecting(true);

        reconnectTimer = setTimeout(() => {
          if (ptyId) connectWs(ptyId);
        }, delay);
      };

      ws.onerror = () => {
        // onclose will fire after onerror
      };
    }

    // Initialize: create PTY, then connect WebSocket
    (async () => {
      try {
        ptyId = await createPty();
        setInitializing(false);
        connectWs(ptyId);
      } catch {
        setInitializing(false);
        setFailed(true);
      }
    })();

    // Expose reconnect for the manual reconnect button
    reconnectRef.current = () => {
      retries = 0;
      setFailed(false);
      setReconnecting(true);
      if (ptyId) connectWs(ptyId);
    };

    // Send keystrokes to WS
    const dataDisposable = term.onData((data) => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        const encoder = new TextEncoder();
        ws.send(encoder.encode(data));
      }
    });

    // Track selection for mobile copy button
    const selectionDisposable = term.onSelectionChange(() => {
      setHasSelection(term.hasSelection());
    });

    // ResizeObserver: fit terminal + send resize to backend
    const observer = new ResizeObserver(() => {
      fitAddon.fit();
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        const { cols, rows } = term;
        ws.send(JSON.stringify({ type: 'resize', cols, rows }));
      }
    });
    observer.observe(containerRef.current);

    return () => {
      intentionalClose = true;
      dataDisposable.dispose();
      selectionDisposable.dispose();
      observer.disconnect();
      if (reconnectTimer) clearTimeout(reconnectTimer);
      wsRef.current?.close();
      term.dispose();
      termRef.current = null;
    };
  }, [projectDir]);

  /** Handles manual reconnect after max retries exhausted. */
  const handleManualReconnect = useCallback(() => {
    reconnectRef.current();
  }, []);

  /** Copies the current terminal selection to the clipboard. */
  const handleCopy = useCallback(async () => {
    const term = termRef.current;
    if (!term || !term.hasSelection()) return;
    try {
      await navigator.clipboard.writeText(term.getSelection());
    } catch {
      // Clipboard API may not be available
    }
  }, []);

  /** Pastes from the clipboard into the terminal. */
  const handlePaste = useCallback(async () => {
    try {
      const text = await navigator.clipboard.readText();
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN && text) {
        const encoder = new TextEncoder();
        ws.send(encoder.encode(text));
      }
    } catch {
      // Clipboard API may not be available
    }
  }, []);

  return (
    <div style={styles.wrapper}>
      <div ref={containerRef} style={styles.terminal} />

      {/* Initializing overlay */}
      {initializing && (
        <div style={styles.overlay}>
          <span style={styles.overlayText}>Connecting to terminal...</span>
        </div>
      )}

      {/* Reconnecting overlay */}
      {reconnecting && (
        <div style={styles.overlay}>
          <span style={styles.overlayText}>Reconnecting...</span>
        </div>
      )}

      {/* Connection lost overlay */}
      {failed && (
        <div style={styles.overlay}>
          <span style={styles.overlayText}>Connection lost</span>
          <button
            data-btn="outline-green"
            style={styles.reconnectBtn}
            onClick={handleManualReconnect}
          >
            Reconnect
          </button>
        </div>
      )}

      {/* Mobile floating buttons */}
      {isMobile && connected && (
        <div style={styles.mobileButtons}>
          {hasSelection && (
            <button style={styles.mobileBtn} onClick={handleCopy}>
              Copy
            </button>
          )}
          <button style={styles.mobileBtn} onClick={handlePaste}>
            Paste
          </button>
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  wrapper: {
    position: 'relative',
    flex: 1,
    minHeight: 0,
    overflow: 'hidden',
  },
  terminal: {
    width: '100%',
    height: '100%',
  },
  overlay: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    background: 'rgba(6, 12, 14, 0.85)',
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 16,
    zIndex: 10,
  },
  overlayText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 13,
    color: 'var(--muted)',
    letterSpacing: '0.04em',
  },
  reconnectBtn: {
    background: 'transparent',
    border: '1px solid rgba(61,220,132,0.35)',
    color: 'var(--green)',
    borderRadius: 4,
    padding: '6px 18px',
    fontSize: 12,
    fontWeight: 500,
    cursor: 'pointer',
  },
  mobileButtons: {
    position: 'absolute',
    bottom: 12,
    right: 12,
    display: 'flex',
    gap: 8,
    zIndex: 5,
  },
  mobileBtn: {
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    color: 'var(--text)',
    borderRadius: 4,
    padding: '6px 14px',
    fontSize: 12,
    fontWeight: 500,
    cursor: 'pointer',
  },
};
```

- [ ] **Step 2: Verify build**

```bash
task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/components/Terminal.tsx
git commit -m "ui: rewire Terminal to OpenCode PTY via /api/opencode/pty/:id/connect"
```

---

## Task 11: Create Egress Log page

New page showing outbound connections table and allowlist editor.

**File:** `web/src/pages/Egress.tsx` (new)

- [ ] **Step 1: Create the page**

```tsx
import { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  getEgressLog,
  getEgressAllowlist,
  setEgressAllowlist,
  logout,
  type EgressLogEntry,
} from '../api/client';

/** Egress renders the egress log viewer and allowlist editor. Shows a table of
 *  outbound connections with timestamp, destination, port, and allow/block
 *  status. The allowlist section lets users add or remove allowed destinations. */
export default function Egress() {
  const navigate = useNavigate();
  const [entries, setEntries] = useState<EgressLogEntry[]>([]);
  const [allowlist, setAllowlist] = useState<string[]>([]);
  const [newEntry, setNewEntry] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');

  const fetchData = useCallback(async () => {
    try {
      const [log, al] = await Promise.all([
        getEgressLog(),
        getEgressAllowlist(),
      ]);
      setEntries(log);
      setAllowlist(al.entries);
    } catch {
      window.location.href = '/login';
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const handleAddEntry = async () => {
    const trimmed = newEntry.trim();
    if (!trimmed || allowlist.includes(trimmed)) return;

    const updated = [...allowlist, trimmed];
    setSaving(true);
    setError('');
    setSuccess('');
    try {
      await setEgressAllowlist(updated);
      setAllowlist(updated);
      setNewEntry('');
      setSuccess('Allowlist updated');
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to update allowlist');
    } finally {
      setSaving(false);
    }
  };

  const handleRemoveEntry = async (entry: string) => {
    const updated = allowlist.filter(e => e !== entry);
    setSaving(true);
    setError('');
    setSuccess('');
    try {
      await setEgressAllowlist(updated);
      setAllowlist(updated);
      setSuccess('Allowlist updated');
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to update allowlist');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div style={styles.container}>
      <header style={styles.header}>
        <span style={styles.wordmark}>APPX</span>
        <div style={styles.headerActions}>
          <button
            data-btn="text-nav"
            style={{ ...styles.navBtn, color: 'var(--muted)' }}
            onClick={() => logout().then(() => { window.location.href = '/login'; })}
          >
            Logout
          </button>
        </div>
      </header>

      <main style={styles.main}>
        <div style={styles.pageHeader}>
          <button style={styles.backBtn} onClick={() => navigate('/')} aria-label="Back to dashboard">&#8592;</button>
          <span style={styles.pageTitle}>EGRESS LOG</span>
        </div>

        {/* Allowlist editor */}
        <div style={styles.card}>
          <h3 style={styles.cardTitle}>Allowlist</h3>
          <p style={styles.description}>
            Destinations the OpenCode agent is allowed to reach. Format: <code style={styles.code}>host:port</code>
          </p>

          {error && <div style={styles.errorMsg}>{error}</div>}
          {success && <div style={styles.successMsg}>{success}</div>}

          <div style={styles.allowlistItems}>
            {allowlist.map(entry => (
              <div key={entry} style={styles.allowlistItem}>
                <span style={styles.allowlistText}>{entry}</span>
                <button
                  data-btn="text-red"
                  style={styles.removeBtn}
                  onClick={() => handleRemoveEntry(entry)}
                  disabled={saving}
                >
                  Remove
                </button>
              </div>
            ))}
          </div>

          <div style={styles.inputRow}>
            <input
              style={styles.input}
              type="text"
              value={newEntry}
              onChange={e => setNewEntry(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && handleAddEntry()}
              placeholder="api.example.com:443"
            />
            <button
              data-btn="primary"
              style={styles.addBtn}
              onClick={handleAddEntry}
              disabled={saving || !newEntry.trim()}
            >
              Add
            </button>
          </div>
        </div>

        {/* Connection log */}
        <div style={{ ...styles.card, marginTop: 16 }}>
          <h3 style={styles.cardTitle}>Connection Log</h3>

          {loading ? (
            <span style={styles.loadingText}>Loading...</span>
          ) : entries.length === 0 ? (
            <span style={styles.emptyText}>No outbound connections logged yet</span>
          ) : (
            <div style={styles.table}>
              <div style={styles.tableHeader}>
                <span style={{ ...styles.tableCell, flex: 2 }}>TIMESTAMP</span>
                <span style={{ ...styles.tableCell, flex: 3 }}>DESTINATION</span>
                <span style={{ ...styles.tableCell, flex: 1 }}>PORT</span>
                <span style={{ ...styles.tableCell, flex: 1 }}>STATUS</span>
              </div>
              {entries.map(entry => (
                <div key={entry.id} style={styles.tableRow}>
                  <span style={{ ...styles.tableCellValue, flex: 2 }}>
                    {new Date(entry.timestamp).toLocaleTimeString()}
                  </span>
                  <span style={{ ...styles.tableCellValue, flex: 3 }}>
                    {entry.destination}
                  </span>
                  <span style={{ ...styles.tableCellValue, flex: 1 }}>
                    {entry.port}
                  </span>
                  <span style={{
                    ...styles.tableCellValue,
                    flex: 1,
                    color: entry.status === 'allowed' ? 'var(--green)' : 'var(--red)',
                  }}>
                    {entry.status.toUpperCase()}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      </main>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    minHeight: '100vh',
  },
  header: {
    borderBottom: '1px solid var(--border)',
    padding: '14px 24px',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  wordmark: {
    fontFamily: "'DM Sans', sans-serif",
    fontSize: 14,
    fontWeight: 500,
    letterSpacing: '0.35em',
    color: 'var(--text)',
  },
  headerActions: {
    display: 'flex',
    alignItems: 'center',
    gap: 4,
  },
  navBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    padding: '5px 10px',
    fontSize: 13,
    cursor: 'pointer',
  },
  main: {
    padding: '28px 24px',
    maxWidth: 800,
    margin: '0 auto',
  },
  pageHeader: {
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    marginBottom: 20,
  },
  backBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    fontSize: 20,
    cursor: 'pointer',
    padding: '0 4px',
    lineHeight: 1,
  },
  pageTitle: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    letterSpacing: '0.12em',
    color: 'var(--muted)',
  },
  card: {
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '20px 22px',
  },
  cardTitle: {
    margin: '0 0 8px',
    fontSize: 14,
    fontWeight: 500,
    color: 'var(--text)',
  },
  description: {
    color: 'var(--muted)',
    fontSize: 13,
    lineHeight: 1.6,
    margin: '0 0 18px',
  },
  code: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    background: 'var(--bg)',
    padding: '1px 5px',
    borderRadius: 3,
    color: 'var(--text)',
  },
  errorMsg: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--red)',
    marginBottom: 14,
    padding: '7px 10px',
    background: 'var(--red-dim)',
    border: '1px solid rgba(255,107,107,0.2)',
    borderRadius: 4,
  },
  successMsg: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--green)',
    marginBottom: 14,
    padding: '7px 10px',
    background: 'var(--green-dim)',
    border: '1px solid rgba(61,220,132,0.2)',
    borderRadius: 4,
  },
  allowlistItems: {
    display: 'flex',
    flexDirection: 'column',
    gap: 4,
    marginBottom: 14,
  },
  allowlistItem: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: '6px 10px',
    background: 'var(--bg)',
    borderRadius: 4,
    border: '1px solid var(--border)',
  },
  allowlistText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
    color: 'var(--text)',
  },
  removeBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    padding: '2px 6px',
    fontSize: 11,
    cursor: 'pointer',
  },
  inputRow: {
    display: 'flex',
    gap: 8,
  },
  input: {
    flex: 1,
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '8px 12px',
    color: 'var(--text)',
    fontSize: 13,
    outline: 'none',
  },
  addBtn: {
    background: 'var(--blue)',
    border: 'none',
    color: '#fff',
    borderRadius: 4,
    padding: '8px 18px',
    fontSize: 13,
    fontWeight: 500,
    cursor: 'pointer',
  },
  loadingText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
    color: 'var(--muted)',
  },
  emptyText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
    color: 'var(--muted)',
  },
  table: {
    display: 'flex',
    flexDirection: 'column',
  },
  tableHeader: {
    display: 'flex',
    padding: '8px 10px',
    borderBottom: '1px solid var(--border)',
  },
  tableCell: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    letterSpacing: '0.1em',
    color: 'var(--muted)',
  },
  tableRow: {
    display: 'flex',
    padding: '8px 10px',
    borderBottom: '1px solid var(--border)',
  },
  tableCellValue: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--text)',
  },
};
```

- [ ] **Step 2: Verify build**

```bash
task web
```

- [ ] **Step 3: Commit**

```bash
git add web/src/pages/Egress.tsx
git commit -m "ui: add Egress log viewer with allowlist editor"
```

---

## Task 12: Update routing -- add Egress route, update Settings

**File:** `web/src/App.tsx`

- [ ] **Step 1: Add Egress route**

```tsx
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import Login from './pages/Login';
import Dashboard from './pages/Dashboard';
import Settings from './pages/Settings';
import Project from './pages/Project';
import Egress from './pages/Egress';

/** App is the root component that sets up client-side routing. Unauthenticated
 *  users are directed to /login; authenticated users see the Dashboard at /. */
export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/" element={<Dashboard />} />
        <Route path="/projects/:id" element={<Project />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/egress" element={<Egress />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  );
}
```

- [ ] **Step 2: Update Settings page -- remove terminal buffer size, add egress link**

In `web/src/pages/Settings.tsx`, remove the terminal buffer size section (OpenCode manages its own PTY buffers). The Anthropic API key section stays. Add a link to the Egress page.

Remove from the Settings page:
- The `bufferSize`, `bufferInput`, `bufferSaving`, `bufferError`, `bufferSuccess` state variables
- The `getTerminalBufferSize` and `setTerminalBufferSize` imports
- The `handleBufferSave` function
- The entire Terminal card section (the `<div>` containing "Terminal" / "Scrollback buffer size")

Add after the API Key card:
```tsx
<div style={{ ...styles.card, marginTop: 16 }}>
  <h3 style={styles.cardTitle}>Egress Control</h3>
  <p style={styles.description}>
    View outbound connections and manage the network allowlist.
  </p>
  <button
    data-btn="outline-green"
    style={styles.egressBtn}
    onClick={() => navigate('/egress')}
  >
    Open Egress Log
  </button>
</div>
```

Add to the styles object:
```typescript
egressBtn: {
  background: 'transparent',
  border: '1px solid rgba(61,220,132,0.35)',
  color: 'var(--green)',
  borderRadius: 4,
  padding: '6px 18px',
  fontSize: 12,
  fontWeight: 500,
  cursor: 'pointer',
},
```

- [ ] **Step 3: Verify build**

```bash
task web
```

- [ ] **Step 4: Commit**

```bash
git add web/src/App.tsx web/src/pages/Settings.tsx
git commit -m "ui: add Egress route, simplify Settings (remove terminal buffer size)"
```

---

## Task 13: Clean up unused imports and dead code

- [ ] **Step 1: Remove old API functions no longer imported anywhere**

Verify that the following are no longer imported by any component. If `client.ts` was replaced entirely in Task 2, these are already gone:
- `startProject`, `stopProject`, `resetProject`
- `updateProject`
- `createSession`, `listSessions`, `deleteSession`

- [ ] **Step 2: Run linter**

```bash
task lint
```

Fix any unused import warnings or type errors.

- [ ] **Step 3: Verify full build**

```bash
task build
```

- [ ] **Step 4: Run tests**

```bash
task test
```

Note: Go tests may fail if the backend handlers still reference old types. This is expected -- the backend was updated in Steps 1-6 of Phase 5. If tests fail, the issue is backend-side, not frontend-side. The frontend should compile and lint cleanly.

- [ ] **Step 5: Commit any fixes**

```bash
git add -A web/src/
git commit -m "chore: clean up unused imports and dead code from frontend"
```

---

## Task 14: Final verification

- [ ] **Step 1: Full frontend build**

```bash
task web
```

Expected: builds cleanly with no errors or warnings.

- [ ] **Step 2: Full project build**

```bash
task build
```

Expected: Go binary compiles with embedded frontend.

- [ ] **Step 3: Lint check**

```bash
task lint
```

Expected: no errors.

- [ ] **Step 4: Manual verification checklist**

Verify the following by running `./appx --http --port 8080` (or `./appx -port 8443` for HTTPS) and checking in the browser:

```
[ ] Dashboard loads at /
[ ] OpenCode health indicator shows in header (green if OC running, red if not)
[ ] "Egress" nav link in header navigates to /egress
[ ] Project cards show: name, assigned port, health status (running/not started)
[ ] No Start/Stop/Reset buttons on project cards
[ ] "Open" button on project card navigates to /projects/:id
[ ] "+ New Project" modal has name field only (no port field)
[ ] Creating a project shows it in the list with auto-assigned port
[ ] Project page has Agent and Terminal tabs
[ ] Agent tab shows session list sidebar + chat panel
[ ] Creating a session in Agent tab works (requires OpenCode running)
[ ] Sending a prompt shows user message immediately (optimistic)
[ ] Terminal tab connects to OpenCode PTY (requires OpenCode running)
[ ] Egress page shows connection log table
[ ] Egress allowlist editor adds/removes entries
[ ] Settings page shows API key management + Egress link (no terminal buffer)
[ ] 401 on any API call redirects to /login
[ ] Subdomain link on project card opens correct URL
```

- [ ] **Step 5: Final commit**

```bash
git add -A
git commit -m "feat: Phase 5 Step 7 complete -- frontend adapted for de-Docker architecture"
```
