import { useState, useEffect, useCallback, useRef } from 'react';
import { useNavigate } from 'react-router-dom';
import { getProjects, getServerConfig, logout, type Project } from '../api/client';
import ProjectCard from '../components/ProjectCard';
import CreateProjectModal from '../components/CreateProjectModal';

const POLL_INTERVAL = 10000;

/** Dashboard is the main authenticated page. Fetches and displays projects
 *  with app health status. Polls every 10s to detect app start/stop.
 *  Shows the Pi agent runtime in the header. */
export default function Dashboard() {
  const navigate = useNavigate();
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [showCreate, setShowCreate] = useState(false);
  const [baseDomain, setBaseDomain] = useState('localhost');
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchProjects = useCallback(() => {
    getProjects()
      .then(setProjects)
      .catch(() => { window.location.href = '/login'; })
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    getServerConfig()
      .then((cfg) => {
        setBaseDomain(cfg.baseDomain || 'localhost');
      })
      .catch(() => {});
    fetchProjects();
    pollRef.current = setInterval(fetchProjects, POLL_INTERVAL);
    return () => { if (pollRef.current) clearInterval(pollRef.current); };
  }, [fetchProjects]);

  return (
    <div style={styles.container}>
      <header style={styles.header}>
        <div style={styles.headerLeft}>
          <span style={styles.wordmark}>APPX</span>
          <span style={styles.agentStatus} aria-label="Pi agent runtime">
            <span style={styles.agentDot} />
            PI
          </span>
        </div>
        <div style={styles.headerActions}>
          <button data-btn="new-project" style={styles.newProjectBtn} onClick={() => setShowCreate(true)}>
            + New Project
          </button>
          <span style={styles.separator}>|</span>
          <button data-btn="text-nav" style={styles.navBtn} onClick={() => navigate('/settings')}>Settings</button>
          <button data-btn="text-nav" style={styles.navBtn} onClick={() => navigate('/egress')}>Egress</button>
          <button data-btn="text-nav" style={styles.navBtn} onClick={() => navigate('/shell')}>Shell</button>
          <button data-btn="text-nav" style={{ ...styles.navBtn, color: 'var(--muted)' }}
            onClick={() => logout().then(() => { window.location.href = '/login'; })}>
            Logout
          </button>
        </div>
      </header>

      <main style={styles.main}>
        {loading ? (
          <div style={styles.grid}>
            {[0, 1, 2].map(i => <div key={i} style={styles.skeleton} />)}
          </div>
        ) : projects.length === 0 ? (
          <div style={styles.empty}>
            <p style={styles.emptyTitle}>No projects</p>
            <p style={styles.emptyHint}>Click + New Project to get started</p>
          </div>
        ) : (
          <div style={styles.grid}>
            {projects.map((p, i) => (
              <ProjectCard key={p.id} project={p} onUpdate={fetchProjects} index={i} baseDomain={baseDomain} />
            ))}
          </div>
        )}
      </main>

      {showCreate && (
        <CreateProjectModal
          onCreated={() => { setShowCreate(false); fetchProjects(); }}
          onClose={() => setShowCreate(false)}
        />
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: { minHeight: '100vh' },
  header: { borderBottom: '1px solid var(--border)', padding: '14px 24px', display: 'flex', alignItems: 'center', justifyContent: 'space-between' },
  headerLeft: { display: 'flex', alignItems: 'center', gap: 16 },
  wordmark: { fontFamily: "'DM Sans', sans-serif", fontSize: 14, fontWeight: 500, letterSpacing: '0.35em', color: 'var(--text)' },
  agentStatus: { display: 'inline-flex', alignItems: 'center', gap: 8, fontFamily: "'JetBrains Mono', monospace", fontSize: 12, letterSpacing: '0.08em', color: 'var(--green)' },
  agentDot: { width: 7, height: 7, borderRadius: '50%', background: 'var(--green)', boxShadow: '0 0 10px rgba(61, 220, 132, 0.45)' },
  headerActions: { display: 'flex', alignItems: 'center', gap: 4 },
  newProjectBtn: { background: 'transparent', border: '1px solid var(--border)', color: 'var(--text)', borderRadius: 4, padding: '5px 14px', fontSize: 13, cursor: 'pointer' },
  separator: { color: 'var(--subtle)', fontSize: 14, padding: '0 6px', userSelect: 'none' as const },
  navBtn: { background: 'transparent', border: 'none', color: 'var(--muted)', padding: '5px 10px', fontSize: 13, cursor: 'pointer' },
  main: { padding: '28px 24px', maxWidth: 1080, margin: '0 auto' },
  grid: { display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: 12 },
  skeleton: { background: 'var(--surface)', borderRadius: 4, height: 120, border: '1px solid var(--border)' },
  empty: { textAlign: 'center' as const, padding: '80px 0' },
  emptyTitle: { fontFamily: "'DM Sans', sans-serif", fontSize: 20, color: 'var(--muted)', margin: '0 0 10px', fontWeight: 400 },
  emptyHint: { fontFamily: "'JetBrains Mono', monospace", fontSize: 12, color: 'var(--subtle)', margin: 0 },
};
