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
  lastError?: string;
  createdAt: string;
  projectDir?: string;
}

/** Server config returned by GET /api/config. */
export interface ServerConfig {
  baseDomain: string;
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

export interface AgentAuthProvider {
  provider: string;
  name: string;
  configured: boolean;
  credentialType?: 'api_key' | 'oauth';
  source?: 'stored' | 'runtime' | 'environment' | 'fallback' | 'models_json_key' | 'models_json_command';
  label?: string;
  supportsApiKey: boolean;
  supportsSubscription: boolean;
  modelCount: number;
  availableModelCount: number;
}

export interface AgentOAuthFlowState {
  id: string;
  provider: string;
  providerName: string;
  status: 'starting' | 'prompt' | 'auth' | 'waiting' | 'complete' | 'error' | 'cancelled';
  authUrl?: string;
  instructions?: string;
  prompt?: {
    message: string;
    placeholder?: string;
    allowEmpty?: boolean;
  };
  progress: string[];
  error?: string;
  expiresAt: string;
}

export type AgentCustomProviderApi = 'openai-completions' | 'openai-responses' | 'anthropic-messages';

export interface AgentCustomProviderModel {
  id: string;
  name?: string;
  api?: AgentCustomProviderApi;
  reasoning?: boolean;
  thinkingLevelMap?: Partial<Record<'off' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh', string | null>>;
  input?: Array<'text' | 'image'>;
  contextWindow?: number;
  maxTokens?: number;
  compat?: Record<string, unknown>;
}

export interface AgentCustomProvider {
  provider: string;
  name?: string;
  baseUrl?: string;
  api?: AgentCustomProviderApi;
  apiKeyConfigured: boolean;
  modelCount: number;
  models: AgentCustomProviderModel[];
}

/** Fetches Pi provider auth status. No secret values are returned. */
export function getAgentAuthProviders() {
  return request<{ providers: AgentAuthProvider[] }>('/agent/auth/providers');
}

/** Stores an API key for a Pi provider in the agent runtime user's auth storage. */
export function setAgentProviderApiKey(provider: string, key: string) {
  return request<{ ok: true }>(`/agent/auth/providers/${encodeURIComponent(provider)}/api-key`, {
    method: 'PUT',
    body: JSON.stringify({ key }),
  });
}

/** Removes a stored Pi provider credential from the agent runtime user's auth storage. */
export function deleteAgentProviderCredential(provider: string) {
  return request<{ ok: true }>(`/agent/auth/providers/${encodeURIComponent(provider)}`, {
    method: 'DELETE',
  });
}

/** Starts a Pi subscription OAuth flow for a provider such as OpenAI Codex or Anthropic. */
export function startAgentProviderSubscription(provider: string) {
  return request<AgentOAuthFlowState>(
    `/agent/auth/providers/${encodeURIComponent(provider)}/subscription/start`,
    { method: 'POST' },
  );
}

/** Fetches the current state for a pending subscription auth flow. */
export function getAgentSubscriptionFlow(flowId: string) {
  return request<AgentOAuthFlowState>(`/agent/auth/subscription/${encodeURIComponent(flowId)}`);
}

/** Continues a pending subscription auth flow with prompt input or a pasted redirect URL/code. */
export function continueAgentSubscriptionFlow(flowId: string, value: string) {
  return request<AgentOAuthFlowState>(`/agent/auth/subscription/${encodeURIComponent(flowId)}/continue`, {
    method: 'POST',
    body: JSON.stringify({ value }),
  });
}

/** Cancels a pending subscription auth flow. */
export function cancelAgentSubscriptionFlow(flowId: string) {
  return request<AgentOAuthFlowState>(`/agent/auth/subscription/${encodeURIComponent(flowId)}`, {
    method: 'DELETE',
  });
}

/** Lists Pi custom providers managed through agent-server models.json. */
export function getAgentCustomProviders() {
  return request<{ providers: AgentCustomProvider[] }>('/agent/custom/providers');
}

/** Creates or updates a Pi custom provider, including LiteLLM-compatible providers. */
export function upsertAgentCustomProvider(body: {
  provider: string;
  name?: string;
  baseUrl: string;
  api: AgentCustomProviderApi;
  apiKey?: string;
  models: AgentCustomProviderModel[];
}) {
  return request<AgentCustomProvider>('/agent/custom/providers', {
    method: 'PUT',
    body: JSON.stringify(body),
  });
}

/** Removes a custom Pi provider from models.json. */
export function deleteAgentCustomProvider(provider: string) {
  return request<{ ok: true }>(`/agent/custom/providers/${encodeURIComponent(provider)}`, {
    method: 'DELETE',
  });
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
