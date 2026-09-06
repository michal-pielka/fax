(() => {
  /* Read from CSS so --cols stays the single source of truth. */
  const COLS = parseInt(getComputedStyle(document.documentElement)
    .getPropertyValue('--cols'), 10);
  const $ = (id) => document.getElementById(id);
  const body = $('body'), roll = $('roll'), tools = $('tools'), status = $('status');
  const printBtn = $('print');

  $('divTop').textContent = '-'.repeat(COLS);
  $('divBot').textContent = '-'.repeat(COLS);

  /* Only what the printer does. Anything else would look right here and
     silently vanish on paper. */
  const TOOLS = [
    { key: 'bold',  label: 'B', title: 'Bold',      attr: 'bold' },
    { key: 'under', label: 'U', title: 'Underline', attr: 'under' },
  ];

  const lineOf = (node) => {
    let n = node;
    while (n && n !== body) {
      if (n.nodeType === 1 && n.classList.contains('ln')) return n;
      n = n.parentNode;
    }
    return null;
  };

  function selectedLines() {
    const sel = getSelection();
    if (!sel.rangeCount) return [];
    const r = sel.getRangeAt(0);
    const all = [...body.children];
    const a = lineOf(r.startContainer), b = lineOf(r.endContainer);
    if (!a) return [];
    const i = all.indexOf(a), j = b ? all.indexOf(b) : i;
    return all.slice(Math.min(i, j), Math.max(i, j) + 1);
  }

  /* Never leave the caret in .body itself: typing there makes bare text
     nodes for normalize() to clean up. */
  function caretInto(line, atEnd) {
    const r = document.createRange();
    r.selectNodeContents(line);
    r.collapse(!atEnd);
    const s = getSelection();
    s.removeAllRanges();
    s.addRange(r);
  }

  /* Per-line styling needs every child to be a real .ln. Strays are moved
     into the previous line, not replaced, so the caret still resolves. */
  function normalize() {
    if (!body.firstChild) {
      body.innerHTML = '<div class="ln"><br></div>';
      caretInto(body.firstElementChild);
      return;
    }

    const sel = getSelection();
    const r = sel.rangeCount ? sel.getRangeAt(0) : null;
    const node = r && r.startContainer, off = r && r.startOffset;
    let moved = false;

    [...body.childNodes].forEach(n => {
      if (n.nodeType === 1 && n.classList.contains('ln')) return;
      let line = n.previousElementSibling;
      if (!line || !line.classList.contains('ln')) {
        line = document.createElement('div');
        line.className = 'ln';
        n.before(line);
      }
      const br = line.querySelector('br');
      if (br) br.remove();
      line.appendChild(n);
      moved = true;
    });

    if (moved && node && body.contains(node)) {
      try {
        const nr = document.createRange();
        nr.setStart(node, off);
        nr.collapse(true);
        sel.removeAllRanges();
        sel.addRange(nr);
      } catch { /* node no longer addressable; leave the caret alone */ }
    }
  }

  TOOLS.forEach(t => {
    const b = document.createElement('button');
    b.textContent = t.label; b.title = t.title; b.dataset.key = t.key;
    b.addEventListener('mousedown', e => e.preventDefault());
    b.addEventListener('click', () => applyTool(t));
    tools.append(b);
  });

  let saved = [];

  function applyTool(t) {
    const lines = saved.length ? saved : selectedLines();
    if (!lines.length) return;
    const allOn = lines.every(l => l.dataset[t.attr] === '1');
    lines.forEach(l => { if (allOn) delete l.dataset[t.attr]; else l.dataset[t.attr] = '1'; });
    syncTools(lines);
  }

  function syncTools(lines) {
    tools.querySelectorAll('button').forEach(b => {
      const t = TOOLS.find(x => x.key === b.dataset.key);
      b.setAttribute('aria-pressed', String(lines.every(l => l.dataset[t.attr] === '1')));
    });
  }

  function placeTools() {
    const sel = getSelection(), lines = selectedLines();
    if (!lines.length || sel.isCollapsed) { tools.classList.remove('on'); saved = []; return; }
    saved = lines;
    const r = sel.getRangeAt(0).getBoundingClientRect();
    tools.classList.add('on');
    const w = tools.offsetWidth;
    tools.style.left = Math.max(8, Math.min(innerWidth - w - 8, r.left + r.width / 2 - w / 2 + scrollX)) + 'px';
    tools.style.top = (r.top + scrollY - tools.offsetHeight - 8) + 'px';
    syncTools(lines);
  }

  document.addEventListener('selectionchange', () => {
    if (document.activeElement === body) placeTools();
  });

  /* Overflow depends on where text wraps, so it cannot be known in advance.
     The edit lands, gets measured, and is rolled back if it did not fit. */
  const atCapacity = () => body.scrollHeight > body.clientHeight;

  /* The caret is recorded as a line index plus an offset, because rolling back
     replaces the nodes it used to point at. */
  function caretMark() {
    const s = getSelection();
    if (!s.rangeCount) return null;
    const r = s.getRangeAt(0);
    const line = lineOf(r.startContainer);
    return line ? { i: [...body.children].indexOf(line), off: r.startOffset } : null;
  }

  function caretRestore(mark) {
    const line = body.children[mark.i];
    if (!line) return;
    const text = line.firstChild && line.firstChild.nodeType === 3 ? line.firstChild : null;
    const node = text || line;
    const max = text ? text.length : line.childNodes.length;
    const r = document.createRange();
    r.setStart(node, Math.min(mark.off, max));
    r.collapse(true);
    const s = getSelection();
    s.removeAllRanges();
    s.addRange(r);
  }

  let rollback = null;

  body.addEventListener('beforeinput', (e) => {
    const removing = /^(delete|history)/.test(e.inputType);
    rollback = removing ? null : { html: body.innerHTML, caret: caretMark() };
  });

  body.addEventListener('input', () => {
    normalize();
    if (rollback && atCapacity()) {
      body.innerHTML = rollback.html;
      if (rollback.caret) caretRestore(rollback.caret);
    }
    /* Browsers scroll a clipped box to chase the caret even with
       overflow: hidden, which slides the top of the receipt out of view. */
    body.scrollTop = 0;
    setStatus('');
  });

  /* Plain text only, and trimmed to fit. The rollback above is
     all-or-nothing, which on a phone silently ate most pastes. */
  body.addEventListener('paste', (e) => {
    e.preventDefault();

    const text = (e.clipboardData || window.clipboardData)?.getData('text/plain') || '';
    if (!text) return;

    const before = { html: body.innerHTML, caret: caretMark() };

    /* Insert the first n characters and report whether they fit. execCommand
       is deprecated and still the only way to keep the undo stack. */
    const fits = (n) => {
      body.innerHTML = before.html;
      if (before.caret) caretRestore(before.caret);
      else caretInto(body.lastElementChild, true);

      if (n) document.execCommand('insertText', false, text.slice(0, n));
      normalize();

      return !atCapacity();
    };

    if (!fits(text.length)) {
      /* Largest prefix that still fits. About a dozen rebuilds of a nine-row
         div, once, on a paste -- cheaper than it looks. */
      let lo = 0, hi = text.length;

      while (lo < hi) {
        const mid = Math.ceil((lo + hi) / 2);
        if (fits(mid)) lo = mid; else hi = mid - 1;
      }

      fits(lo);
      setStatus('trimmed to fit the paper');
    } else {
      setStatus('');
    }

    body.scrollTop = 0;
  });

  body.addEventListener('keydown', (e) => {
    const meta = e.metaKey || e.ctrlKey;
    if (meta && e.key === 'Enter') { e.preventDefault(); print(); return; }
    if (meta && e.key.toLowerCase() === 'b') { e.preventDefault(); applyTool(TOOLS[0]); return; }
    if (meta && e.key.toLowerCase() === 'u') { e.preventDefault(); applyTool(TOOLS[1]); return; }
  });

  document.addEventListener('mousedown', (e) => {
    if (!tools.contains(e.target) && !body.contains(e.target)) tools.classList.remove('on');
  });

  /* The server wants flat text plus character ranges, so each styled line
     becomes one span over its slice of the joined text. */
  function document_() {
    const lines = [...body.children].map(l => l.textContent.replace(/ /g, ' '));
    const spans = [];
    let at = 0;

    [...body.children].forEach((l, i) => {
      const style = {};
      if (l.dataset.bold) style.bold = true;
      if (l.dataset.under) style.underline = true;

      if (Object.keys(style).length && lines[i].length) {
        spans.push({ start: at, end: at + lines[i].length, style });
      }
      at += lines[i].length + 1; // +1 for the newline that joins them
    });

    return { text: lines.join('\n'), ...(spans.length && { spans }) };
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

  function feed() {
    roll.classList.add('out');

    onceEnded('feed-out', () => {
      body.innerHTML = '<div class="ln"><br></div>';
      roll.classList.remove('out');
      roll.classList.add('in');

      onceEnded('feed-in', () => roll.classList.remove('in'));

      body.focus();
      caretInto(body.firstElementChild);
    });
  }

  /* Asked once on arrival and again when the tab comes back, not polled:
     the print request itself is the authority, and answers offline or no
     paper on its own. This only saves typing a message into a dead machine. */
  async function checkPrinter() {
    let s;

    try {
      s = await (await fetch('/api/state')).json();
    } catch {
      return; /* Unknown is not offline. Let the print request decide. */
    }

    if (!s.online) setStatus('printer is offline', true);
    else if (!s.paper) setStatus('printer is out of paper', true);
    else if (status.classList.contains('bad')) setStatus('');
  }

  checkPrinter();
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') checkPrinter();
  });

  $('print').addEventListener('click', print);

  normalize();
  body.focus();
  caretInto(body.firstElementChild);
})();
