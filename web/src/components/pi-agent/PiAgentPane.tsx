import { useCallback, useState } from 'react';
import PiChatPanel from './PiChatPanel';
import PiSessionList from './PiSessionList';

export default function PiAgentPane({ projectId }: { projectId: string }) {
  const [activeSessionId, setActiveSessionId] = useState<string | null>(null);
  const [refreshTick, setRefreshTick] = useState(0);
  const refreshSessions = useCallback(() => setRefreshTick((value) => value + 1), []);

  return (
    <div style={styles.agentLayout}>
      <PiSessionList
        projectId={projectId}
        activeSessionId={activeSessionId}
        refreshTick={refreshTick}
        onSelectSession={setActiveSessionId}
      />
      {activeSessionId ? (
        <PiChatPanel
          projectId={projectId}
          sessionId={activeSessionId}
          onTurnComplete={refreshSessions}
        />
      ) : (
        <div style={styles.centered}>
          <span style={styles.statusLabel}>Select or create a session</span>
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  agentLayout: { flex: 1, display: 'flex', minHeight: 0 },
  centered: { flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 16 },
  statusLabel: { fontFamily: "'JetBrains Mono', monospace", fontSize: 13, color: 'var(--muted)', letterSpacing: '0.04em' },
};
