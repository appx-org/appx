import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  deleteAgentProviderCredential,
  deleteApiKey,
  getAgentAuthProviders,
  getApiKeyStatus,
  getServerConfig,
  logout,
  setAgentProviderApiKey,
  setApiKey,
  type AgentAuthProvider,
} from '../api/client';

function sourceLabel(source?: AgentAuthProvider['source']) {
  switch (source) {
    case 'stored':
      return 'Stored';
    case 'runtime':
      return 'Runtime';
    case 'environment':
      return 'Environment';
    case 'fallback':
      return 'Fallback';
    case 'models_json_key':
      return 'Models JSON';
    case 'models_json_command':
      return 'Command';
    default:
      return 'Not set';
  }
}

function providerSortScore(provider: AgentAuthProvider) {
  if (provider.configured) return 0;
  if (provider.provider === 'anthropic') return 1;
  if (provider.provider === 'openai') return 2;
  if (provider.provider === 'google') return 3;
  return 4;
}

/** Settings page for runtime credentials, egress, and account actions. */
export default function Settings() {
  const navigate = useNavigate();
  const [agentBackend, setAgentBackend] = useState<'opencode' | 'pi' | null>(null);
  const [keySet, setKeySet] = useState<boolean | null>(null);
  const [providers, setProviders] = useState<AgentAuthProvider[]>([]);
  const [selectedProvider, setSelectedProvider] = useState('');
  const [newKey, setNewKey] = useState('');
  const [saving, setSaving] = useState(false);
  const [loadingProviders, setLoadingProviders] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');

  const sortedProviders = useMemo(
    () =>
      [...providers].sort(
        (a, b) =>
          providerSortScore(a) - providerSortScore(b) ||
          b.availableModelCount - a.availableModelCount ||
          a.provider.localeCompare(b.provider),
      ),
    [providers],
  );

  const selected = sortedProviders.find((provider) => provider.provider === selectedProvider);
  const configuredCount = sortedProviders.filter((provider) => provider.configured).length;

  const loadPiAuth = useCallback(async () => {
    setLoadingProviders(true);
    try {
      const res = await getAgentAuthProviders();
      setProviders(res.providers);
      setSelectedProvider((current) => {
        if (current && res.providers.some((provider) => provider.provider === current)) {
          return current;
        }
        const preferred =
          res.providers.find((provider) => provider.configured) ||
          res.providers.find((provider) => provider.provider === 'anthropic') ||
          res.providers.find((provider) => provider.provider === 'openai') ||
          res.providers[0];
        return preferred?.provider ?? '';
      });
    } finally {
      setLoadingProviders(false);
    }
  }, []);

  useEffect(() => {
    getServerConfig()
      .then(async (cfg) => {
        setAgentBackend(cfg.agentBackend || 'opencode');
        if (cfg.agentBackend === 'pi') {
          await loadPiAuth();
          return;
        }
        const res = await getApiKeyStatus();
        setKeySet(res.set);
      })
      .catch(() => {
        window.location.href = '/login';
      });
  }, [loadPiAuth]);

  const handleOpenCodeSave = async () => {
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

  const handleOpenCodeDelete = async () => {
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

  const handlePiSave = async () => {
    if (!selectedProvider || !newKey.trim()) return;
    setSaving(true);
    setError('');
    setSuccess('');
    try {
      await setAgentProviderApiKey(selectedProvider, newKey.trim());
      await loadPiAuth();
      setNewKey('');
      setSuccess(`${selectedProvider} credential saved.`);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to save credential');
    } finally {
      setSaving(false);
    }
  };

  const handlePiDelete = async () => {
    if (!selectedProvider) return;
    setSaving(true);
    setError('');
    setSuccess('');
    try {
      await deleteAgentProviderCredential(selectedProvider);
      await loadPiAuth();
      setSuccess(`${selectedProvider} stored credential removed.`);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to remove credential');
    } finally {
      setSaving(false);
    }
  };

  const isPi = agentBackend === 'pi';

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
          {isPi ? (
            <>
              <h3 style={styles.cardTitle}>Agent Credentials</h3>
              <p style={styles.description}>
                Pi provider auth for the agent service user.
              </p>

              <div style={styles.statusRow}>
                <span style={styles.statusLabel}>Status</span>
                {loadingProviders ? (
                  <span style={styles.statusMuted}>Loading...</span>
                ) : configuredCount > 0 ? (
                  <span style={{ ...styles.status, color: 'var(--green)' }}>
                    <span style={{ ...styles.dot, background: 'var(--green)' }} />
                    {configuredCount} configured
                  </span>
                ) : (
                  <span style={{ ...styles.status, color: 'var(--muted)' }}>
                    <span style={{ ...styles.dot, background: 'var(--muted)' }} />
                    None stored
                  </span>
                )}
              </div>

              {error && <div style={styles.error}>{error}</div>}
              {success && <div style={styles.successMsg}>{success}</div>}

              <div style={styles.inputRow}>
                <select
                  style={styles.select}
                  value={selectedProvider}
                  onChange={(event) => setSelectedProvider(event.target.value)}
                  disabled={loadingProviders || sortedProviders.length === 0}
                >
                  {sortedProviders.map((provider) => (
                    <option key={provider.provider} value={provider.provider}>
                      {provider.provider}
                    </option>
                  ))}
                </select>
                <input
                  style={styles.input}
                  type="password"
                  placeholder={selectedProvider ? `${selectedProvider} API key` : 'Provider API key'}
                  value={newKey}
                  onChange={(event) => setNewKey(event.target.value)}
                  onKeyDown={(event) => event.key === 'Enter' && handlePiSave()}
                />
                <button
                  data-btn="primary"
                  style={styles.saveBtn}
                  onClick={handlePiSave}
                  disabled={saving || !selectedProvider || !newKey.trim()}
                >
                  {saving ? 'Saving...' : 'Save'}
                </button>
              </div>

              {selected && (
                <div style={styles.providerMeta}>
                  <span style={styles.statusLabel}>Selected</span>
                  <span style={selected.configured ? styles.providerConfigured : styles.providerUnset}>
                    {sourceLabel(selected.source)}
                  </span>
                  <span style={styles.statusMuted}>
                    {selected.availableModelCount}/{selected.modelCount} models available
                  </span>
                </div>
              )}

              {selected?.source === 'stored' && (
                <button
                  data-btn="text-red"
                  style={styles.removeBtn}
                  onClick={handlePiDelete}
                  disabled={saving}
                >
                  Remove stored credential
                </button>
              )}

              <div style={styles.providerList}>
                {sortedProviders.length === 0 ? (
                  <span style={styles.emptyText}>No providers reported by agent-server</span>
                ) : (
                  sortedProviders.slice(0, 12).map((provider) => (
                    <button
                      key={provider.provider}
                      type="button"
                      style={
                        provider.provider === selectedProvider
                          ? styles.providerRowActive
                          : styles.providerRow
                      }
                      onClick={() => setSelectedProvider(provider.provider)}
                    >
                      <span style={styles.providerName}>{provider.provider}</span>
                      <span style={provider.configured ? styles.providerConfigured : styles.providerUnset}>
                        {sourceLabel(provider.source)}
                      </span>
                      <span style={styles.providerCount}>
                        {provider.availableModelCount}/{provider.modelCount}
                      </span>
                    </button>
                  ))
                )}
              </div>
            </>
          ) : (
            <>
              <h3 style={styles.cardTitle}>Legacy OpenCode Anthropic Key</h3>
              <p style={styles.description}>
                Used only when <code style={styles.code}>APPX_AGENT_BACKEND=opencode</code>.
              </p>

              <div style={styles.statusRow}>
                <span style={styles.statusLabel}>Status</span>
                {keySet === null ? (
                  <span style={styles.statusMuted}>Loading...</span>
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
                  placeholder="sk-ant-..."
                  value={newKey}
                  onChange={(event) => setNewKey(event.target.value)}
                  onKeyDown={(event) => event.key === 'Enter' && handleOpenCodeSave()}
                />
                <button
                  data-btn="primary"
                  style={styles.saveBtn}
                  onClick={handleOpenCodeSave}
                  disabled={saving || !newKey.trim()}
                >
                  {saving ? 'Saving...' : 'Save'}
                </button>
              </div>

              {keySet && (
                <button
                  data-btn="text-red"
                  style={styles.removeBtn}
                  onClick={handleOpenCodeDelete}
                  disabled={saving}
                >
                  Remove key
                </button>
              )}
            </>
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
    maxWidth: 700,
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
    textTransform: 'uppercase',
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
    display: 'grid',
    gridTemplateColumns: '160px minmax(0, 1fr) auto',
    gap: 8,
    marginBottom: 12,
  },
  select: {
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '8px 10px',
    color: 'var(--text)',
    fontSize: 13,
    outline: 'none',
    minWidth: 0,
  },
  input: {
    minWidth: 0,
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
    color: 'var(--red)',
    padding: '4px 0',
    fontSize: 12,
    cursor: 'pointer',
  },
  error: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--red)',
    background: 'var(--red-dim)',
    padding: '8px 10px',
    borderRadius: 4,
    marginBottom: 12,
    overflowWrap: 'anywhere',
  },
  successMsg: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--green)',
    marginBottom: 12,
  },
  providerMeta: {
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    marginBottom: 10,
    minHeight: 22,
  },
  providerList: {
    marginTop: 16,
    borderTop: '1px solid var(--border)',
  },
  providerRow: {
    width: '100%',
    display: 'grid',
    gridTemplateColumns: 'minmax(0, 1fr) 120px 64px',
    gap: 8,
    alignItems: 'center',
    padding: '9px 0',
    border: 'none',
    borderBottom: '1px solid var(--border)',
    background: 'transparent',
    color: 'var(--text)',
    cursor: 'pointer',
    textAlign: 'left',
  },
  providerRowActive: {
    width: '100%',
    display: 'grid',
    gridTemplateColumns: 'minmax(0, 1fr) 120px 64px',
    gap: 8,
    alignItems: 'center',
    padding: '9px 0 9px 8px',
    border: 'none',
    borderBottom: '1px solid var(--border)',
    borderLeft: '2px solid var(--cyan)',
    background: 'var(--surface-hover)',
    color: 'var(--text)',
    cursor: 'pointer',
    textAlign: 'left',
  },
  providerName: {
    minWidth: 0,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
  },
  providerConfigured: {
    color: 'var(--green)',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
  },
  providerUnset: {
    color: 'var(--muted)',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
  },
  providerCount: {
    color: 'var(--muted)',
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    textAlign: 'right',
  },
  emptyText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
    paddingTop: 14,
    display: 'block',
  },
};
