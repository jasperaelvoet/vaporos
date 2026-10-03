// pages/devices-scale.js: a device's interface size in words and numbers
// (CONTRACTS Display policy, Scaling), pure: jstest runs it. GET /display
// screens[] holds the sizes; the session's screen (GET /status stream, else
// session.begin) names the one playing.

export const KIND_NAMES = { phone: 'Phone', handheld: 'Handheld', tablet: 'Tablet', laptop: 'Laptop', monitor: 'Monitor', tv: 'TV' };
const A = { phone: 'a phone', handheld: 'a handheld', tablet: 'a tablet', laptop: 'a laptop', monitor: 'a monitor', tv: 'a TV' };
const FROM = { name: 'its name', browser: 'its browser', resolution: 'its resolution', history: 'its earlier resolutions', stream: 'how it streams' };
// There is no tablet glyph: a tablet is drawn as the phone.
const GLYPHS = { phone: 'phone', handheld: 'gamepad', tablet: 'phone', laptop: 'laptop', monitor: 'monitor', tv: 'tv' };

export const SIZE_MIN = 0.4;
export const SIZE_MAX = 2.5;

export const percent = (size) => Math.round((Number(size) || 1) * 100);
export const isAuto = (sc) => sc.kind_from !== 'you';

// liveScreen is the screen of the session playing now: GET /display's,
// joined by the session's screen id, else what the session says itself
// (ui_scale 0 is Steam's own size). null without one (scaling off, or an
// older VaporOS).
export function liveScreen(display, ss) {
  const ref = ss && ss.screen;
  if (!ref || typeof ref !== 'object') return null;
  const id = String(ref.id ?? '');
  const list = display && Array.isArray(display.screens) ? display.screens : [];
  const sc = list.find((s) => s && s.id === id);
  if (sc) return sc;
  return { id, name: ref.name || '', kind: ref.kind, kind_from: ref.kind_from, guess: ref.kind, size: 1, steam_auto: ref.ui_scale === 0, savable: id !== '' };
}

// autoWords is what Automatic does for this screen, after "Automatic: ":
// its kind in effect and why, or for a kind the user picked the guess
// without its reason (GET /display keeps none for it).
export function autoWords(sc) {
  const kind = isAuto(sc) ? sc.kind : sc.guess;
  if (!A[kind]) return "VaporOS can't tell what it is yet, so it's sized like a TV";
  const from = isAuto(sc) ? FROM[sc.kind_from] : '';
  return `looks like ${A[kind]}${from ? `, from ${from}` : ''}`;
}

// sizeWords is Playing now's Interface size: "Phone · 110%",
// "Automatic · looks like a phone" or "Steam's own size".
export function sizeWords(sc) {
  if (sc.steam_auto) return "Steam's own size";
  const pct = percent(sc.size);
  const tail = pct === 100 ? '' : `${pct}%`;
  if (!isAuto(sc)) return `${KIND_NAMES[sc.kind] || 'Automatic'} · ${pct}%`;
  const looks = A[sc.kind] ? `looks like ${A[sc.kind]}` : '';
  return ['Automatic', looks, tail].filter(Boolean).join(' · ');
}

// steamNote is the line for how sizing Steam stands (GET /display
// steam_ui); error marks the ones that are a fault.
export function steamNote(state) {
  if (state === 'no-debugger' || state === 'unsupported') return { text: "Can't size Steam right now. Steam's own setting still works.", error: true };
  if (state === 'starting') return { text: "Steam is starting. The size applies once it's ready.", error: false };
  // A resumed stream ran no begin: VaporOS holds nothing until the next.
  if (state === 'resumed') return { text: "This stream was resumed. Changes apply from this device's next start.", error: false };
  // off has none: with sizing off Playing now says Steam's own size, and Adjust goes.
  return { text: '', error: false };
}

// bounds are the sizes between which the screen's scale moves on the mode
// it streams now (GET /display size_min and size_max: beyond them Steam's
// own bounds hold it), else 40% to 250%.
export function bounds(sc) {
  const lo = Number(sc && sc.size_min);
  const hi = Number(sc && sc.size_max);
  return lo >= SIZE_MIN && hi <= SIZE_MAX && lo <= hi ? [lo, hi] : [SIZE_MIN, SIZE_MAX];
}

// stepSize is the screen's next 10% step up (dir 1) or down (-1), on the
// 10% grid (an adopted 1.137 goes to 1.2 or 1.1) and within its bounds. A
// size beyond them steps from the bound, so no tap is one Steam ignores.
export function stepSize(sc, dir) {
  const [lo, hi] = bounds(sc);
  const from = Math.min(hi, Math.max(lo, Number(sc.size) || 1));
  const tenths = Math.round(from * 1000) / 100;
  const next = dir > 0 ? Math.floor(tenths + 1e-6) + 1 : Math.ceil(tenths - 1e-6) - 1;
  return Math.min(hi, Math.max(lo, next / 10));
}

// canStep: a step that way still changes the screen's scale.
export function canStep(sc, dir) {
  const [lo, hi] = bounds(sc);
  const size = Number(sc.size) || 1;
  if (hi - lo < 1e-6) return false;
  return dir > 0 ? size < hi - 1e-6 : size > lo + 1e-6;
}

// vetoed: the kind in effect is this session's veto (a phone streaming to
// something bigger), which VaporOS never stores as the user's.
const vetoed = (sc) => sc.kind_from === 'stream' && !!sc.guess && sc.kind !== sc.guess;

// isDefault: automatic at 100%, sized by VaporOS (what Reset gives).
export const isDefault = (sc) => isAuto(sc) && percent(sc.size) === 100 && !sc.steam_auto;

// edit is PUT /display/screens/{id} on a copy of the screen, for the page
// to show before the answer: a new kind resets the size to 1.0 unless the
// body has one, a size alone pins the kind in effect (not the session's
// veto), and a kind or a size without steam_auto turns it off. Automatic
// again shows the guess, its reason left to the answer. Another kind has
// other bounds, which only the answer knows.
export function edit(sc, body) {
  const next = { ...sc };
  const stored = isAuto(sc) ? '' : sc.kind;
  let pick = stored;
  if (body.kind !== undefined) {
    pick = body.kind === 'auto' ? '' : body.kind;
    if (pick !== stored) next.size = 1;
  }
  if (body.size !== undefined) {
    next.size = body.size;
    if (body.kind === undefined && !pick && !vetoed(sc)) pick = sc.kind;
  }
  if (body.steam_auto !== undefined) next.steam_auto = body.steam_auto;
  else if (body.kind !== undefined || body.size !== undefined) next.steam_auto = false;
  if (pick) Object.assign(next, { kind: pick, kind_from: 'you' });
  else if (!isAuto(sc)) Object.assign(next, { kind: sc.guess || 'unknown', kind_from: '' });
  if (next.kind !== sc.kind) {
    delete next.size_min;
    delete next.size_max;
  }
  return next;
}

// kindGlyph is the icon for a device's name when every screen of that name
// is the same kind, else '' (the name decides).
export function kindGlyph(name, screens) {
  const kinds = new Set((Array.isArray(screens) ? screens : []).filter((s) => s && s.name === name).map((s) => s.kind));
  return kinds.size === 1 ? GLYPHS[[...kinds][0]] || '' : '';
}
