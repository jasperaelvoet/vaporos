'use client';
// A command or a key, verbatim, with a copy button: the <pre>'s text is
// exactly `code` (what gets copied), and comment lines are dimmed. The button
// appears only after hydration, so without JavaScript the block is plain,
// selectable text. The block stays ash on any ground, white-hot included.
//
// Lines never wrap: a soft wrap in the middle of a flag reads as a second
// command, next to the real `\` continuations. A long line scrolls instead
// (the <pre> is focusable, named by the title, with its ring drawn inside the
// rounded box), and an amber edge on the right says there is more
// (.code-scroll, base.css; pure CSS, gone once scrolled to the end).
import { useEffect, useId, useRef, useState, useSyncExternalStore, type ReactNode } from 'react';
import type { Code } from '@/content/types';

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    // Insecure contexts and older browsers: a hidden textarea and execCommand.
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.className = 'fixed top-0 left-0 opacity-0';
    document.body.append(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    return ok;
  }
}

const noSubscribe = () => () => {};

/** Apple keyboards copy with ⌘C. */
function copyKeys(): string {
  if (typeof navigator === 'undefined') return 'Ctrl+C';
  const platform = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData?.platform ?? navigator.platform ?? '';
  return /mac|iphone|ipad|ipod/i.test(platform) ? '⌘C' : 'Ctrl+C';
}

export function CodeBlock({ code, title = 'Terminal', lang = 'sh', className = '' }: Partial<Code> & { code: string; className?: string }) {
  // false in the server HTML, true once hydrated: the button only shows when it can work.
  const hydrated = useSyncExternalStore(noSubscribe, () => true, () => false);
  const [state, setState] = useState<'idle' | 'done' | 'failed'>('idle');
  const timer = useRef<number | undefined>(undefined);
  const titleId = useId();

  useEffect(() => () => window.clearTimeout(timer.current), []);

  async function onCopy() {
    const ok = await copyText(code);
    setState(ok ? 'done' : 'failed');
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setState('idle'), 1800);
  }

  const lines = code.split('\n');
  return (
    <figure className={`min-w-0 overflow-hidden rounded-lg bg-ash text-bone inset-ring-1 inset-ring-line ${className}`} data-lang={lang}>
      <figcaption className="flex min-h-11 items-center gap-3 border-b border-line py-1 pr-1.5 pl-4">
        <span id={titleId} className="telemetry mr-auto truncate text-[0.75rem] text-dim">
          {title}
        </span>
        {hydrated && (
          // The visible word is the name's start (WCAG 2.5.3); the title says what gets copied.
          <button
            type="button"
            onClick={onCopy}
            className="min-h-9 rounded-md px-3 text-[0.8125rem] font-semibold text-smoke inset-ring-1 inset-ring-line transition-colors hover:text-bone hover:inset-ring-smoke"
          >
            {state === 'done' ? 'Copied' : state === 'failed' ? `Press ${copyKeys()}` : 'Copy'}
            {state === 'idle' && <span className="sr-only"> {title}</span>}
          </button>
        )}
      </figcaption>
      <pre
        tabIndex={0}
        aria-labelledby={titleId}
        className="code-scroll overflow-x-auto p-4 text-[0.8125rem] leading-relaxed whitespace-pre focus-visible:-outline-offset-2"
      >
        <code>
          {lines.map((l, i) => (
            <span key={i} className={/^\s*#/.test(l) ? 'text-dim' : undefined}>
              {l}
              {i < lines.length - 1 ? '\n' : ''}
            </span>
          ))}
        </code>
      </pre>
      <p role="status" className="sr-only">
        {state === 'done' ? 'Copied to the clipboard' : state === 'failed' ? 'Copying failed; select the text and copy it' : ''}
      </p>
    </figure>
  );
}

/** A short inline command or name, in the page's text. */
export function InlineCode({ children }: { children: ReactNode }) {
  // A long name (an image reference, a path) may break anywhere rather than widen the column.
  return <code className="rounded-xs bg-char px-1 py-0.5 text-[0.88em] text-bone [overflow-wrap:anywhere]">{children}</code>;
}

/** A key on a keyboard. */
export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="rounded-xs border border-line bg-soot px-1.5 text-[0.85em] text-bone">{children}</kbd>;
}
