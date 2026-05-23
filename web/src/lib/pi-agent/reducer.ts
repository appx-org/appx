import type {
  AgentEvent,
  AgentMessage,
  AssistantMessagePartial,
  MessageContent,
  SessionState,
  UiMessage,
  UiMessagePart,
} from './types';

type TextPart = Extract<UiMessagePart, { type: 'text' }>;
type ToolPart = Extract<UiMessagePart, { type: 'tool' }>;
type ToolPatch = Partial<Omit<ToolPart, 'id' | 'type'>>;
type ToolInfo = { id: string; patch: ToolPatch };

function toolInfoFromContent(content: Record<string, unknown> | undefined, contentIndex?: number): ToolInfo | null {
  if (!content) return null;
  if (content.type !== 'toolCall' && content.type !== 'tool_call' && content.type !== 'tool_use') return null;
  const id = String(content.id ?? content.toolCallId ?? content.tool_use_id ?? `content-${contentIndex ?? 0}`);
  return {
    id,
    patch: {
      contentIndex,
      name: String(content.name ?? content.toolName ?? content.tool_name ?? 'tool'),
      args: content.arguments ?? content.args ?? content.input,
    },
  };
}

function contentFromPartial(partial: AssistantMessagePartial | undefined, contentIndex: number): Record<string, unknown> | undefined {
  const content = partial?.content?.[contentIndex];
  return content && typeof content === 'object' ? (content as Record<string, unknown>) : undefined;
}

function toolInfoFromPartial(partial: AssistantMessagePartial | undefined, contentIndex: number): ToolInfo {
  return (
    toolInfoFromContent(contentFromPartial(partial, contentIndex), contentIndex) ?? {
      id: `content-${contentIndex}`,
      patch: { contentIndex, name: 'tool' },
    }
  );
}

