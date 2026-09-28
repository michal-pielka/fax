/* The page: wires the textarea, the keys, the photo and the gesture to the
   pure parts in js/. What the receipt looks like and how it moves live there;
   this decides only what happens when. */

import { rowsOf, longestPrefix } from './js/paper.js';
import { TOOLS, toolsIn, effective, reconcile, rangeHas, runs, toDocument } from './js/styles.js';
import { fit, dither, unpack, encodePNG } from './js/photo.js';
import { sheet } from './js/gesture.js';

/* Read from CSS so --cols and --rows stay the single source of truth. */
const cssInt = (name) => parseInt(getComputedStyle(document.documentElement).getPropertyValue(name), 10);
const COLS = cssInt('--cols'), ROWS = cssInt('--rows');
const $ = (id) => document.getElementById(id);
const ta = $('text'), mirror = $('mirror'), roll = $('roll'), keys = $('keys'), status = $('status');
const body = $('body'), canvas = $('photo'), file = $('file');

$('divTop').textContent = '-'.repeat(COLS);
$('divBot').textContent = '-'.repeat(COLS);

const fits = (text) => rowsOf(text, COLS) <= ROWS;

/* One byte per character of ta.value. Raw: what was applied. What shows and
   what prints is effective(). */
let styles = new Uint8Array(0);

/* The typing mode: the style every typed character gets. Set only by the
   keys or their shortcuts, never by what happens to be near the caret, and
   kept until switched off the same way. */
let mode = 0;

/* The value after the last edit we accounted for. */
let lastText = '';

/* Set by checkPrinter: while true, the standing message is the offline
   warning, and typing must not talk over it. */
let offline = false;

const selectionHas = (bit) => rangeHas(ta.value, styles, ta.selectionStart, ta.selectionEnd, bit);

/* A key with text selected styles the selection and nothing else. With no
   selection it switches the typing mode. */
function applyTool(t) {
  const a = ta.selectionStart, b = ta.selectionEnd;

  if (a === b) {
    mode ^= t.bit;
    syncKeys();
    return;
  }

  const allOn = selectionHas(t.bit);
  if (allOn === null) return;
  for (let i = a; i < b; i++) styles[i] = allOn ? styles[i] & ~t.bit : styles[i] | t.bit;

  render();
  syncKeys();
}

/* ---- rendering ------------------------------------------------------ */

/* The mirror shows the styled text under a transparent textarea. Same font,
   same width, same wrapping, so its glyphs sit exactly under the invisible
   ones the caret moves through. */
function render() {
  const text = ta.value;
  const frag = document.createDocumentFragment();

  for (const { start, end, style } of runs(text, effective(text, styles))) {
    const run = text.slice(start, end);
    if (!style) { frag.append(run); continue; }
    const span = document.createElement('span');
    span.className = toolsIn(style).map(t => t.cls).join(' ');
    span.textContent = run;
    frag.append(span);
  }

  /* A trailing newline needs something after it or the browser drops the
     empty last row; the textarea itself does the same trick internally. */
  if (text.endsWith('\n')) frag.append('\u200b');

  mirror.replaceChildren(frag);
}

/* ---- the keys ------------------------------------------------------- */

/* The label sits in its own element so the reverse key can draw a filled
   cell around its letter. A key must not move focus: if the paper is being
   written on it keeps the caret and selection, and if it is not, pressing a
   key must not open a phone's keyboard. pointerdown covers touch, where
   cancelling mousedown alone comes too late. Nothing here calls focus(). */
const key = (cls, label, title, onClick) => {
  const b = document.createElement('button');
  b.type = 'button'; b.className = cls; b.title = title;
  b.append(Object.assign(document.createElement('span'), { textContent: label }));
  b.addEventListener('pointerdown', e => e.preventDefault());
  b.addEventListener('mousedown', e => e.preventDefault());
  b.addEventListener('click', onClick);
  keys.append(b);
  return b;
};

