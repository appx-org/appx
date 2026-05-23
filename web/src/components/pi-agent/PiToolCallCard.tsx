import { useState } from 'react';
import type { UiMessagePart } from '../../lib/pi-agent/types';

type Tool = Extract<UiMessagePart, { type: 'tool' }>;

function formatJson(value: unknown): string {
  if (value === undefined || value === null) return '';
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function truncate(text: string, limit: number): string {
  return text.length > limit ? `${text.slice(0, limit)}...` : text;
}

function summarise(tool: Tool): string {
  const args = (tool.args ?? {}) as Record<string, unknown>;
  switch (tool.name) {
    case 'read':
      return String(args.path ?? args.file_path ?? '');
    case 'write':
      return String(args.path ?? args.file_path ?? '');
    case 'edit':
      return String(args.path ?? args.file_path ?? '');
    case 'bash':
      return String(args.command ?? '').replace(/\s+/g, ' ').trim();
    case 'glob':
    case 'grep':
      return String(args.pattern ?? args.query ?? '');
    default:
      return truncate(formatJson(args).replace(/\s+/g, ' '), 90);
  }
}

export default function PiToolCallCard({ tool }: { tool: Tool }) {
  const [open, setOpen] = useState(true);
  const result = formatJson(tool.result);
  const args = formatJson(tool.args);
  const summary = summarise(tool);

  return (
    <div style={styles.container}>
      <button type="button" style={styles.header} onClick={() => setOpen((value) => !value)}>
        <span style={{ ...styles.status, ...(tool.status === 'error' ? styles.statusError : {}) }}>
          {tool.status}
        </span>
        <span style={styles.name}>{tool.name}</span>
        {summary && <span style={styles.summary}>{summary}</span>}
        <span style={styles.chevron}>{open ? '▾' : '▸'}</span>
      </button>
      {open && (
        <div style={styles.body}>
          {args && args !== '{}' && (
            <>
              <span style={styles.label}>arguments</span>
              <pre style={styles.pre}>{truncate(args, 4000)}</pre>
            </>
          )}
          {result && (
            <>
              <span style={styles.label}>{tool.isError ? 'error' : 'result'}</span>
              <pre style={styles.pre}>{truncate(result, 4000)}</pre>
            </>
          )}
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    border: '1px solid var(--border)',
    borderRadius: 4,
    overflow: 'hidden',
    background: 'var(--surface)',
  },
  header: {
    width: '100%',
    display: 'grid',
    gridTemplateColumns: 'auto auto minmax(0, 1fr) auto',
    alignItems: 'center',
    gap: 8,
    padding: '6px 8px',
    border: 'none',
    borderBottom: '1px solid var(--border)',
    background: 'transparent',
    color: 'var(--text)',
    cursor: 'pointer',
    textAlign: 'left',
  },
  status: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 9,
    color: 'var(--green)',
    textTransform: 'uppercase',
  },
  statusError: { color: 'var(--red)' },
  name: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--cyan)',
  },
  summary: {
    minWidth: 0,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
    fontSize: 11,
    color: 'var(--muted)',
  },
  chevron: {
    fontSize: 11,
    color: 'var(--muted)',
  },
  body: {
    padding: 8,
    display: 'flex',
    flexDirection: 'column',
    gap: 6,
  },
  label: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 9,
    color: 'var(--muted)',
    textTransform: 'uppercase',
  },
  pre: {
    margin: 0,
    whiteSpace: 'pre-wrap',
    overflowWrap: 'anywhere',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    lineHeight: 1.45,
    color: 'var(--text)',
  },
};
