import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import type { Project } from '../api/client';
import { deleteProject } from '../api/client';

/** ProjectCard renders a single project as a card with app health status,
 *  assigned port, subdomain link, and delete control. The left border is
 *  color-coded: green when the app is running, muted when not started. */
export default function ProjectCard({
  project,
  onUpdate,
  index,
  baseDomain,
}: {
  project: Project;
  onUpdate: () => void;
  index: number;
  baseDomain: string;
}) {
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  const [loading, setLoading] = useState(false);

  const statusClr = project.appRunning ? 'var(--green)' : 'var(--muted)';
  const statusLabel = project.appRunning ? 'RUNNING' : 'NOT STARTED';

  const handleDelete = async () => {
    setLoading(true);
    try {
      await deleteProject(project.id);
    } catch (e) {
      alert(e instanceof Error ? e.message : 'Failed to delete');
    } finally {
      setLoading(false);
      setConfirming(false);
      onUpdate();
    }
  };

  /** Builds the app subdomain URL for this project. */
  const subdomainUrl = (() => {
    const proto = window.location.protocol;
    const port = window.location.port;
    const portSuffix = port ? `:${port}` : '';
    return `${proto}//${project.name}.${baseDomain}${portSuffix}/`;
  })();

  return (
    <div
      data-card="project"
      style={{
        ...styles.card,
        borderLeft: `2px solid ${statusClr}`,
        animation: 'fadeSlideIn 0.3s ease both',
        animationDelay: `${index * 50}ms`,
      }}
    >
      <div style={styles.header}>
        <span style={styles.name}>{project.name}</span>
        <span style={styles.statusWrap}>
          <span style={{ ...styles.dot, background: statusClr }} />
          <span style={{ ...styles.statusText, color: statusClr }}>
            {statusLabel}
          </span>
        </span>
      </div>

      <div style={styles.meta}>
        <span style={styles.port}>:{project.assignedPort}</span>
        {project.appRunning && (
          <a
            href={subdomainUrl}
            target="_blank"
            rel="noopener noreferrer"
            style={styles.subdomainLink}
          >
            {project.name}.{baseDomain}
          </a>
        )}
      </div>

      <div style={styles.actions}>
        <button
          data-btn="outline-green"
          style={styles.outlineGreenBtn}
          onClick={() => navigate(`/projects/${project.id}`)}
        >
          Open
        </button>

        <div style={styles.deleteArea}>
          {confirming ? (
            <span style={styles.confirmGroup}>
              <span style={styles.confirmText}>Delete all data?</span>
              <button
                data-btn="text-red"
                style={{ ...styles.textBtn, color: 'var(--muted)' }}
                onClick={handleDelete}
                disabled={loading}
              >
                Yes
              </button>
              <button
                data-btn="text"
                style={styles.textBtn}
                onClick={() => setConfirming(false)}
              >
                No
              </button>
            </span>
          ) : (
            <button
              data-btn="text-red"
              style={{ ...styles.textBtn, color: 'var(--muted)' }}
              onClick={() => setConfirming(true)}
              disabled={loading}
            >
              Delete
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  card: {
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderLeft: '2px solid var(--subtle)',
    borderRadius: 4,
    padding: '16px 18px',
    display: 'flex',
    flexDirection: 'column',
    gap: 10,
  },
  header: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  name: {
    fontSize: 14,
    fontWeight: 500,
    color: 'var(--text)',
  },
  statusWrap: {
    display: 'flex',
    alignItems: 'center',
    gap: 6,
  },
  dot: {
    width: 7,
    height: 7,
    borderRadius: '50%',
    flexShrink: 0,
  },
  statusText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    textTransform: 'uppercase' as const,
    letterSpacing: '0.07em',
  },
  meta: {
    display: 'flex',
    alignItems: 'center',
    gap: 12,
  },
  port: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
  },
  subdomainLink: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--cyan)',
    textDecoration: 'none',
  },
  actions: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    marginTop: 2,
  },
  outlineGreenBtn: {
    background: 'transparent',
    border: '1px solid rgba(61,220,132,0.35)',
    color: 'var(--green)',
    borderRadius: 4,
    padding: '4px 14px',
    fontSize: 12,
    fontWeight: 500,
    cursor: 'pointer',
  },
  textBtn: {
    background: 'transparent',
    border: 'none',
    color: 'var(--muted)',
    padding: '4px 8px',
    fontSize: 12,
    cursor: 'pointer',
  },
  deleteArea: {
    marginLeft: 'auto',
  },
  confirmGroup: {
    display: 'flex',
    alignItems: 'center',
    gap: 4,
  },
  confirmText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
    marginRight: 2,
  },
};
