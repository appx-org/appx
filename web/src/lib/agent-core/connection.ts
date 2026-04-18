import type { Event, OpencodeClient } from '@opencode-ai/sdk/v2/client';

export type ConnectionStatus = 'disconnected' | 'connecting' | 'connected';

export interface ConnectionOptions {
  client: OpencodeClient;
  onEvent: (event: Event) => void;
  onStatusChange: (status: ConnectionStatus) => void;
}

const HEARTBEAT_TIMEOUT_MS = 15_000;

/**
 * createConnection starts an SSE event stream and dispatches events.
 * Handles heartbeat detection and auto-reconnect with exponential backoff.
 * Returns a cleanup function to stop the connection.
 */
export function createConnection(opts: ConnectionOptions): () => void {
  let stopped = false;
  let heartbeatTimer: ReturnType<typeof setTimeout> | undefined;
  let currentAbort: AbortController | undefined;

  const clearHeartbeat = () => {
    if (heartbeatTimer !== undefined) {
      clearTimeout(heartbeatTimer);
      heartbeatTimer = undefined;
    }
  };

  const resetHeartbeat = () => {
    clearHeartbeat();
    heartbeatTimer = setTimeout(() => {
      currentAbort?.abort();
    }, HEARTBEAT_TIMEOUT_MS);
  };

  const run = async () => {
    let retryDelay = 3_000;

    while (!stopped) {
      opts.onStatusChange('connecting');
      currentAbort = new AbortController();

      try {
        const result = await opts.client.event.subscribe(
          {},
          { signal: currentAbort.signal },
        );

        opts.onStatusChange('connected');
        resetHeartbeat();
        retryDelay = 3_000;

        for await (const event of result.stream) {
          if (stopped) break;
          resetHeartbeat();
          opts.onEvent(event as Event);
        }
      } catch (e) {
        if (stopped) break;
        if (e instanceof DOMException && e.name === 'AbortError') {
          continue;
        }
        opts.onStatusChange('connecting');
        await new Promise((r) => setTimeout(r, retryDelay));
        retryDelay = Math.min(retryDelay * 1.5, 30_000);
      }
    }

    clearHeartbeat();
    opts.onStatusChange('disconnected');
  };

  run();

  return () => {
    stopped = true;
    clearHeartbeat();
    currentAbort?.abort();
  };
}
