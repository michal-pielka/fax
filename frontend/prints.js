/* The wall: every print, oldest first, on one table you pan and zoom, with
   new ones arriving live. One canvas draws it all. Each receipt is drawn
   once per size into its own small canvas, a tile, and every frame paints
   the tiles on screen at the size closest to the zoom, so detail fades out
   gradually as you zoom away and the wall costs the same with ten prints
   or ten thousand. */

import { fillReceipt } from './js/receipt.js';
import { layout, drawReceipt, receiptHeight, MARGIN } from './js/tile.js';
import { clamp, zoomAt, fitBox, columnsFor, slotAt, visibleSlots, slotUnder } from './js/view.js';

const root = getComputedStyle(document.documentElement);
const css = (name) => root.getPropertyValue(name).trim();
const COLS = parseInt(css('--cols'), 10), ROWS = parseInt(css('--rows'), 10);
const $ = (id) => document.getElementById(id);
const viewport = $('viewport'), canvas = $('wall'), tpl = $('receipt');
const detail = $('detail'), detailSheet = $('detailSheet'), detailMeta = $('detailMeta');
const fresh = $('fresh'), count = $('count'), status = $('status');
const ctx = canvas.getContext('2d');

/* The receipt's type size on the wall: fixed, so the layout never depends
   on the window. The page's own maximum, so zoom 1 is the real thing. */
const FONT_SIZE = 31;
const FONT = 'VT323';
const GAP = 64;
/* Past this the text is larger than on the real page, and no clearer. */
const MAX_SCALE = 1.5;
/* Tile sizes, each half the next. A frame uses the smallest one that is at
   least as sharp as the screen needs. */
const LEVELS = [1 / 16, 1 / 8, 1 / 4, 1 / 2, 1, 2, 4];
/* Milliseconds a frame may spend drawing new tiles; the rest wait for the
   next frame, shown meanwhile at whatever size is at hand. */
const FRAME_BUDGET = 6;
/* Pixels of tiles kept, about 100 MB. The least recently used go first. */
const PIXEL_BUDGET = 24e6;
const FADE_MS = 600;

const colors = {
  paper: css('--paper'), ink: css('--ink'), dim: css('--ink-dim'), ghost: css('--ink-ghost'),
  shadow: `rgb(${css('--shade')} / 0.13)`,
};

const prints = [];      // oldest first
const index = new Map(); // id -> position in prints
const tiles = new Map(); // `${id}@${level}` -> canvas, oldest use first
const photos = new Map(); // id -> Image
const arrivedAt = new Map(); // id -> time it came in live, for the fade
let tilePixels = 0;
let view = { x: 0, y: 0, s: 1 };
let minScale = 0.05;
let cols = 1;
let L = null; // the receipt's layout, see js/tile.js
let grid = null; // slot spacing for js/view.js

const photoURL = (p) => `/api/prints/${encodeURIComponent(p.id)}/photo`;
const heightOf = (p) => receiptHeight(L, p);
const rowCount = () => Math.max(1, Math.ceil(prints.length / cols));
const boxOf = (i) => [...slotAt(i, cols, grid), L.w, heightOf(prints[i])];

/* ---- tiles --------------------------------------------------------------- */

function photoOf(p) {
  if (p.kind !== 'photo') return null;
  let img = photos.get(p.id);
  if (!img) {
    img = new Image();
    // Tiles drawn before the picture arrived show a blank; redraw them.
    img.onload = () => { dropTiles(p.id); redraw(); };
    img.src = photoURL(p);
    photos.set(p.id, img);
  }
  return img;
}

function makeTile(p, k) {
  const t = document.createElement('canvas');
  t.width = Math.ceil((L.w + 2 * MARGIN) * k);
  t.height = Math.ceil((heightOf(p) + 2 * MARGIN) * k);
  const c = t.getContext('2d');
  c.setTransform(k, 0, 0, k, MARGIN * k, MARGIN * k);
  drawReceipt(c, L, p, k, colors, photoOf(p));

  const key = `${p.id}@${k}`;
  tiles.set(key, t);
  tilePixels += t.width * t.height;
  for (const [old, o] of tiles) {
    if (tilePixels <= PIXEL_BUDGET || old === key) break;
    tiles.delete(old);
    tilePixels -= o.width * o.height;
  }
  return t;
}

/* A tile of p at level k if one exists, marked as just used. */
function tileAt(p, k) {
  const key = `${p.id}@${k}`, t = tiles.get(key);
  if (t) { tiles.delete(key); tiles.set(key, t); }
  return t;
}

function dropTiles(id) {
  for (const k of LEVELS) {
    const key = `${id}@${k}`, t = tiles.get(key);
    if (t) { tiles.delete(key); tilePixels -= t.width * t.height; }
  }
}

/* ---- drawing ------------------------------------------------------------- */

let queued = false;

/* Everything that moves the camera or changes the wall asks for a frame;
   however many ask, it is drawn once. */
