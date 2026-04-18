import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { login } from '../api/client';

/** Login renders the password-only authentication page. On successful login it
 *  navigates to the dashboard. The server sets the session cookie automatically. */
export default function Login() {
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const navigate = useNavigate();

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError('');
    try {
      await login(password);
      navigate('/');
    } catch {
      setError('Invalid password');
    }
  }

  return (
    <div style={styles.container}>
      <div style={styles.form}>
        <div style={styles.wordmark}>APPX</div>
        <div style={styles.tagline}>self-hosted agent platform</div>
        <form onSubmit={handleSubmit} style={styles.formInner}>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="Password"
            style={styles.input}
            autoFocus
          />
          {error && <p style={styles.error}>{error}</p>}
          <button
            type="submit"
            style={styles.button}
            data-btn="login"
          >
            Login
          </button>
        </form>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    minHeight: '100vh',
  },
  form: {
    width: 300,
  },
  wordmark: {
    fontFamily: "'DM Sans', sans-serif",
    fontSize: 20,
    fontWeight: 500,
    letterSpacing: '0.4em',
    color: 'var(--text)',
    marginBottom: 8,
  },
  tagline: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
    marginBottom: 40,
    letterSpacing: '0.02em',
  },
  formInner: {
    display: 'flex',
    flexDirection: 'column',
  },
  input: {
    width: '100%',
    padding: '10px 12px',
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    color: 'var(--text)',
    fontSize: 14,
    boxSizing: 'border-box',
  },
  error: {
    color: 'var(--red)',
    fontSize: 12,
    fontFamily: "'JetBrains Mono', monospace",
    margin: '8px 0 0',
  },
  button: {
    width: '100%',
    padding: '10px 0',
    marginTop: 12,
    background: 'transparent',
    color: 'var(--text)',
    border: '1px solid rgba(255,255,255,0.18)',
    borderRadius: 4,
    fontSize: 14,
    fontWeight: 500,
    cursor: 'pointer',
  },
};
