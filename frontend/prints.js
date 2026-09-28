/* The wall: every print, oldest first, on one table you pan and zoom, with
   new ones arriving live. Close up, the receipts are the preview's own
   markup, so a print looks here as it did to whoever sent it. Far out, one
   canvas draws them as sketches instead. Either way only what is on screen
   is drawn, so the wall costs the same with ten prints or ten thousand. */

import { fillReceipt } from './js/receipt.js';
import { rowLengths } from './js/paper.js';
import { zoomAt, fitBox, columnsFor, slotAt, visibleSlots, slotUnder } from './js/view.js';

const cssInt = (name) => parseInt(getComputedStyle(document.documentElement).getPropertyValue(name), 10);
const COLS = cssInt('--cols');
const $ = (id) => document.getElementById(id);
const viewport = $('viewport'), world = $('world'), sketch = $('sketch'), tpl = $('receipt');
const detail = $('detail'), detailSheet = $('detailSheet'), detailMeta = $('detailMeta');
const fresh = $('fresh'), count = $('count'), status = $('status');
const ctx = sketch.getContext('2d');

/* Past this the text is larger than on the real page, and no clearer. */
const MAX_SCALE = 1.5;
/* From here up real receipts are shown; below, the canvas sketches. */
const DETAIL_SCALE = 0.3;
/* A sketch this many pixels wide or more shows its photo. */
const THUMB_WIDTH = 24;
const GAP = 64;
/* A photo's width in printer dots: its height is in the same unit. */
const DOTS = 384;
/* Receipts kept built after scrolling away, so coming back is instant. */
const CACHE = 300;
/* Receipts built per frame: zooming in past DETAIL_SCALE wants dozens at
   once, and building them all in one frame stalls it. The rest stay
   sketched until their turn. */
const BUILD_PER_FRAME = 12;

const prints = [];      // oldest first
const index = new Map(); // id -> position in prints
const built = new Map(); // id -> receipt element, on the wall or cached
const shown = new Set(); // ids whose receipt is on the wall now
const thumbs = new Map(); // id -> Image, for the sketches
let view = { x: 0, y: 0, s: 1 };
let minScale = 0.05;
let cols = 1;
let geo = null; // the receipt's measurements, see measure()

const photoURL = (p) => `/api/prints/${encodeURIComponent(p.id)}/photo`;

function receipt(print, lazy = true) {
  const el = tpl.content.firstElementChild.cloneNode(true);
  fillReceipt(el, print, COLS, photoURL(print), lazy);
  return el;
}

/* ---- the receipt, measured once ---------------------------------------- */

/* Everything the grid and the sketches need, read off one real receipt: its
   size, where its body is, and a bar for every line of its frame. Footer
   bars are measured from the body's bottom, which a photo pushes down. */
function measure() {
  const el = receipt({ id: '', kind: 'text', text: 'x' });
  world.append(el);
  const r = el.getBoundingClientRect();
  const body = el.querySelector('.body').getBoundingClientRect();
  const bodyBottom = body.bottom - r.top;

  const bar = (node, dy) => {
    const range = document.createRange();
    range.selectNodeContents(node);
    const t = range.getBoundingClientRect();
    return { x: t.left - r.left, y: t.top - r.top - dy, w: t.width, h: t.height, color: getComputedStyle(node).color };
  };
  const [head, foot] = [...el.querySelectorAll('.fixed')];
  const [divTop, divBot] = [...el.querySelectorAll('.divider')];

  const g = {
    w: r.width,
    h: r.height,
    bodyX: body.left - r.left,
    bodyY: body.top - r.top,
    bodyW: body.width,
    bodyH: body.height,
    lineH: body.height / cssInt('--rows'),
    ink: getComputedStyle(el.querySelector('.mirror')).color,
    head: [...head.children, divTop].map((n) => bar(n, 0)),
    foot: [divBot, ...foot.children].map((n) => bar(n, bodyBottom)),
  };
  el.remove();

  g.pad = GAP;
  g.pitchX = g.w + GAP;
  // Every slot is as tall as the tallest receipt, a square photo.
  g.pitchY = g.h - g.bodyH + g.bodyW + GAP;
  return g;
}

