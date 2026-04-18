import { useEffect, useRef, useState } from 'react';
import type { Event, OpencodeClient } from '@opencode-ai/sdk/v2/client';
import {
  createConnection,
  type ConnectionStatus,
} from '../agent-core/connection';

/**
 * useEventStream manages an SSE connection to OpenCode, dispatching events
 * via the provided callback. Reconnects automatically on heartbeat timeout.
 */
export function useEventStream(
  client: OpencodeClient | null,
  onEvent: (event: Event) => void,
): ConnectionStatus {
  const [status, setStatus] = useState<ConnectionStatus>('disconnected');
  const onEventRef = useRef(onEvent);

  useEffect(() => {
    onEventRef.current = onEvent;
  });

  useEffect(() => {
    if (!client) return;

    const stop = createConnection({
      client,
      onEvent: (e) => onEventRef.current(e),
      onStatusChange: setStatus,
    });

    return stop;
  }, [client]);

  return status;
}
