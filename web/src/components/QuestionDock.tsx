import { useState } from 'react';
import type { QuestionRequest, QuestionAnswer } from '@opencode-ai/sdk/v2/client';

interface QuestionDockProps {
  question: QuestionRequest;
  onAnswer: (requestID: string, answers: QuestionAnswer[]) => void;
  onReject: (requestID: string) => void;
}

/** QuestionDock shows an agent question with radio/text options and submit. */
export default function QuestionDock({ question, onAnswer, onReject }: QuestionDockProps) {
  const [answers, setAnswers] = useState<string[][]>(question.questions.map(() => []));

  const handleSelect = (qIdx: number, label: string, multiple: boolean) => {
    setAnswers((prev) => {
      const next = [...prev];
      if (multiple) {
        const current = next[qIdx];
        next[qIdx] = current.includes(label) ? current.filter((l) => l !== label) : [...current, label];
      } else {
        next[qIdx] = [label];
      }
      return next;
    });
  };

  const handleSubmit = () => { onAnswer(question.id, answers); };
  const hasAnswer = answers.some((a) => a.length > 0);

  return (
    <div style={styles.dock}>
      {question.questions.map((q, qIdx) => (
        <div key={qIdx} style={styles.questionBlock}>
          {q.header && <div style={styles.header}>{q.header}</div>}
          <div style={styles.questionText}>{q.question}</div>
          <div style={styles.options}>
            {q.options.map((opt) => (
              <button key={opt.label} style={answers[qIdx].includes(opt.label) ? styles.optionSelected : styles.option}
                onClick={() => handleSelect(qIdx, opt.label, q.multiple ?? false)}>
                <span style={styles.optionLabel}>{opt.label}</span>
                {opt.description && <span style={styles.optionDesc}>{opt.description}</span>}
              </button>
            ))}
          </div>
        </div>
      ))}
      <div style={styles.actions}>
        <button style={styles.rejectBtn} onClick={() => onReject(question.id)}>Dismiss</button>
        <button style={styles.submitBtn} onClick={handleSubmit} disabled={!hasAnswer}>Submit</button>
      </div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  dock: { background: 'var(--surface)', border: '1px solid var(--cyan)', borderRadius: 6, padding: '12px 16px', margin: '0 20px 8px' },
  questionBlock: { marginBottom: 10 },
  header: { fontFamily: "'JetBrains Mono', monospace", fontSize: 10, letterSpacing: '0.05em', color: 'var(--cyan)', marginBottom: 4 },
  questionText: { fontSize: 13, color: 'var(--text)', marginBottom: 8 },
  options: { display: 'flex', flexDirection: 'column' as const, gap: 4 },
  option: { display: 'flex', flexDirection: 'column' as const, gap: 2, padding: '8px 12px', background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 4, cursor: 'pointer', textAlign: 'left' as const },
  optionSelected: { display: 'flex', flexDirection: 'column' as const, gap: 2, padding: '8px 12px', background: 'var(--cyan-dim)', border: '1px solid var(--cyan)', borderRadius: 4, cursor: 'pointer', textAlign: 'left' as const },
  optionLabel: { fontSize: 12, color: 'var(--text)', fontWeight: 500 },
  optionDesc: { fontSize: 11, color: 'var(--muted)' },
  actions: { display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 8 },
  rejectBtn: { background: 'transparent', border: '1px solid var(--border)', color: 'var(--muted)', borderRadius: 4, padding: '5px 14px', fontSize: 11, cursor: 'pointer' },
  submitBtn: { background: 'var(--blue)', border: 'none', color: '#fff', borderRadius: 4, padding: '5px 14px', fontSize: 11, fontWeight: 500, cursor: 'pointer' },
};