TOOLS.forEach(t => {
  /* The reverse key's letter is drawn by CSS inside a filled cell. */
  const b = key(t.key + ' style', t.key === 'invert' ? '' : t.label, t.title, () => applyTool(t));
  b.dataset.key = t.key;
  b.setAttribute('aria-pressed', 'false');
});

/* Two more keys, not styles: one puts a picture on the paper, the other
   takes it off again. */
key('pick', 'PHOTO', 'Print a photo instead', () => file.click());
key('remove', 'REMOVE', 'Back to words', () => setPhoto(null));

/* Pressed means: with a selection, every selected character has it; with a
   bare caret, it is part of the typing mode. */
function syncKeys() {
  const caret = ta.selectionStart === ta.selectionEnd;
  keys.querySelectorAll('button.style').forEach(btn => {
    const t = TOOLS.find(x => x.key === btn.dataset.key);
    const on = caret ? !!(mode & t.bit) : !!selectionHas(t.bit);
    btn.setAttribute('aria-pressed', String(on));
  });
}

/* selectionchange arrives lazily in Chromium; keyup and pointerup cover
   the gap. All three only refresh what the keys show. */
document.addEventListener('selectionchange', () => { if (document.activeElement === ta) syncKeys(); });
ta.addEventListener('keyup', syncKeys);
ta.addEventListener('pointerup', syncKeys);

/* ---- editing -------------------------------------------------------- */

/* Edits whose outcome is known before they land are refused before they
   land: no rollback, nothing for the undo stack to see. */
ta.addEventListener('beforeinput', (e) => {
  let data = null;
  if (e.inputType === 'insertText') data = e.data ?? '';
  else if (e.inputType === 'insertParagraph' || e.inputType === 'insertLineBreak') data = '\n';
  if (data === null) return;

  const next = ta.value.slice(0, ta.selectionStart) + data + ta.value.slice(ta.selectionEnd);
  if (!fits(next)) e.preventDefault();
});

/* Paste is the one edit routinely larger than the paper: trim it to fit
   rather than refuse it, which on a phone silently ate most pastes. */
ta.addEventListener('paste', (e) => {
  e.preventDefault();

  /* A picture on the clipboard -- copied from Photos, a screenshot -- is
     the quietest way to send one. */
  const pic = imageIn(e.clipboardData);
  if (pic) { loadPhoto(pic); return; }

  const text = e.clipboardData?.getData('text/plain') || '';
  if (!text) return;

  const before = ta.value.slice(0, ta.selectionStart), after = ta.value.slice(ta.selectionEnd);
  const part = longestPrefix(text, (p) => fits(before + p + after));
  /* execCommand is deprecated and still the only way to keep the undo
     stack; it also fires input, so reconcile runs as for typing. */
  if (!document.execCommand('insertText', false, part)) {
    ta.setRangeText(part, ta.selectionStart, ta.selectionEnd, 'end');
    ta.dispatchEvent(new Event('input'));
  }

  setStatus(part.length < text.length ? 'trimmed to fit the paper' : '');
});

ta.addEventListener('input', () => {
  /* Anything that slipped past beforeinput -- IME, drop, autocorrect.
     Rare, so a plain restore is acceptable here. */
  if (!fits(ta.value)) {
    ta.value = lastText;
  }

  styles = reconcile(lastText, ta.value, styles, mode, ta.selectionEnd);
  lastText = ta.value;
  render();
  syncKeys();
  refreshStatus();
});

ta.addEventListener('keydown', (e) => {
  const meta = e.metaKey || e.ctrlKey;
  if (!meta) return;
  if (e.key === 'Enter') { e.preventDefault(); if (receipt.resting) submit(); return; }
  const tool = TOOLS.find(t => t.shortcut === e.key.toLowerCase());
  if (tool) { e.preventDefault(); applyTool(tool); }
});

