import { useReducer, useEffect, useCallback, useMemo, useRef } from 'react';
import type { Event, Part } from '@opencode-ai/sdk/v2/client';
import { applyAction, getSessionID, type ReducerAction } from '../agent-core/reducers';
import { initialSessionState, type SessionState } from '../agent-core/types';
import { useEventStream } from './useEventStream';
import type { ConnectionStatus } from '../agent-core/connection';
import { getClient } from '../../api/opencode';

function reducer(state: SessionState, action: ReducerAction): SessionState {
  return applyAction(state, action);
}

export interface UseSessionResult {
  state: SessionState;
  connectionStatus: ConnectionStatus;
  sendPrompt: (text: string) => Promise<void>;
  abort: () => Promise<void>;
}

/**
 * useSession provides full session state for a given session and project.
 * Connects to SSE, loads initial messages, and exposes prompt/abort actions.
 * Filters SSE events to only process those matching the active sessionId.
 */
export function useSession(
  sessionId: string | null,
  projectDir: string,
): UseSessionResult {
  const [state, dispatch] = useReducer(reducer, initialSessionState);
  const sessionIdRef = useRef(sessionId);

  const client = useMemo(
    () => (projectDir ? getClient(projectDir) : null),
    [projectDir],
  );

  // Keep ref in sync for the SSE event filter
  useEffect(() => {
    sessionIdRef.current = sessionId;
  });

  // Reset state when session changes
  useEffect(() => {
    dispatch({ type: '__reset' });
  }, [sessionId]);

  // Filter SSE events by sessionID before dispatching
  const handleEvent = useCallback((event: Event) => {
    const activeId = sessionIdRef.current;
    if (!activeId) return;

    const eventSessionId = getSessionID(event);
    // Allow events with no sessionID (server.heartbeat, etc.) or matching sessionID
    if (eventSessionId && eventSessionId !== activeId) return;

    dispatch(event);
  }, []);

  const connectionStatus = useEventStream(client, handleEvent);

  // Load (or re-sync) messages whenever the connection becomes 'connected'.
  // This covers both the initial load and recovery after an SSE reconnection:
  // if the connection dropped mid-stream, missed part.updated/part.delta events
  // would leave state stale. Re-fetching fills the gap. upsertById in the reducer
  // makes this idempotent — existing messages/parts are replaced in place.
  useEffect(() => {
    if (connectionStatus !== 'connected' || !client || !sessionId) return;
    let cancelled = false;

    (async () => {
      try {
        const res = await client.session.messages({ sessionID: sessionId });
        if (cancelled) return;
        const data = res.data as Array<{
          info: import('@opencode-ai/sdk/v2/client').Message;
          parts: Part[];
        }>;
        if (!data) return;
        for (const item of data) {
          dispatch({
            type: 'message.updated',
            properties: { sessionID: sessionId, info: item.info },
          } as Event);
          for (const part of item.parts) {
            dispatch({
              type: 'message.part.updated',
              properties: { sessionID: sessionId, part, time: Date.now() },
            } as Event);
          }
        }
      } catch (e) {
        console.error('Failed to sync messages:', e);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [connectionStatus, client, sessionId]);

  const sendPrompt = useCallback(
    async (text: string) => {
      if (!client || !sessionId) return;
      await client.session.promptAsync({
        sessionID: sessionId,
        parts: [{ type: 'text', text }],
      });
    },
    [client, sessionId],
  );

  const abort = useCallback(async () => {
    if (!client || !sessionId) return;
    await client.session.abort({ sessionID: sessionId });
  }, [client, sessionId]);

  return { state, connectionStatus, sendPrompt, abort };
}
