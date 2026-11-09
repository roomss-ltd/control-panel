// Review layer: the panel injects this into an agent's HTML page it shows beside a chat (/api/fs/raw?review=1, fs.go).
// It runs inside the page's sandbox (no storage, no network) and draws the marks you make with the tools in the
// panel's header (artside.js): a pin (comment anywhere), a highlight (select text, say what to change), and pen,
// arrow, box and text drawn over the page. Each mark carries what it points at in words an agent can find in the
// source. The panel keeps them in "<page>.comments.json" ({type:'cp-comments', op:'load'|'save'}) and sends them.
// Everything lives in a shadow root, so the page's own CSS can't reach it and it can't reach the page's.
(() => {
  if (window.parent === window || window.__cpReview) return;
  window.__cpReview = true;
  const post = (o) => parent.postMessage({ type: 'cp-comments', ...o }, '*');
  const host = document.createElement('div');
  host.style.cssText = 'position:absolute;left:0;top:0;width:0;height:0;z-index:2147483647';
  const root = host.attachShadow({ mode: 'open' });
  const C = '#9a7bff';
  root.innerHTML = `<style>
:host{all:initial}
*{box-sizing:border-box;font:500 13px/1.45 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
svg{position:absolute;left:0;top:0;overflow:visible;pointer-events:none}
svg.on{pointer-events:auto;cursor:crosshair}
.pin{position:absolute;width:24px;height:24px;margin:-24px 0 0 0;border-radius:12px 12px 12px 2px;background:${C};color:#fff;display:grid;place-items:center;font-weight:700;font-size:11px;cursor:pointer;box-shadow:0 0 0 2px #fff,0 4px 12px rgba(0,0,0,.4)}
.pin.done{background:#737373}
.pin.flash{animation:f 1s 2}@keyframes f{50%{transform:scale(1.4)}}
.pop{position:absolute;width:290px;padding:10px;border-radius:12px;background:#1c1c1c;color:#e5e5e5;box-shadow:0 0 0 1px rgba(255,255,255,.12),0 16px 40px rgba(0,0,0,.6);display:flex;flex-direction:column;gap:8px}
.pop small{color:#a3a3a3;font-size:11px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.pop q{color:#d8ccff;font-style:italic;overflow:hidden;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical}
.pop p{margin:0;white-space:pre-wrap;overflow-wrap:anywhere}
textarea{width:100%;min-height:64px;resize:vertical;padding:8px;border-radius:8px;border:1px solid #404040;background:#111;color:#f5f5f5}
textarea:focus{outline:none;border-color:${C}}
.row{display:flex;gap:6px;justify-content:flex-end}
button{border:0;border-radius:999px;padding:5px 12px;background:#2a2a2a;color:#e5e5e5;cursor:pointer}
button:hover{background:#333}button.go{background:#f5f5f5;color:#000}
.selbar{position:absolute;display:flex;gap:4px;padding:4px;border-radius:999px;background:#1c1c1c;box-shadow:0 0 0 1px rgba(255,255,255,.12),0 8px 24px rgba(0,0,0,.5)}
.selbar[hidden]{display:none}
</style><svg class="ink"></svg><div class="pins"></div>`;
  const $ = (s) => root.querySelector(s);
  const svg = $('.ink');
  const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  let marks = [], tool = '', pop = null, cur = null;
  const docW = () => Math.max(document.documentElement.scrollWidth, 1);
  const docH = () => Math.max(document.documentElement.scrollHeight, 1);
  const X = (fx) => fx * docW(); // x is kept as a fraction of the page's width (it reflows), y in px

  // what a point is on, in words an agent can find in the page's source
  function describe(el) {
    if (!el || el === document.documentElement || el === document.body) return 'the page';
    const bits = [];
    const txt = (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 80);
    bits.push(`<${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}${typeof el.className === 'string' && el.className.trim() ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : ''}>`);
    if (txt) bits.push(`"${txt}"`);
    let h = el.closest('section, article, header, main, [id]')?.querySelector('h1, h2, h3');
    if (!h) for (let p = el; p && !h; p = p.previousElementSibling || p.parentElement) if (/^H[1-3]$/.test(p.tagName)) h = p;
    if (h && !h.contains(el)) bits.push(`under "${h.textContent.replace(/\s+/g, ' ').trim().slice(0, 60)}"`);
    return bits.join(' ');
  }
  const at = (px, py) => { // the page's element at a page point (our own layer stepped aside)
    host.style.display = 'none';
    const el = document.elementFromPoint(px - scrollX, py - scrollY);
    host.style.display = '';
    return describe(el);
  };

  // highlights: the quoted text found again in the page after a reload, painted with the CSS Custom Highlight API
  const hlStyle = document.createElement('style');
  hlStyle.textContent = '::highlight(cp-review){background:rgba(154,123,255,.35)}';
  (document.head || document.documentElement).append(hlStyle);
  function findText(q) {
    const w = document.createTreeWalker(document.body || document.documentElement, NodeFilter.SHOW_TEXT);
    let norm = '';
    const map = []; // map[i]: the [node, offset] of the i-th character of the page's text, whitespace runs as one space
    for (let n; (n = w.nextNode());) {
      if (n.parentElement?.closest('script, style, noscript')) continue;
      for (let o = 0; o < n.data.length; o++) {
        const sp = /\s/.test(n.data[o]);
        if (sp && norm.endsWith(' ')) continue;
        norm += sp ? ' ' : n.data[o];
        map.push([n, o]);
      }
    }
    const want = q.replace(/\s+/g, ' ').trim().slice(0, 200);
    const i = norm.indexOf(want);
    if (i < 0 || !want) return null;
    const r = document.createRange();
    const [en, eo] = map[i + want.length - 1];
    try { r.setStart(...map[i]); r.setEnd(en, eo + 1); } catch { return null; }
    return r;
  }
  function paintHighlights() {
    if (!window.CSS?.highlights || typeof Highlight === 'undefined') return;
    const rs = marks.filter((m) => m.kind === 'hl' && !m.done && m.quote).map((m) => findText(m.quote)).filter(Boolean);
    CSS.highlights.set('cp-review', new Highlight(...rs));
  }

  // the marks: pins as DOM, the rest as SVG paths over the whole page
  const arrowHead = (x1, y1, x2, y2) => { const a = Math.atan2(y2 - y1, x2 - x1), l = 14; return `M${x2} ${y2}L${x2 - l * Math.cos(a - 0.45)} ${y2 - l * Math.sin(a - 0.45)}M${x2} ${y2}L${x2 - l * Math.cos(a + 0.45)} ${y2 - l * Math.sin(a + 0.45)}`; };
  function shape(m) {
    const p = (m.pts || []).map(([fx, y]) => [X(fx), y]);
    const st = `fill="none" stroke="${C}" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"`;
    if (m.kind === 'pen' && p.length) return `<path ${st} d="M${p.map((q) => q.join(' ')).join('L')}"/>`;
    if (m.kind === 'arrow' && p.length > 1) { const [[a, b], [c, d]] = [p[0], p[p.length - 1]]; return `<path ${st} d="M${a} ${b}L${c} ${d}${arrowHead(a, b, c, d)}"/>`; }
    if (m.kind === 'box' && p.length > 1) { const [[a, b], [c, d]] = [p[0], p[p.length - 1]]; return `<rect ${st} rx="4" x="${Math.min(a, c)}" y="${Math.min(b, d)}" width="${Math.abs(c - a)}" height="${Math.abs(d - b)}"/>`; }
    if (m.kind === 'text' && p.length) return `<text x="${p[0][0]}" y="${p[0][1]}" fill="${C}" stroke="#fff" stroke-width="4" paint-order="stroke" font-size="18" font-weight="700" font-family="ui-sans-serif,system-ui,sans-serif">${esc(m.label || '')}</text>`;
    return '';
  }
  // where a mark's number sits: a pin's point, a shape's first point
  const anchor = (m) => (m.kind === 'pin' || m.kind === 'hl' ? [X(m.x), m.y] : m.pts?.length ? [X(m.pts[0][0]), m.pts[0][1]] : [0, 0]);
  function draw() {
    svg.setAttribute('width', docW()); svg.setAttribute('height', docH());
    svg.innerHTML = marks.filter((m) => !m.done).map(shape).join('') + (cur ? shape(cur) : '');
    $('.pins').innerHTML = marks.map((m, i) => { const [x, y] = anchor(m); return m.done && m.kind !== 'pin' ? '' : `<div class="pin${m.done ? ' done' : ''}" data-i="${i}" style="left:${x}px;top:${y}px">${i + 1}</div>`; }).join('');
    paintHighlights();
  }
  const save = () => { post({ op: 'save', comments: marks }); draw(); };
  const close = () => { pop?.remove(); pop = null; post({ op: 'busy', busy: false }); };

  function openPop(x, y, html) {
    pop?.remove();
    pop = document.createElement('div');
    pop.className = 'pop';
    pop.style.left = Math.max(4, Math.min(x + 8, docW() - 300)) + 'px';
    pop.style.top = y + 8 + 'px';
    pop.innerHTML = html;
    root.append(pop);
    post({ op: 'busy', busy: true });
    return pop;
  }
  // a new mark: say what should change (a drawing may go without words)
  function ask(m, x, y, optional) {
    const p = openPop(x, y, `<small>${esc(m.target)}</small>${m.quote ? `<q>${esc(m.quote)}</q>` : ''}<textarea placeholder="${optional ? 'What should change here? (optional)' : 'What should change here?'}"></textarea><div class="row"><button type="button" data-p="x">Cancel</button><button type="button" class="go" data-p="ok">Save</button></div>`);
    const ta = p.querySelector('textarea');
    ta.focus({ preventScroll: true });
    const ok = () => { const t = ta.value.trim(); if (!t && !optional) return cancel(); m.text = t; marks.push(m); close(); save(); };
    const cancel = () => { close(); draw(); };
    ta.addEventListener('keydown', (k) => { if (k.key === 'Enter' && !k.shiftKey) { k.preventDefault(); ok(); } if (k.key === 'Escape') cancel(); });
    p.addEventListener('click', (k) => { const b = k.target.closest('[data-p]'); if (b) b.dataset.p === 'ok' ? ok() : cancel(); });
  }
  function show(i) {
    const m = marks[i];
    if (!m) return;
    const [x, y] = anchor(m);
    scrollTo({ top: Math.max(0, y - innerHeight / 3), behavior: 'smooth' });
    const p = openPop(x, y, `<small>${esc(m.target)}</small>${m.quote ? `<q>${esc(m.quote)}</q>` : ''}<p>${esc(m.text || '(a drawing)')}</p><div class="row"><button type="button" data-p="rm">Delete</button><button type="button" data-p="done">${m.done ? 'Reopen' : 'Resolve'}</button></div>`);
    p.addEventListener('click', (k) => {
      const b = k.target.closest('[data-p]');
      if (!b) return;
      if (b.dataset.p === 'rm') marks.splice(i, 1); else m.done = !m.done;
      close(); save();
    });
  }
  const newMark = (kind, e) => ({ id: Date.now().toString(36) + Math.random().toString(36).slice(2, 5), kind, at: new Date().toISOString(), x: e.pageX / docW(), y: e.pageY, text: '' });

  // pin and text: a click on the page; highlight: a selection; pen, arrow, box: a drag on the drawing layer
  document.addEventListener('click', (e) => {
    if (e.composedPath().includes(host)) return;
    if (tool !== 'pin' && tool !== 'text') return;
    e.preventDefault(); e.stopPropagation();
    const m = newMark(tool, e);
    m.target = at(e.pageX, e.pageY);
    if (tool === 'pin') return ask(m, e.pageX, e.pageY, false);
    const p = openPop(e.pageX, e.pageY, `<small>Text on the page, at ${esc(m.target)}</small><textarea placeholder="The words to put here"></textarea><div class="row"><button type="button" data-p="x">Cancel</button><button type="button" class="go" data-p="ok">Add</button></div>`);
    const ta = p.querySelector('textarea');
    ta.focus({ preventScroll: true });
    const ok = () => { const t = ta.value.trim(); close(); if (!t) return; m.label = t; m.text = `Text written on the page: "${t}"`; m.pts = [[m.x, m.y]]; marks.push(m); save(); };
    ta.addEventListener('keydown', (k) => { if (k.key === 'Enter' && !k.shiftKey) { k.preventDefault(); ok(); } if (k.key === 'Escape') close(); });
    p.addEventListener('click', (k) => { const b = k.target.closest('[data-p]'); if (b) b.dataset.p === 'ok' ? ok() : close(); });
  }, true);
  // a selection: with the highlight tool it's a comment at once; with no tool a bar offers Comment or Copy (as in the chats)
  const comment = (e) => {
    const sel = getSelection(), q = String(sel || '').replace(/\s+/g, ' ').trim();
    if (!q || !sel.rangeCount) return;
    const r = sel.getRangeAt(0).getBoundingClientRect();
    const m = newMark('hl', e);
    Object.assign(m, { x: (r.left + scrollX) / docW(), y: r.top + scrollY, quote: q.slice(0, 400), target: describe(sel.anchorNode?.parentElement) });
    sel.removeAllRanges();
    ask(m, r.left + scrollX, r.bottom + scrollY, false);
  };
  const bar = document.createElement('div');
  bar.className = 'selbar';
  bar.hidden = true;
  bar.innerHTML = '<button type="button" class="go" data-s="c">Comment</button><button type="button" data-s="copy">Copy</button>';
  root.append(bar);
  bar.addEventListener('pointerdown', (k) => k.preventDefault()); // keep the selection
  bar.addEventListener('click', (k) => {
    const b = k.target.closest('[data-s]');
    if (!b) return;
    bar.hidden = true;
    if (b.dataset.s === 'c') return comment(k);
    const t = String(getSelection());
    try { document.execCommand('copy'); } catch { navigator.clipboard?.writeText(t).catch(() => {}); }
    getSelection().removeAllRanges();
  });
  document.addEventListener('selectionchange', () => { if (!bar.hidden && getSelection().isCollapsed) bar.hidden = true; });
  document.addEventListener('mouseup', (e) => {
    if (e.composedPath().includes(host) || pop) return;
    if (tool === 'hl') return comment(e);
    if (tool) return;
    setTimeout(() => { // after the click that ends it has settled the selection
      const sel = getSelection();
      if (sel.isCollapsed || !sel.rangeCount || !String(sel).trim()) return (bar.hidden = true);
      const r = sel.getRangeAt(0).getBoundingClientRect();
      bar.hidden = false;
      const top = r.top + scrollY - bar.offsetHeight - 8;
      bar.style.left = Math.max(4, Math.min(r.left + scrollX + r.width / 2 - bar.offsetWidth / 2, docW() - bar.offsetWidth - 4)) + 'px';
      bar.style.top = (top < scrollY ? r.bottom + scrollY + 8 : top) + 'px';
    });
  });
  svg.addEventListener('pointerdown', (e) => {
    if (!['pen', 'arrow', 'box'].includes(tool)) return;
    e.preventDefault();
    svg.setPointerCapture(e.pointerId);
    cur = newMark(tool, e);
    cur.pts = [[e.pageX / docW(), e.pageY]];
  });
  svg.addEventListener('pointermove', (e) => {
    if (!cur) return;
    const pt = [e.pageX / docW(), e.pageY];
    if (cur.kind === 'pen') cur.pts.push(pt); else cur.pts[1] = pt;
    draw();
  });
  svg.addEventListener('pointerup', (e) => {
    if (!cur) return;
    const m = cur;
    cur = null;
    const p = m.pts.map(([fx, y]) => [X(fx), y]);
    const xs = p.map((q) => q[0]), ys = p.map((q) => q[1]);
    if (Math.max(...xs) - Math.min(...xs) < 6 && Math.max(...ys) - Math.min(...ys) < 6) return draw(); // a click, not a drawing
    if (m.kind === 'pen' && m.pts.length > 60) m.pts = m.pts.filter((_, i) => i % Math.ceil(m.pts.length / 60) === 0 || i === m.pts.length - 1);
    const mid = [(Math.min(...xs) + Math.max(...xs)) / 2, (Math.min(...ys) + Math.max(...ys)) / 2];
    m.target = m.kind === 'arrow' ? `from ${at(...p[0])} to ${at(...p[p.length - 1])}` : m.kind === 'box' ? `a box around ${at(...mid)}` : `a drawing over ${at(...mid)}`;
    draw();
    ask(m, e.pageX, e.pageY, true);
  });
  root.addEventListener('click', (e) => { const pin = e.target.closest('.pin'); if (pin) show(+pin.dataset.i); });
  addEventListener('keydown', (e) => { if (e.key === 'Escape') { close(); draw(); } });
  addEventListener('resize', draw);
  let st = 0;
  addEventListener('scroll', () => { clearTimeout(st); st = setTimeout(() => post({ op: 'scrolled', y: scrollY }), 150); }, { passive: true });

  addEventListener('message', (e) => {
    const m = e.data;
    if (e.source !== parent || !m || m.type !== 'cp-comments') return;
    if (m.op === 'loaded') { marks = m.ok && Array.isArray(m.comments) ? m.comments.filter((c) => c && typeof c === 'object') : []; draw(); if (m.y) scrollTo(0, m.y); }
    if (m.op === 'tool') {
      tool = m.tool || '';
      svg.classList.toggle('on', ['pen', 'arrow', 'box'].includes(tool));
      document.documentElement.style.cursor = tool === 'pin' || tool === 'text' ? 'crosshair' : tool === 'hl' ? 'text' : '';
      close();
    }
    if (m.op === 'show') { const i = marks.findIndex((x) => x.id === m.id); if (i >= 0) { show(i); root.querySelector(`.pin[data-i="${i}"]`)?.classList.add('flash'); } }
  });
  (document.body || document.documentElement).append(host);
  draw();
  if (document.readyState !== 'complete') addEventListener('load', draw);
  post({ op: 'load' });
})();
