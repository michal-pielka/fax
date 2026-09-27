/* A print as its sender saw it before sliding it off: the same frame, the
   same styled text, the same dots. The wall at /prints is made of these. */

import { TOOLS, toolsIn, effective, runs } from './styles.js';

/* One byte of style bits per character, from the server's spans. */
export function stylesOf(text, spans = []) {
  const out = new Uint8Array(text.length);
  for (const { start, end, style } of spans) {
    const bits = TOOLS.reduce((b, t) => (style[t.json] ? b | t.bit : b), 0);
    for (let i = start; i < end && i < text.length; i++) out[i] |= bits;
  }
  return out;
}

/* Fill a clone of the receipt template in prints.html with one print. A
   lazy photo loads only once it is near the screen. */
export function fillReceipt(root, print, cols, photoURL, lazy = true) {
  for (const d of root.querySelectorAll('.divider')) d.textContent = '-'.repeat(cols);

  const body = root.querySelector('.body');
  if (print.kind === 'photo') {
    const img = document.createElement('img');
    img.className = 'photo';
    img.src = photoURL;
    img.alt = 'A printed photo';
    img.loading = lazy ? 'lazy' : 'eager';
    img.decoding = 'async';
    // The real size up front, so the receipt has its height before loading.
    img.width = 384;
    img.height = print.rows;
    body.classList.add('has-photo');
    body.append(img);
    return;
  }

  const text = print.text;
  const mirror = root.querySelector('.mirror');
  for (const { start, end, style } of runs(text, effective(text, stylesOf(text, print.spans)))) {
    const run = text.slice(start, end);
    if (!style) { mirror.append(run); continue; }
    const span = document.createElement('span');
    span.className = toolsIn(style).map(t => t.cls).join(' ');
    span.textContent = run;
    mirror.append(span);
  }
  // As in the preview: a trailing newline needs something after it to show.
  if (text.endsWith('\n')) mirror.append('\u200b');
}
