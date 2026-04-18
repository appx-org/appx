import { useCallback } from 'react';
import type { OpencodeClient, QuestionAnswer } from '@opencode-ai/sdk/v2/client';

/**
 * usePermissions provides actions to respond to permission and question requests.
 */
export function usePermissions(client: OpencodeClient | null) {
  const respondPermission = useCallback(
    async (requestID: string, reply: 'once' | 'always' | 'reject') => {
      if (!client) return;
      await client.permission.reply({ requestID, reply });
    },
    [client],
  );

  const answerQuestion = useCallback(
    async (requestID: string, answers: QuestionAnswer[]) => {
      if (!client) return;
      await client.question.reply({ requestID, answers });
    },
    [client],
  );

  const rejectQuestion = useCallback(
    async (requestID: string) => {
      if (!client) return;
      await client.question.reject({ requestID });
    },
    [client],
  );

  return { respondPermission, answerQuestion, rejectQuestion };
}
