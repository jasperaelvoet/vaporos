'use client';
// Beside the live demo's frame (demo-stage.tsx): the buttons that make
// things happen on the pretend PC (one per scenario, src/lib/demo-protocol.ts),
// Moonlight on the Steam Deck, where the pairing PIN shows up, and what is
// real and what is made up. /demo/ on phones shows the same in its bottom
// sheet.
import type { ReactNode } from 'react';
import { useId } from 'react';
import { Icon } from '@/components/ui/icon';
import { Rich } from '@/components/ui/rich';
import { demoStage as copy } from '@/content/demo';
import type { IconName } from '@/lib/icons';
import type { ScenarioId } from '@/lib/demo-protocol';
import type { DemoChannel } from './use-demo';

const ICONS: Record<ScenarioId, IconName> = {
  stream: 'tv',
  'stream-end': 'tv',
  pair: 'phone',
  update: 'update',
  'power-off': 'power',
  wake: 'power',
  reset: 'restart',
};

export function ScenarioPanel({ ch, headingId, onRun, children }: { ch: DemoChannel; headingId: string; onRun?: () => void; children?: ReactNode }) {
  const id = useId();
  const s = ch.state;
  // A button that flips (Start/End the stream, Power it off/Wake it) keeps its
  // slot's key, so it stays the same element and keeps the focus.
  const primary: [string, ScenarioId][] = [
    ['stream', s?.streaming ? 'stream-end' : 'stream'],
    ['pair', 'pair'],
    ['update', 'update'],
  ];
  const more: [string, ScenarioId][] = [
    ['power', s?.device === 'asleep' ? 'wake' : 'power-off'],
    ['reset', 'reset'],
  ];
  const button = ([slot, sc]: [string, ScenarioId], quiet: boolean) => (
    <li key={slot} className={quiet ? 'demo-sc demo-sc-quiet' : 'demo-sc'}>
      <button
        type="button"
        className="demo-sc-btn"
        aria-describedby={`${id}-${slot}`}
        onClick={() => {
          ch.run(sc);
          onRun?.();
        }}
      >
        <Icon name={ICONS[sc]} className="demo-sc-icon" />
        {copy.scenarios[sc].label}
      </button>
      <p id={`${id}-${slot}`} className="demo-sc-help">
        {copy.scenarios[sc].help}
      </p>
    </li>
  );
  return (
    <section className="demo-panel" aria-labelledby={headingId}>
      <h2 id={headingId} className="demo-panel-title">
        {copy.panelTitle}
      </h2>
      <p className="demo-panel-lead">{copy.panelLead}</p>
      <ul className="demo-scs">{primary.map((sc) => button(sc, false))}</ul>
      {children}
      <p className="demo-more telemetry" aria-hidden="true">
        {copy.moreLabel}
      </p>
      <ul className="demo-scs demo-scs-more">{more.map((sc) => button(sc, true))}</ul>
    </section>
  );
}

export function MoonlightMock({ ch }: { ch: DemoChannel }) {
  const m = copy.moonlight;
  const pairing = ch.state?.pairing ?? null;
  return (
    <section className="demo-ml" aria-label={m.label} data-ml={pairing ? 'pin' : ch.paired ? 'paired' : 'idle'}>
      <p className="demo-ml-bar" aria-hidden="true">
        <span className="demo-ml-app">{m.app}</span>
        <span className="demo-ml-host telemetry">{m.host}</span>
      </p>
      {pairing ? (
        <>
          <p className="demo-ml-text">{m.waiting}</p>
          <p className="demo-ml-pin telemetry">
            <span aria-hidden="true">{pairing.pin}</span>
            <span className="sr-only">{pairing.pin.split('').join(' ')}</span>
          </p>
        </>
      ) : ch.paired ? (
        <p className="demo-ml-text demo-ml-done">
          <Icon name="check" className="demo-ml-check" />
          {m.paired}
        </p>
      ) : (
        <p className="demo-ml-text">{m.idle}</p>
      )}
    </section>
  );
}

export function Notes() {
  return (
    <div className="demo-notes">
      <h3 className="demo-notes-title">{copy.notes.title}</h3>
      <p>
        <Rich text={copy.notes.real} />
      </p>
      <p>
        <Rich text={copy.notes.fake} />
      </p>
    </div>
  );
}
