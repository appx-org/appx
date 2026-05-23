export type Role = 'user' | 'assistant' | 'system' | 'tool' | 'toolResult';

export type TextContent = { type: 'text'; text: string };
export type ThinkingContent = { type: 'thinking'; thinking?: string; text?: string; redacted?: boolean };
export type ToolCallContent = {
  type: 'tool_call' | 'toolCall' | 'tool_use';
  toolCallId?: string;
  id?: string;
  toolName?: string;
  name?: string;
  args?: unknown;
  arguments?: unknown;
  input?: unknown;
};
export type MessageContent = TextContent | ThinkingContent | ToolCallContent | Record<string, unknown>;

export type AgentMessage = {
  role: Role;
  content: MessageContent[];
  timestamp: string | number;
};

export type AssistantMessagePartial = { content?: MessageContent[] };

export type AssistantMessageEvent =
  | { type: 'text_start'; contentIndex: number; partial?: AssistantMessagePartial }
  | { type: 'text_delta'; contentIndex: number; delta: string; partial?: AssistantMessagePartial }
  | { type: 'text_end'; contentIndex: number; content: string; partial?: AssistantMessagePartial }
  | { type: 'thinking_start'; contentIndex: number; partial?: AssistantMessagePartial }
  | { type: 'thinking_delta'; contentIndex: number; delta: string; partial?: AssistantMessagePartial }
  | { type: 'thinking_end'; contentIndex: number; content?: string; partial?: AssistantMessagePartial }
  | { type: 'toolcall_start'; contentIndex: number; partial?: AssistantMessagePartial }
  | { type: 'toolcall_delta'; contentIndex: number; delta: string; partial?: AssistantMessagePartial }
  | { type: 'toolcall_end'; contentIndex: number; toolCall?: ToolCallContent; partial?: AssistantMessagePartial }
  | { type: 'tool_call_start'; toolCallId: string; toolName: string; contentIndex?: number }
  | { type: 'tool_call_args_delta'; toolCallId: string; delta: string; contentIndex?: number }
  | { type: 'tool_call_end'; toolCallId: string; contentIndex?: number };

export type AgentEvent =
  | { type: 'agent_start' }
  | { type: 'turn_start' }
  | { type: 'message_start'; message: AgentMessage }
  | { type: 'message_update'; message: AgentMessage; assistantMessageEvent: AssistantMessageEvent }
  | { type: 'message_end'; message: AgentMessage }
  | { type: 'tool_execution_start'; toolCallId: string; toolName: string; args: unknown }
  | { type: 'tool_execution_update'; toolCallId: string; toolName: string; args: unknown; partialResult: unknown }
  | { type: 'tool_execution_end'; toolCallId: string; toolName: string; result: unknown; isError: boolean }
  | { type: 'turn_end'; message: AgentMessage; toolResults: unknown[] }
  | { type: 'agent_end'; messages: AgentMessage[] };

export type UiMessagePart =
  | { type: 'text'; text: string; contentIndex?: number }
  | {
      type: 'tool';
      id: string;
      name: string;
      contentIndex?: number;
      args?: unknown;
      result?: unknown;
      isError?: boolean;
      status: 'pending' | 'running' | 'done' | 'error';
    };

export type UiMessage = {
  role: Role;
  parts: UiMessagePart[];
  streaming: boolean;
  timestamp: string | number;
};

export type SessionState = {
  sessionId: string | null;
  messages: UiMessage[];
  status: 'idle' | 'starting' | 'streaming';
  error: string | null;
  connected: boolean;
};

export const initialSessionState: SessionState = {
  sessionId: null,
  messages: [],
  status: 'idle',
  error: null,
  connected: false,
};
