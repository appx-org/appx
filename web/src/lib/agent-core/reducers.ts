import type { Event, Message, Part } from '@opencode-ai/sdk/v2/client';
import type { SessionState } from './types';
import { initialSessionState } from './types';

/** Action type for the reducer — either an SSE Event or an internal reset. */
export type ReducerAction = Event | { type: '__reset' };

/** getSessionID extracts the sessionID from an event's properties, if present. */
export function getSessionID(event: Event): string | undefined {
  const props = event.properties as Record<string, unknown>;
  if ('sessionID' in props && typeof props.sessionID === 'string') {
    return props.sessionID;
  }
  // message.part.delta and some events have it nested in the part
  if ('part' in props && typeof props.part === 'object' && props.part !== null) {
    const part = props.part as Record<string, unknown>;
    if ('sessionID' in part && typeof part.sessionID === 'string') {
      return part.sessionID;
    }
  }
  return undefined;
}

/** findIndex finds the index of an item by id in an array. Returns -1 if not found. */
function findIndex(arr: { id: string }[], id: string): number {
  for (let i = 0; i < arr.length; i++) {
    if (arr[i].id === id) return i;
  }
  return -1;
}

/** upsertById replaces an existing item or appends a new one. */
function upsertById<T extends { id: string }>(arr: T[], item: T): T[] {
  const idx = findIndex(arr, item.id);
  if (idx >= 0) {
    const next = [...arr];
    next[idx] = item;
    return next;
  }
  return [...arr, item];
}

/** removeById returns a new array without the item matching id. */
function removeById<T extends { id: string }>(arr: T[], id: string): T[] {
  return arr.filter((x) => x.id !== id);
}

/** applyAction handles both SSE events and internal actions (like reset). */
export function applyAction(state: SessionState, action: ReducerAction): SessionState {
  if (action.type === '__reset') return initialSessionState;
  return applyEvent(state, action);
}

/** applyEvent is a pure reducer that applies an SSE event to session state. */
export function applyEvent(state: SessionState, event: Event): SessionState {
  switch (event.type) {
    case 'message.updated': {
      const msg = event.properties.info as Message;
      return {
        ...state,
        messages: upsertById(state.messages, msg),
      };
    }

    case 'message.removed': {
      const { messageID } = event.properties;
      // eslint-disable-next-line @typescript-eslint/no-unused-vars
      const { [messageID]: _, ...restParts } = state.parts;
      return {
        ...state,
        messages: state.messages.filter((m) => m.id !== messageID),
        parts: restParts,
      };
    }

    case 'message.part.updated': {
      const part = event.properties.part as Part;
      const msgId = part.messageID;
      const existing = state.parts[msgId] ?? [];
      return {
        ...state,
        parts: {
          ...state.parts,
          [msgId]: upsertById(existing, part),
        },
      };
    }

    case 'message.part.removed': {
      const { messageID, partID } = event.properties;
      const existing = state.parts[messageID];
      if (!existing) return state;
      return {
        ...state,
        parts: {
          ...state.parts,
          [messageID]: removeById(existing, partID),
        },
      };
    }

    case 'message.part.delta': {
      const { messageID, partID, field, delta } = event.properties;
      let existing = state.parts[messageID] ?? [];
      let idx = findIndex(existing, partID);
      if (idx < 0) {
        // part.updated was missed (e.g. during an SSE reconnection gap). Create
        // a stub text part so the delta isn't lost. The next message re-sync
        // (triggered by reconnection) will replace this with the full part.
        existing = [...existing, { id: partID, type: 'text', messageID, text: '' } as Part];
        idx = existing.length - 1;
      }
      const part = { ...existing[idx] } as Record<string, unknown>;
      part[field] = ((part[field] as string) ?? '') + delta;
      const next = [...existing];
      next[idx] = part as Part;
      return {
        ...state,
        parts: { ...state.parts, [messageID]: next },
      };
    }

    case 'session.status': {
      const { status } = event.properties;
      return {
        ...state,
        status: status.type === 'busy' ? 'running' : 'idle',
      };
    }

    case 'session.idle': {
      return { ...state, status: 'idle' };
    }

    case 'session.error': {
      const err = event.properties.error;
      return {
        ...state,
        status: 'error',
        error: err ? JSON.stringify(err) : 'Unknown error',
      };
    }

    case 'permission.asked': {
      const perm = event.properties;
      return {
        ...state,
        pendingPermissions: [...state.pendingPermissions, perm],
      };
    }

    case 'permission.replied': {
      const { requestID } = event.properties;
      return {
        ...state,
        pendingPermissions: state.pendingPermissions.filter(
          (p) => p.id !== requestID,
        ),
      };
    }

    case 'question.asked': {
      const q = event.properties;
      return {
        ...state,
        pendingQuestions: [...state.pendingQuestions, q],
      };
    }

    case 'question.replied':
    case 'question.rejected': {
      const { requestID } = event.properties;
      return {
        ...state,
        pendingQuestions: state.pendingQuestions.filter(
          (q) => q.id !== requestID,
        ),
      };
    }

    case 'todo.updated': {
      return {
        ...state,
        todos: event.properties.todos,
      };
    }

    default:
      return state;
  }
}
