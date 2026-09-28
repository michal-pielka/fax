/* A receipt drawn on a canvas instead of built from HTML, for the wall at
   /prints, where hundreds are on screen at once. Laid out like .paper in
   styles.css: change one, change the other. */

import { TOOLS, effective } from './styles.js';
import { stylesOf } from './receipt.js';
import { rowRanges } from './paper.js';

/* The frame, as in index.html and backend/internal/render. */
const TITLE = 'FAX';
const HEAD = 'THE SLOWEST SOCIAL NETWORK';
const FOOT = ['COMMITTED TO PHYSICAL MEDIA', '*** fax.pielka.sh ***'];

/* A photo's width in printer dots; its height is in the same unit. */
const DOTS = 384;
/* Room around the paper for its shadow, in CSS pixels. */
export const MARGIN = 16;

const BIT = Object.fromEntries(TOOLS.map((t) => [t.key, t.bit]));

/* The receipt's measurements in CSS pixels at font size `f`, the same sums
   styles.css makes: 3ch margins, 0.85em top and bottom, a double-size
   title on a 1.1 line, then one line each for tagline and dividers. */
export function layout(ctx, font, f, cols, rows) {
  ctx.font = `${f}px ${font}`;
  const ch = ctx.measureText('0').width;
  const padX = 3 * ch, padY = 0.85 * f, titleH = 2.2 * f;
  return {
    font, f, ch, cols, rows, padX, padY, titleH,
    w: cols * ch + 2 * padX,
    bodyW: cols * ch,
    bodyY: padY + titleH + 2 * f,
  };
}

export const bodyHeight = (L, p) => (p.kind === 'photo' ? (L.bodyW * p.rows) / DOTS : L.rows * L.f);
export const receiptHeight = (L, p) => L.bodyY + bodyHeight(L, p) + 3 * L.f + L.padY;

/* Draw print p into ctx, whose transform already maps CSS pixels to the
   canvas. k is that scale, for the few things canvas does not scale: shadow
   blur, and whether a photo is being shrunk or enlarged. */
export function drawReceipt(ctx, L, p, k, colors, photo) {
  const h = receiptHeight(L, p);

  ctx.save();
  ctx.shadowColor = colors.shadow;
  ctx.shadowBlur = 10 * k;
  ctx.fillStyle = colors.paper;
  paper(ctx, L.w, h, 7, 3.5);
  ctx.fill();
  ctx.restore();

  const line = (size, box) => {
    ctx.font = `${size}px ${L.font}`;
    const m = ctx.measureText('X');
    return (box - (m.fontBoundingBoxAscent + m.fontBoundingBoxDescent)) / 2 + m.fontBoundingBoxAscent;
  };
  const centred = (text, y, size, box, color) => {
    const base = line(size, box);
    ctx.textAlign = 'center';
    ctx.fillStyle = color;
    ctx.fillText(text, L.w / 2, y + base);
    ctx.textAlign = 'left';
  };

  const f = L.f, divider = '-'.repeat(L.cols);
  let y = L.padY;
  centred(TITLE, y, 2 * f, L.titleH, colors.ink); y += L.titleH;
  centred(HEAD, y, f, f, colors.dim); y += f;
  centred(divider, y, f, f, colors.ghost); y += f;

  if (p.kind === 'photo') drawPhoto(ctx, L, p, k, photo, y, colors);
  else drawText(ctx, L, p, y, line(f, f), colors);
  y += bodyHeight(L, p);

  centred(divider, y, f, f, colors.ghost); y += f;
  for (const text of FOOT) { centred(text, y, f, f, colors.dim); y += f; }
}

/* The torn paper: a zigzag along the top and bottom edges. */
function paper(ctx, w, h, tooth, depth) {
  const n = Math.ceil(w / tooth);
  ctx.beginPath();
  ctx.moveTo(0, depth);
  for (let i = 0; i < n; i++) {
    ctx.lineTo(Math.min(w, (i + 0.5) * tooth), 0);
    ctx.lineTo(Math.min(w, (i + 1) * tooth), depth);
  }
  ctx.lineTo(w, h - depth);
  for (let i = n - 1; i >= 0; i--) {
    ctx.lineTo(Math.min(w, (i + 0.5) * tooth), h);
    ctx.lineTo(i * tooth, h - depth);
  }
  ctx.closePath();
}

/* Row by row, in runs of one style, as the preview's mirror shows it. */
function drawText(ctx, L, p, top, base, colors) {
  const { f, ch } = L, text = p.text;
  const shown = effective(text, stylesOf(text, p.spans));
  ctx.font = `${f}px ${L.font}`;

  rowRanges(text, L.cols).slice(0, L.rows).forEach(([a, b], r) => {
    const y = top + r * f;
    for (let i = a; i < b;) {
      let j = i + 1;
      while (j < b && shown[j] === shown[i]) j++;
      const x = L.padX + (i - a) * ch, run = text.slice(i, j), style = shown[i], w = (j - i) * ch;

      if (style & BIT.invert) { ctx.fillStyle = colors.ink; ctx.fillRect(x, y, w, f); }
      ctx.fillStyle = style & BIT.invert ? colors.paper : colors.ink;
      ctx.fillText(run, x, y + base);
      if (style & BIT.bold) ctx.fillText(run, x + 0.5, y + base);
      if (style & BIT.under) ctx.fillRect(x, y + base + 2, w, Math.max(2, f / 12));
      i = j;
    }
  });
}

function drawPhoto(ctx, L, p, k, photo, top, colors) {
  const h = bodyHeight(L, p);
  if (!photo?.complete || !photo.naturalWidth) {
    ctx.fillStyle = colors.ghost;
    ctx.fillRect(L.padX, top, L.bodyW, h);
    return;
  }
  // Pixelated when enlarged, like the preview; smoothed when shrunk.
  ctx.imageSmoothingEnabled = (L.bodyW * k) / DOTS < 1;
  ctx.drawImage(photo, L.padX, top, L.bodyW, h);
}
