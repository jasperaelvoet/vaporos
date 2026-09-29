'use client';
// A terminal-style code block with a copy button. The <pre>'s text is exactly
// `code` (what gets copied); comment lines are dimmed. The button renders
// only after hydration, so without JavaScript the block is just selectable text.
import { useEffect, useRef, useState, useSyncExternalStore } from 'react';
import type { Code } from '@/content/types';

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.append(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    return ok;
  }
}

const noSubscribe = () => () => {};

export function CodeBlock({ code, title = 'Terminal', lang = 'sh' }: Partial<Code> & { code: string }) {
  // false in the server HTML, true once hydrated: the button only shows when it can work.
  const hydrated = useSyncExternalStore(noSubscribe, () => true, () => false);
  const [state, setState] = useState<'idle' | 'done' | 'failed'>('idle');
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  async function onCopy() {
    const ok = await copyText(code);
    setState(ok ? 'done' : 'failed');
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setState('idle'), 1800);
  }

  const lines = code.split('\n');
  return (
    <figure className="min-w-0 overflow-hidden rounded-xl border border-line-2 bg-[#080d13]" data-lang={lang}>
      <figcaption className="flex items-center gap-3 border-b border-line px-3.5 py-2">
        <span className="mr-auto truncate font-mono text-xs text-fg-3">{title}</span>
        {hydrated && (
          <button
            type="button"
            onClick={onCopy}
            aria-label={`Copy ${title}`}
            className="rounded-md border border-line-2 px-2.5 py-1 text-xs font-semibold text-fg-2 hover:text-fg"
          >
            {state === 'done' ? 'Copied' : state === 'failed' ? 'Press Ctrl+C' : 'Copy'}
          </button>
        )}
      </figcaption>
      <pre tabIndex={0} className="overflow-x-auto p-4 font-mono text-[13px] leading-relaxed whitespace-pre-wrap break-words text-[#d5e6f7]">
        <code>
          {lines.map((l, i) => (
            <span key={i} className={/^\s*#/.test(l) ? 'text-fg-3' : undefined}>
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
