'use client';
// B5's first chapter (slot D5, updates): two copies of the system as two
// slots. "Play an update" downloads a version into the slot you aren't
// running, restarts into it and keeps the old one; "Play a bad update" does
// the same, but the new version doesn't start and the PC goes back by
// itself. It plays once when the beat arrives (motion allowed), and on
// every press. Heat is the running slot: it fills from the bottom while a
// version downloads, and a failed start turns the slot cold, with tape.
//
// The server renders the resting state (A running, B previous) and the log
// says it in words; the log is a polite live region, so each step is heard.
import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { Button } from '@/components/ui/button';
import { updatesStory as copy } from '@/content/story';
import { motionAllowed } from '@/lib/motion/prefs';

type Heat = 'hot' | 'cool' | 'filling' | 'failed';
interface Slot {
  heat: Heat;
  role: string;
  version: string;
  status: string;
  fill?: number;
}
type Log = (string | { b: string })[];

const V = copy.versions;
/** A log line from its template: the versions in bold. */
const say = (tpl: string): Log =>
  tpl.split(/(\{\w+\})/).map((part) => {
    const key = /^\{(\w+)\}$/.exec(part)?.[1] as keyof typeof V | undefined;
    return key && key in V ? { b: V[key] } : part;
  });
/** The log as one plain line, for the live region. */
const plain = (log: Log) => log.map((part) => (typeof part === 'string' ? part : part.b)).join('');
const REST: { a: Slot; b: Slot; log: Log } = {
  a: { heat: 'hot', role: copy.roles.running, version: V.running, status: copy.status.running },
  b: { heat: 'cool', role: copy.roles.previous, version: V.previous, status: copy.status.kept },
  log: say(copy.log.rest),
};

export function AbSlots({ children }: { children?: ReactNode }) {
  const [a, setA] = useState<Slot>(REST.a);
  const [b, setB] = useState<Slot>(REST.b);
  const [log, setLog] = useState<Log>(REST.log);
  const [announce, setAnnounce] = useState(false);
  const timers = useRef<number[]>([]);
  const root = useRef<HTMLDivElement>(null);

  const clear = () => {
    timers.current.forEach((t) => window.clearTimeout(t));
    timers.current = [];
  };
  const later = (ms: number, fn: () => void) => {
    timers.current.push(window.setTimeout(fn, motionAllowed() ? ms : Math.min(ms, 60)));
  };

  // A button press: from now on the live region follows the log.
  const press = (bad: boolean) => {
    setAnnounce(true);
    play(bad);
  };

  const play = (bad: boolean) => {
    clear();
    setA(REST.a);
    setB({ heat: 'filling', role: copy.roles.next, version: V.next, status: `${copy.status.downloading} · 0%`, fill: 0 });
    setLog(say(copy.log.downloading));
    let p = 0;
    const tick = () => {
      p = Math.min(100, p + 14);
      setB((s) => ({ ...s, fill: p, status: p < 100 ? `${copy.status.downloading} · ${p}%` : copy.status.checked }));
      if (p < 100) later(260, tick);
      else later(700, restart);
    };
    const restart = () => {
      setLog(say(copy.log.restart));
      setA({ heat: 'cool', role: copy.roles.previous, version: V.running, status: copy.status.kept });
      if (!bad) {
        later(900, () => {
          setB((s) => ({ ...s, heat: 'hot', role: copy.roles.running, status: copy.status.started }));
          setLog(say(copy.log.started));
        });
      } else {
        later(900, () => {
          setB((s) => ({ ...s, heat: 'failed', role: copy.roles.failed, status: copy.status.failed }));
          setLog(say(copy.log.failed));
        });
        later(2300, () => {
          setA({ heat: 'hot', role: copy.roles.running, version: V.running, status: copy.status.back });
          setLog(say(copy.log.back));
        });
      }
    };
    later(400, tick);
  };

  // Play once when the slots arrive, if motion is allowed.
  useEffect(() => {
    const el = root.current;
    if (!el) return;
    const io = new IntersectionObserver(
      (es) => {
        if (!es[0]?.isIntersecting) return;
        io.disconnect();
        if (motionAllowed()) play(false);
      },
      { rootMargin: '0px 0px -35% 0px' },
    );
    io.observe(el);
    return () => {
      io.disconnect();
      clear();
    };
    // play and clear only touch refs and state setters
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div ref={root} className="beat-cols ab">
      <div className="ab-text">
        {children}
        <div className="ab-ctl">
          <Button size="md" variant="hot" onClick={() => press(false)}>
            {copy.play}
          </Button>
          <Button size="md" variant="ghost" onClick={() => press(true)}>
            {copy.playBad}
          </Button>
        </div>
        <p className="ab-log telemetry" aria-hidden="true">
          {log.map((part, i) => (typeof part === 'string' ? part : <b key={i}>{part.b}</b>))}
        </p>
        {/* What a screen reader hears: the resting state, and each step of a
            run someone started with a button. The autoplay as the slots
            scroll in stays silent (the region is live from the start, so the
            first step of a pressed run is announced too). */}
        <p className="sr-only" aria-live="polite">
          {plain(announce ? log : REST.log)}
        </p>
      </div>
      <div className="slots" role="group" aria-label={copy.slotsLabel}>
        <SlotCard id="slot-a" letter="A" slot={a} />
        <SlotCard id="slot-b" letter="B" slot={b} />
      </div>
    </div>
  );
}

function SlotCard({ id, letter, slot }: { id: string; letter: string; slot: Slot }) {
  return (
    <div id={id} className="slot" data-heat-state={slot.heat} style={{ '--fill': (slot.fill ?? 0) * 0.62 } as CSSProperties}>
      <span className="slot-cool" aria-hidden="true" />
      <span className="slot-heat" aria-hidden="true" />
      <span className="slot-cold" aria-hidden="true" />
      <div>
        <p className="slot-top">
          <span className="slot-role">{slot.role}</span>
          <span className="slot-letter telemetry">{letter}</span>
        </p>
        <p className="slot-ver telemetry">{slot.version}</p>
      </div>
      <p className="slot-stat telemetry">{slot.status}</p>
    </div>
  );
}
