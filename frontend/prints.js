/* The wall: every print, oldest first, on one table you pan and zoom, with
   new ones arriving live. The receipts are the preview's own markup, so a
   print looks here exactly as it did to whoever sent it. */

import { fillReceipt } from './js/receipt.js';
import { zoomAt, fitBox, columnsFor } from './js/view.js';

const cssInt = (name) => parseInt(getComputedStyle(document.documentElement).getPropertyValue(name), 10);
const COLS = cssInt('--cols');
const $ = (id) => document.getElementById(id);
const viewport = $('viewport'), world = $('world'), tpl = $('receipt');
const detail = $('detail'), detailSheet = $('detailSheet'), detailMeta = $('detailMeta');
const fresh = $('fresh'), count = $('count'), status = $('status');

/* Past this the text is larger than on the real page, and no clearer. */
const MAX_SCALE = 1.5;
/* Below this a receipt is a few pixels wide: drop what only costs. */
const FAR_SCALE = 0.2;

const byId = new Map(); // id -> { print, el }
let view = { x: 0, y: 0, s: 1 };
let minScale = 0.05;
let newest = null;

const photoURL = (p) => `/api/prints/${encodeURIComponent(p.id)}/photo`;

function receipt(print, lazy = true) {
  const el = tpl.content.firstElementChild.cloneNode(true);
  fillReceipt(el, print, COLS, photoURL(print), lazy);
  return el;
}

function add(print) {
  if (byId.has(print.id)) return null;
  const el = receipt(print);
  el.dataset.id = print.id;
  byId.set(print.id, { print, el });
  world.append(el);
  newest = el;
  return el;
}

function remove(id) {
  const entry = byId.get(id);
  if (!entry) return;
  entry.el.remove();
  byId.delete(id);
  if (detail.open && detail.dataset.id === id) detail.close();
  showCount();
}

function showCount() {
  count.textContent = `${byId.size} ${byId.size === 1 ? 'PRINT' : 'PRINTS'}`;
  status.textContent = byId.size ? '' : 'nothing printed yet';
}

/* ---- the camera ------------------------------------------------------ */

let settle = 0;

function apply() {
  world.style.transform = `translate(${view.x}px, ${view.y}px) scale(${view.s})`;
  world.classList.toggle('far', view.s < FAR_SCALE);
  world.classList.add('moving');
  clearTimeout(settle);
  settle = setTimeout(() => world.classList.remove('moving'), 200);
}

function zoom(cx, cy, k) {
  view = zoomAt(view, cx, cy, k, minScale, MAX_SCALE);
  apply();
}

/* The box an element occupies on the unscaled wall. */
const boxOf = (el) => [el.offsetLeft, el.offsetTop, el.offsetWidth, el.offsetHeight];

function fitAll() {
  view = fitBox(0, 0, world.offsetWidth, world.offsetHeight, innerWidth, innerHeight, MAX_SCALE);
  apply();
}

