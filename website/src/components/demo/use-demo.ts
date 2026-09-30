'use client';
// The parent's end of the demo channel (src/lib/demo-protocol.ts): what the
// frame last said, a way to send it commands, and whether it ever started.
//
// Every page load in the frame reloads the demo's runtime, which says
// `ready` once it listens. Commands wait in a queue until then; when the
// page that said `ready` goes away (a link inside the frame, a reset), the
// queue holds them again until the next one.
import { useCallback, useEffect, useRef, useState, type RefObject } from 'react';
import { demoStage } from '@/content/demo';
import { acceptFromDemo, runMessage, type DemoState, type ScenarioId, type ToDemo } from '@/lib/demo-protocol';

/** How long the frame gets to say `ready` before the stage offers the demo in a tab of its own. */
export const DEMO_START_MS = 8000;

/**
 * How long after its first `ready` the frame stays out of sight: the page
 * inside fills in its first answers then (Home's state word takes its full
 * size), and those moves happen off screen, not as layout shifts.
 */
export const DEMO_SETTLE_MS = 450;

export interface DemoChannel {
  /** The frame said `ready` once, and has settled since (DEMO_SETTLE_MS). */
  started: boolean;
  /** No `ready` within DEMO_START_MS of the frame being given its page. */
  failed: boolean;
  state: DemoState | null;
  /** The device that paired last, until another starts pairing. */
  paired: string | null;
  /** What just happened, in words, for the polite live region. */
  said: string;
  run: (scenario: ScenarioId) => void;
  /** Call when the iframe gets its src. */
  armed: () => void;
}

export function useDemoChannel(frame: RefObject<HTMLIFrameElement | null>): DemoChannel {
  const [started, setStarted] = useState(false);
  const [failed, setFailed] = useState(false);
  const [state, setState] = useState<DemoState | null>(null);
  const [paired, setPaired] = useState<string | null>(null);
  const [said, setSaid] = useState('');
  const last = useRef<DemoState | null>(null);
  const listening = useRef<Window | null>(null);
  const queue = useRef<ToDemo[]>([]);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const settle = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const flush = useCallback(() => {
    const win = listening.current;
    if (!win) return;
    for (const m of queue.current.splice(0)) win.postMessage(m, location.origin);
  }, []);

  useEffect(() => {
    const onMessage = (ev: MessageEvent) => {
      const win = frame.current?.contentWindow;
      const m = acceptFromDemo(ev, win, location.origin);
      if (!m || !win) return;
      switch (m.event) {
        case 'ready': {
          listening.current = win;
          // The page that listens now goes away on the next navigation.
          try {
            win.addEventListener('pagehide', () => {
              if (listening.current === win) listening.current = null;
            }, { once: true });
          } catch {
            /* same origin: this always works */
          }
          clearTimeout(timer.current);
          if (!settle.current) settle.current = setTimeout(() => setStarted(true), DEMO_SETTLE_MS);
          setFailed(false);
          flush();
          break;
        }
        case 'state': {
          const before = last.current;
          last.current = m.state;
          setState(m.state);
          if (m.state.pairing) setPaired(null);
          const lines = before ? describe(before, m.state) : [];
          if (lines.length) setSaid(lines.join(' '));
          break;
        }
        case 'paired':
          setPaired(m.device);
          setSaid(fill(demoStage.announce.paired, { device: m.device }));
          break;
        case 'notice':
          setSaid(fill(demoStage.announce.notice, { text: m.text }));
          break;
      }
    };
    addEventListener('message', onMessage);
    return () => {
      removeEventListener('message', onMessage);
      clearTimeout(timer.current);
      clearTimeout(settle.current);
    };
  }, [frame, flush]);

  const run = useCallback(
    (scenario: ScenarioId) => {
      queue.current.push(runMessage(scenario));
      flush();
    },
    [flush],
  );

  const armed = useCallback(() => {
    clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      if (!listening.current) setFailed(true);
    }, DEMO_START_MS);
  }, []);

  return { started, failed, state, paired, said, run, armed };
}

/** Fills {name} placeholders. */
export const fill = (s: string, v: Record<string, string>) => s.replace(/\{(\w+)\}/g, (_, k: string) => v[k] ?? '');

/** What changed between two states, in words (the live region). */
export function describe(prev: DemoState, next: DemoState): string[] {
  const a = demoStage.announce;
  const out: string[] = [];
  if (prev.device === 'asleep' && next.device !== 'asleep') out.push(a.awake);
  if (next.device === 'asleep' && prev.device !== 'asleep') out.push(a.asleep);
  if (next.streaming && next.streaming !== prev.streaming) out.push(fill(a.streaming, { client: next.streaming }));
  if (!next.streaming && prev.streaming && next.device !== 'asleep') out.push(a.streamEnded);
  if (next.pairing && next.pairing.device !== prev.pairing?.device) out.push(fill(a.pairing, next.pairing));
  if (next.device === 'updating' && prev.device !== 'updating') out.push(a.updating);
  if (next.device === 'restart-needed' && prev.device !== 'restart-needed') out.push(a.restart);
  return out;
}
