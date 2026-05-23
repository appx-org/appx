import { useCallback, useEffect, useState } from 'react';
import { createPiSession, listPiSessions, type PiSessionInfo } from '../../api/piAgent';

function labelFor(session: PiSessionInfo) {
  return session.firstMessage?.trim() || 'Untitled';
}

export default function PiSessionList({
  projectId,
  activeSessionId,
  refreshTick,
  onSelectSession,
}: {
  projectId: string;
  activeSessionId: string | null;
  refreshTick: number;
  onSelectSession: (id: string) => void;
}) {
  const [sessions, setSessions] = useState<PiSessionInfo[]>([]);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState('');

  const fetchSessions = useCallback(async () => {
    try {
      const res = await listPiSessions(projectId);
      setSessions(res.sessions);
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load sessions');
    }
  }, [projectId]);

  useEffect(() => {
    void fetchSessions();
  }, [fetchSessions, refreshTick]);

  const handleCreate = async () => {
    setCreating(true);
    setError('');
    try {
      const session = await createPiSession(projectId);
      await fetchSessions();
      onSelectSession(session.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create session');
    } finally {
      setCreating(false);
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <span style={styles.title}>SESSIONS</span>
        <button style={styles.createBtn} onClick={handleCreate} disabled={creating}>
          {creating ? '...' : '+ New'}
        </button>
      </div>
      {error && <div style={styles.error}>{error}</div>}
      <div style={styles.list}>
        {sessions.length === 0 ? (
          <span style={styles.emptyText}>No sessions yet</span>
        ) : (
          sessions.map((session) => (
            <button
              key={session.id}
              style={session.id === activeSessionId ? styles.itemActive : styles.item}
              onClick={() => onSelectSession(session.id)}
              title={labelFor(session)}
            >
              <span style={styles.itemTitle}>{labelFor(session)}</span>
              <span style={styles.itemMeta}>
                {session.id.slice(0, 8)} · {session.messageCount} msg
              </span>
            </button>
          ))
        )}
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    display: 'flex',
    flexDirection: 'column',
    borderRight: '1px solid var(--border)',
    width: 220,
    flexShrink: 0,
  },
  header: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: '12px 14px',
    borderBottom: '1px solid var(--border)',
  },
  title: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    letterSpacing: '0.1em',
    color: 'var(--muted)',
  },
  createBtn: {
    background: 'transparent',
    border: '1px solid rgba(61,220,132,0.35)',
    color: 'var(--green)',
    borderRadius: 4,
    padding: '3px 10px',
    fontSize: 11,
    cursor: 'pointer',
  },
  error: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--red)',
    padding: '6px 14px',
    overflowWrap: 'anywhere',
  },
  list: { flex: 1, overflowY: 'auto' },
  emptyText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
    padding: '16px 14px',
    display: 'block',
  },
  item: {
    display: 'flex',
    flexDirection: 'column',
    gap: 2,
    width: '100%',
    padding: '10px 14px',
    background: 'transparent',
    border: 'none',
    borderBottom: '1px solid var(--border)',
    cursor: 'pointer',
    textAlign: 'left',
  },
  itemActive: {
    display: 'flex',
    flexDirection: 'column',
    gap: 2,
    width: '100%',
    padding: '10px 14px',
    background: 'var(--surface-hover)',
    border: 'none',
    borderBottom: '1px solid var(--border)',
    borderLeft: '2px solid var(--cyan)',
    cursor: 'pointer',
    textAlign: 'left',
  },
  itemTitle: {
    fontSize: 12,
    color: 'var(--text)',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  itemMeta: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--muted)',
  },
};
