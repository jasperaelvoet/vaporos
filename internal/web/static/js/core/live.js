// core/live.js: the event stream. Events are hints, GETs the truth (R1).
// It keeps the connection state (G8) and closes the stream for the
// back/forward cache and while the reconnection scene is up.

import { API, goLogin, refreshMe, transport } from './api.js';

const topics = new Map(); // topic → Set of handlers
const reconnectFns = new Set();
const visibleFns = new Set();
const linkFns = new Set();
let source = null;
let opened = false;
let retryDelay = 1000;
let retryTimer = 0;
let connectingTimer = 0;
let auth = true;
let paused = false;

// state: live, connecting or offline; since: when it stopped being live.
export const link = { state: 'connecting', since: Date.now() };

// Handlers get the data and whether it arrived live (not from the replay).
export function on(topic, fn) {
  let set = topics.get(topic);
  if (!set) {
    set = new Set();
    topics.set(topic, set);
    if (source) listen(source, topic);
  }
  set.add(fn);
  return () => set.delete(fn);
}

// onReconnect: after a drop, the back/forward cache or 30 s hidden.
export function onReconnect(fn) {
  reconnectFns.add(fn);
  return () => reconnectFns.delete(fn);
}

// onVisible: back after at least 2 s hidden (a PIN prompt may wait, CRIT 38).
export function onVisible(fn) {
  visibleFns.add(fn);
  return () => visibleFns.delete(fn);
}

export function onLink(fn) {
  linkFns.add(fn);
  fn(link);
  return () => linkFns.delete(fn);
}

let openedAt = 0;
const replayWindow = () => Date.now() - openedAt < 400;

function listen(es, topic) {
  es.addEventListener(topic, (ev) => {
    let data;
    try {
      data = JSON.parse(ev.data);
    } catch {
      return;
    }
    const live = !replayWindow();
    for (const fn of topics.get(topic) || []) {
      try {
        fn(data, live);
      } catch (e) {
        console.error(`event ${topic}:`, e);
      }
    }
  });
}

let offlineTimer = 0;

// A stream down for 2 s reads as connecting; still down after 10 s (the
// browser keeps retrying a box that is off), as offline.
function setLink(state) {
  if (state === 'connecting') {
    if (!connectingTimer && link.state === 'live') {
      connectingTimer = setTimeout(() => {
        connectingTimer = 0;
        if (!source || source.readyState !== 1) apply('connecting');
      }, 2000);
    } else if (link.state !== 'live' && link.state !== 'offline') {
      apply('connecting');
    }
    if (!offlineTimer) {
      offlineTimer = setTimeout(() => {
        offlineTimer = 0;
        if (!source || source.readyState !== 1) apply('offline');
      }, 10000);
    }
    return;
  }
  clearTimeout(connectingTimer);
  clearTimeout(offlineTimer);
  connectingTimer = offlineTimer = 0;
  apply(state);
}

function apply(state) {
  if (link.state === state) return;
  const wasLive = link.state === 'live';
  link.state = state;
  if (state === 'live') link.since = 0;
  else if (wasLive || !link.since) link.since = Date.now();
  for (const fn of linkFns) fn(link);
}

// auth false (installer, first run): a refused stream only retries.
export function connect({ auth: needAuth = true } = {}) {
  auth = needAuth;
  paused = false;
  open(false);
}

function open(passive) {
  const ES = transport().EventSource;
  if (source || paused || typeof ES === 'undefined') return;
  setLink('connecting');
  const es = new ES(API + '/events' + (passive ? '?passive=1' : ''));
  source = es;
  for (const t of topics.keys()) listen(es, t);
  es.onopen = () => {
    openedAt = Date.now();
    setLink('live');
    retryDelay = 1000;
    if (opened) for (const fn of reconnectFns) fn();
    opened = true;
  };
  es.onerror = () => {
    if (es.readyState !== 2) {
      setLink('connecting'); // the browser retries by itself
      return;
    }
    // Refused or gone: back off, check the session, reopen passively.
    es.close();
    if (source === es) source = null;
    setLink('offline');
    schedule();
  };
}

function schedule() {
  clearTimeout(retryTimer);
  if (paused) return;
  retryTimer = setTimeout(async () => {
    if (auth) {
      try {
        const me = await refreshMe();
        if (!me.authenticated) {
          goLogin({ ended: true });
          return;
        }
      } catch {
        /* unreachable: just retry */
      }
    }
    open(true);
  }, retryDelay);
  retryDelay = Math.min(retryDelay * 2, 30000);
}

export function pause() {
  paused = true;
  clearTimeout(retryTimer);
  if (source) {
    source.close();
    source = null;
  }
}

export function resume() {
  paused = false;
  retryDelay = 1000;
  if (!source) open(true);
}

addEventListener('pagehide', () => {
  if (source) {
    source.close();
    source = null;
  }
});
addEventListener('pageshow', (e) => {
  if (e.persisted && opened && !paused) {
    open(true);
  }
});

let hiddenAt = 0;
document.addEventListener('visibilitychange', () => {
  if (document.hidden) {
    hiddenAt = Date.now();
    return;
  }
  const away = hiddenAt ? Date.now() - hiddenAt : 0;
  hiddenAt = 0;
  if (away < 2000 || !opened) return;
  for (const fn of visibleFns) fn(away);
  if (away > 30000) for (const fn of reconnectFns) fn();
});