/* Glide to a receipt, sized to read. */
function flyTo(el) {
  const target = fitBox(...boxOf(el), innerWidth, innerHeight, 1, 0.8);
  const from = { ...view }, start = performance.now();
  const step = (now) => {
    const t = Math.min(1, (now - start) / 600), e = 1 - (1 - t) ** 3;
    view = { x: from.x + (target.x - from.x) * e, y: from.y + (target.y - from.y) * e, s: from.s + (target.s - from.s) * e };
    apply();
    if (t < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

/* Every pointer is tracked, so one finger pans and two pinch. A press that
   barely moves is a click on whatever receipt it started on. */
const pointers = new Map();
let pressed = null, travelled = 0;

viewport.addEventListener('pointerdown', (e) => {
  viewport.setPointerCapture(e.pointerId);
  pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
  if (pointers.size === 1) { pressed = e.target.closest('.roll'); travelled = 0; }
  viewport.classList.add('grabbing');
});

viewport.addEventListener('pointermove', (e) => {
  const prev = pointers.get(e.pointerId);
  if (!prev) return;

  if (pointers.size === 1) {
    view = { ...view, x: view.x + e.clientX - prev.x, y: view.y + e.clientY - prev.y };
    travelled += Math.hypot(e.clientX - prev.x, e.clientY - prev.y);
  } else {
    const [a, b] = [...pointers.values()];
    const other = a === prev ? b : a;
    const before = Math.hypot(prev.x - other.x, prev.y - other.y);
    const after = Math.hypot(e.clientX - other.x, e.clientY - other.y);
    // Half the finger's movement, as it moves the midpoint by half.
    view = { ...view, x: view.x + (e.clientX - prev.x) / 2, y: view.y + (e.clientY - prev.y) / 2 };
    if (before > 0) view = zoomAt(view, (e.clientX + other.x) / 2, (e.clientY + other.y) / 2, after / before, minScale, MAX_SCALE);
    travelled = Infinity;
  }

  pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
  apply();
});

function release(e) {
  if (!pointers.delete(e.pointerId)) return;
  if (pointers.size === 0) {
    viewport.classList.remove('grabbing');
    if (e.type === 'pointerup' && pressed && travelled < 6) open(pressed.dataset.id);
    pressed = null;
  }
}

viewport.addEventListener('pointerup', release);
viewport.addEventListener('pointercancel', release);

viewport.addEventListener('wheel', (e) => {
  e.preventDefault();
  // A trackpad pinch arrives as a wheel with ctrlKey and small deltas.
  const px = e.deltaY * (e.deltaMode === 1 ? 16 : 1);
  zoom(e.clientX, e.clientY, Math.exp(-px * (e.ctrlKey ? 0.01 : 0.0015)));
}, { passive: false });

viewport.addEventListener('keydown', (e) => {
  const c = [innerWidth / 2, innerHeight / 2];
  const pan = { ArrowLeft: [80, 0], ArrowRight: [-80, 0], ArrowUp: [0, 80], ArrowDown: [0, -80] }[e.key];
  if (e.key === '+' || e.key === '=') zoom(...c, 1.25);
  else if (e.key === '-') zoom(...c, 0.8);
  else if (e.key === '0') fitAll();
  else if (pan) { view = { ...view, x: view.x + pan[0], y: view.y + pan[1] }; apply(); }
  else return;
  e.preventDefault();
});

/* ---- one print, full size -------------------------------------------- */

function open(id) {
  const entry = byId.get(id);
  if (!entry) return;
  const el = receipt(entry.print, false);
  detailSheet.replaceChildren(el);
  detailMeta.textContent = `${new Date(entry.print.time).toLocaleString()}  #${id.slice(0, 8)}`;
  detail.dataset.id = id;
  history.replaceState(null, '', `#${id}`);
  if (!detail.open) detail.showModal();
}

detail.addEventListener('close', () => history.replaceState(null, '', location.pathname));
$('detailBack').addEventListener('click', () => detail.close());
// A click on the table around the receipt, not on the receipt, closes it.
detail.addEventListener('click', (e) => { if (!e.target.closest('.roll, .back')) detail.close(); });

/* ---- live ------------------------------------------------------------- */

function arrived(print) {
  const el = add(print);
  if (!el) return;
  el.classList.add('fresh');
  showCount();
  fresh.hidden = false;
}

fresh.addEventListener('click', () => {
  fresh.hidden = true;
  if (newest) flyTo(newest);
});

/* Whatever printed while the stream was down: the newest page, oldest first. */
async function catchUp() {
  const { prints } = await (await fetch('/api/prints?limit=200')).json();
  for (const p of prints.reverse()) arrived(p);
}

/* Until the first load is in, events wait: appended early they would land
   before the older prints and scramble the order. */
let loaded = false;
const waiting = [];
const whenLoaded = (fn) => (loaded ? fn() : waiting.push(fn));

/* EventSource retries on its own after a dropped connection, but gives up
   for good on a refusal (too many viewers), so that case retries here. */
function listen(delay = 5000) {
  const es = new EventSource('/api/prints/stream');
  let opened = false;
  es.addEventListener('open', () => { if (opened) catchUp().catch(() => {}); opened = true; delay = 5000; });
  es.addEventListener('print', (e) => whenLoaded(() => arrived(JSON.parse(e.data))));
  es.addEventListener('hide', (e) => whenLoaded(() => remove(JSON.parse(e.data).id)));
  es.addEventListener('error', () => {
    if (es.readyState !== EventSource.CLOSED) return;
    setTimeout(() => listen(Math.min(delay * 2, 60000)), delay);
  });
}

/* ---- start ------------------------------------------------------------ */

async function loadAll() {
  const all = [];
  for (let before = ''; ;) {
    const res = await fetch(`/api/prints?limit=1000${before && `&before=${encodeURIComponent(before)}`}`);
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const { prints, more } = await res.json();
    all.push(...prints);
    if (!more || !prints.length) return all.reverse();
    before = prints[prints.length - 1].id;
  }
}

async function start() {
  status.textContent = 'loading...';
  // Listen first, so nothing printed during the load is missed; add()
  // ignores anything that arrives twice.
  listen();
  const prints = await loadAll();
  for (const p of prints) add(p);
  showCount();
  loaded = true;
  waiting.splice(0).forEach((fn) => fn());

  // Size the grid from a text receipt, the common shape, to the window's.
  const sample = world.firstElementChild;
  if (sample) {
    world.style.setProperty('--wall-cols',
      columnsFor(byId.size, sample.offsetWidth + 64, sample.offsetHeight + 64, innerWidth, innerHeight));
  }
  minScale = Math.min(0.5, fitBox(0, 0, world.offsetWidth, world.offsetHeight, innerWidth, innerHeight, MAX_SCALE).s * 0.8);

  const linked = byId.get(location.hash.slice(1));
  if (linked) { view = fitBox(...boxOf(linked.el), innerWidth, innerHeight, 1, 0.8); apply(); open(linked.print.id); }
  else if (byId.size <= 300 || !newest) fitAll();
  else { view = fitBox(...boxOf(newest), innerWidth, innerHeight, 1, 0.3); apply(); }

  viewport.focus();
}

start().catch(() => {
  status.textContent = 'could not load the prints';
  status.classList.add('bad');
});
