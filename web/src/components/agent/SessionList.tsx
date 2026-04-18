import { useState, useEffect, useCallback, useMemo } from 'react';
import { getClient } from '../../api/opencode';
import type { Session } from '@opencode-ai/sdk/v2/client';

/** SessionList displays OpenCode sessions for a project directory.
 *  Allows creating, selecting, and deleting sessions. */
export default function SessionList({
  projectDir,
  activeSessionId,
  onSelectSession,
}: {
  projectDir: string;
  activeSessionId: string | null;
  onSelectSession: (id: string) => void;
}) {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState('');

  const client = useMemo(
    () => (projectDir ? getClient(projectDir) : null),
    [projectDir],
  );

  const fetchSessions = useCallback(async () => {
    if (!client) return;
    try {
      const res = await client.session.list({});
      if (!res.error && res.data) {
        setSessions(res.data as Session[]);
      }
    } catch {
      // OpenCode may not be running yet
    }
  }, [client]);

  useEffect(() => {
    fetchSessions();
  }, [fetchSessions]);

  const handleCreate = async () => {
    if (!client) return;
    setCreating(true);
    setError('');
    try {
      const res = await client.session.create({});
      if (res.error) {
        setError(String(res.error));
        return;
      }
      const session = res.data as Session | undefined;
      if (session?.id) {
        setSessions((prev) =>
          prev.some((s) => s.id === session.id) ? prev : [...prev, session],
        );
        onSelectSession(session.id);
      } else {
        setError('Failed to create session');
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to create session');
    } finally {
      setCreating(false);
    }
  };

  const handleDelete = async (e: React.MouseEvent, sessionId: string) => {
    e.stopPropagation();
    if (!client) return;
    try {
      await client.session.delete({ sessionID: sessionId });
      setSessions((prev) => prev.filter((s) => s.id !== sessionId));
    } catch (err) {
      console.error('Failed to delete session:', err);
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <span style={styles.title}>SESSIONS</span>
        <button
          style={styles.createBtn}
          onClick={handleCreate}
          disabled={creating}
        >
          {creating ? '...' : '+ New'}
        </button>
      </div>
      {error && <div style={styles.error}>{error}</div>}
      <div style={styles.list}>
        {sessions.length === 0 ? (
          <span style={styles.emptyText}>No sessions yet</span>
        ) : (
          sessions.map((s) => (
            <button
              key={s.id}
              style={
                s.id === activeSessionId ? styles.itemActive : styles.item
              }
              onClick={() => onSelectSession(s.id)}
            >
              <div style={styles.itemRow}>
                <span style={styles.itemTitle}>
                  {s.title || 'Untitled'}
                </span>
                <button
                  style={styles.deleteBtn}
                  onClick={(e) => handleDelete(e, s.id)}
                  title="Delete session"
                >
                  ×
                </button>
              </div>
              <span style={styles.itemId}>{s.id.slice(0, 8)}</span>
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
    textAlign: 'left' as const,
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
    textAlign: 'left' as const,
  },
  itemRow: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  itemTitle: {
    fontSize: 12,
    color: 'var(--text)',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap' as const,
    flex: 1,
  },
  deleteBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    fontSize: 14,
    cursor: 'pointer',
    padding: '0 4px',
    lineHeight: 1,
  },
  itemId: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--muted)',
  },
};
