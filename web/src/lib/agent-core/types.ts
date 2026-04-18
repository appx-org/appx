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
