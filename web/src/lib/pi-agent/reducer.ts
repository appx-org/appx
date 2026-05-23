import type {
  AgentEvent,
  AgentMessage,
  MessageContent,
  SessionState,
  UiMessage,
  UiMessagePart,
} from './types';

function partsFromContent(content: MessageContent[] | undefined): UiMessagePart[] {
  if (!content) return [];
  const parts: UiMessagePart[] = [];
  for (const c of content as Array<Record<string, unknown>>) {
    if (c.type === 'text') {
      parts.push({ type: 'text', text: String(c.text ?? '') });
    } else if (c.type === 'toolCall' || c.type === 'tool_call' || c.type === 'tool_use') {
      parts.push({
        type: 'tool',
        id: String(c.id ?? c.toolCallId ?? ''),
        name: String(c.name ?? c.toolName ?? 'tool'),
        args: c.arguments ?? c.args ?? c.input,
        status: 'pending',
      });
    }
  }
  return parts;
}

const isToolResultMessage = (m: AgentMessage): boolean => {
  const role = m.role as string;
  if (role === 'toolResult' || role === 'tool') return true;
  if (Array.isArray(m.content)) {
    return m.content.some((c) => {
      const type = (c as { type?: string }).type;
      return type === 'toolResult' || type === 'tool_result';
    });
  }
  return false;
};

function extractToolResultText(message: AgentMessage): { text: string; toolUseId: string | null } {
  const topLevelId = (message as unknown as { toolCallId?: string }).toolCallId ?? null;
  let text = '';
  if (Array.isArray(message.content)) {
    for (const c of message.content as Array<Record<string, unknown>>) {
      if (c.type === 'text') {
        text += String(c.text ?? '');
      } else if (c.type === 'toolResult' || c.type === 'tool_result') {
        const inner = c.content;
        if (typeof inner === 'string') text += inner;
        else if (Array.isArray(inner)) {
          text += inner
            .map((b: Record<string, unknown>) => (b.type === 'text' ? String(b.text ?? '') : ''))
            .join('');
        }
        if (!topLevelId && typeof c.tool_use_id === 'string') {
          return { text, toolUseId: c.tool_use_id };
        }
      }
    }
  }
  return { text, toolUseId: topLevelId };
}

function applyToolResult(
  messages: UiMessage[],
  toolCallId: string,
  patch: Partial<Extract<UiMessagePart, { type: 'tool' }>>,
): UiMessage[] {
  return messages.map((m) => {
    const idx = m.parts.findIndex((p) => p.type === 'tool' && p.id === toolCallId);
    if (idx === -1) return m;
    const next = [...m.parts];
    next[idx] = { ...(next[idx] as Extract<UiMessagePart, { type: 'tool' }>), ...patch };
    return { ...m, parts: next };
  });
}

export type SessionAction =
  | { type: 'agent_event'; event: AgentEvent }
  | { type: 'set_session_id'; sessionId: string }
  | { type: 'set_connected'; connected: boolean }
  | { type: 'set_error'; error: string | null }
  | { type: 'user_prompt_submitted'; text: string }
  | { type: 'load_history'; messages: AgentMessage[] }
  | { type: 'reset' };

export function sessionReducer(state: SessionState, action: SessionAction): SessionState {
  switch (action.type) {
    case 'set_session_id':
      return { ...state, sessionId: action.sessionId };
    case 'set_connected':
      return { ...state, connected: action.connected };
    case 'set_error':
      return { ...state, error: action.error };
    case 'user_prompt_submitted':
      return {
        ...state,
        status: 'starting',
        error: null,
        messages: [
          ...state.messages,
          {
            role: 'user',
            parts: [{ type: 'text', text: action.text }],
            streaming: false,
            timestamp: new Date().toISOString(),
          },
        ],
      };
    case 'reset':
      return { ...state, messages: [], status: 'idle', error: null };
    case 'load_history':
      return loadHistory(state, action.messages);
    case 'agent_event':
      return reduceEvent(state, action.event);
  }
}

function loadHistory(state: SessionState, history: AgentMessage[]): SessionState {
  const messages: UiMessage[] = [];
  for (const m of history) {
    if (isToolResultMessage(m)) continue;
    if (m.role !== 'user' && m.role !== 'assistant') continue;
    const parts = partsFromContent(m.content);
    if (parts.length === 0) continue;
    messages.push({ role: m.role, parts, streaming: false, timestamp: m.timestamp });
  }

  let result = messages;
  for (const m of history) {
    if (!isToolResultMessage(m)) continue;
    const { text, toolUseId } = extractToolResultText(m);
    if (!toolUseId) continue;
    result = applyToolResult(result, toolUseId, {
      status: (m as unknown as { isError?: boolean }).isError ? 'error' : 'done',
      result: text,
      isError: (m as unknown as { isError?: boolean }).isError,
    });
  }

  return { ...state, messages: result, status: 'idle', error: null };
}