/* The textarea scrolls to chase the caret even with the content fitting,
   by a pixel or two on some platforms; the mirror would not follow. */
ta.addEventListener('scroll', () => { ta.scrollTop = 0; });

function setStatus(msg, bad) {
  status.textContent = msg;
  status.classList.toggle('bad', !!bad);
}

/* The only instruction on the page, and only once there is something to
   print. Replaced by whatever happens next. */
const hint = () => (photo || ta.value.trim() ? 'slide the receipt up to print' : '');

/* The standing message: the offline warning while the printer is off,
   otherwise the hint. Transient messages (printing..., errors) set the
   status directly. */
function refreshStatus() {
  if (offline) setStatus('printer is offline', true);
  else setStatus(hint());
}

/* ---- a photo instead of words ---------------------------------------- */

/* The printer's picture: PHOTO_W dots wide, at most PHOTO_H tall. The server
   checks exactly these two numbers. The width is the canvas's, set in the
   HTML, so there is one copy of it on this side. */
const PHOTO_W = canvas.width, PHOTO_H = 384;

/* The picture on the paper, or null: its packed rows and its height. The
   receipt is either words or this, never both. */
let photo = null;

function imageIn(dt) {
  if (!dt) return null;
  const f = [...(dt.files || [])].find(f => f.type.startsWith('image/'));
  if (f) return f;
  const it = [...(dt.items || [])].find(i => i.type.startsWith('image/'));
  return it ? it.getAsFile() : null;
}

/* Read a picture, fit it to the paper, and turn it into dots. */
async function loadPhoto(blob) {
  if (!blob || !blob.type.startsWith('image/')) { setStatus('that is not a picture', true); return; }

  let bmp;
  try {
    bmp = await decode(blob);
  } catch {
    setStatus('could not read that picture', true);
    return;
  }

  const { sy, sh, h } = fit(bmp.width, bmp.height, PHOTO_W, PHOTO_H);

  const work = document.createElement('canvas');
  work.width = PHOTO_W; work.height = h;
  const ctx = work.getContext('2d', { willReadFrequently: true });
  ctx.drawImage(bmp, 0, sy, bmp.width, sh, 0, 0, PHOTO_W, h);
  if (bmp.close) bmp.close();

  const bits = dither(ctx.getImageData(0, 0, PHOTO_W, h).data, PHOTO_W, h);
  setPhoto({ bits, h });
}

/* createImageBitmap honours the camera's orientation tag and is fast;
   an <img> is the fallback where it is missing. */
function decode(blob) {
  if (window.createImageBitmap) return createImageBitmap(blob);
  return new Promise((ok, bad) => {
    const url = URL.createObjectURL(blob), img = new Image();
    img.onload = () => { URL.revokeObjectURL(url); ok(img); };
    img.onerror = () => { URL.revokeObjectURL(url); bad(new Error('decode')); };
    img.src = url;
  });
}

/* Put a picture on the paper, or take it off. The words stay in the hidden
   textarea and come back with it. */
function setPhoto(p) {
  photo = p;
  body.classList.toggle('has-photo', !!p);
  keys.classList.toggle('photo', !!p);

  if (p) {
    canvas.height = p.h;
    const ctx = canvas.getContext('2d');
    const img = ctx.createImageData(PHOTO_W, p.h);
    unpack(p.bits, PHOTO_W, p.h, img.data);
    ctx.putImageData(img, 0, 0);
    ta.blur();
  } else {
    ta.focus();
  }

  refreshStatus();
}

file.addEventListener('change', () => { if (file.files[0]) loadPhoto(file.files[0]); file.value = ''; });

/* Dropped on the paper, from a desktop. */
roll.addEventListener('dragover', (e) => { if (e.dataTransfer.types.includes('Files')) { e.preventDefault(); roll.classList.add('over'); } });
roll.addEventListener('dragleave', () => roll.classList.remove('over'));
roll.addEventListener('drop', (e) => {
  roll.classList.remove('over');
  const pic = imageIn(e.dataTransfer);
  if (!pic) return;
  e.preventDefault();
  loadPhoto(pic);
});