function partsFromContent(content: MessageContent[] | undefined): UiMessagePart[] {
  if (!content) return [];
  const parts: UiMessagePart[] = [];
  for (const [contentIndex, c] of (content as Array<Record<string, unknown>>).entries()) {
    if (c.type === 'text') {
      parts.push({ type: 'text', text: String(c.text ?? ''), contentIndex });
      continue;
    }
    const toolInfo = toolInfoFromContent(c, contentIndex);
    if (toolInfo) {
      parts.push({
        type: 'tool',
        id: toolInfo.id,
        name: toolInfo.patch.name ?? 'tool',
        contentIndex,
        args: toolInfo.patch.args,
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
  patch: ToolPatch,
): UiMessage[] {
  return messages.map((m) => {
    const idx = m.parts.findIndex((p) => p.type === 'tool' && p.id === toolCallId);
    if (idx === -1) return m;
    const next = [...m.parts];
    next[idx] = { ...(next[idx] as ToolPart), ...patch };
    return { ...m, parts: next };
  });
}

function latestAssistantIndex(messages: UiMessage[], requireStreaming: boolean): number {
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i];
    if (message.role === 'assistant' && (!requireStreaming || message.streaming)) return i;
  }
  return -1;
}

function lastTextPartIndex(parts: UiMessagePart[]): number {
  for (let i = parts.length - 1; i >= 0; i--) {
    if (parts[i].type === 'text') return i;
  }
  return -1;
}

function insertPartByContentIndex(parts: UiMessagePart[], part: UiMessagePart): UiMessagePart[] {
  if (typeof part.contentIndex !== 'number') return [...parts, part];
  const insertAt = parts.findIndex(
    (candidate) => typeof candidate.contentIndex === 'number' && candidate.contentIndex > part.contentIndex!,
  );
  if (insertAt === -1) return [...parts, part];
  return [...parts.slice(0, insertAt), part, ...parts.slice(insertAt)];
}

function findToolPartIndex(parts: UiMessagePart[], toolCallId: string, contentIndex?: number): number {
  return parts.findIndex(
    (p) =>
      p.type === 'tool' &&
      ((toolCallId && p.id === toolCallId) ||
        (typeof contentIndex === 'number' && p.contentIndex === contentIndex)),
  );
}

function applyTextDelta(messages: UiMessage[], contentIndex: number | undefined, delta: string): UiMessage[] {
  let messageIndex = latestAssistantIndex(messages, true);
  if (messageIndex === -1) messageIndex = latestAssistantIndex(messages, false);
  if (messageIndex === -1) return messages;

  const nextMessages = [...messages];
  const message = nextMessages[messageIndex];
  let parts = [...message.parts];
  const targetIndex =
    typeof contentIndex === 'number'
      ? parts.findIndex((p) => p.type === 'text' && p.contentIndex === contentIndex)
      : lastTextPartIndex(parts);

  if (targetIndex === -1) {
    parts = insertPartByContentIndex(parts, { type: 'text', text: delta, contentIndex });
  } else {
    const textPart = parts[targetIndex] as TextPart;
    parts[targetIndex] = { ...textPart, text: textPart.text + delta };
  }

  nextMessages[messageIndex] = { ...message, parts };
  return nextMessages;
}

function setTextContent(messages: UiMessage[], contentIndex: number, text: string): UiMessage[] {
  let messageIndex = latestAssistantIndex(messages, true);
  if (messageIndex === -1) messageIndex = latestAssistantIndex(messages, false);
  if (messageIndex === -1) return messages;

  const nextMessages = [...messages];
  const message = nextMessages[messageIndex];
  let parts = [...message.parts];
  const targetIndex = parts.findIndex((p) => p.type === 'text' && p.contentIndex === contentIndex);
  if (targetIndex === -1) {
    parts = insertPartByContentIndex(parts, { type: 'text', text, contentIndex });
  } else {
    const textPart = parts[targetIndex] as TextPart;
    parts[targetIndex] = { ...textPart, text };
  }
  nextMessages[messageIndex] = { ...message, parts };
  return nextMessages;
}

function createToolPart(toolCallId: string, patch: ToolPatch): ToolPart {
  const tool: ToolPart = {
    type: 'tool',
    id: toolCallId,
    name: patch.name ?? 'tool',
    status: patch.status ?? 'pending',
  };
  if ('contentIndex' in patch) tool.contentIndex = patch.contentIndex;
  if ('args' in patch) tool.args = patch.args;
  if ('result' in patch) tool.result = patch.result;
  if ('isError' in patch) tool.isError = patch.isError;
  return tool;
}

function upsertToolPart(messages: UiMessage[], toolCallId: string, patch: ToolPatch): UiMessage[] {
  let found = false;
  const patched = messages.map((m) => {
    const idx = findToolPartIndex(m.parts, toolCallId, patch.contentIndex);
    if (idx === -1) return m;
    found = true;
    const next = [...m.parts];
    next[idx] = { ...(next[idx] as ToolPart), ...patch };
    return { ...m, parts: next };
  });
  if (found) return patched;

  let messageIndex = latestAssistantIndex(patched, true);
  if (messageIndex === -1) messageIndex = latestAssistantIndex(patched, false);
  if (messageIndex === -1) return patched;

  const nextMessages = [...patched];
  const message = nextMessages[messageIndex];
  nextMessages[messageIndex] = {
    ...message,
    parts: insertPartByContentIndex(message.parts, createToolPart(toolCallId, patch)),
  };
  return nextMessages;
}

function appendToolArgsDelta(
  messages: UiMessage[],
  toolCallId: string,
  delta: string,
  contentIndex?: number,
): UiMessage[] {
  let found = false;
  const patched = messages.map((m) => {
    const idx = findToolPartIndex(m.parts, toolCallId, contentIndex);
    if (idx === -1) return m;
    found = true;
    const next = [...m.parts];
    const current = next[idx] as ToolPart;
    next[idx] = {
      ...current,
      args: `${typeof current.args === 'string' ? current.args : ''}${delta}`,
    };
    return { ...m, parts: next };
  });

  if (found) return patched;
  return upsertToolPart(messages, toolCallId, { args: delta, contentIndex, status: 'pending' });
}

function resultToText(result: unknown): string {
  if (result === undefined || result === null) return '';
  if (typeof result === 'string') return result;
  if (typeof result === 'object' && Array.isArray((result as { content?: unknown }).content)) {
    return ((result as { content: Array<{ type?: string; text?: string }> }).content)
      .filter((c) => c.type === 'text')
      .map((c) => c.text ?? '')
      .join('');
  }
  try {
    return JSON.stringify(result, null, 2);
  } catch {
    return String(result);
  }
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

      const initialParts = partsFromContent(event.message.content);
      const newMsg: UiMessage = {
        role: event.message.role,
        parts: initialParts,
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
      if (ev.type === 'text_start') {
        return {
          ...state,
          messages: setTextContent(state.messages, ev.contentIndex, ''),
          status: 'streaming',
        };
      }
      if (ev.type === 'text_delta') {
        return {
          ...state,
          messages: applyTextDelta(state.messages, ev.contentIndex, ev.delta ?? ''),
          status: 'streaming',
        };
      }
      if (ev.type === 'text_end') {
        return {
          ...state,
          messages: setTextContent(state.messages, ev.contentIndex, ev.content ?? ''),
          status: 'streaming',
        };
      }
      if (ev.type === 'thinking_start' || ev.type === 'thinking_delta' || ev.type === 'thinking_end') {
        return { ...state, status: 'streaming' };
      }
      if (ev.type === 'toolcall_start') {
        const toolInfo = toolInfoFromPartial(ev.partial, ev.contentIndex);
        return {
          ...state,
          messages: upsertToolPart(state.messages, toolInfo.id, {
            ...toolInfo.patch,
            status: 'pending',
          }),
          status: 'streaming',
        };
      }
      if (ev.type === 'toolcall_delta') {
        const toolInfo = toolInfoFromPartial(ev.partial, ev.contentIndex);
        if (!('args' in toolInfo.patch) || toolInfo.patch.args === undefined) {
          return {
            ...state,
            messages: appendToolArgsDelta(state.messages, toolInfo.id, ev.delta ?? '', ev.contentIndex),
            status: 'streaming',
          };
        }
        return {
          ...state,
          messages: upsertToolPart(state.messages, toolInfo.id, {
            ...toolInfo.patch,
            status: 'pending',
          }),
          status: 'streaming',
        };
      }
      if (ev.type === 'toolcall_end') {
        const toolInfo = toolInfoFromContent(
          ev.toolCall as unknown as Record<string, unknown> | undefined,
          ev.contentIndex,
        ) ?? toolInfoFromPartial(ev.partial, ev.contentIndex);
        return {
          ...state,
          messages: upsertToolPart(state.messages, toolInfo.id, {
            ...toolInfo.patch,
            status: 'pending',
          }),
          status: 'streaming',
        };
      }
      if (ev.type === 'tool_call_start') {
        return {
          ...state,
          messages: upsertToolPart(state.messages, ev.toolCallId, {
            contentIndex: ev.contentIndex,
            name: ev.toolName,
            status: 'pending',
          }),
          status: 'streaming',
        };
      }
      if (ev.type === 'tool_call_args_delta') {
        return {
          ...state,
          messages: appendToolArgsDelta(state.messages, ev.toolCallId, ev.delta ?? '', ev.contentIndex),
          status: 'streaming',
        };
      }
      if (ev.type === 'tool_call_end') {
        return {
          ...state,
          messages: upsertToolPart(state.messages, ev.toolCallId, {
            contentIndex: ev.contentIndex,
            status: 'pending',
          }),
          status: 'streaming',
        };
      }
      return state;
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
          const prev = m.parts.find(
            (q) =>
              q.type === 'tool' &&
              ((p.id && q.id === p.id) ||
                (typeof p.contentIndex === 'number' && q.contentIndex === p.contentIndex)),
          ) as ToolPart | undefined;
          return prev ? { ...p, status: prev.status, result: prev.result, isError: prev.isError } : p;
        });
        return { ...m, parts: merged.length > 0 ? merged : m.parts, streaming: false };
      });
      return { ...state, messages };
    }
    case 'tool_execution_start':
      return {
        ...state,
        messages: upsertToolPart(state.messages, event.toolCallId, {
          name: event.toolName,
          args: event.args,
          status: 'running',
        }),
      };
    case 'tool_execution_update':
      return {
        ...state,
        messages: upsertToolPart(state.messages, event.toolCallId, {
          name: event.toolName,
          args: event.args,
          status: 'running',
          result: resultToText(event.partialResult),
        }),
      };
    case 'tool_execution_end': {
      return {
        ...state,
        messages: upsertToolPart(state.messages, event.toolCallId, {
          name: event.toolName,
          status: event.isError ? 'error' : 'done',
          result: resultToText(event.result),
          isError: event.isError,
        }),
      };
    }
    case 'agent_end':
      return {
        ...state,
        status: 'idle',
        messages: state.messages.map((m) => (m.streaming ? { ...m, streaming: false } : m)),
      };
    default:
      return state;
  }
}
