import { useCallback, useEffect, useRef, useState } from 'react';
import {
  getPiSessionSettings,
  listPiModels,
  updatePiSessionSettings,
  type PiAgentModel,
  type PiSessionModelSettings,
  type ThinkingLevel,
} from '../../api/piAgent';
import { usePiSession } from '../../lib/pi-agent/useSession';
import Markdown from '../Markdown';
import PiToolCallCard from './PiToolCallCard';

function modelOptionValue(model: PiAgentModel): string {
  return JSON.stringify([model.provider, model.id]);
}

function parseModelOptionValue(value: string): { provider: string; modelId: string } | null {
  try {
    const parsed = JSON.parse(value) as unknown;
    if (!Array.isArray(parsed) || typeof parsed[0] !== 'string' || typeof parsed[1] !== 'string') {
      return null;
    }
    return { provider: parsed[0], modelId: parsed[1] };
  } catch {
    return null;
  }
}

function modelLabel(model: PiAgentModel): string {
  return model.name && model.name !== model.id
    ? `${model.name} - ${model.provider}/${model.id}`
    : `${model.provider}/${model.id}`;
}

const thinkingLabels: Record<ThinkingLevel, string> = {
  off: 'Off',
  minimal: 'Minimal',
  low: 'Low',
  medium: 'Medium',
  high: 'High',
  xhigh: 'X-high',
};

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
  const [models, setModels] = useState<PiAgentModel[]>([]);
  const [sessionSettings, setSessionSettings] = useState<PiSessionModelSettings | null>(null);
  const [settingsBusy, setSettingsBusy] = useState(false);
  const [settingsError, setSettingsError] = useState('');
  const scrollRef = useRef<HTMLDivElement>(null);
  const pinnedRef = useRef(true);
  const prevStatusRef = useRef(state.status);

  const isRunning = state.status === 'streaming' || state.status === 'starting';
  const controlsDisabled = settingsBusy || isRunning || Boolean(sessionSettings?.isStreaming);
  const modelValue = sessionSettings?.model ? modelOptionValue(sessionSettings.model) : '';
  const thinkingLevels: ThinkingLevel[] = sessionSettings?.availableThinkingLevels ?? ['off'];

  const loadModelSettings = useCallback(async () => {
    try {
      const [modelList, settings] = await Promise.all([
        listPiModels(projectId),
        getPiSessionSettings(projectId, sessionId),
      ]);
      setModels(modelList.models);
      setSessionSettings(settings);
      setSettingsError('');
    } catch (err) {
      setSettingsError(err instanceof Error ? err.message : String(err));
    }
  }, [projectId, sessionId]);

  useEffect(() => {
    void loadModelSettings();
  }, [loadModelSettings]);

  useEffect(() => {
    if (state.status === 'idle' && sessionSettings?.isStreaming) {
      void loadModelSettings();
    }
  }, [state.status, sessionSettings?.isStreaming, loadModelSettings]);

  useEffect(() => {
    if (!pinnedRef.current) return;
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [state.messages]);

  useEffect(() => {
    if (prevStatusRef.current !== 'idle' && state.status === 'idle') {
      onTurnComplete();
      void loadModelSettings();
    }
    prevStatusRef.current = state.status;
  }, [state.status, onTurnComplete, loadModelSettings]);

  const updateModelSettings = async (
    body: { provider?: string; modelId?: string; thinkingLevel?: ThinkingLevel },
  ) => {
    setSettingsBusy(true);
    try {
      const next = await updatePiSessionSettings(projectId, sessionId, body);
      setSessionSettings(next);
      setSettingsError('');
    } catch (err) {
      setSettingsError(err instanceof Error ? err.message : String(err));
    } finally {
      setSettingsBusy(false);
    }
  };

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
        <div style={styles.headerStatusBlock}>
          <span style={styles.headerTitle}>PI AGENT</span>
          <span style={isRunning ? styles.headerStatusActive : styles.headerStatus}>
            {!state.connected ? 'connecting' : isRunning ? state.status : 'idle'}
          </span>
          {settingsError && <span style={styles.settingsError} title={settingsError}>model settings unavailable</span>}
        </div>
        <div style={styles.modelControls} aria-label="Agent model settings">
          <label style={styles.modelLabel}>
            <span style={styles.controlLabel}>Model</span>
            <select
              style={styles.modelSelect}
              value={modelValue}
              onChange={(e) => {
                const next = parseModelOptionValue(e.target.value);
                if (next) void updateModelSettings(next);
              }}
              disabled={controlsDisabled || models.length === 0}
              title={sessionSettings?.model ? modelLabel(sessionSettings.model) : 'No model selected'}
            >
              {!sessionSettings?.model && <option value="">No model</option>}
              {models.map((model) => (
                <option
                  key={`${model.provider}/${model.id}`}
                  value={modelOptionValue(model)}
                  disabled={!model.available}
                >
                  {model.available ? modelLabel(model) : `${modelLabel(model)} - unavailable`}
                </option>
              ))}
            </select>
          </label>
          <label style={styles.thinkingLabel}>
            <span style={styles.controlLabel}>Think</span>
            <select
              style={styles.thinkingSelect}
              value={sessionSettings?.thinkingLevel ?? 'off'}
              onChange={(e) => void updateModelSettings({ thinkingLevel: e.target.value as ThinkingLevel })}
              disabled={controlsDisabled || !sessionSettings || thinkingLevels.length <= 1}
              title={
                sessionSettings?.supportsThinking
                  ? 'Thinking level for the next agent turn'
                  : 'Selected model does not support thinking'
              }
            >
              {thinkingLevels.map((level) => (
                <option key={level} value={level}>{thinkingLabels[level]}</option>
              ))}
            </select>
          </label>
        </div>
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
    display: 'grid',
    gridTemplateColumns: 'minmax(116px, 170px) minmax(0, 1fr)',
    alignItems: 'end',
    gap: 16,
    padding: '10px 20px',
    borderBottom: '1px solid var(--border)',
    background: 'var(--surface)',
  },
  headerStatusBlock: {
    display: 'grid',
    gap: 3,
    minWidth: 0,
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
  headerStatusActive: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--cyan)',
  },
  settingsError: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 10,
    color: 'var(--red)',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  },
  modelControls: {
    display: 'grid',
    gridTemplateColumns: 'minmax(0, 1fr) 116px',
    gap: 8,
    alignItems: 'end',
    minWidth: 0,
  },
  modelLabel: {
    display: 'grid',
    gap: 3,
    minWidth: 0,
  },
  thinkingLabel: {
    display: 'grid',
    gap: 3,
    minWidth: 0,
  },
  controlLabel: {
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 9,
    letterSpacing: '0.1em',
    color: 'var(--muted)',
    textTransform: 'uppercase',
  },
  modelSelect: {
    minWidth: 0,
    height: 32,
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    color: 'var(--text)',
    fontSize: 12,
    padding: '5px 8px',
    outline: 'none',
  },
  thinkingSelect: {
    minWidth: 0,
    height: 32,
    background: 'var(--bg)',
    border: '1px solid var(--border)',
    borderRadius: 4,
    color: 'var(--text)',
    fontSize: 12,
    padding: '5px 8px',
    outline: 'none',
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
