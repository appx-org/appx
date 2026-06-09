import { useEffect, useRef, useState, useCallback } from 'react';
import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebLinksAddon } from '@xterm/addon-web-links';
import '@xterm/xterm/css/xterm.css';

const MAX_RETRIES = 5;
const BASE_DELAY = 1000;
const MAX_DELAY = 8000;
const INTENTIONAL_CODES = [1000, 4004];

interface TerminalProps {
  /** cwd is the working directory for the shell. If omitted, the server's
   *  working directory is used. Pass project.projectDir for project terminals. */
  cwd?: string;
}

/** Terminal renders an xterm.js terminal connected to a local PTY via appx's
 *  /api/shell endpoints (creack/pty). No agent-runtime dependency. Handles
 *  auto-reconnect with exponential backoff, resize,
 *  ring buffer replay on reconnect, and mobile copy/paste. */
export default function Terminal({ cwd }: TerminalProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<XTerm | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectRef = useRef<() => void>(() => {});

  const [connected, setConnected] = useState(false);
  const [reconnecting, setReconnecting] = useState(false);
  const [failed, setFailed] = useState(false);
  const [initializing, setInitializing] = useState(true);
  const [hasSelection, setHasSelection] = useState(false);
  const [isMobile] = useState(() => 'ontouchstart' in window);

  useEffect(() => {
    if (!containerRef.current) return;

    let intentionalClose = false;
    let retries = 0;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    let shellId: string | null = null;

    const term = new XTerm({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: "'JetBrains Mono', monospace",
      theme: {
        background: '#060c0e', foreground: '#e2f4f8', cursor: '#00e5ff',
        selectionBackground: 'rgba(0, 229, 255, 0.25)',
        black: '#0d1214', red: '#ff6b6b', green: '#3ddc84', yellow: '#f5c518',
        blue: '#0369a1', magenta: '#c084fc', cyan: '#00e5ff', white: '#e2f4f8',
        brightBlack: '#1a2c30', brightRed: '#ff8a8a', brightGreen: '#5ce89d',
        brightYellow: '#f7d24a', brightBlue: '#0284c7', brightMagenta: '#d4a5fd',
        brightCyan: '#33ecff', brightWhite: '#ffffff',
      },
    });

    const fitAddon = new FitAddon();
    const webLinksAddon = new WebLinksAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(webLinksAddon);
    termRef.current = term;
    term.open(containerRef.current);
    fitAddon.fit();
    term.focus();

    async function createShell(): Promise<string> {
      const res = await fetch('/api/shell', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ cwd: cwd ?? '' }),
      });
      if (!res.ok) throw new Error(`shell create failed: ${res.status}`);
      const data = await res.json();
      return data.id;
    }

    async function resizeShell(id: string, cols: number, rows: number) {
      try {
        await fetch(`/api/shell/${id}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ cols, rows }),
        });
      } catch {
        // Non-fatal — terminal still usable without correct dimensions.
      }
    }

    function connectWs(id: string) {
      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const url = `${proto}//${window.location.host}/api/shell/${id}/connect`;
      const ws = new WebSocket(url);
      ws.binaryType = 'arraybuffer';
      wsRef.current = ws;

      ws.onopen = () => {
        retries = 0;
        setConnected(true);
        setReconnecting(false);
        setFailed(false);
        fitAddon.fit();
        term.focus();
        resizeShell(id, term.cols, term.rows);
      };

      ws.onmessage = (ev) => {
        if (ev.data instanceof ArrayBuffer) {
          term.write(new Uint8Array(ev.data));
        } else {
          term.write(ev.data);
        }
      };

      ws.onclose = (ev) => {
        setConnected(false);
        if (intentionalClose || INTENTIONAL_CODES.includes(ev.code)) return;
        if (retries >= MAX_RETRIES) { setReconnecting(false); setFailed(true); return; }
        const delay = Math.min(BASE_DELAY * Math.pow(2, retries), MAX_DELAY);
        retries += 1;
        setReconnecting(true);
        reconnectTimer = setTimeout(() => { if (shellId) connectWs(shellId); }, delay);
      };

      ws.onerror = () => {
        // onclose fires after onerror — reconnect logic lives there.
      };
    }

    (async () => {
      try {
        shellId = await createShell();
        setInitializing(false);
        connectWs(shellId);
      } catch {
        setInitializing(false);
        setFailed(true);
      }
    })();

    reconnectRef.current = () => {
      retries = 0;
      setFailed(false);
      setReconnecting(true);
      if (shellId) connectWs(shellId);
    };

    const dataDisposable = term.onData((data) => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(data);
      }
    });

    const selectionDisposable = term.onSelectionChange(() => {
      setHasSelection(term.hasSelection());
    });

    const observer = new ResizeObserver(() => {
      fitAddon.fit();
      if (shellId) resizeShell(shellId, term.cols, term.rows);
    });
    observer.observe(containerRef.current);

    return () => {
      intentionalClose = true;
      dataDisposable.dispose();
      selectionDisposable.dispose();
      observer.disconnect();
      if (reconnectTimer) clearTimeout(reconnectTimer);
      wsRef.current?.close();
      term.dispose();
      termRef.current = null;
    };
  }, [cwd]);

  const handleManualReconnect = useCallback(() => { reconnectRef.current(); }, []);

  const handleCopy = useCallback(async () => {
    const term = termRef.current;
    if (!term || !term.hasSelection()) return;
    try { await navigator.clipboard.writeText(term.getSelection()); } catch { /* clipboard unavailable */ }
  }, []);

  const handlePaste = useCallback(async () => {
    try {
      const text = await navigator.clipboard.readText();
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN && text) {
        ws.send(text);
      }
    } catch { /* clipboard unavailable */ }
  }, []);

  const handleFocus = useCallback(() => { termRef.current?.focus(); }, []);

  return (
    <div style={styles.wrapper} onClick={handleFocus}>
      <div ref={containerRef} style={styles.terminal} />
      {initializing && <div style={styles.overlay}><span style={styles.overlayText}>Connecting to terminal...</span></div>}
      {reconnecting && <div style={styles.overlay}><span style={styles.overlayText}>Reconnecting...</span></div>}
      {failed && (
        <div style={styles.overlay}>
          <span style={styles.overlayText}>Connection lost</span>
          <button style={styles.reconnectBtn} onClick={handleManualReconnect}>Reconnect</button>
        </div>
      )}
      {isMobile && connected && (
        <div style={styles.mobileButtons}>
          {hasSelection && <button style={styles.mobileBtn} onClick={handleCopy}>Copy</button>}
          <button style={styles.mobileBtn} onClick={handlePaste}>Paste</button>
        </div>
      )}
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  wrapper: { position: 'relative', flex: 1, minHeight: 0, overflow: 'hidden' },
  terminal: { width: '100%', height: '100%' },
  overlay: { position: 'absolute', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(6,12,14,0.85)', display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 16, zIndex: 10 },
  overlayText: { fontFamily: "'JetBrains Mono', monospace", fontSize: 13, color: 'var(--muted)', letterSpacing: '0.04em' },
  reconnectBtn: { background: 'transparent', border: '1px solid rgba(61,220,132,0.35)', color: 'var(--green)', borderRadius: 4, padding: '6px 18px', fontSize: 12, fontWeight: 500, cursor: 'pointer' },
  mobileButtons: { position: 'absolute', bottom: 12, right: 12, display: 'flex', gap: 8, zIndex: 5 },
  mobileBtn: { background: 'var(--surface)', border: '1px solid var(--border)', color: 'var(--text)', borderRadius: 4, padding: '6px 14px', fontSize: 12, fontWeight: 500, cursor: 'pointer' },
};
