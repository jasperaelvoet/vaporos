'use client';
// B2's picture (slot D2): pick a Moonlight device and the virtual display
// takes its shape. The frame's heat is the device's pixel rate: 3840 × 2160
// at 60 Hz is the hottest mode here, a Steam Deck the coolest.
//
// The frame's aspect ratio and the readouts are registered CSS properties
// (--ar, --n-w, --n-h, --n-hz, --n-pps; story.css) on spring transitions, so
// a pick only changes four numbers and the browser does the rest; the
// numbers print through CSS counters. Under reduced motion or Pause motion
// they jump. While the beat is in view the devices take turns every 2.6 s,
// until someone picks one or moves focus into the picker; a screen reader
// hears only picks (the live region), never the turns.
import { useEffect, useRef, useState, type CSSProperties } from 'react';
import { around, paintIsotherms } from '@/direction/redline/painter';
import { useThermalCanvas } from '@/direction/redline/use-thermal-canvas';
import { devices, screenStory as copy, type Device } from '@/content/story';
import { motionAllowed, subscribeMotion } from '@/lib/motion/prefs';

const MAX_PPS = 3840 * 2160 * 60;
const pps = (d: Device) => d.w * d.h * d.hz;
const mode = (d: Device) => `${d.w} × ${d.h} · ${d.hz} ${copy.hz}${d.hdr ? ` · ${copy.hdr}` : ''}`;
const millions = (d: Device) => Math.round(pps(d) / 1e6);

export function ScreenFollows() {
  const [index, setIndex] = useState(0);
  const [said, setSaid] = useState('');
  const picked = useRef(false);
  const root = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const d = devices[index];
  const load = pps(d) / MAX_PPS;

  useThermalCanvas(
    canvas,
    (cv) => {
      const W = cv.width;
      const H = cv.height;
      paintIsotherms(
        cv,
        around({
          cx: W * 0.52,
          cy: H * 0.64,
          hw: 18,
          hh: 18,
          r: 18,
          rise: 150,
          nf: 0.0075,
          warp: 52,
          reach: 40 + 104 * load,
          peak: 0.36 + 0.62 * Math.sqrt(load),
          ambient: 0.06,
          h: H,
        }),
        { bands: 12 },
      );
      if (motionAllowed() && typeof cv.animate === 'function') {
        cv.animate([{ filter: 'brightness(1.6)' }, { filter: 'brightness(1)' }], { duration: 500, easing: 'ease-out' });
      }
    },
    [load],
    { size: { width: 384, height: 288 } },
  );

  // Take turns while in view, until a pick; never under reduced motion or Pause motion.
  useEffect(() => {
    const el = root.current;
    if (!el) return;
    let timer = 0;
    let inView = false;
    const stop = () => {
      window.clearInterval(timer);
      timer = 0;
    };
    const run = () => {
      stop();
      if (!inView || picked.current || !motionAllowed() || el.matches(':focus-within')) return;
      timer = window.setInterval(() => {
        if (picked.current || el.matches(':focus-within')) return stop();
        setIndex((i) => (i + 1) % devices.length);
      }, 2600);
    };
    const io = new IntersectionObserver(
      (es) => {
        inView = !!es[0]?.isIntersecting;
        run();
      },
      { rootMargin: '-20% 0px -20% 0px' },
    );
    io.observe(el);
    const off = subscribeMotion(run);
    el.addEventListener('focusin', stop);
    el.addEventListener('focusout', run);
    return () => {
      stop();
      io.disconnect();
      off();
      el.removeEventListener('focusin', stop);
      el.removeEventListener('focusout', run);
    };
  }, []);

  const pick = (i: number) => {
    picked.current = true;
    setIndex(i);
    const p = devices[i];
    setSaid(`${p.name}: ${mode(p)}, ${millions(p)} ${copy.pps}.`);
  };

  const vars = {
    '--ar': (d.w / d.h).toFixed(4),
    '--n-w': d.w,
    '--n-h': d.h,
    '--n-hz': d.hz,
    '--n-pps': millions(d),
  } as CSSProperties;

  return (
    <div ref={root} className="beat-cols screen-cols">
      <div className="screen-pick">
        <div className="devices" role="group" aria-label={copy.pickLabel}>
          {devices.map((dev, i) => (
            <button key={dev.name} type="button" className="dev" aria-pressed={i === index} onClick={() => pick(i)}>
              <span className="dev-name">{dev.name}</span>
              <span className="dev-pip" aria-hidden="true" />
              <span className="dev-mode telemetry">{mode(dev)}</span>
            </button>
          ))}
        </div>
        <p className="beat-note">
          <strong>{copy.note.strong}</strong> {copy.note.text}
        </p>
      </div>
      <figure className="stage" style={vars} aria-label={copy.stageLabel}>
        <div className="stage-screen">
          <div className="frame">
            <canvas ref={canvas} aria-hidden="true" />
            <span className="frame-label telemetry" aria-hidden="true">
              {d.name}
            </span>
          </div>
        </div>
        <div className="stage-dims" aria-hidden="true">
          <div className="tach telemetry">
            <b>
              <span className="n-w" /> × <span className="n-h" />
            </b>
            <b className="tach-hz">
              <span className="n-hz" /> <span className="tach-unit">{copy.hz}</span>
            </b>
            <span className={`hdr ${d.hdr ? '' : 'hdr-off'}`}>{copy.hdr}</span>
            <small>{copy.display}</small>
          </div>
          <div className="pps telemetry">
            <b className="n-pps" />
            <small>{copy.pps}</small>
          </div>
        </div>
        <p className="sr-only" aria-live="polite">
          {said}
        </p>
      </figure>
    </div>
  );
}
