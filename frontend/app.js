(() => {
  /* Read from CSS so --cols and --rows stay the single source of truth. */
  const cssInt = (name) => parseInt(getComputedStyle(document.documentElement).getPropertyValue(name), 10);
  const COLS = cssInt('--cols'), ROWS = cssInt('--rows');
  const $ = (id) => document.getElementById(id);
  const ta = $('text'), mirror = $('mirror'), roll = $('roll'), keys = $('keys'), status = $('status');
  const printBtn = $('print');

  $('divTop').textContent = '-'.repeat(COLS);
  $('divBot').textContent = '-'.repeat(COLS);

  /* The textarea holds the text and the browser owns editing: caret, selection,
     undo, IME, mobile keyboards. This module owns only two things the textarea
     cannot: a style per character, and the size of the paper. */
  const BOLD = 1, UNDER = 2, INVERT = 4;

  /* Only what the printer does. Anything else would look right here and
     silently vanish on paper. */
  const TOOLS = [
    { key: 'bold',   label: 'B', title: 'Bold (Ctrl+B)',                    bit: BOLD },
    { key: 'under',  label: 'U', title: 'Underline (Ctrl+U)',               bit: UNDER },
    { key: 'invert', label: 'A', title: 'Reverse, white on black (Ctrl+I)', bit: INVERT },
  ];

  /* One byte per character of ta.value, kept the same length at all times.
     Raw: what was applied. What shows and what prints is effective(). */
  let styles = new Uint8Array(0);

  /* The typing mode: the style every typed character gets. Set only by the
     keys or their shortcuts, never by what happens to be near the caret, and
     kept until switched off the same way. */
  let mode = 0;

  const isSpace = (c) => c === ' ' || c === '\n';

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

  /* Styles as they appear on paper. A space has no glyph, so a style on it is
     a stray underline or a black cell: it keeps one only between two styled
     characters on the same line, where it joins them. Newlines never do. */
  function effective(text = ta.value, raw = styles) {
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

  /* The value and selection after the last edit we accounted for. */
  let last = { text: '' };

  function remember() {
    last = { text: ta.value };
  }

  /* Reconcile styles after the textarea changed underneath us. Every edit a
     textarea can make -- typing, deleting, paste, drop, IME, undo -- replaces
     one contiguous range, so the old and new text differ in one place. Find
     it, and decide what the inserted characters look like. */
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
    styles = next;
  }

  /* A key with text selected styles the selection and nothing else. With no
     selection it switches the typing mode. */
  function applyTool(t) {
    const a = ta.selectionStart, b = ta.selectionEnd;

    if (a === b) {
      mode ^= t.bit;
      syncKeys();
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
    for (let i = a; i < b; i++) styles[i] = allOn ? styles[i] & ~t.bit : styles[i] | t.bit;

    render();
    syncKeys();
  }

  /* ---- rendering ------------------------------------------------------ */

  function classesOf(style) {
    const c = [];
    if (style & BOLD) c.push('b');
    if (style & UNDER) c.push('u');
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

  /* ---- the keys ------------------------------------------------------- */

  TOOLS.forEach(t => {
    const b = document.createElement('button');
    b.type = 'button';
    b.title = t.title; b.dataset.key = t.key; b.className = t.key;
    b.setAttribute('aria-pressed', 'false');
    /* The label sits in its own element so the reverse key can draw a
       filled cell around its letter. */
    const label = document.createElement('span');
    /* The reverse key's letter is drawn by CSS inside a filled cell. */
    if (t.key !== 'invert') label.textContent = t.label;
    b.append(label);
    /* A key must not move focus: if the paper is being written on it keeps
       the caret and selection, and if it is not, pressing a key must not
       open a phone's keyboard. pointerdown covers touch, where cancelling
       mousedown alone comes too late. Nothing here ever calls focus(). */
    b.addEventListener('pointerdown', e => e.preventDefault());
    b.addEventListener('mousedown', e => e.preventDefault());
    b.addEventListener('click', () => applyTool(t));
    keys.append(b);
  });

  /* Pressed means: with a selection, every selected character has it; with a
     bare caret, it is part of the typing mode. */
  function syncKeys() {
    const a = ta.selectionStart, b = ta.selectionEnd;
    keys.querySelectorAll('button').forEach(btn => {
      const t = TOOLS.find(x => x.key === btn.dataset.key);
      let on;
      if (a === b) on = !!(mode & t.bit);
      else {
        on = false;
        for (let i = a; i < b; i++) {
          if (isSpace(ta.value[i])) continue;
          on = true;
          if (!(styles[i] & t.bit)) { on = false; break; }
        }
      }
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
    }

    reconcile();
    remember();
    render();
    syncKeys();
    setStatus('');
  });

  ta.addEventListener('keydown', (e) => {
    const meta = e.metaKey || e.ctrlKey;
    if (!meta) return;
    if (e.key === 'Enter') { e.preventDefault(); print(); return; }
    const tool = { b: 'bold', u: 'under', i: 'invert' }[e.key.toLowerCase()];
    if (tool) { e.preventDefault(); applyTool(TOOLS.find(t => t.key === tool)); }
  });

  /* The textarea scrolls to chase the caret even with the content fitting,
     by a pixel or two on some platforms; the mirror would not follow. */
  ta.addEventListener('scroll', () => { ta.scrollTop = 0; });

  /* ---- printing ------------------------------------------------------- */

  /* The server wants flat text plus character ranges, so each run of styled
     characters becomes one span. */
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
      if (shown[i] & UNDER) style.underline = true;
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

  /* A fresh sheet. The typing mode is the sender's setting, not the sheet's,
     so it survives. */
  function clear() {
    ta.value = '';
    styles = new Uint8Array(0);
    remember();
    render();
    syncKeys();
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
