// ext.js: what System › Extensions and its row on System say, pure (jstest
// runs it). The document is GET /extensions (docs/CONTRACTS.md); the
// words follow design/voice.md, and a chip only ever names a state of
// design/tokens.json.

import { capitalize } from './copy.js';
import { bytes, percent } from './fmt.js';

// CHIPS: the states that are tokens.json states (state.*.label), by token;
// the others are facts in neutral words (PLAIN).
export const CHIPS = {
  installing: { state: 'installing', label: 'Installing' },
  'restart-needed': { state: 'restart-needed', label: 'Restart needed' },
  'needs-attention': { state: 'fault', label: 'Needs attention' },
};

export const PLAIN = {
  installed: 'Installed',
  'not-installed': 'Not installed',
  'not-in-this-version': 'Not in this version',
};

export const ALWAYS_ON = 'Always on';
export const AFTER_RESTART = 'Takes effect after a restart.';
// SKIP is when a change applies while the next start leaves extensions out.
export const SKIP = 'The next start is without extensions. This change applies at the restart after that.';

// NOTES are lines a card adds for one extension: always, and while it is
// not mounted.
const NOTES = {
  proton: {
    always: 'Steam runs Windows games with it. VaporOS adds it for you and keeps it apart like every extension.',
    missing: "Windows games use Valve's Proton until this finishes.",
  },
};

// PERMS: the verified permission categories in plain words, in the order
// descriptor.Permissions lists them.
const PERMS = {
  service: 'Runs a system service',
  'user-service': 'Runs a background service next to Steam',
  udev: 'Adds device rules',
  sysctl: 'Changes kernel settings',
  modules: 'Loads kernel modules',
  polkit: 'Adds system permission rules',
  dbus: 'Adds services to the system bus',
  'compat-tool': 'Adds a Steam compatibility tool',
};

const WHEN = { install: 'when you install it', update: 'when it updates', launch: 'each time it starts' };
const CHECKED = {
  pinned: 'VaporOS checks it against a fingerprint it ships.',
  'publisher-hash': "Checked against the publisher's own fingerprint.",
  none: 'Nothing checks it.',
};

const list = (v) => (Array.isArray(v) ? v : []);
const dot = (s) => String(s).replace(/([^.!?])$/, '$1.');

// and joins names: "A", "A and B", "A, B and C".
export function and(names) {
  const n = names.filter(Boolean);
  if (n.length < 2) return n.join('');
  return `${n.slice(0, -1).join(', ')} and ${n[n.length - 1]}`;
}

// size is the image's size, as drives are measured; '' when unknown.
export const size = (n) => (Number(n) > 0 ? bytes(n) : '');

// progress is how far an install is, 0-100, or null when it has no total
// (sealing, or a download that has not started).
export function progress(x) {
  const p = x && x.progress;
  if (!p || !(Number(p.total) > 0)) return null;
  return percent((Number(p.bytes) / Number(p.total)) * 100);
}

// chip is the state chip of a card, or null when its state is a fact.
// fault carries the reason in at most two sentences.
export function chip(x) {
  const c = CHIPS[x && x.state];
  if (!c) return null;
  let label = c.label;
  if (x.state === 'installing') {
    const p = progress(x);
    if (p !== null) label = `${c.label} · ${p}%`;
  }
  const out = { state: c.state, label, reason: '' };
  if (x.state === 'needs-attention') out.reason = reason(x.reason);
  return out;
}

// reason keeps a server's reason to its first two sentences.
export function reason(text) {
  const t = String(text || '').trim();
  if (!t) return 'Something went wrong. Try again, or remove it.';
  // A sentence ends at . ! or ? before a space ("truckersmp.com" ends none).
  const ends = [...t.matchAll(/[.!?](?=\s|$)/g)];
  return dot(capitalize(t.slice(0, ends.length > 1 ? ends[1].index + 1 : t.length).trim()));
}

// plain is the neutral words for a card's state: "Always on" for a core
// extension, else the fact, or '' when a chip says it.
export function plain(x) {
  if (x.core) return ALWAYS_ON;
  if (CHIPS[x.state]) return '';
  return PLAIN[x.state] || '';
}

// from is the card's second line: upstream, licence and size.
export function from(x) {
  const u = x.upstream || {};
  return [u.name ? `From ${u.name}` : '', u.license || '', size(x.size)].filter(Boolean).join(' · ');
}

