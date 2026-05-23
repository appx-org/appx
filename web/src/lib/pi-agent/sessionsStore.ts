import {
  abortPiSession,
  getPiSessionSettings,
  getPiSessionMessages,
  listPiExtensionUiRequests,
  piEventsUrl,
  respondPiExtensionUiRequest,
  sendPiPrompt,
  type PiExtensionUiResponse,
} from '../../api/piAgent';
import { sessionReducer, type SessionAction } from './reducer';
import {
  type AgentEvent,
  type ExtensionUiRequest,
  type AgentMessage,
  type SessionState,
  initialSessionState,
} from './types';

type Entry = {
  state: SessionState;
  es: EventSource;
  poll?: number;
  pollBusy?: boolean;
  lastPromptAt?: number;
};

const entries = new Map<string, Entry>();
const listeners = new Map<string, Set<() => void>>();

function key(projectId: string, sessionId: string) {
  return `${projectId}:${sessionId}`;
}

function emit(entryKey: string): void {
  const set = listeners.get(entryKey);
  if (!set) return;
  for (const listener of set) listener();
}

function dispatch(entryKey: string, action: SessionAction): void {
  const entry = entries.get(entryKey);
  if (!entry) return;
  const next = sessionReducer(entry.state, action);
  if (next === entry.state) return;
  entry.state = next;
  emit(entryKey);
}

async function refreshExtensionRequests(projectId: string, sessionId: string, entryKey: string): Promise<void> {
  const entry = entries.get(entryKey);
  if (!entry || entry.pollBusy) return;
  if (entry.state.status === 'idle' && entry.state.extensionRequests.length === 0) return;

  entry.pollBusy = true;
  try {
    const pending = await listPiExtensionUiRequests(projectId, sessionId);
    const requests = pending.requests as ExtensionUiRequest[];
    if (requests.length > 0 || entry.state.extensionRequests.length > 0) {
      dispatch(entryKey, { type: 'load_extension_requests', requests });
    }

    const current = entries.get(entryKey);
    if (!current || current.state.status === 'idle') return;
    if (current.lastPromptAt && Date.now() - current.lastPromptAt < 3_000) return;

    const settings = await getPiSessionSettings(projectId, sessionId);
    if (!settings.isStreaming) {
      const history = await getPiSessionMessages(projectId, sessionId);
      dispatch(entryKey, { type: 'load_history', messages: history.messages as AgentMessage[] });
    }
  } catch {
    // SSE remains the primary channel; polling is only a recovery path.
  } finally {
    const latest = entries.get(entryKey);
    if (latest) latest.pollBusy = false;
  }
}

export function attach(projectId: string, sessionId: string): SessionState {
  const entryKey = key(projectId, sessionId);
  const existing = entries.get(entryKey);
  if (existing) return existing.state;

  const es = new EventSource(piEventsUrl(projectId, sessionId));
  const initial: SessionState = { ...initialSessionState, sessionId };
  const entry: Entry = { state: initial, es };
  entries.set(entryKey, entry);
  entry.poll = window.setInterval(() => {
    void refreshExtensionRequests(projectId, sessionId, entryKey);
  }, 1_500);
  emit(entryKey);

  es.onopen = () => dispatch(entryKey, { type: 'set_connected', connected: true });
  es.onerror = () => dispatch(entryKey, { type: 'set_connected', connected: false });
  es.onmessage = (e) => {
    if (!e.data || !String(e.data).trim().startsWith('{')) return;
    try {
      const parsed = JSON.parse(e.data) as AgentEvent;
      dispatch(entryKey, { type: 'agent_event', event: parsed });
    } catch (err) {
      console.error('[pi sessionsStore] bad event:', err, e.data);
    }
  };

  void getPiSessionMessages(projectId, sessionId)
    .then((r) => {
      const cur = entries.get(entryKey);
      if (!cur) return;
      if (cur.state.status !== 'idle' || cur.state.messages.length > 0) return;
      dispatch(entryKey, { type: 'load_history', messages: r.messages as AgentMessage[] });
    })
    .catch((err) => dispatch(entryKey, { type: 'set_error', error: err instanceof Error ? err.message : String(err) }));

  void listPiExtensionUiRequests(projectId, sessionId)
    .then((r) => {
      dispatch(entryKey, {
        type: 'load_extension_requests',
        requests: r.requests as ExtensionUiRequest[],
      });
    })
    .catch((err) => dispatch(entryKey, { type: 'set_error', error: err instanceof Error ? err.message : String(err) }));

  return initial;
}

export function subscribe(projectId: string, sessionId: string, listener: () => void): () => void {
  const entryKey = key(projectId, sessionId);
  let set = listeners.get(entryKey);
  if (!set) {
    set = new Set();
    listeners.set(entryKey, set);
  }
  set.add(listener);
  return () => {
    set?.delete(listener);
    if (set?.size === 0) {
      listeners.delete(entryKey);
      detach(projectId, sessionId);
    }
  };
}

export function getSnapshot(projectId: string, sessionId: string): SessionState {
  return entries.get(key(projectId, sessionId))?.state ?? initialSessionState;
}

export async function sendPrompt(projectId: string, sessionId: string, text: string): Promise<void> {
  const entryKey = key(projectId, sessionId);
  attach(projectId, sessionId);
  const entry = entries.get(entryKey);
  if (entry) entry.lastPromptAt = Date.now();
  dispatch(entryKey, { type: 'user_prompt_submitted', text });
  try {
    await sendPiPrompt(projectId, sessionId, text);
    void refreshExtensionRequests(projectId, sessionId, entryKey);
  } catch (err) {
    dispatch(entryKey, { type: 'set_error', error: err instanceof Error ? err.message : String(err) });
    dispatch(entryKey, { type: 'agent_event', event: { type: 'agent_end', messages: [] } });
    throw err;
  }
}

export async function abortSession(projectId: string, sessionId: string): Promise<void> {
  const entryKey = key(projectId, sessionId);
  try {
    await abortPiSession(projectId, sessionId);
  } catch (err) {
    dispatch(entryKey, { type: 'set_error', error: err instanceof Error ? err.message : String(err) });
  }

  try {
    const history = await getPiSessionMessages(projectId, sessionId);
    dispatch(entryKey, { type: 'load_history', messages: history.messages as AgentMessage[] });
  } catch {
    dispatch(entryKey, { type: 'agent_event', event: { type: 'agent_end', messages: [] } });
  }
  void refreshExtensionRequests(projectId, sessionId, entryKey);
}

export async function respondExtensionRequest(
  projectId: string,
  sessionId: string,
  requestId: string,
  response: PiExtensionUiResponse,
): Promise<void> {
  await respondPiExtensionUiRequest(projectId, sessionId, requestId, response);
  dispatch(key(projectId, sessionId), { type: 'extension_ui_response', requestId });
}

export function detach(projectId: string, sessionId: string): void {
  const entryKey = key(projectId, sessionId);
  const entry = entries.get(entryKey);
  if (!entry) return;
  entry.es.close();
  if (entry.poll) window.clearInterval(entry.poll);
  entries.delete(entryKey);
  listeners.delete(entryKey);
}