const bodyHeight = (p) => (p.kind === 'photo' ? (geo.bodyW * p.rows) / DOTS : geo.bodyH);
const heightOf = (p) => geo.h - geo.bodyH + bodyHeight(p);
const rowCount = () => Math.max(1, Math.ceil(prints.length / cols));
const boxOf = (i) => [...slotAt(i, cols, geo), geo.w, heightOf(prints[i])];

/* ---- drawing ------------------------------------------------------------- */

let queued = false, settle = 0;

/* Everything that moves the camera or changes the wall asks for a frame;
   however many ask, it is drawn once. */
function redraw() {
  if (queued) return;
  queued = true;
  requestAnimationFrame(draw);
}

function draw() {
  queued = false;
  world.style.transform = `translate(${view.x}px, ${view.y}px) scale(${view.s})`;

  const slots = visibleSlots(view, innerWidth, innerHeight, cols, rowCount(), geo, 1);
  if (view.s >= DETAIL_SCALE) {
    const behind = showReceipts(slots);
    paintSketches(slots, (id) => shown.has(id));
    if (behind) redraw();
  } else {
    showReceipts(null);
    paintSketches(slots);
  }

  // Promoted only while moving: a layer that stays promoted is scaled as
  // a bitmap and turns blurry.
  world.classList.add('moving');
  clearTimeout(settle);
  settle = setTimeout(() => world.classList.remove('moving'), 200);
}

function* indicesIn({ c0, c1, r0, r1 }) {
  for (let r = r0; r <= r1; r++) {
    for (let c = c0; c <= c1; c++) {
      const i = r * cols + c;
      if (i < prints.length) yield i;
    }
  }
}

/* Put the receipts in `slots` on the wall and take every other one off.
   null takes them all off. True if some are still waiting to be built. */
function showReceipts(slots) {
  const want = new Set();
  let budget = BUILD_PER_FRAME, behind = false;
  if (slots) {
    for (const i of indicesIn(slots)) {
      const p = prints[i];
      let el = built.get(p.id);
      if (!el && budget === 0) { behind = true; continue; }
      want.add(p.id);
      if (!el) {
        budget--;
        el = receipt(p);
        el.dataset.id = p.id;
        built.set(p.id, el);
      }
      const [x, y] = slotAt(i, cols, geo);
      el.style.left = `${x}px`;
      el.style.top = `${y}px`;
      if (!shown.has(p.id)) { world.append(el); shown.add(p.id); }
    }
  }

  for (const id of shown) {
    if (want.has(id)) continue;
    built.get(id)?.remove();
    shown.delete(id);
  }

  // Forget the longest-unshown receipts once the cache is full.
  for (const id of built.keys()) {
    if (built.size <= CACHE + shown.size) break;
    if (!shown.has(id)) built.delete(id);
  }

  return behind;
}

/* Each receipt as a sketch: the paper, its frame as bars, a bar per row of
   text, and its photo once it is big enough to see. In passes by colour,
   since changing colour is what costs. `skip` leaves out the ones already
   shown as real receipts. */
