import { useMemo, useRef, useEffect } from 'react';
import { marked } from 'marked';
import DOMPurify from 'dompurify';

interface MarkdownProps {
  text: string;
}

/** Markdown renders a markdown string as sanitized HTML with copy buttons on code blocks. */
export default function Markdown({ text }: MarkdownProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  const html = useMemo(() => {
    if (!text) return '';
    const raw = marked.parse(text, { async: false }) as string;
    return DOMPurify.sanitize(raw);
  }, [text]);

  // Add copy buttons to code blocks after render
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const pres = container.querySelectorAll('pre');
    pres.forEach((pre) => {
      if (pre.querySelector('[data-copy-btn]')) return;
      const btn = document.createElement('button');
      btn.setAttribute('data-copy-btn', '');
      btn.textContent = 'Copy';
      Object.assign(btn.style, copyBtnStyle);
      btn.addEventListener('click', () => {
        const code = pre.querySelector('code');
        const text = code?.textContent ?? pre.textContent ?? '';
        navigator.clipboard.writeText(text).then(() => {
          btn.textContent = 'Copied!';
          setTimeout(() => {
            btn.textContent = 'Copy';
          }, 2000);
        });
      });
      pre.style.position = 'relative';
      pre.appendChild(btn);
    });
  }, [html]);

  return (
    <div
      ref={containerRef}
      className="appx-markdown"
      style={styles.container}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

const copyBtnStyle: Partial<CSSStyleDeclaration> = {
  position: 'absolute',
  top: '6px',
  right: '6px',
  background: 'var(--surface)',
  border: '1px solid var(--border)',
  color: 'var(--muted)',
  borderRadius: '3px',
  padding: '2px 8px',
  fontSize: '10px',
  cursor: 'pointer',
  fontFamily: "'JetBrains Mono', monospace",
};

const styles: Record<string, React.CSSProperties> = {
  container: {
    fontSize: 13,
    lineHeight: 1.6,
    color: 'var(--text)',
    fontFamily: "'DM Sans', sans-serif",
    wordBreak: 'break-word',
    overflowWrap: 'break-word',
  },
};

// Global markdown styles — inject once
const styleId = 'appx-markdown-styles';
if (typeof document !== 'undefined' && !document.getElementById(styleId)) {
  const style = document.createElement('style');
  style.id = styleId;
  style.textContent = `
    .appx-markdown p { margin: 0 0 8px 0; }
    .appx-markdown p:last-child { margin-bottom: 0; }
    .appx-markdown pre {
      background: var(--surface);
      border: 1px solid var(--border);
      border-radius: 4px;
      padding: 12px;
      overflow-x: auto;
      margin: 8px 0;
      position: relative;
    }
    .appx-markdown code {
      font-family: 'JetBrains Mono', monospace;
      font-size: 12px;
    }
    .appx-markdown :not(pre) > code {
      background: var(--surface);
      border: 1px solid var(--border);
      border-radius: 3px;
      padding: 1px 5px;
      font-size: 12px;
    }
    .appx-markdown ul, .appx-markdown ol { margin: 4px 0; padding-left: 20px; }
    .appx-markdown li { margin: 2px 0; }
    .appx-markdown a { color: var(--cyan); text-decoration: none; }
    .appx-markdown a:hover { text-decoration: underline; }
    .appx-markdown h1, .appx-markdown h2, .appx-markdown h3 {
      margin: 12px 0 6px 0;
      color: var(--text);
    }
    .appx-markdown blockquote {
      border-left: 3px solid var(--border);
      margin: 8px 0;
      padding: 4px 12px;
      color: var(--muted);
    }
    .appx-markdown table { border-collapse: collapse; margin: 8px 0; }
    .appx-markdown th, .appx-markdown td {
      border: 1px solid var(--border);
      padding: 6px 10px;
      font-size: 12px;
    }
    .appx-markdown th { background: var(--surface); }
  `;
  document.head.appendChild(style);
}
