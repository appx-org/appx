import { useNavigate } from 'react-router-dom';
import Terminal from '../components/Terminal';

/** Shell renders a full-page direct server PTY terminal.
 *  Accessible at /shell. Uses appx's own /api/shell endpoints — works
 *  independently of the agent runtime. */
export default function Shell() {
  const navigate = useNavigate();
  return (
    <div style={styles.page}>
      <div style={styles.header}>
        <span style={styles.back} onClick={() => navigate('/')}>&#8592;</span>
        <span style={styles.label}>Server Shell</span>
        <span style={styles.sub}>direct PTY</span>
      </div>
      <div style={styles.termWrapper}>
        <Terminal />
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  page: { display: 'flex', flexDirection: 'column', height: '100vh', background: 'var(--bg)', fontFamily: "'JetBrains Mono', monospace" },
  header: { display: 'flex', alignItems: 'center', gap: 12, padding: '10px 16px', borderBottom: '1px solid var(--border)', flexShrink: 0 },
  back: { fontSize: 18, color: 'var(--muted)', cursor: 'pointer', userSelect: 'none' as const, padding: '4px 8px', borderRadius: 4 },
  label: { fontSize: 13, fontWeight: 600, color: 'var(--cyan)', letterSpacing: '0.05em' },
  sub: { fontSize: 11, color: 'var(--muted)' },
  termWrapper: { flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0, padding: 8 },
};
