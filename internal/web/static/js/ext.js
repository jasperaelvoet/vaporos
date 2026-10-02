// ext.js: what System › Extensions and its row on System say, pure (jstest
// runs it). The document is GET /extensions (docs/CONTRACTS.md); the
// words follow design/voice.md, and a chip only ever names a state of
// design/tokens.json.

import { capitalize } from './copy.js';
import { bytes, percent } from './fmt.js';

// CHIPS: the extension states that are states (tokens.json state.*.label),
// by the state token they take. Every other state is a fact, said in
// neutral words (PLAIN).
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

// lines are what a card says under its summary, each {text, tone}: what
// a restart changes, its own notes, what installing it pulls in, and its
// helper's status lines. ctx is {names, enabled}, as for removal.
export function lines(x, { names = {}, enabled = new Set() } = {}) {
  const out = [];
  const note = NOTES[x.id];
  if (note) {
    out.push({ text: note.always, tone: '' });
    if (!x.mounted && note.missing) out.push({ text: note.missing, tone: '' });
  }
  if (x.state === 'restart-needed') {
    if (x.wanted && !x.mounted) out.push({ text: 'It is added at the next restart.', tone: '' });
    else if (!x.wanted && x.mounted && !x.core) out.push({ text: 'It is removed at the next restart.', tone: '' });
    else out.push({ text: 'Its changes take effect at the next restart.', tone: '' });
  }
  if (x.state === 'not-in-this-version') {
    out.push({ text: "This version of VaporOS doesn't have it. It comes back with a version that does.", tone: '' });
  }
  const req = list(x.requires).filter((id) => names[id] && !enabled.has(id)).map((id) => names[id]);
  if (req.length && !x.mounted && !x.wanted) out.push({ text: `Installing it also installs ${and(req)}.`, tone: '' });
  for (const s of list(x.status)) {
    const text = s && s.text ? dot(s.text) : '';
    // A helper may say what a note already says: once is enough.
    if (text && !out.some((l) => l.text.includes(text))) out.push({ text, tone: s.tone === 'error' || s.tone === 'warning' ? s.tone : '' });
  }
  return out;
}

// can is "What it can do": the verified permissions and the rest of what
// the extension may change, in plain sentences.
export function can(x) {
  const out = [];
  for (const p of list(x.permissions)) {
    if (!PERMS[p]) continue;
    out.push(p === 'service' && x.runs_as_root ? `${PERMS[p]} as root` : PERMS[p]);
  }
  if (x.needs_password && !x.runs_as_root) out.push('Sets kernel module options');
  if (list(x.settings).some((s) => s && s.restart)) out.push('Has settings that take effect after a restart');
  if (x.web) out.push('Has its own web page, reachable from your network');
  if (!out.length) out.push('Adds files only: nothing runs on its own');
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

// removal says whether a card offers Remove: never for a core extension,
// only for one that is wanted or mounted, and with why (Remove disabled)
// while an installed extension needs it. ctx is {names, enabled}: the
// ids of the extensions that are wanted, mounted or core.
export function removal(x, { names = {}, enabled = new Set() } = {}) {
  if (x.core || (!x.wanted && !x.mounted)) return { show: false, why: '' };
  const by = list(x.required_by).filter((id) => enabled.has(id)).map((id) => names[id] || id);
  return { show: true, why: by.length ? `${and(by)} ${by.length === 1 ? 'needs' : 'need'} it. Remove ${by.length === 1 ? 'that' : 'those'} first.` : '' };
}

// canInstall: neither core nor wanted, and in this version.
export const canInstall = (x) => !x.core && !x.wanted && x.state === 'not-installed';

// restartText is the page's restart card: what the next restart adds and
// removes, by name, else the server's reason, else in general.
export function restartText(doc) {
  const xs = list(doc && doc.extensions).filter((x) => x.state === 'restart-needed');
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
