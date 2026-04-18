import type { ConnectionStatus } from '../lib/agent-core/connection';

interface StatusBarProps {
  agentStatus: 'idle' | 'running' | 'error';
  connectionStatus: ConnectionStatus;
}

/** StatusBar shows agent status and SSE connection health. */
export default function StatusBar({ agentStatus, connectionStatus }: StatusBarProps) {
  const agentColor = agentStatus === 'running' ? 'var(--yellow)' : agentStatus === 'error' ? 'var(--red)' : 'var(--green)';
  const connColor = connectionStatus === 'connected' ? 'var(--green)' : connectionStatus === 'connecting' ? 'var(--yellow)' : 'var(--red)';

  return (
    <div style={styles.bar}>
      <div style={styles.item}>
        <span style={{ ...styles.dot, background: agentColor }} />
        <span style={styles.label}>{agentStatus === 'running' ? 'Agent running' : agentStatus === 'error' ? 'Agent error' : 'Agent idle'}</span>
      </div>
      <div style={styles.item}>
        <span style={{ ...styles.dot, background: connColor }} />
        <span style={styles.label}>{connectionStatus === 'connected' ? 'Connected' : connectionStatus === 'connecting' ? 'Reconnecting...' : 'Disconnected'}</span>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  bar: { display: 'flex', gap: 16, padding: '6px 20px', borderTop: '1px solid var(--border)', background: 'var(--bg)' },
  item: { display: 'flex', alignItems: 'center', gap: 6 },
  dot: { width: 6, height: 6, borderRadius: '50%', flexShrink: 0 },
  label: { fontFamily: "'JetBrains Mono', monospace", fontSize: 10, color: 'var(--muted)' },
};
