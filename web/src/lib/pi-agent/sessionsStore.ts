import {
  abortPiSession,
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

export function attach(projectId: string, sessionId: string): SessionState {
  const entryKey = key(projectId, sessionId);
  const existing = entries.get(entryKey);
  if (existing) return existing.state;

  const es = new EventSource(piEventsUrl(projectId, sessionId));
  const initial: SessionState = { ...initialSessionState, sessionId };
  entries.set(entryKey, { state: initial, es });

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
    if (set?.size === 0) listeners.delete(entryKey);
  };
}

export function getSnapshot(projectId: string, sessionId: string): SessionState {
  return entries.get(key(projectId, sessionId))?.state ?? initialSessionState;
}

export async function sendPrompt(projectId: string, sessionId: string, text: string): Promise<void> {
  const entryKey = key(projectId, sessionId);
  dispatch(entryKey, { type: 'user_prompt_submitted', text });
  try {
    await sendPiPrompt(projectId, sessionId, text);
  } catch (err) {
    dispatch(entryKey, { type: 'set_error', error: err instanceof Error ? err.message : String(err) });
    dispatch(entryKey, { type: 'agent_event', event: { type: 'agent_end', messages: [] } });
    throw err;
  }
}

export async function abortSession(projectId: string, sessionId: string): Promise<void> {
  await abortPiSession(projectId, sessionId);
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
  entries.delete(entryKey);
  listeners.delete(entryKey);
}
