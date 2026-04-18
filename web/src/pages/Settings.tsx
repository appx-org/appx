import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { getApiKeyStatus, setApiKey, deleteApiKey, logout } from '../api/client';

/** Settings page for managing the Anthropic API key and navigating to related settings.
 *  Shows whether a key is configured, allows setting a new key or removing the existing one,
 *  and provides a link to the egress log and allowlist. */
export default function Settings() {
  const navigate = useNavigate();
  const [keySet, setKeySet] = useState<boolean | null>(null);
  const [newKey, setNewKey] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');

  useEffect(() => {
    getApiKeyStatus()
      .then(res => setKeySet(res.set))
      .catch(() => { window.location.href = '/login'; });
  }, []);

  const handleSave = async () => {
    if (!newKey.trim()) return;
    setSaving(true);
    setError('');
    setSuccess('');
    try {
      await setApiKey(newKey.trim());
      setKeySet(true);
      setNewKey('');
      setSuccess('API key saved.');
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to save key');
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async () => {
    setSaving(true);
    setError('');
    setSuccess('');
    try {
      await deleteApiKey();
      setKeySet(false);
      setSuccess('API key removed.');
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to remove key');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div style={styles.container}>
      <header style={styles.header}>
        <span style={styles.wordmark}>APPX</span>
        <div style={styles.headerActions}>
          <button
            data-btn="text-nav"
            style={{ ...styles.navBtn, color: 'var(--muted)' }}
            onClick={() => logout().then(() => { window.location.href = '/login'; })}
          >
            Logout
          </button>
        </div>
      </header>

      <main style={styles.main}>
        <div style={styles.pageHeader}>
          <button style={styles.backBtn} onClick={() => navigate('/')} aria-label="Back to dashboard">&#8592;</button>
          <span style={styles.pageTitle}>SETTINGS</span>
        </div>

        <div style={styles.card}>
          <h3 style={styles.cardTitle}>Anthropic API Key</h3>
          <p style={styles.description}>
            Required for AI agents (OpenCode) in project containers.
            You can also set <code style={styles.code}>ANTHROPIC_API_KEY</code> as
            an environment variable on the host.
          </p>

          <div style={styles.statusRow}>
            <span style={styles.statusLabel}>Status</span>
            {keySet === null ? (
              <span style={styles.statusMuted}>Loading…</span>
            ) : keySet ? (
              <span style={{ ...styles.status, color: 'var(--green)' }}>
                <span style={{ ...styles.dot, background: 'var(--green)' }} />
                Configured
              </span>
            ) : (
              <span style={{ ...styles.status, color: 'var(--red)' }}>
                <span style={{ ...styles.dot, background: 'var(--red)' }} />
                Not set
              </span>
            )}
          </div>

          {error && <div style={styles.error}>{error}</div>}
          {success && <div style={styles.successMsg}>{success}</div>}

          <div style={styles.inputRow}>
            <input
              style={styles.input}
              type="password"
              placeholder="sk-ant-…"
              value={newKey}
              onChange={e => setNewKey(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && handleSave()}
            />
            <button
              data-btn="primary"
              style={styles.saveBtn}
              onClick={handleSave}
              disabled={saving || !newKey.trim()}
            >
              {saving ? 'Saving…' : 'Save'}
            </button>
          </div>

          {keySet && (
            <button
              data-btn="text-red"
              style={styles.removeBtn}
              onClick={handleDelete}
              disabled={saving}
            >
              Remove key
            </button>
          )}
        </div>

        <div style={{ ...styles.card, marginTop: 16, cursor: 'pointer' }} onClick={() => navigate('/egress')}>
          <h3 style={styles.cardTitle}>Egress Control</h3>
          <p style={{ ...styles.description, margin: 0 }}>
            View outbound connection log and manage the allowlist for agent network access.
          </p>
        </div>
      </main>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    minHeight: '100vh',
  },
  header: {
    borderBottom: '1px solid var(--border)',
    padding: '14px 24px',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  wordmark: {
    fontFamily: "'DM Sans', sans-serif",
    fontSize: 14,
    fontWeight: 500,
    letterSpacing: '0.35em',
    color: 'var(--text)',
  },
  headerActions: {
    display: 'flex',
    alignItems: 'center',
    gap: 4,
  },
  navBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    padding: '5px 10px',
    fontSize: 13,
    cursor: 'pointer',
  },
  main: {
    padding: '28px 24px',
    maxWidth: 600,
    margin: '0 auto',
  },
  pageHeader: {
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    marginBottom: 20,
  },
  backBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    fontSize: 20,
    cursor: 'pointer',
    padding: '0 4px',
    lineHeight: 1,
  },
  pageTitle: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    letterSpacing: '0.12em',
    color: 'var(--muted)',
  },
  card: {
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '20px 22px',
  },
  cardTitle: {
    margin: '0 0 8px',
    fontSize: 14,
    fontWeight: 500,
    color: 'var(--text)',
  },
  description: {
    color: 'var(--muted)',
    fontSize: 13,
    lineHeight: 1.6,
    margin: '0 0 18px',
  },
  code: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    background: 'var(--bg)',
    padding: '1px 5px',
    borderRadius: 3,
    color: 'var(--text)',
  },
  statusRow: {
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    marginBottom: 18,
    fontSize: 13,
  },
  statusLabel: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    letterSpacing: '0.1em',
    color: 'var(--muted)',
    textTransform: 'uppercase' as const,
  },
  dot: {
    display: 'inline-block',
    width: 7,
    height: 7,
    borderRadius: '50%',
    marginRight: 6,
    flexShrink: 0,
  },
  status: {
    fontSize: 13,
    fontWeight: 500,
    display: 'flex',
    alignItems: 'center',
  },
  statusMuted: {
    color: 'var(--muted)',
    fontSize: 13,
  },
  inputRow: {
    display: 'flex',
    gap: 8,
    marginBottom: 12,
  },
  input: {
    flex: 1,
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '8px 12px',
    color: 'var(--text)',
    fontSize: 13,
    outline: 'none',
  },
  saveBtn: {
    background: 'var(--blue)',
    border: 'none',
    color: '#fff',
    borderRadius: 4,
    padding: '8px 18px',
    fontSize: 13,
    fontWeight: 500,
    cursor: 'pointer',
  },
  removeBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    padding: '4px 0',
    fontSize: 12,
    cursor: 'pointer',
  },
  error: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--red)',
    marginBottom: 14,
    padding: '7px 10px',
    background: 'var(--red-dim)',
    border: '1px solid rgba(255,107,107,0.2)',
    borderRadius: 4,
  },
  successMsg: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--green)',
    marginBottom: 14,
    padding: '7px 10px',
    background: 'var(--green-dim)',
    border: '1px solid rgba(61,220,132,0.2)',
    borderRadius: 4,
  },
};
