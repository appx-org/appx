import { useEffect, useRef, useState } from 'react';
import { usePiSession } from '../../lib/pi-agent/useSession';
import Markdown from '../Markdown';
import PiToolCallCard from './PiToolCallCard';

export default function PiChatPanel({
  projectId,
  sessionId,
  onTurnComplete,
}: {
  projectId: string;
  sessionId: string;
  onTurnComplete: () => void;
}) {
  const { state, sendPrompt, abort } = usePiSession(projectId, sessionId);
  const [input, setInput] = useState('');
  const [sending, setSending] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const pinnedRef = useRef(true);
  const prevStatusRef = useRef(state.status);

  const isRunning = state.status === 'streaming' || state.status === 'starting';

  useEffect(() => {
    if (!pinnedRef.current) return;
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [state.messages]);

  useEffect(() => {
    if (prevStatusRef.current !== 'idle' && state.status === 'idle') {
      onTurnComplete();
    }
    prevStatusRef.current = state.status;
  }, [state.status, onTurnComplete]);

  const handleSend = async () => {
    const text = input.trim();
    if (!text || sending) return;
    setInput('');
    setSending(true);
    try {
      await sendPrompt(text);
    } catch (err) {
      console.error('Failed to send Pi prompt:', err);
    } finally {
      setSending(false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      void handleSend();
    }
  };

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <span style={styles.headerTitle}>PI AGENT</span>
        <span style={styles.headerStatus}>
          {!state.connected ? 'connecting' : isRunning ? state.status : 'idle'}
        </span>
      </div>

      <div
        ref={scrollRef}
        style={styles.messages}
        onScroll={() => {
          const el = scrollRef.current;
          if (!el) return;
          pinnedRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        }}
      >
        {state.messages.length === 0 && (
          <div style={styles.empty}>
            <span style={styles.emptyText}>Send a prompt to start</span>
          </div>
        )}
        {state.messages.map((message, index) => (
          <div
            key={`${message.timestamp}-${index}`}
            style={message.role === 'user' ? styles.userMsg : styles.assistantMsg}
          >
            <span style={styles.msgRole}>{message.role === 'user' ? 'YOU' : 'AGENT'}</span>
            {message.parts.length === 0 && message.streaming && <Markdown text="..." />}
            {message.parts.map((part, partIndex) =>
              part.type === 'text' ? (
                part.text ? <Markdown key={partIndex} text={part.text} /> : null
              ) : (
                <PiToolCallCard key={part.id || partIndex} tool={part} />
              ),
            )}
          </div>
        ))}
      </div>

      {state.error && <div style={styles.errorBanner}>{state.error}</div>}

      <div style={styles.inputBar}>
        <textarea
          style={styles.input}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder={isRunning ? 'Agent is working...' : 'Send a message...'}
          rows={1}
          disabled={sending}
        />
        {isRunning ? (
          <button style={styles.abortBtn} onClick={abort}>
            Stop
          </button>
        ) : (
          <button style={styles.sendBtn} onClick={handleSend} disabled={sending || !input.trim()}>
            {sending ? '...' : 'Send'}
          </button>
        )}
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  container: {
    flex: 1,
    display: 'flex',
    flexDirection: 'column',
    minHeight: 0,
    overflow: 'hidden',
  },
  header: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: '10px 20px',
    borderBottom: '1px solid var(--border)',
    background: 'var(--surface)',
  },
  headerTitle: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    letterSpacing: '0.1em',
    color: 'var(--muted)',
  },
  headerStatus: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--green)',
  },
  messages: {
    flex: 1,
    overflowY: 'auto',
    padding: '16px 20px',
    display: 'flex',
    flexDirection: 'column',
    gap: 12,
  },
  empty: {
    flex: 1,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
  },
  emptyText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
    color: 'var(--muted)',
  },
  userMsg: {
    padding: '10px 14px',
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    alignSelf: 'flex-end',
    maxWidth: '80%',
    overflowWrap: 'anywhere',
  },
  assistantMsg: {
    padding: '10px 14px',
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    alignSelf: 'flex-start',
    maxWidth: '90%',
    display: 'flex',
    flexDirection: 'column',
    gap: 8,
    overflowWrap: 'anywhere',
  },
  msgRole: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 9,
    letterSpacing: '0.1em',
    color: 'var(--muted)',
    display: 'block',
    marginBottom: 4,
  },
  errorBanner: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--red)',
    padding: '6px 20px',
    background: 'var(--red-dim)',
    overflowWrap: 'anywhere',
  },
  inputBar: {
    display: 'flex',
    gap: 8,
    padding: '12px 20px',
    borderTop: '1px solid var(--border)',
    background: 'var(--surface)',
  },
  input: {
    flex: 1,
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '10px 12px',
    fontSize: 13,
    color: 'var(--text)',
    outline: 'none',
    resize: 'none',
    fontFamily: "'DM Sans', sans-serif",
    lineHeight: 1.4,
  },
  sendBtn: {
    background: 'var(--blue)',
    border: 'none',
    color: '#fff',
    borderRadius: 4,
    padding: '10px 18px',
    fontSize: 13,
    fontWeight: 500,
    cursor: 'pointer',
    alignSelf: 'flex-end',
  },
  abortBtn: {
    background: 'var(--red)',
    border: 'none',
    color: '#fff',
    borderRadius: 4,
    padding: '10px 18px',
    fontSize: 13,
    fontWeight: 500,
    cursor: 'pointer',
    alignSelf: 'flex-end',
  },
};
