import { useState, useEffect, useCallback } from 'react';
import {
  getEgressPending,
  approveEgressRequest,
  denyEgressRequest,
  type EgressPendingRequest,
} from '../api/client';

/** EgressRequestDock polls for pending egress permission requests and renders
 *  Allow/Deny buttons for each. Shown in the project view when the agent asks
 *  for access to a blocked host. */
export default function EgressRequestDock() {
  const [requests, setRequests] = useState<EgressPendingRequest[]>([]);

  const poll = useCallback(async () => {
    try {
      const data = await getEgressPending();
      setRequests(data.requests ?? []);
    } catch {
      // Ignore — dashboard may not be reachable briefly during restart.
    }
  }, []);

  useEffect(() => {
    const initial = window.setTimeout(() => void poll(), 0);
    const interval = window.setInterval(() => void poll(), 2000);
    return () => {
      window.clearTimeout(initial);
      window.clearInterval(interval);
    };
  }, [poll]);

  const handleApprove = async (id: string) => {
    await approveEgressRequest(id);
    setRequests((prev) => prev.filter((r) => r.id !== id));
  };

  const handleDeny = async (id: string) => {
    await denyEgressRequest(id);
    setRequests((prev) => prev.filter((r) => r.id !== id));
  };

  if (requests.length === 0) return null;

  return (
    <div style={styles.container}>
      {requests.map((req) => (
        <div key={req.id} style={styles.dock}>
          <div style={styles.header}>
            <span style={styles.icon}>&#x1F310;</span>
            <span style={styles.title}>Egress Access Requested</span>
          </div>
          <div style={styles.info}>
            <span style={styles.label}>Host:</span>
            <code style={styles.value}>{req.host}:{req.port}</code>
          </div>
          {req.reason && (
            <div style={styles.info}>
              <span style={styles.label}>Reason:</span>
              <span style={styles.value}>{req.reason}</span>
            </div>
          )}
          <div style={styles.actions}>
            <button style={styles.denyBtn} onClick={() => handleDeny(req.id)}>Deny</button>
            <button style={styles.allowBtn} onClick={() => handleApprove(req.id)}>Allow</button>
          </div>
        </div>
      ))}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { display: 'flex', flexDirection: 'column', gap: 8 },
  dock: { background: 'var(--surface)', border: '1px solid var(--yellow)', borderRadius: 6, padding: '12px 16px', margin: '0 20px 8px' },
  header: { display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 },
  icon: { fontSize: 14 },
  title: { fontFamily: "'JetBrains Mono', monospace", fontSize: 11, letterSpacing: '0.05em', color: 'var(--yellow)', fontWeight: 500 },
  info: { display: 'flex', gap: 6, marginBottom: 6, fontSize: 12 },
  label: { color: 'var(--muted)', fontFamily: "'JetBrains Mono', monospace", fontSize: 11 },
  value: { color: 'var(--text)', fontFamily: "'JetBrains Mono', monospace", fontSize: 11 },
  actions: { display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 8 },
  denyBtn: { background: 'transparent', border: '1px solid var(--red)', color: 'var(--red)', borderRadius: 4, padding: '5px 14px', fontSize: 11, cursor: 'pointer' },
  allowBtn: { background: 'var(--green)', border: 'none', color: '#fff', borderRadius: 4, padding: '5px 14px', fontSize: 11, fontWeight: 500, cursor: 'pointer' },
};
