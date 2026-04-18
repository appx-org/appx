import { useState, useRef, useEffect, useMemo } from 'react';
import type {
  Message,
  UserMessage,
  AssistantMessage,
  Part,
  TextPart,
  ToolPart,
  ReasoningPart,
} from '@opencode-ai/sdk/v2/client';
import { useSession } from '../../lib/agent-react/useSession';
import { usePermissions } from '../../lib/agent-react/usePermissions';
import { getClient } from '../../api/opencode';
import Markdown from '../Markdown';
import ToolCallCard from '../ToolCallCard';
import PermissionDock from '../PermissionDock';
import QuestionDock from '../QuestionDock';
import EgressRequestDock from '../EgressRequestDock';
import StatusBar from '../StatusBar';

interface Turn {
  user: UserMessage;
  assistants: AssistantMessage[];
}

function groupIntoTurns(messages: Message[]): Turn[] {
  const users = messages.filter((m): m is UserMessage => m.role === 'user');
  return users.map((user) => ({
    user,
    assistants: messages.filter(
      (m): m is AssistantMessage =>
        m.role === 'assistant' && m.parentID === user.id,
    ),
  }));
}

function renderPart(part: Part) {
  switch (part.type) {
    case 'text':
      return <Markdown key={part.id} text={(part as TextPart).text} />;
    case 'tool':
      return <ToolCallCard key={part.id} part={part as ToolPart} />;
    case 'reasoning':
      return (
        <details key={part.id} style={partStyles.reasoning}>
          <summary style={partStyles.reasoningSummary}>Thinking...</summary>
          <pre style={partStyles.reasoningText}>
            {(part as ReasoningPart).text}
          </pre>
        </details>
      );
    default:
      return null;
  }
}

/** ChatPanel renders the full agent conversation for a session. Uses the
 *  headless core hooks for SSE streaming, state management, and actions. */
export default function ChatPanel({
  sessionId,
  projectDir,
}: {
  sessionId: string;
  projectDir: string;
}) {
  const { state, connectionStatus, sendPrompt, abort } = useSession(
    sessionId,
    projectDir,
  );
  const client = useMemo(
    () => (projectDir ? getClient(projectDir) : null),
    [projectDir],
  );
  const { respondPermission, answerQuestion, rejectQuestion } =
    usePermissions(client);

  const [input, setInput] = useState('');
  const [sending, setSending] = useState(false);
  const bottomRef = useRef<HTMLDivElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const pinnedRef = useRef(true);

  const turns = useMemo(() => groupIntoTurns(state.messages), [state.messages]);
  const isRunning = state.status === 'running';

  // Auto-scroll to bottom only when the user is already pinned there.
  useEffect(() => {
    if (pinnedRef.current) {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
    }
  }, [state.messages, state.parts]);

  // Re-pin when the user sends a new message so the response scrolls into view.
  // Also re-pin when a previously-running agent becomes idle (turn complete).
  const prevRunningRef = useRef(false);
  useEffect(() => {
    if (!prevRunningRef.current && isRunning) {
      pinnedRef.current = true;
    }
    prevRunningRef.current = isRunning;
  }, [isRunning]);

  const handleSend = async () => {
    const text = input.trim();
    if (!text || sending || isRunning) return;
    setInput('');
    setSending(true);
    try {
      await sendPrompt(text);
    } catch (e) {
      console.error('Failed to send prompt:', e);
    } finally {
      setSending(false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  return (
    <div style={styles.container}>
      {/* Messages */}
      <div
        ref={scrollRef}
        style={styles.messages}
        onScroll={() => {
          const el = scrollRef.current;
          if (!el) return;
          // Consider "pinned" when within 80px of the bottom.
          pinnedRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        }}
      >
        {turns.length === 0 && (
          <div style={styles.empty}>
            <span style={styles.emptyText}>Send a prompt to start</span>
          </div>
        )}
        {turns.map((turn) => (
          <div key={turn.user.id} style={styles.turn}>
            {/* User message */}
            <div style={styles.userMsg}>
              <span style={styles.msgRole}>YOU</span>
              {(state.parts[turn.user.id] ?? []).map(renderPart)}
              {!(state.parts[turn.user.id]?.length) && (
                <span style={styles.userText}>(prompt)</span>
              )}
            </div>
            {/* Assistant messages */}
            {turn.assistants.map((asst) => (
              <div key={asst.id} style={styles.assistantMsg}>
                <span style={styles.msgRole}>AGENT</span>
                {(state.parts[asst.id] ?? []).map(renderPart)}
                {asst.error && (
                  <div style={styles.msgError}>
                    {JSON.stringify(asst.error)}
                  </div>
                )}
              </div>
            ))}
          </div>
        ))}
        <div ref={bottomRef} />
      </div>

      {/* Docks */}
      {state.pendingPermissions.map((perm) => (
        <PermissionDock
          key={perm.id}
          permission={perm}
          onRespond={respondPermission}
        />
      ))}
      {state.pendingQuestions.map((q) => (
        <QuestionDock
          key={q.id}
          question={q}
          onAnswer={answerQuestion}
          onReject={rejectQuestion}
        />
      ))}
      <EgressRequestDock />

      {/* Error banner */}
      {state.error && <div style={styles.errorBanner}>{state.error}</div>}

      {/* Input */}
      <div style={styles.inputBar}>
        <textarea
          style={styles.input}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder={isRunning ? 'Agent is working...' : 'Send a message...'}
          rows={1}
          disabled={sending || isRunning}
        />
        {isRunning ? (
          <button style={styles.abortBtn} onClick={abort}>
            Stop
          </button>
        ) : (
          <button
            style={styles.sendBtn}
            onClick={handleSend}
            disabled={sending || !input.trim()}
          >
            {sending ? '...' : 'Send'}
          </button>
        )}
      </div>

      {/* Status bar */}
      <StatusBar
        agentStatus={state.status}
        connectionStatus={connectionStatus}
      />
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
  messages: {
    flex: 1,
    overflowY: 'auto',
    padding: '16px 20px',
    display: 'flex',
    flexDirection: 'column',
    gap: 16,
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
  turn: {
    display: 'flex',
    flexDirection: 'column',
    gap: 8,
  },
  userMsg: {
    padding: '10px 14px',
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    alignSelf: 'flex-end',
    maxWidth: '80%',
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
    gap: 6,
  },
  msgRole: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 9,
    letterSpacing: '0.1em',
    color: 'var(--muted)',
    display: 'block',
    marginBottom: 4,
  },
  userText: {
    fontSize: 13,
    color: 'var(--text)',
    whiteSpace: 'pre-wrap' as const,
  },
  msgError: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--red)',
    padding: '6px 8px',
    background: 'var(--red-dim)',
    borderRadius: 3,
  },
  errorBanner: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--red)',
    padding: '6px 20px',
    background: 'var(--red-dim)',
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
    resize: 'none' as const,
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

const partStyles: Record<string, React.CSSProperties> = {
  reasoning: {
    border: '1px solid var(--border)',
    borderRadius: 4,
    padding: '4px 8px',
    margin: '4px 0',
  },
  reasoningSummary: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
    cursor: 'pointer',
  },
  reasoningText: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 11,
    color: 'var(--muted)',
    margin: '6px 0 0',
    whiteSpace: 'pre-wrap' as const,
    lineHeight: 1.4,
  },
};
