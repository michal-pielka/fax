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

/* The wall is a grid of equal slots: slot i sits at column i % cols, row
   i / cols. g is { pad, pitchX, pitchY, w } in wall pixels: the margin, the
   distance between slots, and a receipt's width. */
export const slotAt = (i, cols, g) =>
  [g.pad + (i % cols) * g.pitchX, g.pad + Math.floor(i / cols) * g.pitchY];

/* The columns and rows of slots at least partly on screen, `margin` slots
   wider on every side. Empty (c0 > c1 or r0 > r1) when none are. */
export function visibleSlots(view, vw, vh, cols, rows, g, margin = 0) {
  const x0 = -view.x / view.s, y0 = -view.y / view.s;
  const x1 = x0 + vw / view.s, y1 = y0 + vh / view.s;
  return {
    c0: Math.max(0, Math.floor((x0 - g.pad) / g.pitchX) - margin),
    c1: Math.min(cols - 1, Math.floor((x1 - g.pad) / g.pitchX) + margin),
    r0: Math.max(0, Math.floor((y0 - g.pad) / g.pitchY) - margin),
    r1: Math.min(rows - 1, Math.floor((y1 - g.pad) / g.pitchY) + margin),
  };
}

/* The slot under a screen point, and where in it, or null in a gap. */
export function slotUnder(view, cx, cy, cols, g) {
  const wx = (cx - view.x) / view.s - g.pad, wy = (cy - view.y) / view.s - g.pad;
  const col = Math.floor(wx / g.pitchX), row = Math.floor(wy / g.pitchY);
  const dx = wx - col * g.pitchX, dy = wy - row * g.pitchY;
  if (col < 0 || col >= cols || row < 0 || dx > g.w) return null;
  return { index: row * cols + col, dy };
}
