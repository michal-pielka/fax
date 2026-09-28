/* The paper's size, as arithmetic. The mirror and the textarea are styled to
   break in exactly the same places as the printer, so these sums are the
   layout, and nothing has to be measured. */

/* Rows a text occupies once the printer hard-wraps it at `cols`. An empty
   line is still a row. backend/internal/doc.RowsOf does the same sum. */
export function rowsOf(text, cols) {
  let rows = 0;
  for (const line of text.split('\n')) rows += Math.max(1, Math.ceil(line.length / cols));
  return rows;
}

/* The length of every printed row: each line hard-wrapped at `cols`, as
   the printer does. The zoomed-out wall draws a text as these bars. */
export function rowLengths(text, cols) {
  const rows = [];
  for (const line of text.split('\n')) {
    if (!line.length) { rows.push(0); continue; }
    for (let i = 0; i < line.length; i += cols) rows.push(Math.min(cols, line.length - i));
  }
  return rows;
}

/* The longest prefix of `s` that `ok` accepts, given that every shorter
   prefix of an accepted one is accepted too. A binary search, because a
   paste can be far too big to try one character at a time. */
export function longestPrefix(s, ok) {
  let lo = 0, hi = s.length;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    if (ok(s.slice(0, mid))) lo = mid; else hi = mid - 1;
  }
  return s.slice(0, lo);
}
