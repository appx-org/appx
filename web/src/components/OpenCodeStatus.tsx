import { useState, useEffect, useRef } from 'react';
import { getOpenCodeHealth } from '../api/client';

const POLL_INTERVAL = 10000;

/** OpenCodeStatus renders a small health indicator for the OpenCode server.
 *  Polls every 10 seconds. Shows a colored dot with label: green when healthy,
 *  red when down, gray on initial load. */
export default function OpenCodeStatus() {
  const [healthy, setHealthy] = useState<boolean | null>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    const check = () => {
      getOpenCodeHealth()
        .then(res => setHealthy(res.healthy))
        .catch(() => setHealthy(false));
    };

    check();
    pollRef.current = setInterval(check, POLL_INTERVAL);

    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, []);

  const color = healthy === null ? 'var(--muted)' : healthy ? 'var(--green)' : 'var(--red)';

  return (
    <span style={styles.wrapper}>
      <span style={{ ...styles.dot, background: color }} />
      <span style={{ ...styles.label, color }}>OPENCODE</span>
    </span>
  );
}

const styles: Record<string, React.CSSProperties> = {
  wrapper: { display: 'flex', alignItems: 'center', gap: 5 },
  dot: { width: 6, height: 6, borderRadius: '50%', flexShrink: 0 },
  label: { fontFamily: "'JetBrains Mono', monospace", fontSize: 10, letterSpacing: '0.07em' },
};