function reduceEvent(state: SessionState, event: AgentEvent): SessionState {
  switch (event.type) {
    case 'agent_start':
      return { ...state, status: 'starting', error: null };
    case 'turn_start':
      return state;
    case 'message_start': {
      if (isToolResultMessage(event.message)) return state;
      if (event.message.role !== 'user' && event.message.role !== 'assistant') return state;

      const last = state.messages[state.messages.length - 1];
      const txt = ((event.message.content ?? []) as Array<{ type?: string; text?: string }>)
        .filter((c) => c.type === 'text')
        .map((c) => c.text ?? '')
        .join('');
      if (
        event.message.role === 'user' &&
        last?.role === 'user' &&
        last.parts[0]?.type === 'text' &&
        last.parts[0].text === txt
      ) {
        return state;
      }

      const newMsg: UiMessage = {
        role: event.message.role,
        parts: event.message.role === 'assistant' ? [{ type: 'text', text: '' }] : partsFromContent(event.message.content),
        streaming: event.message.role === 'assistant',
        timestamp: event.message.timestamp,
      };
      return {
        ...state,
        status: event.message.role === 'assistant' ? 'streaming' : state.status,
        messages: [...state.messages, newMsg],
      };
    }
    case 'message_update': {
      const ev = event.assistantMessageEvent;
      if (ev.type !== 'text_delta') return state;
      const messages = [...state.messages];
      for (let i = messages.length - 1; i >= 0; i--) {
        const m = messages[i];
        if (m.role !== 'assistant' || !m.streaming) continue;
        const parts = [...m.parts];
        const lastIdx = parts.length - 1;
        if (lastIdx >= 0 && parts[lastIdx].type === 'text') {
          const t = parts[lastIdx] as Extract<UiMessagePart, { type: 'text' }>;
          parts[lastIdx] = { type: 'text', text: t.text + (ev.delta ?? '') };
        } else {
          parts.push({ type: 'text', text: ev.delta ?? '' });
        }
        messages[i] = { ...m, parts };
        break;
      }
      return { ...state, messages, status: 'streaming' };
    }
    case 'message_end': {
      if (isToolResultMessage(event.message)) {
        const { text, toolUseId } = extractToolResultText(event.message);
        const isError = (event.message as unknown as { isError?: boolean }).isError;
        if (!toolUseId) return state;
        return {
          ...state,
          messages: applyToolResult(state.messages, toolUseId, {
            status: isError ? 'error' : 'done',
            result: text,
            isError,
          }),
        };
      }
      if (event.message.role !== 'user' && event.message.role !== 'assistant') return state;
      const finalisedParts = partsFromContent(event.message.content);
      let replaced = false;
      const messages = state.messages.map((m) => {
        if (replaced) return m;
        if (m.role !== event.message.role || !m.streaming) return m;
        replaced = true;
        const merged = finalisedParts.map((p) => {
          if (p.type !== 'tool') return p;
          const prev = m.parts.find((q) => q.type === 'tool' && q.id === p.id) as
            | Extract<UiMessagePart, { type: 'tool' }>
            | undefined;
          return prev ? { ...p, status: prev.status, result: prev.result, isError: prev.isError } : p;
        });
        return { ...m, parts: merged.length > 0 ? merged : m.parts, streaming: false };
      });
      return { ...state, messages };
    }
    case 'tool_execution_start':
      return {
        ...state,
        messages: applyToolResult(state.messages, event.toolCallId, {
          name: event.toolName,
          args: event.args,
          status: 'running',
        }),
      };
    case 'tool_execution_end': {
      let resultText = '';
      const r = event.result as { content?: Array<{ type?: string; text?: string }> } | string | undefined;
      if (typeof r === 'string') resultText = r;
      else if (r && Array.isArray(r.content)) {
        resultText = r.content
          .filter((c) => c.type === 'text')
          .map((c) => c.text ?? '')
          .join('');
      } else if (r) {
        try {
          resultText = JSON.stringify(r, null, 2);
        } catch {
          resultText = String(r);
        }
      }
      return {
        ...state,
        messages: applyToolResult(state.messages, event.toolCallId, {
          status: event.isError ? 'error' : 'done',
          result: resultText,
          isError: event.isError,
        }),
      };
    }
    case 'agent_end':
      return { ...state, status: 'idle' };
    default:
      return state;
  }
}