// context is what a card needs to know of the others: their names, the
// ids a restart keeps (wanted or core), each card by id, and whether the
// next start leaves extensions out (skip).
export function context(doc) {
  const xs = list(doc && doc.extensions);
  return {
    names: names(doc),
    enabled: new Set(xs.filter((x) => x.wanted || x.core).map((x) => x.id)),
    byId: new Map(xs.map((x) => [x.id, x])),
    skip: !!(doc && doc.skip_once),
  };
}

// adds is what installing x also installs, by id, as the box adds it:
// what x requires, directly or not, that is neither wanted nor core.
export function adds(x, { enabled = new Set(), byId = new Map() } = {}) {
  const out = [];
  const seen = new Set([x.id]);
  const walk = (y) => {
    for (const id of list(y.requires)) {
      if (seen.has(id)) continue;
      seen.add(id);
      if (byId.has(id)) walk(byId.get(id));
      if (!enabled.has(id)) out.push(id);
    }
  };
  walk(x);
  return out;
}

// lines are what a card says under its summary, each {text, tone}: what
// a restart changes, its own notes, what installing it pulls in, and its
// helper's status lines. ctx is context(doc).
export function lines(x, ctx = {}) {
  const out = [];
  const say = (text, tone = '') => out.push({ text, tone });
  const note = NOTES[x.id];
  if (note) {
    say(note.always);
    if (!x.mounted && note.missing) say(note.missing);
  }
  if (x.state === 'restart-needed') {
    if (ctx.skip) say(SKIP);
    else if (x.wanted && !x.mounted) say('It is added at the next restart.');
    else if (!x.wanted && x.mounted && !x.core) say('It is removed at the next restart.');
    else say('Its changes take effect at the next restart.');
  }
  if (x.state === 'not-in-this-version') {
    const why = String(x.reason || '').trim();
    say(why ? reason(why) : "This version of VaporOS doesn't have it. It comes back with a version that does.");
  }
  const req = also(x, ctx);
  if (req.length && !x.wanted && !x.core) say(`Installing it also installs ${and(req)}.`);
  for (const s of list(x.status)) {
    const text = s && s.text ? dot(s.text) : '';
    // A helper may say what a note already says: once is enough.
    if (text && !out.some((l) => l.text.includes(text))) say(text, s.tone === 'error' || s.tone === 'warning' ? s.tone : '');
  }
  return out;
}

// also names what installing x also installs, by the names of their cards.
const also = (x, ctx) => adds(x, ctx).map((id) => (ctx.names || {})[id]).filter(Boolean);

// some names what a list of {name} holds: the names, and the ones without
// a name as "a game", "some games" or "one other game" (noun "game").
function some(items, noun) {
  const named = items.map((i) => String((i && i.name) || '').trim());
  const rest = named.filter((n) => !n).length;
  const out = [...new Set(named.filter(Boolean))];
  if (!out.length) return rest === 1 ? `a ${noun}` : `some ${noun}s`;
  if (rest) out.push(rest === 1 ? `one other ${noun}` : `${rest} other ${noun}s`);
  return and(out);
}

// can is "What it can do" in plain sentences: what the build verified and
// what it sets up in Steam, nothing the document does not carry.
export function can(x) {
  const out = [];
  for (const p of list(x.permissions)) {
    if (!PERMS[p]) continue;
    out.push(p === 'service' && x.runs_as_root ? `${PERMS[p]} as root` : PERMS[p]);
  }
  if (x.module_options) out.push('Sets kernel module options (takes effect after a restart)');
  const st = x.steam || {};
  const forces = list(st.forces);
  const hooks = list(st.hooks);
  const shortcuts = list(st.shortcuts);
  if (forces.length) out.push(`Makes Steam run ${some(forces, 'game')} with Proton`);
  if (hooks.length) out.push(`Starts ${some(hooks, 'game')} through VaporOS so ${x.name || x.id} can join in`);
  if (shortcuts.length) out.push(`Adds ${some(shortcuts, 'shortcut')} to your Steam library`);
  if (x.web) out.push('Has its own web page, reachable from your network');
  return out.map(dot);
}

