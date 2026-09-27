/* The wall's camera: a pan (x, y) in screen pixels and a scale s, applied to
   the whole wall as one transform. Pure, so the arithmetic is tested. */

export const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v));

/* Zoom by k, keeping the screen point (cx, cy) over the same spot. */
export function zoomAt(view, cx, cy, k, min, max) {
  const s = clamp(view.s * k, min, max);
  const r = s / view.s;
  return { s, x: cx - (cx - view.x) * r, y: cy - (cy - view.y) * r };
}

/* The view that shows the box (bx, by, bw, bh) whole and centred in a
   vw x vh window, leaving a margin, never past max. */
export function fitBox(bx, by, bw, bh, vw, vh, max, margin = 0.9) {
  const s = Math.min(max, (vw / bw) * margin, (vh / bh) * margin);
  return { s, x: (vw - bw * s) / 2 - bx * s, y: (vh - bh * s) / 2 - by * s };
}

/* Columns that lay n cells of cw x ch out in roughly the window's shape. */
export function columnsFor(n, cw, ch, vw, vh) {
  return Math.max(1, Math.round(Math.sqrt((n * ch * vw) / (cw * vh))));
}
