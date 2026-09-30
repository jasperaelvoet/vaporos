'use client';
// B5's second chapter (slot D5, power): one evening as a heat ribbon, thin
// and dark while the PC sleeps, thick and white-hot while it streams. As
// the ribbon scrolls through the screen, a playhead runs from 18:00 to
// midnight and the readout says what the PC is doing at that minute. The
// figure's label tells the whole evening in words, so the moving readout is
// hidden from screen readers. Without JavaScript, or with motion off, the
// playhead rests at 20:40, mid-stream.
import { useEffect, useRef, type CSSProperties } from 'react';
import { paintIsotherms, smoothstep, valueNoise } from '@/direction/redline/painter';
import { useThermalCanvas } from '@/direction/redline/use-thermal-canvas';
import { evening } from '@/content/story';
import { motionAllowed, subscribeMotion } from '@/lib/motion/prefs';
import { trackBox, whileNear } from '@/lib/motion/scroll-frame';

const SPAN = evening.end - evening.start;
const pct = (h: number) => `${(((h - evening.start) / SPAN) * 100).toFixed(2)}%`;

/** The PC's heat through the evening: asleep, woken, streaming, idle, asleep. */
function heatAt(h: number): number {
  const on = smoothstep(19.17, 19.25, h) * (1 - smoothstep(22.33, 22.62, h));
  const play = smoothstep(19.42, 19.56, h) * (1 - smoothstep(22.08, 22.3, h));
  return 0.05 + 0.33 * on + 0.52 * play + 0.035 * Math.sin(h * 9.7) * play;
}

function clock(h: number): string {
  const hh = Math.floor(h);
  const mm = Math.floor((h - hh) * 60);
  return `${String(hh % 24).padStart(2, '0')}:${String(mm).padStart(2, '0')}`;
}

function stateAt(h: number): string {
  return (evening.spans.find((s) => h >= s.from && h < s.to) ?? evening.spans[evening.spans.length - 1]).state;
}

export function EveningRibbon() {
  const fig = useRef<HTMLElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const time = useRef<HTMLSpanElement>(null);
  const state = useRef<HTMLSpanElement>(null);

  useThermalCanvas(canvas, (cv, { dpr }) => {
    const W = cv.width;
    const H = cv.height;
    const mid = H / 2;
    const col = new Float32Array(W);
    for (let x = 0; x < W; x++) col[x] = heatAt(evening.start + (x / W) * SPAN);
    paintIsotherms(
      cv,
      (x, y) => {
        const L = col[Math.min(W - 1, x)];
        const sig = (0.1 + 0.42 * L) * H;
        const n = valueNoise((x * 0.012) / dpr, (y * 0.03) / dpr) - 0.5;
        const dy = (y - mid + n * 26 * dpr * L) / sig;
        return L * 1.12 * Math.exp(-0.5 * dy * dy) + 0.03;
      },
      { bands: 12, line: 0.4, px: dpr },
    );
  });

  // The playhead follows the scroll while motion is allowed: a job in the
  // page's scroll frame, only while the ribbon is within a screen of the
  // viewport, reading scrollY against the ribbon's cached box.
  useEffect(() => {
    const el = fig.current;
    if (!el) return;
    const box = trackBox(el);
    let h = evening.rest;
    let shown = -1;
    const set = (at: number) => {
      el.style.setProperty('--at', pct(at));
      if (time.current) time.current.textContent = clock(at);
      if (state.current) state.current.textContent = stateAt(at);
    };
    const job = {
      read: () => {
        const vh = window.innerHeight;
        const top = box.top - window.scrollY;
        // top at 80% of the screen → 18:18; bottom at 30% → 23:42
        const p = Math.min(1, Math.max(0, (vh * 0.8 - top) / Math.max(1, box.height + vh * 0.5)));
        h = evening.start + 0.3 + p * (SPAN - 0.6);
      },
      write: () => {
        // A minute is the readout's finest step.
        const at = Math.round(h * 60) / 60;
        if (at === shown) return;
        shown = at;
        set(at);
      },
    };
    let stop: (() => void) | null = null;
    const apply = () => {
      stop?.();
      stop = null;
      shown = -1;
      if (motionAllowed()) stop = whileNear(el, job);
      else set(evening.rest);
    };
    const off = subscribeMotion(apply);
    apply();
    return () => {
      stop?.();
      box.dispose();
      off();
    };
  }, []);

  return (
    <figure ref={fig} className="ribbon" aria-label={evening.label} style={{ '--at': pct(evening.rest) } as CSSProperties}>
      <div className="rb-ev rb-up" aria-hidden="true">
        {evening.up.map((e) => (
          <span key={e.time} style={{ left: pct(e.at) }}>
            <b className="telemetry">{e.time}</b>
            {e.text}
          </span>
        ))}
      </div>
      <div className="rb-box" aria-hidden="true">
        <canvas ref={canvas} />
        <span className="rb-head" />
      </div>
      <div className="rb-ev rb-down" aria-hidden="true">
        {evening.down.map((e) => (
          <span key={e.time} style={{ left: pct(e.at) }}>
            <b className="telemetry">{e.time}</b>
            {e.text}
          </span>
        ))}
      </div>
      <div className="rb-axis telemetry" aria-hidden="true">
        {evening.axis.map((a) => (
          <span key={a}>{a}</span>
        ))}
      </div>
      <p className="rb-read telemetry" aria-hidden="true">
        <span ref={time}>{clock(evening.rest)}</span>
        <span ref={state} className="rb-state">
          {stateAt(evening.rest)}
        </span>
      </p>
    </figure>
  );
}