// downloads is "What it downloads": each download in plain words, with a
// warning when it runs code VaporOS cannot check.
export function downloads(x) {
  return list(x.downloads).map((d) => {
    const when = WHEN[d.when] ? `, ${WHEN[d.when]}` : '';
    const text = [`${capitalize(String(d.what || 'Files'))} from ${d.from || 'the internet'}${when}.`];
    const unchecked = d.checked !== 'pinned';
    if (d.runs_code && unchecked) text.push("It runs as a program, and VaporOS can't check these files.");
    else if (CHECKED[d.checked]) text.push(CHECKED[d.checked]);
    return { text: text.join(' '), warn: !!(d.runs_code && unchecked) };
  });
}

// moreLabel names a card's fold by what it holds: what it can do, what it
// downloads, or only what is good to know.
export function moreLabel(can, downloads) {
  if (can && downloads) return 'What it can do and downloads';
  if (can) return 'What it can do';
  return downloads ? 'What it downloads' : 'Good to know';
}

// drives are what a drive setting offers, from GET /storage's disks, as
// {path, text, system}: each adopted game drive that is there by its
// folder (mounted_at), then the system drive, always, as /var.
export function drives(disks) {
  const xs = list(disks).filter(Boolean);
  const text = (name, d) => [name, d && Number(d.free) > 0 ? `${bytes(d.free)} free` : ''].filter(Boolean).join(' · ');
  const out = [];
  for (const d of xs) {
    const path = String(d.mounted_at || '');
    if (d.adopted && !d.missing && !d.is_system && path.startsWith('/') && path !== '/var' && !out.some((o) => o.path === path)) {
      out.push({ path, text: text(String(d.label || d.model || 'Drive'), d), system: false });
    }
  }
  out.push({ path: '/var', text: text('System drive', xs.find((d) => d.is_system && (d.label === 'vos_data' || d.partlabel === 'vos_data'))), system: true });
  return out;
}

// required are the settings to pick before adding x: only a drive can be.
export const required = (x) => list(x && x.settings).filter((s) => s && s.key && s.required && s.type === 'disk');

// driveChoices are a drive setting's options, {value, text, disabled}:
// "" (none), which a required one shows only until a drive is picked and
// never takes, the drives, and a saved one that isn't there.
export function driveChoices(s, ds) {
  const v = String(s.value || '');
  const out = v && s.required ? [] : [{ value: '', text: 'Choose a drive', disabled: !!s.required }];
  for (const d of list(ds)) out.push({ value: d.path, text: d.text });
  if (v && !out.some((o) => o.value === v)) out.push({ value: v, text: "A drive that isn't connected" });
  return out;
}

// settingHint is a setting's hint: its help and, when it needs one, the
// restart.
export function settingHint(s) {
  return [s.help ? dot(s.help) : '', s.restart ? AFTER_RESTART : ''].filter(Boolean).join(' ');
}

// choiceLabel shows a choice's identifier as words: "low_noise" → "Low noise".
export const choiceLabel = (c) => capitalize(String(c).replace(/_+/g, ' '));

// webLabel is the words of the link to an extension's own page.
export function webLabel(x) {
  const label = String((x.web && x.web.label) || x.name || x.id || '').trim();
  return /^open\s/i.test(label) ? label : `Open ${label}`;
}

// webURL is an extension's own page on this PC, on the address the
// control center was reached at.
export function webURL(x, hostname) {
  const port = Number(x && x.web && x.web.port);
  if (!Number.isInteger(port) || port < 1 || port > 65535 || !hostname) return '';
  return `http://${hostname}:${port}/`;
}

// webLink is webURL while it runs: mounted, wanted or core, web_running.
export const webLink = (x, hostname) => (x && x.mounted && (x.wanted || x.core) && x.web_running ? webURL(x, hostname) : '');

// removal says whether a card offers Remove: only for one the user added
// (wanted, never core), with why (Remove disabled) while one a restart
// keeps (wanted or core) needs it. ctx is context(doc).
export function removal(x, { names = {}, enabled = new Set() } = {}) {
  if (x.core || !x.wanted) return { show: false, why: '' };
  const by = list(x.required_by).filter((id) => enabled.has(id)).map((id) => names[id] || id);
  return { show: true, why: by.length ? `${and(by)} ${by.length === 1 ? 'needs' : 'need'} it. Remove ${by.length === 1 ? 'that' : 'those'} first.` : '' };
}