/* Pasted anywhere on the page, not only into the words. */
document.addEventListener('paste', (e) => {
  if (document.activeElement === ta) return;
  const pic = imageIn(e.clipboardData);
  if (pic) { e.preventDefault(); loadPhoto(pic); }
});

/* ---- printing: slide the receipt off the top ------------------------ */

/* Picked up by its frame -- anything but the writing area, unless that holds
   a photo, and never the keys. */
const receipt = sheet(roll, {
  canGrab: (e) => !(e.target.closest('.body') && !photo) && !e.target.closest('.keys'),
  onThrow: () => submit(),
});

/* Send the job and let the receipt go. The request and the flight run
   together; whichever finishes last decides when the next sheet arrives,
   so the paper is never swapped in front of someone's eyes. */
async function submit() {
  const doc = photo ? null : toDocument(ta.value, styles);
  if (!photo && !doc.text.trim()) {
    setStatus('nothing to print', true);
    receipt.settle();
    return;
  }

  /* The same door for both: the content type says which. */
  const request = photo
    ? { headers: { 'Content-Type': 'image/png' }, body: encodePNG(photo.bits, PHOTO_W, photo.h) }
    : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(doc) };

  const gone = receipt.fly();
  setStatus('printing...');

  let ok = false, message = '';

  try {
    /* The server answers within its own chain of timeouts (35s at worst);
       a little past that, a silent connection is declared dead rather than
       leaving the sheet flown and the page stuck on "printing...". */
    const res = await fetch('/api/print', { method: 'POST', signal: AbortSignal.timeout(45000), ...request });

    if (res.ok) ok = true;
    else {
      const { error } = await res.json().catch(() => ({}));
      message = error || `failed (${res.status})`;
    }
  } catch (err) {
    message = err?.name === 'TimeoutError' ? 'the printer is not answering' : 'could not reach the printer';
  }

  await gone;

  if (ok) {
    clear();
    setStatus('printed');
  } else {
    /* The words come back with the paper: with no queue behind the
       printer, a failed send means they exist only here. */
    setStatus(message, true);
  }

  /* The fresh sheet after a print, or the same one back after a refusal. */
  receipt.arrive();
}

/* A fresh sheet. The typing mode is the sender's setting, not the sheet's,
   so it survives. */
function clear() {
  ta.value = '';
  styles = new Uint8Array(0);
  lastText = '';
  render();
  syncKeys();
  if (photo) setPhoto(null);
}

/* For keyboards and screen readers: the same job, without the gesture. */
$('print').addEventListener('click', () => { if (receipt.resting) submit(); });

/* Asked once on arrival and again when the tab comes back, not polled:
   the print request itself is the authority, and answers offline or out
   of paper on its own. This only saves typing into a machine that is off. */
async function checkPrinter() {
  let s;

  try {
    s = await (await fetch('/api/state')).json();
  } catch {
    return; /* Unknown is not offline. Let the print request decide. */
  }

  /* Refresh the standing message only when it changed or went stale; a
     transient one (printing..., an error) is left alone. */
  offline = !s.online;
  if (offline || status.textContent === 'printer is offline') refreshStatus();
}

checkPrinter();
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') checkPrinter();
});

/* ---- the question in the corner ------------------------------------- */

const about = $('about');

$('help').addEventListener('click', () => about.showModal());
$('aboutBack').addEventListener('click', () => about.close());

/* A click on the backdrop closes it; the backdrop is the dialog itself
   outside its content, so the target is the dialog and nothing inside. */
about.addEventListener('click', (e) => { if (e.target === about) about.close(); });

/* Its dividers are drawn from the same column count as the receipt's. */
document.querySelectorAll('.about-divider').forEach(d => { d.textContent = '-'.repeat(COLS); });

clear();
ta.focus();
