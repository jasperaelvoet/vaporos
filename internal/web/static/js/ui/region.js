// ui/region.js: a data region's life (ARCH §6.11, R3): skeleton, then data,
// an empty state or an inline error with Try again, never a toast alone.

import { h } from '../core/dom.js';
import { errorText } from '../core/api.js';

const LATE_MS = 10_000;

export function region(el) {
  const late = setTimeout(() => {
    if (el.getAttribute('aria-busy') === 'true') {
      el.dataset.late = 'true';
    }
  }, LATE_MS);
  const settle = (state) => {
    clearTimeout(late);
    el.dataset.state = state;
    delete el.dataset.late;
    el.removeAttribute('aria-busy');
    settleMain();
  };
  return {
    el,
    loading() {
      el.setAttribute('aria-busy', 'true');
      el.dataset.state = 'loading';
    },
    ready(render) {
      const kids = render ? render() : null;
      if (kids) el.replaceChildren(...[].concat(kids));
      settle('ready');
    },
    empty(text) {
      el.replaceChildren(h('p', { class: 'region-empty', text }));
      settle('empty');
    },
    async error(err, retry, vars) {
      const text = await errorText(err, vars);
      const kids = [h('p', { class: 'region-error-text', text })];
      if (retry) kids.push(h('button', { class: 'btn small', type: 'button', text: 'Try again', onclick: retry }));
      el.replaceChildren(h('div', { class: 'region-error', role: 'alert' }, kids));
      settle('error');
    },
  };
}

export function settleMain() {
  const main = document.getElementById('main');
  if (!main || !document.documentElement.dataset.boot) return;
  if (document.querySelector('main [data-region][aria-busy="true"]')) return;
  main.removeAttribute('aria-busy');
  const status = document.getElementById('main-status');
  if (status) status.textContent = '';
}