// canInstall: neither core nor wanted, in this version and not on its way
// already. One removed until the restart can come back before it.
export const canInstall = (x) => !x.core && !x.wanted && !['not-in-this-version', 'installing'].includes(x.state);

// moduleHint is why x's module options take the password, in can()'s words.
export const moduleHint = (x) => `${x.name || x.id} sets kernel module options, so VaporOS asks for your password.`;

// passwordHint says why adding x asks for the VaporOS password (the first
// of it and what it adds that runs as root or sets module options), or ''.
export function passwordHint(x, ctx = {}) {
  const byId = ctx.byId || new Map();
  const all = [x, ...adds(x, ctx).map((id) => byId.get(id)).filter(Boolean)];
  const root = all.find((y) => y.runs_as_root);
  if (root) return `${root.name || root.id} runs as root, so VaporOS asks for your password.`;
  const mod = all.find((y) => y.module_options);
  return mod ? moduleHint(mod) : '';
}

const STAYS = 'stays, and VaporOS sets it up again.';

// installNote is the install dialog's note: what it also installs, and
// when it comes (one removed until the restart stays). ctx is context(doc).
export function installNote(x, doc, ctx = {}) {
  const a = also(x, ctx);
  // restart.auto speaks of a restart already needed; a new one may happen by itself.
  const r = (doc && doc.restart) || {};
  const when = x.mounted ? `It ${STAYS}` : ctx.skip ? `It downloads now. ${SKIP}` : `It downloads now and is added at the next restart${!r.needed || r.auto ? ', which VaporOS does by itself when nobody is playing' : ''}.`;
  return a.length ? `It also installs ${and(a)}. ${when}` : when;
}

// installedText is the notice once the box took an install.
export const installedText = (x, ctx = {}) => `${x.name || x.id} ${x.mounted ? STAYS : `is downloading. ${ctx.skip ? SKIP : "It's added at the next restart."}`}`;

// wantsPassword: the box refused a change for the VaporOS password it
// needs (403 naming the password), not for anything else.
export const wantsPassword = (err) => !!err && err.status === 403 && /password/i.test(String(err.message || ''));

// restartText is the page's restart card: what the next restart adds and
// removes, by name, else the server's reason (always while the next start
// leaves extensions out), else in general.
export function restartText(doc) {
  const xs = doc && doc.skip_once ? [] : list(doc && doc.extensions).filter((x) => x.state === 'restart-needed');
  const add = xs.filter((x) => x.wanted && !x.mounted).map((x) => x.name || x.id);
  const drop = xs.filter((x) => !x.wanted && x.mounted && !x.core).map((x) => x.name || x.id);
  const is = (n) => (n.length === 1 ? 'is' : 'are');
  if (add.length && drop.length) return `${and(add)} ${is(add)} added and ${and(drop)} ${is(drop)} removed at the next restart.`;
  if (add.length) return `${and(add)} ${is(add)} added at the next restart.`;
  if (drop.length) return `${and(drop)} ${is(drop)} removed at the next restart.`;
  const why = doc && doc.restart && String(doc.restart.reason || '').trim();
  return why ? reason(why) : 'Changes to extensions take effect at the next restart.';
}

// names maps each extension's id to its name.
export function names(doc) {
  const out = {};
  for (const x of list(doc && doc.extensions)) out[x.id] = x.name || x.id;
  return out;
}

// rowLine is the Extensions row on System: [text, tone] as system.js's
// rows are, "3 installed · 1 needs attention".
export function rowLine(doc) {
  if (!doc || !Array.isArray(doc.extensions)) return ["Couldn't load", 'cold'];
  const xs = doc.extensions;
  const installed = xs.filter((x) => x.mounted).length;
  const attention = xs.filter((x) => x.state === 'needs-attention').length;
  const busy = xs.find((x) => x.state === 'installing');
  const head = installed ? `${installed} installed` : 'None installed';
  if (attention) return [`${head} · ${attention} ${attention === 1 ? 'needs' : 'need'} attention`, 'cold'];
  if (busy) {
    const p = progress(busy);
    return [`${head} · installing ${busy.name || busy.id}${p === null ? '' : ` · ${p}%`}`, 'hot'];
  }
  if (doc.restart && doc.restart.needed) return [`${head} · restart needed`, 'hot'];
  return [head, ''];
}
