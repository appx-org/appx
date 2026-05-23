const BASE = '/api';

/**
 * request is the shared HTTP client for all API calls. It prepends the /api
 * base path, sets JSON content-type, and throws on non-2xx responses.
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

/** Ends the current session. */
export function logout() {
  return request<{ status: string }>('/session', { method: 'DELETE' });
}

/** Authenticates with POST /api/login. */
export function login(password: string) {
  return request<{ status: string }>('/login', {
    method: 'POST',
    body: JSON.stringify({ password }),
  });
}

/** A project as returned by the API. */
export interface Project {
  id: string;
  name: string;
  status: string;
  assignedPort: number;
  appRunning: boolean;
  openCodeProjectId?: string;
  lastError?: string;
  createdAt: string;
  projectDir?: string;
}

/** Server config returned by GET /api/config. */
export interface ServerConfig {
  baseDomain: string;
  agentBackend: 'opencode' | 'pi';
}

/** Fetches server runtime configuration including baseDomain. GET /api/config. */
export function getServerConfig() {
  return request<ServerConfig>('/config');
}

/** Fetches all projects. */
export function getProjects() {
  return request<Project[]>('/projects');
}

/** Fetches a single project by ID. */
export function getProject(id: string) {
  return request<Project>(`/projects/${id}`);
}

/** Creates a new project. Port is auto-assigned by the backend. */
export function createProject(name: string) {
  return request<Project>('/projects', {
    method: 'POST',
    body: JSON.stringify({ name }),
  });
}

/** Deletes a project and its directory. */
export function deleteProject(id: string) {
  return request<void>(`/projects/${id}`, { method: 'DELETE' });
}

/** Changes the user password. Requires current password for re-authentication.
 *  All other sessions are invalidated; the current session gets a fresh cookie. */
export function changePassword(currentPassword: string, newPassword: string) {
  return request<{ status: string }>('/settings/password', {
    method: 'PUT',
    body: JSON.stringify({ currentPassword, newPassword }),
  });
}

/** Checks whether an Anthropic API key is configured. */
export function getApiKeyStatus() {
  return request<{ set: boolean }>('/settings/api-key');
}

/** Stores an Anthropic API key. */
export function setApiKey(key: string) {
  return request<{ status: string }>('/settings/api-key', {
    method: 'PUT',
    body: JSON.stringify({ key }),
  });
}

/** Removes the stored Anthropic API key. */
export function deleteApiKey() {
  return request<{ status: string }>('/settings/api-key', { method: 'DELETE' });
}

/** OpenCode server health status. */
export interface OpenCodeHealth {
  healthy: boolean;
}

/** Checks if the OpenCode server is reachable. GET /api/opencode/health. */
export function getOpenCodeHealth() {
  return request<OpenCodeHealth>('/opencode/health');
}

/** A single egress log entry. */
export interface EgressLogEntry {
  id: number;
  destination: string;
  port: number;
  allowed: boolean;
  timestamp: string;
}

/** Paginated egress log response. */
export interface EgressLogResponse {
  entries: EgressLogEntry[];
  total: number;
}

/** Fetches the paginated egress log. GET /api/egress/log. */
export function getEgressLog(offset = 0, limit = 50) {
  return request<EgressLogResponse>(`/egress/log?offset=${offset}&limit=${limit}`);
}

/** Fetches the current egress allowlist. GET /api/egress/allowlist. */
export function getEgressAllowlist() {
  return request<{ entries: string[] }>('/egress/allowlist');
}

/** Updates the egress allowlist. PUT /api/egress/allowlist. */
export function setEgressAllowlist(entries: string[]) {
  return request<{ status: string }>('/egress/allowlist', {
    method: 'PUT',
    body: JSON.stringify({ entries }),
  });
}

/** A pending egress permission request from the agent. */
export interface EgressPendingRequest {
  id: string;
  host: string;
  port: number;
  reason: string;
  createdAt: string;
}

/** Fetches pending egress permission requests. GET /api/egress/pending. */
export function getEgressPending() {
  return request<{ requests: EgressPendingRequest[] }>('/egress/pending');
}

/** Approves a pending egress request, adding host:port to the allowlist. */
export function approveEgressRequest(id: string) {
  return request<{ status: string }>(`/egress/pending/${id}/approve`, { method: 'POST' });
}

/** Denies a pending egress request. */
export function denyEgressRequest(id: string) {
  return request<{ status: string }>(`/egress/pending/${id}/deny`, { method: 'POST' });
}