function paintSketches(slots, skip = () => false) {
  const dpr = devicePixelRatio || 1;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, innerWidth, innerHeight);

  const s = view.s, w = geo.w * s;
  const items = [];
  for (const i of indicesIn(slots)) {
    if (skip(prints[i].id)) continue;
    const [x, y] = slotAt(i, cols, geo);
    items.push({ p: prints[i], x: view.x + x * s, y: view.y + y * s });
  }

  ctx.fillStyle = '#fff';
  for (const { p, x, y } of items) ctx.fillRect(x, y, w, heightOf(p) * s);
  if (w < 12) return;

  const frame = (bars, dy) => (it) => {
    for (const b of bars) {
      ctx.fillStyle = b.color;
      ctx.fillRect(it.x + b.x * s, it.y + (b.y + dy(it.p)) * s + b.h * s * 0.25, b.w * s, b.h * s * 0.5);
    }
  };
  const bodyBottom = (p) => geo.bodyY + bodyHeight(p);
  items.forEach(frame(geo.head, () => 0));
  items.forEach(frame(geo.foot, bodyBottom));

  ctx.fillStyle = geo.ink;
  const charW = geo.bodyW / COLS, lineH = geo.lineH;
  for (const { p, x, y } of items) {
    if (p.kind !== 'text') continue;
    p.bars ??= rowLengths(p.text, COLS);
    p.bars.forEach((len, k) => {
      if (len) ctx.fillRect(x + geo.bodyX * s, y + (geo.bodyY + k * lineH + lineH * 0.3) * s, len * charW * s, lineH * 0.45 * s);
    });
  }

  for (const { p, x, y } of items) {
    if (p.kind !== 'photo') continue;
    const bx = x + geo.bodyX * s, by = y + geo.bodyY * s, bw = geo.bodyW * s, bh = bodyHeight(p) * s;
    const img = w >= THUMB_WIDTH ? thumb(p) : null;
    if (img?.complete && img.naturalWidth) {
      ctx.imageSmoothingEnabled = true;
      ctx.drawImage(img, bx, by, bw, bh);
    } else {
      ctx.fillStyle = '#9a9794';
      ctx.fillRect(bx, by, bw, bh);
    }
  }
}

function thumb(p) {
  let img = thumbs.get(p.id);
  if (!img) {
    img = new Image();
    img.onload = redraw;
    img.src = photoURL(p);
    thumbs.set(p.id, img);
  }
  return img;
}

function sizeSketch() {
  const dpr = devicePixelRatio || 1;
  sketch.width = Math.round(innerWidth * dpr);
  sketch.height = Math.round(innerHeight * dpr);
  redraw();
}

addEventListener('resize', sizeSketch);

/* ---- the camera ------------------------------------------------------- */

function zoom(cx, cy, k) {
  view = zoomAt(view, cx, cy, k, minScale, MAX_SCALE);
  redraw();
}

function wallSize() {
  return [geo.pad * 2 + cols * geo.pitchX - GAP, geo.pad * 2 + rowCount() * geo.pitchY - GAP];
}

function fitAll() {
  view = fitBox(0, 0, ...wallSize(), innerWidth, innerHeight, MAX_SCALE);
  redraw();
}

