export type PiSessionInfo = {
  id: string;
  createdAt: string;
  firstMessage: string;
  messageCount: number;
};

export type ThinkingLevel = 'off' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh';

export type PiAgentModel = {
  provider: string;
  id: string;
  name: string;
  api: string;
  reasoning: boolean;
  available: boolean;
  input: Array<'text' | 'image'>;
  contextWindow: number;
  maxTokens: number;
  defaultThinkingLevel?: ThinkingLevel;
};

export type PiSessionModelSettings = {
  model: PiAgentModel | null;
  thinkingLevel: ThinkingLevel;
  availableThinkingLevels: ThinkingLevel[];
  supportsThinking: boolean;
  isStreaming: boolean;
};

function agentBase(projectId: string) {
  return `/api/projects/${encodeURIComponent(projectId)}/agent`;
}

function formatErrorBody(body: unknown, fallback: string): string {
  if (!body) return fallback;
  if (typeof body === 'string') return body;
  if (typeof body === 'object' && body !== null) {
    const record = body as Record<string, unknown>;
    const message = record.error ?? record.message;
    if (typeof message === 'string') return message;
    try {
      return JSON.stringify(body);
    } catch {
      return fallback;
    }
  }
  return String(body);
}

async function request<T>(input: RequestInfo, init?: RequestInit): Promise<T> {
  const res = await fetch(input, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...init?.headers },
  });
  if (!res.ok) {
    if (res.status === 401) {
      window.location.href = '/login';
      throw new Error('Unauthorized');
    }
    const text = await res.text();
    let body: unknown = text;
    try {
      body = JSON.parse(text);
    } catch {
      // Keep raw text when the proxy returned a plain http.Error response.
    }
    throw new Error(formatErrorBody(body, `${res.status} ${res.statusText}`));
  }
  if (res.status === 204 || res.status === 202) return undefined as T;
  return res.json();
}

export function listPiSessions(projectId: string) {
  return request<{ sessions: PiSessionInfo[] }>(`${agentBase(projectId)}/sessions`);
}

export function createPiSession(projectId: string) {
  return request<{ id: string; createdAt: string }>(`${agentBase(projectId)}/sessions`, {
    method: 'POST',
  });
}

export function listPiModels(projectId: string) {
  return request<{ models: PiAgentModel[] }>(`${agentBase(projectId)}/sessions/models`);
}

export function getPiSessionMessages(projectId: string, sessionId: string) {
  return request<{ id: string; messages: unknown[] }>(
    `${agentBase(projectId)}/sessions/${encodeURIComponent(sessionId)}`,
  );
}

export function getPiSessionSettings(projectId: string, sessionId: string) {
  return request<PiSessionModelSettings>(
    `${agentBase(projectId)}/sessions/${encodeURIComponent(sessionId)}/settings`,
  );
}

export function updatePiSessionSettings(
  projectId: string,
  sessionId: string,
  body: { provider?: string; modelId?: string; thinkingLevel?: ThinkingLevel },
) {
  return request<PiSessionModelSettings>(
    `${agentBase(projectId)}/sessions/${encodeURIComponent(sessionId)}/settings`,
    {
      method: 'PATCH',
      body: JSON.stringify(body),
    },
  );
}

export function sendPiPrompt(projectId: string, sessionId: string, text: string) {
  return request<{ ok: true }>(
    `${agentBase(projectId)}/sessions/${encodeURIComponent(sessionId)}/prompt`,
    {
      method: 'POST',
      body: JSON.stringify({ text }),
    },
  );
}

export function abortPiSession(projectId: string, sessionId: string) {
  return request<{ ok: true }>(
    `${agentBase(projectId)}/sessions/${encodeURIComponent(sessionId)}/abort`,
    { method: 'POST' },
  );
}

export function piEventsUrl(projectId: string, sessionId: string) {
  return `${agentBase(projectId)}/sessions/${encodeURIComponent(sessionId)}/events`;
}
