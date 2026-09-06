(() => {
  /* Read from CSS so --cols and --rows stay the single source of truth. */
  const cssInt = (name) => parseInt(getComputedStyle(document.documentElement).getPropertyValue(name), 10);
  const COLS = cssInt('--cols'), ROWS = cssInt('--rows');
  const $ = (id) => document.getElementById(id);
  const ta = $('text'), mirror = $('mirror'), roll = $('roll'), tools = $('tools'), status = $('status');
  const printBtn = $('print');

  $('divTop').textContent = '-'.repeat(COLS);
  $('divBot').textContent = '-'.repeat(COLS);

  /* The textarea holds the text and the browser owns editing: caret, selection,
     undo, IME, mobile keyboards. This module owns only two things the textarea
     cannot: a style per character, and the size of the paper. */
  const BOLD = 1, UNDER = 2, THICK = 4, INVERT = 8;

  /* Thin and thick underline are one printer setting with two values, so
     switching one on switches the other off. */
  const UNDERLINES = UNDER | THICK;

  /* One byte per character of ta.value, kept the same length at all times.
     Raw: what was applied. What shows and what prints is effective(), below. */
  let styles = new Uint8Array(0);

  const isSpace = (c) => c === ' ' || c === '\n';

  /* Styles as they appear on paper. A space has no glyph, so a style on it is
     a stray underline or a black cell: it keeps one only between two styled
     characters on the same line, where it joins them. Newlines never do. */
  function effective() {
    const text = ta.value, out = new Uint8Array(styles);
    let i = 0;

    while (i < text.length) {
      if (!isSpace(text[i])) { i++; continue; }

      let j = i;
      while (j < text.length && text[j] === ' ') j++;

      /* A run of spaces from i to j. Interior if flanked, on this line, by
         styled non-space characters on both sides. */
      const before = i > 0 && text[i - 1] !== '\n' && styles[i - 1];
      const after = j < text.length && text[j] !== '\n' && styles[j];
      if (!(before && after)) out.fill(0, i, j);

      if (j < text.length && text[j] === '\n') { out[j] = 0; j++; }
      i = Math.max(j, i + 1);
    }

    return out;
  }

  /* Only what the printer does. Anything else would look right here and
     silently vanish on paper. */
  const TOOLS = [
    { key: 'bold',   label: 'B', title: 'Bold (Ctrl+B)',                    bit: BOLD },
    { key: 'under',  label: 'U', title: 'Underline (Ctrl+U)',               bit: UNDER, clears: UNDERLINES },
    { key: 'thick',  label: 'U', title: 'Thick underline (Ctrl+Shift+U)',   bit: THICK, clears: UNDERLINES },
    { key: 'invert', label: 'A', title: 'Reverse, white on black (Ctrl+I)', bit: INVERT },
  ];

  /* Set a tool's bit on a style byte, clearing what it excludes. */
  const withTool = (style, t) => (style & ~(t.clears || 0)) | t.bit;

  /* ---- the paper ------------------------------------------------------ */

  /* Rows a text occupies once the printer hard-wraps it at COLS. The mirror
     and the textarea are styled to break in exactly the same places, so this
     arithmetic is the layout, and nothing has to be measured. */
  function rowsOf(text) {
    let rows = 0;
    for (const line of text.split('\n')) rows += Math.max(1, Math.ceil(line.length / COLS));
    return rows;
  }

  const fits = (text) => rowsOf(text) <= ROWS;

  /* The longest prefix of `insert` that fits at the selection. Used by paste,
     which is the one edit that can be far too big to reject wholesale. */
  function fitting(insert) {
    const before = ta.value.slice(0, ta.selectionStart), after = ta.value.slice(ta.selectionEnd);
    let lo = 0, hi = insert.length;
    while (lo < hi) {
      const mid = Math.ceil((lo + hi) / 2);
      if (fits(before + insert.slice(0, mid) + after)) lo = mid; else hi = mid - 1;
    }
    return insert.slice(0, lo);
  }

  /* ---- styles --------------------------------------------------------- */

  /* Style toggled with nothing selected. It applies to what is typed next,
     until the caret moves, the way a word processor's B button behaves. */
  let pending = null;

  /* The value and selection after the last edit we accounted for. */
  let last = { text: '', start: 0, end: 0 };

  function remember() {
    last = { text: ta.value, start: ta.selectionStart, end: ta.selectionEnd };
  }

  /* Reconcile styles after the textarea changed underneath us. Every edit a
     textarea can make -- typing, deleting, paste, drop, IME, undo -- replaces
     one contiguous range, so the old and new text differ in one place. Find
     it, and give inserted characters the style they would have inherited. */
  function reconcile() {
    const oldT = last.text, newT = ta.value;
    if (oldT === newT) return;

    let p = 0;
    const max = Math.min(oldT.length, newT.length);
    while (p < max && oldT[p] === newT[p]) p++;

    /* Repeated characters make the prefix ambiguous ("aa" -> "aaa"); the
       caret sits right after whatever was inserted, so trust it over the
       scan when the two disagree. */
    const grew = newT.length - oldT.length;
    if (grew > 0) p = Math.min(p, Math.max(0, ta.selectionEnd - grew));

    let s = 0;
    const maxS = Math.min(oldT.length - p, newT.length - p);
    while (s < maxS && oldT[oldT.length - 1 - s] === newT[newT.length - 1 - s]) s++;

    const removed = oldT.length - p - s, inserted = newT.length - p - s;

    /* Typing continues the style of the character before the caret, but
       not across a space: the style ends with the word, which is how it
       stops without a toggle. An explicit toggle overrides all of that. */
    let inherit = 0;
    if (pending !== null) inherit = pending;
    else if (removed > 0) inherit = styles[p];
    else if (p > 0 && !isSpace(oldT[p - 1])) inherit = styles[p - 1];

    const next = new Uint8Array(newT.length);
    next.set(styles.subarray(0, p), 0);
    next.fill(inherit, p, p + inserted);
    next.set(styles.subarray(p + removed), p + inserted);

    styles = next;
  }

  function applyTool(t) {
    const a = ta.selectionStart, b = ta.selectionEnd;

    if (a === b) {
      const base = styleAtCaret();
      pending = base & t.bit ? base & ~t.bit : withTool(base, t);
      /* Anchor the caret: pending lasts as long as edits keep happening here. */
      last.start = last.end = a;
      syncTools();
      return;
    }

    /* Spaces neither count towards "all on" nor matter when set: effective()
       decides what they show. A selection of nothing but spaces is a no-op. */
    let allOn = true, any = false;
    for (let i = a; i < b; i++) {
      if (isSpace(ta.value[i])) continue;
      any = true;
      if (!(styles[i] & t.bit)) { allOn = false; break; }
    }
    if (!any) return;
    for (let i = a; i < b; i++) styles[i] = allOn ? styles[i] & ~t.bit : withTool(styles[i], t);

    render();
    syncTools();
  }

  /* ---- rendering ------------------------------------------------------ */

  function classesOf(style) {
    const c = [];
    if (style & BOLD) c.push('b');
    if (style & UNDER) c.push('u');
    if (style & THICK) c.push('uu');
    if (style & INVERT) c.push('i');
    return c.join(' ');
  }

  /* The mirror shows the styled text under a transparent textarea. Same font,
     same width, same wrapping, so its glyphs sit exactly under the invisible
     ones the caret moves through. */
  function render() {
    const text = ta.value, shown = effective();
    const frag = document.createDocumentFragment();
    let i = 0;

    while (i < text.length) {
      let j = i + 1;
      while (j < text.length && shown[j] === shown[i]) j++;

      const run = text.slice(i, j);
      if (shown[i]) {
        const span = document.createElement('span');
        span.className = classesOf(shown[i]);
        span.textContent = run;
        frag.append(span);
      } else {
        frag.append(run);
      }
      i = j;
    }

    /* A trailing newline needs something after it or the browser drops the
       empty last row; the textarea itself does the same trick internally. */
    if (text.endsWith('\n')) frag.append('​');

    mirror.replaceChildren(frag);
  }

  /* ---- toolbar -------------------------------------------------------- */

  TOOLS.forEach(t => {
    const b = document.createElement('button');
    b.textContent = t.label; b.title = t.title; b.dataset.key = t.key; b.className = t.key;
    /* mousedown, so the textarea keeps focus and its selection. */
    b.addEventListener('mousedown', e => e.preventDefault());
    b.addEventListener('click', () => applyTool(t));
    tools.append(b);
  });

  function styleAtCaret() {
    if (pending !== null) return pending;
    const a = ta.selectionStart;
    return a > 0 && !isSpace(ta.value[a - 1]) ? styles[a - 1] : 0;
  }

  /* Whether a selection has anything a style could show on. */
  function selectionHasInk() {
    for (let i = ta.selectionStart; i < ta.selectionEnd; i++) if (!isSpace(ta.value[i])) return true;
    return false;
  }

  function syncTools() {
    const a = ta.selectionStart, b = ta.selectionEnd;
    tools.querySelectorAll('button').forEach(btn => {
      const t = TOOLS.find(x => x.key === btn.dataset.key);
      let on;
      if (a === b) on = !!(styleAtCaret() & t.bit);
      else {
        on = true;
        for (let i = a; i < b; i++) if (!isSpace(ta.value[i]) && !(styles[i] & t.bit)) { on = false; break; }
      }
      btn.setAttribute('aria-pressed', String(on));
    });
  }

  /* The selection lives in the textarea, which has no geometry API; the
     mirror has identical layout, so measure the same range there. */
  function selectionRect() {
    const a = ta.selectionStart, b = ta.selectionEnd;
    const range = document.createRange();
    let at = 0, startSet = false;

    const walker = document.createTreeWalker(mirror, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const len = node.length;
      if (!startSet && a <= at + len) { range.setStart(node, a - at); startSet = true; }
      if (startSet && b <= at + len) { range.setEnd(node, b - at); return range.getBoundingClientRect(); }
      at += len;
    }
    return null;
  }

  function placeTools() {
    if (!selectionHasInk()) { tools.classList.remove('on'); return; }
    const r = selectionRect();
    if (!r) return;
    tools.classList.add('on');
    const w = tools.offsetWidth;
    tools.style.left = Math.max(8, Math.min(innerWidth - w - 8, r.left + r.width / 2 - w / 2 + scrollX)) + 'px';
    tools.style.top = (r.top + scrollY - tools.offsetHeight - 8) + 'px';
    syncTools();
  }

  document.addEventListener('selectionchange', () => {
    if (document.activeElement !== ta) return;
    /* A moved caret ends a pending toggle; the edit it was for never came. */
    if (ta.selectionStart !== last.start || ta.selectionEnd !== last.end) pending = null;
    last.start = ta.selectionStart; last.end = ta.selectionEnd;
    placeTools();
  });

  document.addEventListener('mousedown', (e) => {
    if (!tools.contains(e.target) && e.target !== ta) tools.classList.remove('on');
  });

  /* ---- editing -------------------------------------------------------- */

  /* Edits whose outcome is known before they land are refused before they
     land: no rollback, nothing for the undo stack to see. */
  ta.addEventListener('beforeinput', (e) => {
    /* A pending style is for typing at the spot it was set. If the caret is
       anywhere else now -- moved by mouse, touch or arrow keys -- it is over.
       Checked here rather than on selectionchange, which Chromium delivers
       lazily enough to arrive after the next keystroke. */
    if (pending !== null && (ta.selectionStart !== last.start || ta.selectionEnd !== last.end)) pending = null;

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
    const text = (e.clipboardData || window.clipboardData)?.getData('text/plain') || '';
    if (!text) return;

    const part = fitting(text);
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
      ta.value = last.text;
      ta.setSelectionRange(last.start, last.end);
    }

    reconcile();
    /* pending survives typing on purpose: it ends when the caret is moved
       by anything other than the text it is styling (see selectionchange). */
    remember();
    render();
    syncTools();
    setStatus('');
  });

  ta.addEventListener('keydown', (e) => {
    /* Moving the caret, even back to the same place, ends a pending style. */
    if (/^(Arrow|Home$|End$|Page)/.test(e.key)) pending = null;

    const meta = e.metaKey || e.ctrlKey;
    if (meta && e.key === 'Enter') { e.preventDefault(); print(); return; }
    if (!meta) return;
    const tool = { b: 'bold', u: e.shiftKey ? 'thick' : 'under', i: 'invert' }[e.key.toLowerCase()];
    if (tool) { e.preventDefault(); applyTool(TOOLS.find(t => t.key === tool)); }
  });

  ta.addEventListener('pointerdown', () => { pending = null; });

  /* The textarea scrolls to chase the caret even with the content fitting,
     by a pixel or two on some platforms; the mirror would not follow. */
  ta.addEventListener('scroll', () => { ta.scrollTop = 0; });

  /* ---- printing ------------------------------------------------------- */

  /* The server wants flat text plus character ranges, so each run of styled
     characters becomes one span. Newlines are never inside a run. */
  function document_() {
    const text = ta.value, shown = effective();
    const spans = [];
    let i = 0;

    while (i < text.length) {
      if (!shown[i]) { i++; continue; }
      let j = i + 1;
      while (j < text.length && shown[j] === shown[i]) j++;
      const style = {};
      if (shown[i] & BOLD) style.bold = true;
      if (shown[i] & UNDER) style.underline = 1;
      if (shown[i] & THICK) style.underline = 2;
      if (shown[i] & INVERT) style.invert = true;
      spans.push({ start: i, end: j, style });
      i = j;
    }

    return { text, ...(spans.length && { spans }) };
  }

  function setStatus(msg, bad) {
    status.textContent = msg;
    status.classList.toggle('bad', !!bad);
  }

  let printing = false;

  /* One request, one answer. The server holds the connection until the
     printer has taken the bytes, so a 200 means the paper is moving. */
  async function print() {
    if (printing || roll.classList.contains('out')) return;

    const doc = document_();
    if (!doc.text.trim()) { setStatus('nothing to print', true); return; }

    printing = true;
    printBtn.disabled = true;
    tools.classList.remove('on');
    setStatus('printing...');

    try {
      const res = await fetch('/api/print', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(doc),
      });

      if (!res.ok) {
        /* Does not clear the editor: with no queue behind it, a failed send
           means the words exist only here. */
        const { error } = await res.json().catch(() => ({}));
        setStatus(error || `failed (${res.status})`, true);
        return;
      }

      setStatus('printed');
      feed();
    } catch {
      setStatus('could not reach the printer', true);
    } finally {
      printing = false;
      printBtn.disabled = false;
    }
  }

  /* animationend rather than setTimeout, so the durations live in one place.
     No rAF or will-change: rAF does not fire in a background tab. */
  function onceEnded(name, fn) {
    let fired = false;

    const finish = () => {
      if (fired) return;

      fired = true;
      roll.removeEventListener('animationend', ended);
      fn();
    };

    const ended = (e) => {
      // animationend bubbles, and the hint dots animate inside this subtree.
      if (e.animationName === name) finish();
    };

    roll.addEventListener('animationend', ended);

    /* A backstop: a missed event parks the receipt off screen. The duration
       is read from the stylesheet, so the timings stay in one place. */
    const seconds = parseFloat(getComputedStyle(roll).animationDuration) || 0;
    setTimeout(finish, seconds * 1000 + 250);
  }

  function clear() {
    ta.value = '';
    styles = new Uint8Array(0);
    pending = null;
    remember();
    render();
  }

  function feed() {
    roll.classList.add('out');

    onceEnded('feed-out', () => {
      clear();
      roll.classList.remove('out');
      roll.classList.add('in');

      onceEnded('feed-in', () => roll.classList.remove('in'));

      ta.focus();
    });
  }

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

    if (!s.online) setStatus('printer is offline', true);
    else if (status.textContent === 'printer is offline') setStatus('');
  }

  checkPrinter();
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') checkPrinter();
  });

  printBtn.addEventListener('click', print);

  clear();
  ta.focus();
})();
