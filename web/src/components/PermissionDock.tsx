import type { PermissionRequest } from '@opencode-ai/sdk/v2/client';

interface PermissionDockProps {
  permission: PermissionRequest;
  onRespond: (requestID: string, reply: 'once' | 'always' | 'reject') => void;
}

/** PermissionDock shows a permission request with allow/deny actions. */
export default function PermissionDock({ permission, onRespond }: PermissionDockProps) {
  return (
    <div style={styles.dock}>
      <div style={styles.header}>
        <span style={styles.icon}>&#x26A0;</span>
        <span style={styles.title}>Permission Required</span>
      </div>
      <div style={styles.info}>
        <span style={styles.label}>Tool:</span>
        <span style={styles.value}>{permission.permission}</span>
      </div>
      {permission.patterns.length > 0 && (
        <div style={styles.patterns}>
          {permission.patterns.map((p, i) => (
            <code key={i} style={styles.pattern}>{p}</code>
          ))}
        </div>
      )}
      <div style={styles.actions}>
        <button style={styles.denyBtn} onClick={() => onRespond(permission.id, 'reject')}>Deny</button>
        <button style={styles.alwaysBtn} onClick={() => onRespond(permission.id, 'always')}>Allow Always</button>
        <button style={styles.allowBtn} onClick={() => onRespond(permission.id, 'once')}>Allow Once</button>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  dock: { background: 'var(--surface)', border: '1px solid var(--yellow)', borderRadius: 6, padding: '12px 16px', margin: '0 20px 8px' },
  header: { display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 },
  icon: { fontSize: 14, color: 'var(--yellow)' },
  title: { fontFamily: "'JetBrains Mono', monospace", fontSize: 11, letterSpacing: '0.05em', color: 'var(--yellow)', fontWeight: 500 },
  info: { display: 'flex', gap: 6, marginBottom: 6, fontSize: 12 },
  label: { color: 'var(--muted)', fontFamily: "'JetBrains Mono', monospace", fontSize: 11 },
  value: { color: 'var(--text)', fontFamily: "'JetBrains Mono', monospace", fontSize: 11 },
  patterns: { display: 'flex', flexWrap: 'wrap' as const, gap: 4, marginBottom: 10 },
  pattern: { fontFamily: "'JetBrains Mono', monospace", fontSize: 10, background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 3, padding: '2px 6px', color: 'var(--text)' },
  actions: { display: 'flex', gap: 8, justifyContent: 'flex-end' },
  denyBtn: { background: 'transparent', border: '1px solid var(--red)', color: 'var(--red)', borderRadius: 4, padding: '5px 14px', fontSize: 11, cursor: 'pointer' },
  alwaysBtn: { background: 'transparent', border: '1px solid var(--green)', color: 'var(--green)', borderRadius: 4, padding: '5px 14px', fontSize: 11, cursor: 'pointer' },
  allowBtn: { background: 'var(--blue)', border: 'none', color: '#fff', borderRadius: 4, padding: '5px 14px', fontSize: 11, fontWeight: 500, cursor: 'pointer' },
};
