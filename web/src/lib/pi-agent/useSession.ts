import { useCallback, useSyncExternalStore } from 'react';
import {
  abortSession as storeAbort,
  attach,
  getSnapshot,
  respondExtensionRequest as storeRespondExtensionRequest,
  sendPrompt as storeSendPrompt,
  subscribe,
} from './sessionsStore';
import type { PiExtensionUiResponse } from '../../api/piAgent';
import { initialSessionState } from './types';

export function usePiSession(projectId: string, sessionId: string | null) {
  if (sessionId) attach(projectId, sessionId);

  const state = useSyncExternalStore(
    useCallback(
      (listener) => (sessionId ? subscribe(projectId, sessionId, listener) : () => {}),
      [projectId, sessionId],
    ),
    useCallback(
      () => (sessionId ? getSnapshot(projectId, sessionId) : initialSessionState),
      [projectId, sessionId],
    ),
  );

  const sendPrompt = useCallback(
    async (text: string) => {
      if (!sessionId) return;
      await storeSendPrompt(projectId, sessionId, text);
    },
    [projectId, sessionId],
  );

  const abort = useCallback(async () => {
    if (!sessionId) return;
    await storeAbort(projectId, sessionId);
  }, [projectId, sessionId]);

  const respondExtensionRequest = useCallback(
    async (requestId: string, response: PiExtensionUiResponse) => {
      if (!sessionId) return;
      await storeRespondExtensionRequest(projectId, sessionId, requestId, response);
    },
    [projectId, sessionId],
  );

  return { state, sendPrompt, abort, respondExtensionRequest };
}
