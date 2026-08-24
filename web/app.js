(() => {
  /* Read from CSS so --cols stays the single source of truth. */
  const COLS = parseInt(getComputedStyle(document.documentElement)
    .getPropertyValue('--cols'), 10);
  const $ = (id) => document.getElementById(id);
  const body = $('body'), roll = $('roll'), tools = $('tools');

  $('divTop').textContent = '-'.repeat(COLS);
  $('divBot').textContent = '-'.repeat(COLS);

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

  /* Styles are per line, because the printer is: ESC a is a line-level
     command and a receipt is a stack of lines, not a flowing paragraph. */
  const TOOLS = [
    { key: 'bold',   label: 'B',  title: 'Bold',            attr: 'bold',  on: '1' },
    { key: 'under',  label: 'U',  title: 'Underline',       attr: 'under', on: '1' },
    { key: 'under2', label: 'U²', title: 'Underline 2 dot', attr: 'under', on: '2' },
    { key: 'strike', label: 'S',  title: 'Strikethrough',   attr: 'strike',on: '1' },
    { key: 'inv',    label: '◧',  title: 'Inverse',         attr: 'inv',   on: '1' },
    { sep: true },
    { key: 'w2',     label: '2W', title: 'Double width',    attr: 'w', on: '2' },
    { key: 'h2',     label: '2H', title: 'Double height',   attr: 'h', on: '2' },
    { key: 'h3',     label: '3H', title: 'Triple height',   attr: 'h', on: '3' },
    { sep: true },
    { key: 'left',   label: '⇤',  title: 'Left',   attr: 'align', on: 'left' },
    { key: 'center', label: '↔',  title: 'Centre', attr: 'align', on: 'center' },
    { key: 'right',  label: '⇥',  title: 'Right',  attr: 'align', on: 'right' },
  ];

  TOOLS.forEach(t => {
    if (t.sep) { const s = document.createElement('span'); s.className = 'sep'; tools.append(s); return; }
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
    const allOn = lines.every(l => l.dataset[t.attr] === t.on);
    lines.forEach(l => { if (allOn) delete l.dataset[t.attr]; else l.dataset[t.attr] = t.on; });
    syncTools(lines);
  }

  function syncTools(lines) {
    tools.querySelectorAll('button').forEach(b => {
      const t = TOOLS.find(x => x.key === b.dataset.key);
      b.setAttribute('aria-pressed', String(lines.every(l => l.dataset[t.attr] === t.on)));
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
  });

  body.addEventListener('keydown', (e) => {
    const meta = e.metaKey || e.ctrlKey;
    if (meta && e.key === 'Enter') { e.preventDefault(); doPrint(); return; }
    if (meta && e.key.toLowerCase() === 'b') { e.preventDefault(); applyTool(TOOLS[0]); return; }
    if (meta && e.key.toLowerCase() === 'u') { e.preventDefault(); applyTool(TOOLS[1]); return; }
  });

  document.addEventListener('mousedown', (e) => {
    if (!tools.contains(e.target) && !body.contains(e.target)) tools.classList.remove('on');
  });

  /* The feed animation is the only confirmation: paper leaves, fresh paper
     arrives. No toast, nothing left on the page. */
  function doPrint() {
    if (roll.classList.contains('out')) return;
    tools.classList.remove('on');
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

  normalize();
  body.focus();
  caretInto(body.firstElementChild);
})();
