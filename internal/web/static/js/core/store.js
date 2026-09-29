// core/store.js: browser storage, guarded: private windows and blocked site
// data throw, and the pages must work without it. Nothing secret is kept.

function area(kind) {
  try {
    return kind === 'local' ? globalThis.localStorage : globalThis.sessionStorage;
  } catch {
    return null;
  }
}

export function getJSON(kind, key, fallback = null) {
  try {
    const raw = area(kind)?.getItem(key);
    return raw == null ? fallback : JSON.parse(raw);
  } catch {
    return fallback;
  }
}

export function setJSON(kind, key, value) {
  try {
    area(kind)?.setItem(key, JSON.stringify(value));
    return true;
  } catch {
    return false;
  }
}

export function getString(kind, key, fallback = '') {
  try {
    return area(kind)?.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}

export function setString(kind, key, value) {
  try {
    area(kind)?.setItem(key, String(value));
    return true;
  } catch {
    return false;
  }
}

export function remove(kind, key) {
  try {
    area(kind)?.removeItem(key);
  } catch {
    /* nothing to remove */
  }
}

// The last-known state (DECISIONS NEW-1): GET /status and the Wake-on-LAN
// adapters, so a page can say how to wake the box once it stops answering.
const LAST = 'vos-last';

export function loadSnapshot() {
  const s = getJSON('local', LAST);
  return s && typeof s === 'object' && s.v === 1 ? s : null;
}

export function saveSnapshot(parts, now = Date.now()) {
  const prev = loadSnapshot() || { v: 1 };
  const next = { ...prev, ...parts, v: 1, at: new Date(now).toISOString() };
  if (next.status) {
    const st = { ...next.status };
    delete st.csrf;
    next.status = st;
  }
  setJSON('local', LAST, next);
  return next;
}