function redraw() {
  if (queued) return;
  queued = true;
  requestAnimationFrame(draw);
}

function draw(now) {
  queued = false;
  const dpr = devicePixelRatio || 1, s = view.s;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, innerWidth, innerHeight);

  const want = LEVELS.find((k) => k >= s * dpr) ?? LEVELS[LEVELS.length - 1];
  const deadline = performance.now() + FRAME_BUDGET;
  let behind = false;

  const { c0, c1, r0, r1 } = visibleSlots(view, innerWidth, innerHeight, cols, rowCount(), grid, 0);
  for (let r = r0; r <= r1; r++) {
    for (let c = c0; c <= c1; c++) {
      const i = r * cols + c;
      if (i >= prints.length) break;
      const p = prints[i];

      let t = tileAt(p, want);
      if (!t && performance.now() < deadline) t = makeTile(p, want);
      if (!t) {
        behind = true;
        t = nearestTile(p, want);
      }

      const [x, y] = slotAt(i, cols, grid);
      const sx = view.x + (x - MARGIN) * s, sy = view.y + (y - MARGIN) * s;
      const w = (L.w + 2 * MARGIN) * s, h = (heightOf(p) + 2 * MARGIN) * s;

      const since = arrivedAt.has(p.id) ? now - arrivedAt.get(p.id) : FADE_MS;
      ctx.globalAlpha = Math.min(1, since / FADE_MS);
      if (since < FADE_MS) behind = true;
      else arrivedAt.delete(p.id);

      if (t) ctx.drawImage(t, sx, sy, w, h);
      else {
        ctx.fillStyle = colors.paper;
        ctx.fillRect(sx + MARGIN * s, sy + MARGIN * s, L.w * s, heightOf(p) * s);
      }
    }
  }

  ctx.globalAlpha = 1;
  if (behind) redraw();
}

/* Any tile of p, the closest size to k first. */
function nearestTile(p, k) {
  const byDistance = [...LEVELS].sort((a, b) => Math.abs(Math.log(a / k)) - Math.abs(Math.log(b / k)));
  for (const level of byDistance) {
    const t = tiles.get(`${p.id}@${level}`);
    if (t) return t;
  }
  return null;
}

function sizeCanvas() {
  const dpr = devicePixelRatio || 1;
  canvas.width = Math.round(innerWidth * dpr);
  canvas.height = Math.round(innerHeight * dpr);
  redraw();
}

addEventListener('resize', sizeCanvas);

/* ---- the camera ------------------------------------------------------- */

function zoom(cx, cy, k) {
  view = zoomAt(view, cx, cy, k, minScale, MAX_SCALE);
  clampView();
  redraw();
}

function wallSize() {
  return [grid.pad * 2 + cols * grid.pitchX - GAP, grid.pad * 2 + rowCount() * grid.pitchY - GAP];
}

/* A drag can never lose the wall: a strip of it always stays on screen.
   Applied to the hand-driven moves only, not to the scripted flights. */
const SLACK = 120;
function clampView() {
  const [w, h] = wallSize();
  view = {
    ...view,
    x: clamp(view.x, SLACK - w * view.s, innerWidth - SLACK),
    y: clamp(view.y, SLACK - h * view.s, innerHeight - SLACK),
  };
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

/* The print under a screen point, if any. */
function printAt(cx, cy) {
  const hit = slotUnder(view, cx, cy, cols, grid);
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
  if (!prev) {
    viewport.classList.toggle('over', !!printAt(e.clientX, e.clientY));
    return;
  }

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
  clampView();
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
  else if (pan) { view = { ...view, x: view.x + pan[0], y: view.y + pan[1] }; clampView(); redraw(); }
  else return;
  e.preventDefault();
});

/* ---- one print, full size, as real HTML ------------------------------- */

function open(id) {
  const p = prints[index.get(id)];
  if (!p) return;
  const el = tpl.content.firstElementChild.cloneNode(true);
  fillReceipt(el, p, COLS, photoURL(p), false);
  detailSheet.replaceChildren(el);
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
  dropTiles(id);
  photos.delete(id);
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
  arrivedAt.set(print.id, performance.now());
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
  // Canvas text needs the face loaded, or it draws, and measures, a fallback.
  const [all] = await Promise.all([loadAll(), document.fonts.load(`${FONT_SIZE}px ${FONT}`)]);

  L = layout(ctx, FONT, FONT_SIZE, COLS, ROWS);
  // Every slot is as tall as the tallest receipt, a square photo.
  grid = { pad: GAP, pitchX: L.w + GAP, pitchY: receiptHeight(L, { kind: 'photo', rows: 384 }) + GAP, w: L.w };

  for (const p of all) add(p);
  showCount();

  cols = columnsFor(prints.length, grid.pitchX, grid.pitchY, innerWidth, innerHeight);
  minScale = Math.min(0.5, fitBox(0, 0, ...wallSize(), innerWidth, innerHeight, MAX_SCALE).s * 0.8);
  sizeCanvas();

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
