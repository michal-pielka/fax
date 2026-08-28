(() => {
  /* Read from CSS so --cols stays the single source of truth. */
  const COLS = parseInt(getComputedStyle(document.documentElement)
    .getPropertyValue('--cols'), 10);
  const $ = (id) => document.getElementById(id);
  const body = $('body'), roll = $('roll'), tools = $('tools'), status = $('status');
  const printBtn = $('print');
  const lamps = {
    online: $('lampOnline'),
    paper: $('lampPaper'),
    busy: $('lampBusy'),
  };

  $('divTop').textContent = '-'.repeat(COLS);
  $('divBot').textContent = '-'.repeat(COLS);

  /* Only what the printer can actually do. ESC/POS has no strikethrough at
     all, and the server's document model carries bold and underline and
     nothing else -- so alignment, sizes and inverse would look right here and
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

  /* Put the caret inside a line, never in .body itself. Typing while the
     caret sits directly in .body produces bare text nodes, which is what
     normalize() then has to clean up. */
  function caretInto(line, atEnd) {
    const r = document.createRange();
    r.selectNodeContents(line);
    r.collapse(!atEnd);
    const s = getSelection();
    s.removeAllRanges();
    s.addRange(r);
  }

  /* contenteditable leaves bare text nodes behind; per-line styling needs
     every child to be a real .ln element.
     Stray nodes are *moved* into the preceding line rather than replaced by a
     new one. Two reasons: a new line per stray node scattered fast typing one
     character per row, and moving keeps the text node object alive so the
     caret offset saved below still resolves. */
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

  /* The paper is a fixed size, so writing space genuinely runs out.
     Whether an insertion overflows cannot be known in advance -- it depends on
     where the text happens to wrap -- so the edit is allowed to land, measured,
     and rolled back if it did not fit. Checking capacity beforehand instead
     always lets one character too many through. */
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

  body.addEventListener('keydown', (e) => {
    const meta = e.metaKey || e.ctrlKey;
    if (meta && e.key === 'Enter') { e.preventDefault(); print(); return; }
    if (meta && e.key.toLowerCase() === 'b') { e.preventDefault(); applyTool(TOOLS[0]); return; }
    if (meta && e.key.toLowerCase() === 'u') { e.preventDefault(); applyTool(TOOLS[1]); return; }
  });

  document.addEventListener('mousedown', (e) => {
    if (!tools.contains(e.target) && !body.contains(e.target)) tools.classList.remove('on');
  });

  /* The server wants flat text plus character-range spans, while the editor
     styles whole lines -- so each styled line becomes one span over its slice
     of the joined text. */
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

  async function print() {
    if (printing || !ready || roll.classList.contains('out')) return;

    const doc = document_();
    if (!doc.text.trim()) { setStatus('nothing to print', true); return; }

    printing = true;
    tools.classList.remove('on');
    setStatus('sending...');

    try {
      const res = await fetch('/api/print', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(doc),
      });

      if (!res.ok) {
        /* Deliberately does not clear the editor: with no queue behind it, a
           failed send means the words only exist in this textarea. */
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
    }
  }

  /* The paper leaving and fresh paper arriving is the confirmation. */
  function feed() {
    roll.classList.add('out');
    setTimeout(() => {
      body.innerHTML = '<div class="ln"><br></div>';
      roll.classList.remove('out');
      roll.classList.add('in');
      setTimeout(() => roll.classList.remove('in'), 440);
      body.focus();
      caretInto(body.firstElementChild);
    }, 780);
  }

  /* The front panel.

     Nothing here polls. The firmware notices the roll run out, publishes it,
     the dispatcher hears it in an MQTT callback, the gateway is already
     holding a stream open, and it arrives here. A lamp changes because
     something happened, not because a timer went off.

     EventSource reconnects on its own, and the server's first act on a new
     connection is to send the current state -- so a dropped connection
     self-heals with nothing to write here. */
  let ready = false;

  function setLamps(s) {
    lamps.online.classList.toggle('on', !!s.online);
    lamps.paper.classList.toggle('on', !!s.paper);
    lamps.busy.classList.toggle('on', !!s.busy);
    lamps.busy.classList.toggle('blink', !!s.busy);

    /* There is one printer and no queue, so a job while it is busy would be
       refused with a 409. Better to say so before the words are typed. */
    ready = !!s.online && !!s.paper && !s.busy;
    printBtn.disabled = !ready;
  }

  /* Dark until the first event, which is a few milliseconds away. Starting
     lit would mean the button is clickable before anything is known. */
  setLamps({});

  const events = new EventSource('/api/events');
  events.onmessage = (e) => {
    try {
      setLamps(JSON.parse(e.data));
    } catch {
      /* One bad event is not worth breaking the page over. */
    }
  };

  /* Dark rather than stale: if the stream is down we do not know anything,
     and the last thing we knew is a guess. */
  events.onerror = () => setLamps({});

  $('print').addEventListener('click', print);

  normalize();
  body.focus();
  caretInto(body.firstElementChild);
})();
