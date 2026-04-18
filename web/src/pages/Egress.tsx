import { useState, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  getEgressLog,
  getEgressAllowlist,
  setEgressAllowlist,
  logout,
  type EgressLogEntry,
} from '../api/client';

/** Egress renders the egress log viewer and allowlist editor. */
export default function Egress() {
  const navigate = useNavigate();
  const [entries, setEntries] = useState<EgressLogEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [allowlist, setAllowlist] = useState<string[]>([]);
  const [newEntry, setNewEntry] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');

  const fetchData = useCallback(async () => {
    try {
      const [log, al] = await Promise.all([getEgressLog(), getEgressAllowlist()]);
      setEntries(log.entries);
      setTotal(log.total);
      setAllowlist(al.entries);
    } catch {
      window.location.href = '/login';
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { fetchData(); }, [fetchData]);

  const saveAllowlist = async (updated: string[]) => {
    setSaving(true);
    setError('');
    setSuccess('');
    try {
      await setEgressAllowlist(updated);
      setAllowlist(updated);
      setSuccess('Allowlist updated');
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to update');
    } finally {
      setSaving(false);
    }
  };

  const handleAdd = async () => {
    const trimmed = newEntry.trim();
    if (!trimmed || allowlist.includes(trimmed)) return;
    await saveAllowlist([...allowlist, trimmed]);
    setNewEntry('');
  };

  const handleRemove = (entry: string) => saveAllowlist(allowlist.filter(e => e !== entry));

  return (
    <div style={styles.container}>
      <header style={styles.header}>
        <span style={styles.wordmark}>APPX</span>
        <button style={styles.navBtn} onClick={() => logout().then(() => { window.location.href = '/login'; })}>Logout</button>
      </header>

      <main style={styles.main}>
        <div style={styles.pageHeader}>
          <button style={styles.backBtn} onClick={() => navigate('/')}>&#8592;</button>
          <span style={styles.pageTitle}>EGRESS LOG</span>
        </div>

        <div style={styles.card}>
          <h3 style={styles.cardTitle}>Allowlist</h3>
          <p style={styles.description}>Destinations the OpenCode agent can reach. Format: <code style={styles.code}>host:port</code></p>
          {error && <div style={styles.errorMsg}>{error}</div>}
          {success && <div style={styles.successMsg}>{success}</div>}
          <div style={styles.allowlistItems}>
            {allowlist.map(e => (
              <div key={e} style={styles.allowlistItem}>
                <span style={styles.allowlistText}>{e}</span>
                <button style={styles.removeBtn} onClick={() => handleRemove(e)} disabled={saving}>Remove</button>
              </div>
            ))}
          </div>
          <div style={styles.inputRow}>
            <input style={styles.input} type="text" value={newEntry}
              onChange={e => setNewEntry(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && handleAdd()}
              placeholder="api.example.com:443" />
            <button style={styles.addBtn} onClick={handleAdd} disabled={saving || !newEntry.trim()}>Add</button>
          </div>
        </div>

        <div style={{ ...styles.card, marginTop: 16 }}>
          <h3 style={styles.cardTitle}>Connection Log {total > 0 && <span style={styles.totalBadge}>{total}</span>}</h3>
          {loading ? (
            <span style={styles.mutedText}>Loading...</span>
          ) : entries.length === 0 ? (
            <span style={styles.mutedText}>No outbound connections logged yet</span>
          ) : (
            <div style={styles.table}>
              <div style={styles.tableHeader}>
                <span style={{ ...styles.tableCell, flex: 2 }}>TIMESTAMP</span>
                <span style={{ ...styles.tableCell, flex: 3 }}>DESTINATION</span>
                <span style={{ ...styles.tableCell, flex: 1 }}>PORT</span>
                <span style={{ ...styles.tableCell, flex: 1 }}>STATUS</span>
              </div>
              {entries.map(e => (
                <div key={e.id} style={styles.tableRow}>
                  <span style={{ ...styles.tableCellVal, flex: 2 }}>{new Date(e.timestamp).toLocaleTimeString()}</span>
                  <span style={{ ...styles.tableCellVal, flex: 3 }}>{e.destination}</span>
                  <span style={{ ...styles.tableCellVal, flex: 1 }}>{e.port}</span>
                  <span style={{ ...styles.tableCellVal, flex: 1, color: e.allowed ? 'var(--green)' : 'var(--red)' }}>
                    {e.allowed ? 'ALLOWED' : 'BLOCKED'}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      </main>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { minHeight: '100vh' },
  header: { borderBottom: '1px solid var(--border)', padding: '14px 24px', display: 'flex', alignItems: 'center', justifyContent: 'space-between' },
  wordmark: { fontFamily: "'DM Sans', sans-serif", fontSize: 14, fontWeight: 500, letterSpacing: '0.35em', color: 'var(--text)' },
  navBtn: { background: 'transparent', border: 'none', color: 'var(--muted)', padding: '5px 10px', fontSize: 13, cursor: 'pointer' },
  main: { padding: '28px 24px', maxWidth: 800, margin: '0 auto' },
  pageHeader: { display: 'flex', alignItems: 'center', gap: 10, marginBottom: 20 },
  backBtn: { background: 'transparent', border: 'none', color: 'var(--muted)', fontSize: 20, cursor: 'pointer', padding: '0 4px', lineHeight: 1 },
  pageTitle: { fontFamily: "'JetBrains Mono', monospace", fontSize: 11, letterSpacing: '0.12em', color: 'var(--muted)' },
  card: { background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 4, padding: '20px 22px' },
  cardTitle: { margin: '0 0 8px', fontSize: 14, fontWeight: 500, color: 'var(--text)', display: 'flex', alignItems: 'center', gap: 8 },
  totalBadge: { fontFamily: "'JetBrains Mono', monospace", fontSize: 10, background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 3, padding: '1px 6px', color: 'var(--muted)' },
  description: { color: 'var(--muted)', fontSize: 13, lineHeight: 1.6, margin: '0 0 18px' },
  code: { fontFamily: "'JetBrains Mono', monospace", fontSize: 11, background: 'var(--bg)', padding: '1px 5px', borderRadius: 3, color: 'var(--text)' },
  errorMsg: { fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: 'var(--red)', marginBottom: 14, padding: '7px 10px', background: 'var(--red-dim)', border: '1px solid rgba(255,107,107,0.2)', borderRadius: 4 },
  successMsg: { fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: 'var(--green)', marginBottom: 14, padding: '7px 10px', background: 'rgba(61,220,132,0.08)', border: '1px solid rgba(61,220,132,0.2)', borderRadius: 4 },
  allowlistItems: { display: 'flex', flexDirection: 'column', gap: 4, marginBottom: 14 },
  allowlistItem: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '6px 10px', background: 'var(--bg)', borderRadius: 4, border: '1px solid var(--border)' },
  allowlistText: { fontFamily: "'JetBrains Mono', monospace", fontSize: 12, color: 'var(--text)' },
  removeBtn: { background: 'transparent', border: 'none', color: 'var(--muted)', padding: '2px 6px', fontSize: 11, cursor: 'pointer' },
  inputRow: { display: 'flex', gap: 8 },
  input: { flex: 1, background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 4, padding: '8px 12px', color: 'var(--text)', fontSize: 13, outline: 'none' },
  addBtn: { background: 'var(--blue)', border: 'none', color: '#fff', borderRadius: 4, padding: '8px 18px', fontSize: 13, fontWeight: 500, cursor: 'pointer' },
  mutedText: { fontFamily: "'JetBrains Mono', monospace", fontSize: 12, color: 'var(--muted)' },
  table: { display: 'flex', flexDirection: 'column' },
  tableHeader: { display: 'flex', padding: '8px 10px', borderBottom: '1px solid var(--border)' },
  tableCell: { fontFamily: "'JetBrains Mono', monospace", fontSize: 10, letterSpacing: '0.1em', color: 'var(--muted)' },
  tableRow: { display: 'flex', padding: '8px 10px', borderBottom: '1px solid var(--border)' },
  tableCellVal: { fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: 'var(--text)' },
};