/* Glide to a receipt, sized to read. */
function flyTo(i) {
  const target = fitBox(...boxOf(i), innerWidth, innerHeight, 1, 0.8);
  const from = { ...view }, start = performance.now();
  const step = (now) => {
    const t = Math.min(1, (now - start) / 600), e = 1 - (1 - t) ** 3;
    view = { x: from.x + (target.x - from.x) * e, y: from.y + (target.y - from.y) * e, s: from.s + (target.s - from.s) * e };
    redraw();
    if (t < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

/* The print under a screen point, if any. Arithmetic, not the DOM, so it
   works the same over receipts and over sketches. */
function printAt(cx, cy) {
  const hit = slotUnder(view, cx, cy, cols, geo);
  const p = hit && prints[hit.index];
  return p && hit.dy <= heightOf(p) ? p : null;
}

/* Every pointer is tracked, so one finger pans and two pinch. A press that
   barely moves is a click on whatever receipt it landed on. */
const pointers = new Map();
let travelled = 0;

viewport.addEventListener('pointerdown', (e) => {
  viewport.setPointerCapture(e.pointerId);
  pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
  if (pointers.size === 1) travelled = 0;
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
  redraw();
});

function release(e) {
  if (!pointers.delete(e.pointerId)) return;
  if (pointers.size) return;
  viewport.classList.remove('grabbing');
  if (e.type === 'pointerup' && travelled < 6) {
    const p = printAt(e.clientX, e.clientY);
    if (p) open(p.id);
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
  else if (pan) { view = { ...view, x: view.x + pan[0], y: view.y + pan[1] }; redraw(); }
  else return;
  e.preventDefault();
});

/* ---- one print, full size -------------------------------------------- */

function open(id) {
  const p = prints[index.get(id)];
  if (!p) return;
  detailSheet.replaceChildren(receipt(p, false));
  detailMeta.textContent = `${new Date(p.time).toLocaleString()}  #${id.slice(0, 8)}`;
  detail.dataset.id = id;
  history.replaceState(null, '', `#${id}`);
  if (!detail.open) detail.showModal();
}

detail.addEventListener('close', () => history.replaceState(null, '', location.pathname));
$('detailBack').addEventListener('click', () => detail.close());
// A click on the table around the receipt, not on the receipt, closes it.
detail.addEventListener('click', (e) => { if (!e.target.closest('.roll, .back')) detail.close(); });

/* ---- the prints ------------------------------------------------------- */

function add(print) {
  if (index.has(print.id)) return false;
  index.set(print.id, prints.length);
  prints.push(print);
  return true;
}

/* Taking one out moves every later print up a slot. */
function remove(id) {
  const i = index.get(id);
  if (i === undefined) return;
  prints.splice(i, 1);
  index.clear();
  prints.forEach((p, k) => index.set(p.id, k));
  built.get(id)?.remove();
  built.delete(id);
  shown.delete(id);
  thumbs.delete(id);
  if (detail.open && detail.dataset.id === id) detail.close();
  showCount();
  redraw();
}

function showCount() {
  count.textContent = `${prints.length} ${prints.length === 1 ? 'PRINT' : 'PRINTS'}`;
  status.textContent = prints.length ? '' : 'nothing printed yet';
}

/* ---- live ------------------------------------------------------------- */

function arrived(print) {
  if (!add(print)) return;
  const el = receipt(print);
  el.dataset.id = print.id;
  el.classList.add('fresh');
  el.addEventListener('animationend', () => el.classList.remove('fresh'), { once: true });
  built.set(print.id, el);
  showCount();
  fresh.hidden = false;
  redraw();
}

fresh.addEventListener('click', () => {
  fresh.hidden = true;
  if (prints.length) flyTo(prints.length - 1);
});

/* Whatever printed while the stream was down: the newest page, oldest first. */
async function catchUp() {
  const { prints: page } = await (await fetch('/api/prints?limit=200')).json();
  for (const p of page.reverse()) arrived(p);
}

/* Until the first load is in, events wait: added early they would land
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
    const { prints: page, more } = await res.json();
    all.push(...page);
    if (!more || !page.length) return all.reverse();
    before = page[page.length - 1].id;
  }
}

async function start() {
  status.textContent = 'loading...';
  // Listen first, so nothing printed during the load is missed; add()
  // ignores anything that arrives twice.
  listen();
  // The receipt is measured in its real face: fonts.ready alone can resolve
  // before the face has even been asked for, and a fallback is wider.
  const [all] = await Promise.all([loadAll(), document.fonts.load('1em VT323')]);
  geo = measure();
  for (const p of all) add(p);
  showCount();

  cols = columnsFor(prints.length, geo.pitchX, geo.pitchY, innerWidth, innerHeight);
  const [W, H] = wallSize();
  minScale = Math.min(0.5, fitBox(0, 0, W, H, innerWidth, innerHeight, MAX_SCALE).s * 0.8);
  sizeSketch();

  loaded = true;
  waiting.splice(0).forEach((fn) => fn());

  const linked = index.get(location.hash.slice(1));
  if (linked !== undefined) { view = fitBox(...boxOf(linked), innerWidth, innerHeight, 1, 0.8); redraw(); open(prints[linked].id); }
  else if (prints.length <= 300) fitAll();
  else { view = fitBox(...boxOf(prints.length - 1), innerWidth, innerHeight, 1, 0.3); redraw(); }

  viewport.focus();
}

start().catch(() => {
  status.textContent = 'could not load the prints';
  status.classList.add('bad');
});
