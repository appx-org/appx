import { createOpencodeClient, type OpencodeClient } from '@opencode-ai/sdk/v2/client';

export type { OpencodeClient };
export type {
  Session,
  Message,
  UserMessage,
  AssistantMessage,
  Part,
  TextPart,
  ToolPart,
  ReasoningPart,
  ToolState,
  Event,
  EventMessagePartDelta,
  EventMessageUpdated,
  EventMessagePartUpdated,
  EventPermissionAsked,
  EventPermissionReplied,
  EventQuestionAsked,
  EventQuestionReplied,
  EventSessionStatus,
  EventSessionIdle,
  EventSessionCreated,
  EventSessionUpdated,
  EventSessionDeleted,
  EventTodoUpdated,
  PermissionRequest,
  QuestionRequest,
  QuestionInfo,
  QuestionOption,
  Todo,
  SessionStatus,
  FileDiff,
} from '@opencode-ai/sdk/v2/client';

const clients = new Map<string, OpencodeClient>();

/** getClient returns a cached SDK client scoped to a project directory. */
export function getClient(directory: string): OpencodeClient {
  let client = clients.get(directory);
  if (!client) {
    client = createOpencodeClient({
      baseUrl: `${window.location.origin}/api/opencode`,
      directory,
    });
    clients.set(directory, client);
  }
  return client;
}
