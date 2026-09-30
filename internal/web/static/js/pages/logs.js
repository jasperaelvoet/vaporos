// pages/logs.js: System › Logs (spec-cc-screens §11). GET /sunshine/logs is
// plain text, the last 2000 lines; it is read on load, on Refresh and,
// while Follow is on and the tab visible, every 5 s for at most 10 minutes
// (R2's bounded poll, sent passive). New lines are never announced. Each
// line keeps its words; a line Sunshine marks Error or Warning also gets a
// mark in the gutter, and its time is set back so the message reads first.

import { api, errorText } from '../core/api.js';
import { byId, h } from '../core/dom.js';
import { region } from '../ui/region.js';
import { notify, shell } from '../ui/shell.js';

const EVERY_MS = 5000;
const FOLLOW_MS = 10 * 60 * 1000;
const LINE = /^(\[[^\]]*\]:?)(\s*)(.*)$/;
const LEVEL = /^(Error|Fatal|Warning)\b/;

let text = null; // the log as last read
let timer = 0;
let since = 0; // when Follow was turned on
let reading = null;

const fmt = import('../fmt.js');
const capitalize = (s) => s.charAt(0).toUpperCase() + s.slice(1);

function lineNode(line) {
  const m = LINE.exec(line);
  const rest = m ? m[3] : line;
  const level = LEVEL.exec(rest);
  const el = h('span', { class: 'log-line' }, m ? [h('span', { class: 'log-time', text: m[1] }), m[2] || ' ', rest] : line || ' ');
  if (level) el.dataset.level = level[1] === 'Warning' ? 'warn' : 'error';
  return el;
}

function render(body) {
  const pre = byId('log-text');
  if (body === text) return;
  const atEnd = text === null || pre.scrollHeight - pre.scrollTop - pre.clientHeight < 48;
  text = body;
  const lines = body.replace(/\n+$/, '').split('\n');
  pre.dataset.empty = String(!body.trim());
  if (!body.trim()) pre.replaceChildren('The log is empty.');
  else pre.replaceChildren(...lines.map(lineNode));
  // Newest lines are at the end; stay there unless someone scrolled up.
  if (atEnd) pre.scrollTop = pre.scrollHeight;
  byId('log-copy').disabled = !body.trim();
}

async function load({ passive = false, button = null } = {}) {
  if (reading) return reading;
  if (button) {
    button.setAttribute('aria-busy', 'true');
    button.disabled = true;
  }
  reading = (async () => {
    try {
      const body = await api('GET', '/sunshine/logs', undefined, { text: true, passive });
      byId('log-error').hidden = true;
      byId('log-text').hidden = false;
      render(String(body ?? ''));
      const { clock } = await fmt;
      byId('log-meta').textContent = `Updated ${clock(new Date().toISOString())}`;
    } catch (err) {
      // A 502 here names what is missing (no Sunshine, no journal): say that,
      // not T5's "check the log" on the log's own page.
      const why = err.status === 502 && err.message ? capitalize(err.message) : await errorText(err);
      byId('log-error-text').textContent = `Couldn't load the log: ${why}`;
      byId('log-error').hidden = false;
      byId('log-text').hidden = text === null;
      if (byId('log-follow').checked) stopFollow('');
    } finally {
      const box = byId('log-box');
      if (box.hasAttribute('aria-busy')) region(box).ready();
      if (button) {
        button.removeAttribute('aria-busy');
        button.disabled = false;
      }
      reading = null;
    }
  })();
  return reading;
}

// ------------------------------------------------------------- Follow

function schedule() {
  clearTimeout(timer);
  if (!byId('log-follow').checked || document.hidden) return;
  timer = setTimeout(async () => {
    if (!byId('log-follow').checked || document.hidden) return;
    if (Date.now() - since >= FOLLOW_MS) {
      stopFollow('Paused after 10 minutes.', true);
      return;
    }
    await load({ passive: true });
    schedule();
  }, EVERY_MS);
}

function startFollow() {
  since = Date.now();
  byId('log-follow').checked = true;
  byId('log-follow-text').textContent = '';
  byId('log-resume').hidden = true;
  load({ passive: true });
  schedule();
}

function stopFollow(why, resume = false) {
  clearTimeout(timer);
  byId('log-follow').checked = false;
  byId('log-follow-text').textContent = why;
  byId('log-resume').hidden = !resume;
}

// ---------------------------------------------------------------- Copy

async function copyLog(btn) {
  if (!text) return;
  const { copyText } = await import('../ui/clipboard.js');
  if ((await copyText(text, { from: btn })) === 'copied') {
    notify('Copied.', { kind: 'ok' });
    return;
  }
  // The browser refused: select the log so it can be copied by hand.
  const range = document.createRange();
  range.selectNodeContents(byId('log-text'));
  const sel = getSelection();
  sel.removeAllRanges();
  sel.addRange(range);
  notify(matchMedia('(pointer: coarse)').matches ? 'The log is selected. Press and hold it to copy.' : 'The log is selected. Copy it with your keyboard.', { kind: 'info' });
}

shell('logs').then(() => {
  const follow = byId('log-follow');
  byId('log-refresh').addEventListener('click', (e) => load({ button: e.currentTarget }));
  byId('log-retry').addEventListener('click', () => load());
  byId('log-copy').addEventListener('click', (e) => copyLog(e.currentTarget));
  follow.addEventListener('change', () => (follow.checked ? startFollow() : stopFollow('')));
  byId('log-resume').addEventListener('click', () => {
    startFollow();
    follow.focus();
  });
  // Hidden, Follow waits; back in view, it goes on (inside its 10 minutes).
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) clearTimeout(timer);
    else if (follow.checked) {
      if (Date.now() - since >= FOLLOW_MS) stopFollow('Paused after 10 minutes.', true);
      else {
        load({ passive: true });
        schedule();
      }
    }
  });
  follow.disabled = false;
  byId('log-refresh').disabled = false;
  load();
});
