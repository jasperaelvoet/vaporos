// core/dom.js: DOM helpers. Text goes in as text, never as markup, and CSS
// gets numbers only as custom properties: the CSP allows nothing else.

export function byId(id) {
  const el = document.getElementById(id);
  if (!el) throw new Error(`#${id} is not on this page`);
  return el;
}

export const optionalById = (id) => document.getElementById(id);

export const $ = (sel, root = document) => root.querySelector(sel);
export const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

// h builds an element; a style key throws (the CSP blocks style attributes).
export function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === 'style') throw new Error('h(): no style attributes; use setVar');
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = v;
    else if (k === 'dataset') Object.assign(el.dataset, v);
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? '' : String(v));
  }
  append(el, kids);
  return el;
}

function append(el, kids) {
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false || kid === '') continue;
    el.append(kid instanceof Node ? kid : String(kid));
  }
}

export function fill(el, ...kids) {
  if (!el) return el;
  el.replaceChildren();
  append(el, kids);
  return el;
}

export function setText(id, text) {
  const el = byId(id);
  el.textContent = text ?? '';
  return el;
}

export const part = (root, name) => root.querySelector(`[data-part="${name}"]`);
export const parts = (root, name) => Array.from(root.querySelectorAll(`[data-part="${name}"]`));

export function cloneTpl(id) {
  return byId(id).content.firstElementChild.cloneNode(true);
}

export function setVar(el, name, value) {
  if (!name.startsWith('--')) throw new Error('setVar: custom properties only');
  el.style.setProperty(name, String(value));
}

const SVG = 'http://www.w3.org/2000/svg';

export function icon(name) {
  const svg = document.createElementNS(SVG, 'svg');
  svg.setAttribute('class', 'icon');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  svg.setAttribute('width', '20');
  svg.setAttribute('height', '20');
  const use = document.createElementNS(SVG, 'use');
  use.setAttribute('href', `#i-${name}`);
  svg.append(use);
  return svg;
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export const reducedMotion = () => matchMedia('(prefers-reduced-motion: reduce)').matches;

export const nextFrame = () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
