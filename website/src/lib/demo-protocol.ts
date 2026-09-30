// The messages between the website and the live demo in its iframe
// (spec-website §8.5). The other side is internal/web/demo/runtime.js, which
// TestExportDemo puts on every page of the demo; tests/unit/check-demo.mjs
// checks the two agree.
//
//   parent → frame   { type: 'vos-demo', v: 1, cmd: 'run', scenario }
//                    { type: 'vos-demo', v: 1, cmd: 'preset', name }
//   frame → parent   { …, event: 'ready', path, manifest }   each page load, once it listens
//                    { …, event: 'state', state }             what the pretend PC is doing
//                    { …, event: 'paired', device }           a PIN went in and pairing worked
//                    { …, event: 'notice', text }             something the demo won't do
//
// Both sides only take messages from their own origin, and the parent only
// from its own frame's window: anything else is ignored. Every navigation
// in the frame reloads the runtime, so the parent sends commands only after
// a `ready`, and queues them until then.

export const DEMO_TYPE = 'vos-demo';
export const DEMO_VERSION = 1;

/**
 * What the parent can make happen: internal/web/fixtures/scripts/<id>.json
 * (demo-manifest.json `scenarios`), plus `reset`, which the runtime handles
 * itself by starting over.
 */
export const SCENARIO_IDS = ['stream', 'stream-end', 'pair', 'update', 'power-off', 'wake', 'reset'] as const;
export type ScenarioId = (typeof SCENARIO_IDS)[number];

/** The device-state vocabulary (MASTER-PLAN §1.3), as the frame reports it. */
export const DEVICE_STATES = ['ready', 'streaming', 'updating', 'restart-needed', 'asleep', 'fault', 'installing', ''] as const;
export type DeviceState = (typeof DEVICE_STATES)[number];

export interface DemoState {
  device: DeviceState;
  /** Who is streaming, or null. */
  streaming: string | null;
  /** A device waiting for its PIN, and the PIN Moonlight shows. */
  pairing: { device: string; pin: string } | null;
  /** A running update: its phase (download, write, verify, install) and percent of the whole. */
  update: { phase: string; percent: number } | null;
}

type Envelope = { type: typeof DEMO_TYPE; v: typeof DEMO_VERSION };

export type ToDemo = Envelope & ({ cmd: 'run'; scenario: ScenarioId; args?: Record<string, unknown> } | { cmd: 'preset'; name: string });

export type FromDemo = Envelope &
  (
    | { event: 'ready'; path: string; manifest: string }
    | { event: 'state'; state: DemoState }
    | { event: 'paired'; device: string }
    | { event: 'notice'; text: string }
  );

export const isScenario = (s: unknown): s is ScenarioId => typeof s === 'string' && (SCENARIO_IDS as readonly string[]).includes(s);

export function runMessage(scenario: ScenarioId): ToDemo {
  return { type: DEMO_TYPE, v: DEMO_VERSION, cmd: 'run', scenario };
}

export function presetMessage(name: string): ToDemo {
  return { type: DEMO_TYPE, v: DEMO_VERSION, cmd: 'preset', name };
}

const obj = (x: unknown): x is Record<string, unknown> => !!x && typeof x === 'object' && !Array.isArray(x);
const str = (x: unknown): x is string => typeof x === 'string';

function parseState(x: unknown): DemoState | null {
  if (!obj(x)) return null;
  const device = (DEVICE_STATES as readonly unknown[]).includes(x.device) ? (x.device as DeviceState) : null;
  if (device === null) return null;
  if (x.streaming !== null && !str(x.streaming)) return null;
  let pairing: DemoState['pairing'] = null;
  if (x.pairing !== null) {
    if (!obj(x.pairing) || !str(x.pairing.device) || !str(x.pairing.pin)) return null;
    pairing = { device: x.pairing.device, pin: x.pairing.pin };
  }
  let update: DemoState['update'] = null;
  if (x.update !== null) {
    if (!obj(x.update) || !str(x.update.phase) || typeof x.update.percent !== 'number' || !Number.isFinite(x.update.percent)) return null;
    update = { phase: x.update.phase, percent: x.update.percent };
  }
  return { device, streaming: x.streaming, pairing, update };
}

/** A message from the demo, checked field by field, or null for anything else. */
export function parseFromDemo(data: unknown): FromDemo | null {
  if (!obj(data) || data.type !== DEMO_TYPE || data.v !== DEMO_VERSION || !str(data.event)) return null;
  const env = { type: DEMO_TYPE, v: DEMO_VERSION } as const;
  switch (data.event) {
    case 'ready':
      return str(data.path) && str(data.manifest) ? { ...env, event: 'ready', path: data.path, manifest: data.manifest } : null;
    case 'state': {
      const state = parseState(data.state);
      return state ? { ...env, event: 'state', state } : null;
    }
    case 'paired':
      return str(data.device) ? { ...env, event: 'paired', device: data.device } : null;
    case 'notice':
      return str(data.text) ? { ...env, event: 'notice', text: data.text } : null;
    default:
      return null;
  }
}

/**
 * A message event the parent may act on: from its own origin and its own
 * frame's window, and well formed. Null for anything else.
 */
export function acceptFromDemo(ev: Pick<MessageEvent, 'origin' | 'source' | 'data'>, frame: Window | null | undefined, origin: string): FromDemo | null {
  if (!frame || ev.source !== frame || ev.origin !== origin) return null;
  return parseFromDemo(ev.data);
}
