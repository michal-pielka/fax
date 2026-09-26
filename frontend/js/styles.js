/* A style per character. The textarea holds the text and the browser owns
   editing; this owns what the textarea cannot: one byte of style bits for
   every character of its value, kept the same length at all times. */

/* Only what the printer does. Anything else would look right here and
   silently vanish on paper. cls is the mirror's class, json the server's
   field name. */
export const TOOLS = [
  { key: 'bold',   label: 'B', title: 'Bold (Ctrl+B)',                    bit: 1, shortcut: 'b', cls: 'b', json: 'bold' },
  { key: 'under',  label: 'U', title: 'Underline (Ctrl+U)',               bit: 2, shortcut: 'u', cls: 'u', json: 'underline' },
  { key: 'invert', label: 'A', title: 'Reverse, white on black (Ctrl+I)', bit: 4, shortcut: 'i', cls: 'i', json: 'invert' },
];

export const toolsIn = (style) => TOOLS.filter(t => style & t.bit);

const isSpace = (c) => c === ' ' || c === '\n';

/* Styles as they appear on paper. A space has no glyph, so a style on it is
   a stray underline or a black cell: it keeps one only between two styled
   characters on the same line, where it joins them. Newlines never do. */
export function effective(text, raw) {
  const out = new Uint8Array(raw);
  let i = 0;

  while (i < text.length) {
    if (!isSpace(text[i])) { i++; continue; }

    let j = i;
    while (j < text.length && text[j] === ' ') j++;

    const before = i > 0 && text[i - 1] !== '\n' && raw[i - 1];
    const after = j < text.length && text[j] !== '\n' && raw[j];
    if (!(before && after)) out.fill(0, i, j);

    if (j < text.length && text[j] === '\n') { out[j] = 0; j++; }
    i = Math.max(j, i + 1);
  }

  return out;
}

/* The styles for newT, given the styles of oldT, after the textarea changed
   underneath us. Every edit a textarea can make -- typing, deleting, paste,
   drop, IME, undo -- replaces one contiguous range, so the old and new text
   differ in one place. Find it, and decide what the inserted characters look
   like. `caret` is the selection end after the edit. */
export function reconcile(oldT, newT, styles, mode, caret) {
  if (oldT === newT) return styles;

  let p = 0;
  const max = Math.min(oldT.length, newT.length);
  while (p < max && oldT[p] === newT[p]) p++;

  /* Repeated characters make the prefix ambiguous ("aa" -> "aaa"); the
     caret sits right after whatever was inserted, so trust it over the
     scan when the two disagree. */
  const grew = newT.length - oldT.length;
  if (grew > 0) p = Math.min(p, Math.max(0, caret - grew));

  let s = 0;
  const maxS = Math.min(oldT.length - p, newT.length - p);
  while (s < maxS && oldT[oldT.length - 1 - s] === newT[newT.length - 1 - s]) s++;

  const removed = oldT.length - p - s, inserted = newT.length - p - s;

  /* The mode wins. Without one, a character inherits a style only when it
     lands strictly inside a styled run, so fixing a typo in a bold word
     keeps it bold, but typing after the word -- even right after a
     selection that was just styled -- starts plain. */
  let inherit = mode;
  if (!mode && inserted > 0) {
    const eff = effective(oldT, styles);
    const q = p + removed; // first old character after the replaced range
    if (p > 0 && q < oldT.length && eff[p - 1] && eff[p - 1] === eff[q]) inherit = eff[p - 1];
  }

  const next = new Uint8Array(newT.length);
  next.set(styles.subarray(0, p), 0);
  next.fill(inherit, p, p + inserted);
  next.set(styles.subarray(p + removed), p + inserted);
  return next;
}

/* Whether every character in [a, b) has `bit`, or null if the range is
   nothing but spaces. Spaces neither count towards "all on" nor matter when
   set: effective() decides what they show. */
export function rangeHas(text, styles, a, b, bit) {
  let any = false;
  for (let i = a; i < b; i++) {
    if (isSpace(text[i])) continue;
    if (!(styles[i] & bit)) return false;
    any = true;
  }
  return any ? true : null;
}

/* Maximal stretches of characters sharing one style. */
export function* runs(text, shown) {
  let i = 0;
  while (i < text.length) {
    let j = i + 1;
    while (j < text.length && shown[j] === shown[i]) j++;
    yield { start: i, end: j, style: shown[i] };
    i = j;
  }
}

/* The document the server wants: flat text plus character ranges, one span
   per run of styled characters as they would print. */
export function toDocument(text, styles) {
  const spans = [...runs(text, effective(text, styles))]
    .filter(r => r.style)
    .map(({ start, end, style }) => ({
      start, end, style: Object.fromEntries(toolsIn(style).map(t => [t.json, true])),
    }));

  return { text, ...(spans.length && { spans }) };
}
