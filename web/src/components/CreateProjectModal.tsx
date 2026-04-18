import { useState } from 'react';
import { createProject } from '../api/client';

/** CreateProjectModal renders a modal to create a new project. Only a name is
 *  required -- the backend auto-assigns a port from the 10000-10999 range. */
export default function CreateProjectModal({
  onCreated,
  onClose,
}: {
  onCreated: () => void;
  onClose: () => void;
}) {
  const [name, setName] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const nameValid = /^[a-z][a-z0-9-]{0,61}[a-z0-9]$/.test(name) || (name.length === 2 && /^[a-z][a-z0-9]$/.test(name));

  const handleSubmit = async (e: { preventDefault: () => void }) => {
    e.preventDefault();
    if (!nameValid) return;

    setSubmitting(true);
    setError('');
    try {
      await createProject(name);
      onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create project');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div data-overlay style={styles.overlay} onClick={onClose}>
      <div style={styles.modal} onClick={e => e.stopPropagation()}>
        <h2 style={styles.title}>New Project</h2>
        <form onSubmit={handleSubmit}>
          <label style={styles.label}>
            <span style={styles.labelText}>NAME</span>
            <input
              style={styles.input}
              type="text"
              value={name}
              onChange={e => setName(e.target.value.toLowerCase())}
              placeholder="my-app"
              autoFocus
            />
            {name && !nameValid && (
              <span style={styles.hint}>Lowercase letters, numbers, hyphens. 2-63 chars.</span>
            )}
          </label>
          <p style={styles.portNote}>A unique port will be assigned automatically.</p>
          {error && <div style={styles.error}>{error}</div>}
          <div style={styles.actions}>
            <button type="button" style={styles.cancelBtn} onClick={onClose}>Cancel</button>
            <button type="submit" style={styles.createBtn} disabled={!nameValid || submitting}>
              {submitting ? 'Creating...' : 'Create'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  overlay: {
    position: 'fixed',
    inset: 0,
    background: 'rgba(0,0,0,0.75)',
    backdropFilter: 'blur(4px)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    zIndex: 100,
  },
  modal: {
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderRadius: 6,
    padding: '24px 28px',
    width: 360,
    maxWidth: '90vw',
  },
  title: { margin: '0 0 22px', fontSize: 15, fontWeight: 500, color: 'var(--text)' },
  label: { display: 'flex', flexDirection: 'column', gap: 7, marginBottom: 10 },
  labelText: { fontFamily: "'JetBrains Mono', monospace", fontSize: 10, letterSpacing: '0.1em', color: 'var(--muted)' },
  input: { background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 4, padding: '8px 12px', fontSize: 13, color: 'var(--text)', outline: 'none', width: '100%' },
  hint: { fontFamily: "'JetBrains Mono', monospace", fontSize: 10, color: 'var(--red)' },
  portNote: { fontFamily: "'JetBrains Mono', monospace", fontSize: 10, color: 'var(--muted)', margin: '0 0 18px', lineHeight: 1.5 },
  error: { fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: 'var(--red)', marginBottom: 14, padding: '7px 10px', background: 'var(--red-dim)', border: '1px solid rgba(255,107,107,0.2)', borderRadius: 4 },
  actions: { display: 'flex', justifyContent: 'flex-end', alignItems: 'center', gap: 4, marginTop: 22 },
  cancelBtn: { background: 'transparent', border: 'none', color: 'var(--muted)', padding: '7px 14px', fontSize: 13, cursor: 'pointer' },
  createBtn: { background: 'var(--blue)', border: 'none', color: '#fff', borderRadius: 4, padding: '7px 20px', fontSize: 13, fontWeight: 500, cursor: 'pointer' },
};
