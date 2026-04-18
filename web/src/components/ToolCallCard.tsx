import { useState } from 'react';
import type { ToolPart } from '@opencode-ai/sdk/v2/client';

interface ToolCallCardProps {
  part: ToolPart;
}

/** ToolCallCard renders a collapsible card for a tool call with status indicator. */
export default function ToolCallCard({ part }: ToolCallCardProps) {
  const { tool, state } = part;
  const status = state.status;
  const isRunning = status === 'running';
  const isError = status === 'error';
  const isCompleted = status === 'completed';

  const [open, setOpen] = useState(isRunning || isError);

  const title =
    (status === 'completed' || status === 'running') && state.title
      ? state.title
      : tool;

  const statusColor = isError
    ? 'var(--red)'
    : isRunning
      ? 'var(--yellow)'
      : isCompleted
        ? 'var(--green)'
        : 'var(--muted)';

  const statusLabel = isError
    ? 'error'
    : isRunning
      ? 'running'
      : isCompleted
        ? 'done'
        : 'pending';

  return (
    <div style={styles.card}>
      <button style={styles.header} onClick={() => setOpen(!open)}>
        <span style={styles.toolName}>{title}</span>
        <span style={{ ...styles.statusBadge, color: statusColor }}>
          {isRunning && <span style={styles.spinner}>&#x27F3;</span>}
          {statusLabel}
        </span>
        <span style={styles.toggle}>{open ? '\u25BE' : '\u25B8'}</span>
      </button>
      {open && (
        <div style={styles.body}>
          {isError && (
            <pre style={styles.errorOutput}>
              {(state as { error: string }).error}
            </pre>
          )}
          {isCompleted && (
            <pre style={styles.output}>
              {(state as { output: string }).output || '(no output)'}
            </pre>
          )}
          {isRunning && (
            <span style={styles.runningText}>Running...</span>
          )}
          {status === 'pending' && (
            <span style={styles.runningText}>Pending...</span>
          )}
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  card: {
    border: '1px solid var(--border)',
    borderRadius: 4,
    overflow: 'hidden',
    margin: '4px 0',
  },
  header: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    width: '100%',
    padding: '8px 12px',
    background: 'var(--surface)',
    border: 'none',
    cursor: 'pointer',
    textAlign: 'left' as const,
  },
  toolName: {
    flex: 1,
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
    color: 'var(--text)',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap' as const,
  },
  statusBadge: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    letterSpacing: '0.05em',
    display: 'flex',
    alignItems: 'center',
    gap: 4,
  },
  spinner: {
    display: 'inline-block',
    animation: 'spin 1s linear infinite',
  },
  toggle: {
    fontSize: 10,
    color: 'var(--muted)',
  },
  body: {
    padding: '8px 12px',
    borderTop: '1px solid var(--border)',
    background: 'var(--bg)',
  },
  output: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--text)',
    margin: 0,
    whiteSpace: 'pre-wrap' as const,
    wordBreak: 'break-word' as const,
    maxHeight: 300,
    overflowY: 'auto' as const,
    lineHeight: 1.4,
  },
  errorOutput: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--red)',
    margin: 0,
    whiteSpace: 'pre-wrap' as const,
    wordBreak: 'break-word' as const,
    maxHeight: 200,
    overflowY: 'auto' as const,
  },
  runningText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
  },
};
