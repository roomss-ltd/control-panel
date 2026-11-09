'use strict';

/* ══ Split view: as many chats on screen as you want ══════════════════════════════
   On the Terminals page, chats side by side, each in a tile you can type in at once: two, four, a whole feature.
   Each tile is the panel itself showing one chat (/?embed=session:pane, app.js EMBED), so every tile has its own
   stream, composer, slingshot and dictation, and none can trip over another's state.

   Ways in:
   · the "Open beside" button in a chat's header: that chat and a picker for the next;
   · a feature's (zellij session's) "Open as feature" on the Terminals page: all its agents at once;
   · Shift-click a terminal in the sidebar's quick access: it joins the split.
   In a tile: ⤢ fills the screen with it (again to go back), ↗ opens it alone (the split stays), × closes it.
   Arranging: drag a tile by its header onto another (the middle swaps them, an edge puts it on that side), and drag
   the lines between tiles to size them (double-click one to even it out; arrow keys when it has focus).
   The layout is rows, each with its height, each holding tiles with their widths: so every tile's width is its own,
   and a row's height is shared by the tiles in it. Tiles are placed absolutely and never moved in the page: an
   iframe that moves reloads its chat.
   The split is remembered (localStorage 'split', 'splitRows') and comes back when you return to Terminals. */
const SPL = { keys: [], rows: [], max: '', el: null };
try { SPL.keys = JSON.parse(store.get('split', '[]')).filter((k) => typeof k === 'string').slice(0, 12); } catch { /* none */ }
try { SPL.rows = JSON.parse(store.get('splitRows', '[]')).filter((r) => r && Array.isArray(r.t)); } catch { /* none */ }
const splitSave = () => { store.set('split', JSON.stringify(SPL.keys)); store.set('splitRows', JSON.stringify(SPL.rows)); };
const SPL_MIN_W = 240, SPL_MIN_H = 160; // px: below that a chat is no longer usable

// rows sum to 1 in height, each row's tiles to 1 in width
function splNorm() {
  SPL.rows = SPL.rows.filter((r) => r.t.length);
  const H = SPL.rows.reduce((a, r) => a + (r.h > 0 ? r.h : 1), 0);
  for (const r of SPL.rows) {
    r.h = (r.h > 0 ? r.h : 1) / H;
    const W = r.t.reduce((a, t) => a + (t.w > 0 ? t.w : 1), 0);
    for (const t of r.t) t.w = (t.w > 0 ? t.w : 1) / W;
  }
}
// the rows follow the list of chats: gone ones leave (their space goes to their neighbours), new ones fill the last
// row up to the usual column count, then start a new row
function splLayout() {
  for (const r of SPL.rows) r.t = r.t.filter((t) => SPL.keys.includes(t.k));
  SPL.rows = SPL.rows.filter((r) => r.t.length);
  const placed = new Set(SPL.rows.flatMap((r) => r.t.map((t) => t.k)));
  const add = SPL.keys.filter((k) => !placed.has(k));
  const cols = splitCols(SPL.keys.length);
  for (const k of add) {
    const last = SPL.rows[SPL.rows.length - 1];
    const avg = (xs) => xs.reduce((a, v) => a + v, 0) / xs.length; // a newcomer gets an average share
    if (last && last.t.length < cols) last.t.push({ k, w: avg(last.t.map((t) => t.w || 1)) });
    else SPL.rows.push({ h: SPL.rows.length ? avg(SPL.rows.map((r) => r.h || 1)) : 1, t: [{ k, w: 1 }] });
  }
  splNorm();
  SPL.keys = SPL.rows.flatMap((r) => r.t.map((t) => t.k)); // reading order
}
function splEven() { for (const r of SPL.rows) { r.h = 1; for (const t of r.t) t.w = 1; } splNorm(); }
const splitCols = (n) => (n <= 3 ? n : n === 4 ? 2 : n <= 9 ? 3 : 4);

// Shows or hides the split view: on Terminals, while no chat is open on its own over it.
function splitShow() {
  if (EMBED) return;
  // on Terminals, while no chat is open on its own over it (↗, or one picked from the list)
  const on = S.view === 'term' && SPL.keys.length > 0 && !D.key;
  if (!SPL.el) {
    SPL.el = document.createElement('section');
    SPL.el.id = 'split';
    SPL.el.className = 'split';
    SPL.el.setAttribute('aria-label', 'Chats side by side');
    SPL.el.innerHTML = '<div class="spl-bar"><b>Side by side</b><span class="spl-n"></span><span class="spacer"></span>' +
      `<button type="button" class="btn sm ghost" data-spl-even title="Give every chat the same size">${ic('grid4')}Even out</button>` +
      `<button type="button" class="btn sm" data-spl-add>${ic('plus')}Add a chat</button>` +
      `<button type="button" class="btn sm ghost" data-spl-exit>${ic('xmark')}Close all</button></div><div class="spl-grid"></div>`;
    $('#app').append(SPL.el);
    SPL.el.addEventListener('click', splitClick);
    SPL.el.addEventListener('pointerdown', splDrag);
    SPL.el.addEventListener('dblclick', (e) => {
      const g = e.target.closest('.spl-g');
      if (!g) return;
      const [a, b, k] = splPair(g), m = (a[k] + b[k]) / 2;
      a[k] = m; b[k] = m;
      splitSave(); splPlace();
    });
    SPL.el.addEventListener('keydown', (e) => {
      const g = e.target.closest('.spl-g');
      const d = { ArrowLeft: -1, ArrowUp: -1, ArrowRight: 1, ArrowDown: 1 }[e.key];
      if (!g || !d) return;
      e.preventDefault();
      const [a, b, k] = splPair(g);
      splNudge(g, a, b, k, a[k], b[k], d * 0.02);
      splitSave();
      $(`.spl-g.${g.classList.contains('v') ? 'v' : 'h'}[data-r="${g.dataset.r}"][data-t="${g.dataset.t}"]`, SPL.el)?.focus();
    });
  }
  SPL.el.hidden = !on;
  document.body.classList.toggle('split-on', on);
  if (!on) return;
  splLayout();
  const grid = $('.spl-grid', SPL.el);
  // tiles are kept by key (an iframe moved or rebuilt reloads its chat), added and removed as the list changes
  const have = new Map([...grid.querySelectorAll('.spl-t')].map((t) => [t.dataset.k, t]));
  for (const [k, t] of have) if (!SPL.keys.includes(k)) t.remove();
  for (const k of SPL.keys) {
    if (have.has(k)) continue;
    const t = document.createElement('div');
    t.className = 'spl-t';
    t.dataset.k = k;
    t.innerHTML = `<div class="spl-th" title="Drag to move it"><i class="nq-dot"></i><span class="spl-tt"></span><em></em><span class="spacer"></span>` +
      `<button type="button" class="icon-btn ghost sm" data-spl-max title="Fill the screen with it">${ic('maximize')}</button>` +
      `<button type="button" class="icon-btn ghost sm" data-spl-solo title="Open it alone">${ic('arrow-up-right')}</button>` +
      `<button type="button" class="icon-btn ghost sm" data-spl-x title="Close this tile">${ic('xmark')}</button></div>` +
      `<iframe src="/?embed=${encodeURIComponent(k)}" title="Chat ${esc(k)}" loading="eager"></iframe>`;
    grid.append(t);
  }
  splPlace();
  $('.spl-n', SPL.el).textContent = SPL.keys.length === 1 ? '1 chat' : `${SPL.keys.length} chats`;
  splitLabels();
}
// every tile and every dividing line to where the rows say (fractions of the grid, so a window resize needs nothing)
function splPlace() {
  const grid = $('.spl-grid', SPL.el);
  const pct = (v) => (v * 100).toFixed(3) + '%';
  const box = (el, x, y, w, h) => Object.assign(el.style, { left: pct(x), top: pct(y), width: pct(w), height: pct(h) });
  grid.classList.toggle('has-max', !!SPL.max);
  for (const g of grid.querySelectorAll('.spl-g')) g.remove();
  let y = 0;
  SPL.rows.forEach((r, ri) => {
    let x = 0;
    r.t.forEach((t, ti) => {
      const el = $(`.spl-t[data-k="${CSS.escape(t.k)}"]`, grid);
      if (el) { el.classList.toggle('max', SPL.max === t.k); SPL.max === t.k ? box(el, 0, 0, 1, 1) : box(el, x, y, t.w, r.h); }
      x += t.w;
      if (ti < r.t.length - 1) splGutter(grid, 'v', ri, ti, x, y, r.h);
    });
    y += r.h;
    if (ri < SPL.rows.length - 1) splGutter(grid, 'h', ri, -1, 0, y, 0);
  });
}
function splGutter(grid, dir, ri, ti, x, y, h) {
  const g = document.createElement('div');
  g.className = 'spl-g ' + dir;
  g.tabIndex = 0;
  g.setAttribute('role', 'separator');
  g.setAttribute('aria-orientation', dir === 'v' ? 'vertical' : 'horizontal');
  g.setAttribute('aria-label', dir === 'v' ? 'Width of the chats either side' : 'Height of the rows above and below');
  g.title = 'Drag to resize · double-click to even out';
  g.dataset.r = ri; g.dataset.t = ti;
  if (dir === 'v') Object.assign(g.style, { left: (x * 100).toFixed(3) + '%', top: (y * 100).toFixed(3) + '%', height: (h * 100).toFixed(3) + '%' });
  else g.style.top = (y * 100).toFixed(3) + '%';
  grid.append(g);
}
// the pair a dividing line sits between: two tiles' widths in a row, or two rows' heights
function splPair(g) {
  const r = SPL.rows[+g.dataset.r];
  return g.classList.contains('v') ? [r.t[+g.dataset.t], r.t[+g.dataset.t + 1], 'w'] : [r, SPL.rows[+g.dataset.r + 1], 'h'];
}
// move a line by d (a fraction of the grid), each side kept at least a usable size
function splNudge(g, a, b, k, a0, b0, d) {
  const grid = $('.spl-grid', SPL.el), span = k === 'w' ? grid.clientWidth : grid.clientHeight;
  const lo = Math.min((k === 'w' ? SPL_MIN_W : SPL_MIN_H) / Math.max(1, span), (a0 + b0) / 2);
  d = Math.max(lo - a0, Math.min(b0 - lo, d));
  a[k] = a0 + d; b[k] = b0 - d;
  splPlace();
}

// dragging: a line resizes; a tile's header moves the tile. While a drag runs the chats don't take the pointer
// (an iframe under the cursor would swallow the moves).
function splDrag(e) {
  if (e.button !== 0 || SPL.max) return;
  const grid = $('.spl-grid', SPL.el);
  const g = e.target.closest('.spl-g');
  if (g) {
    const [a, b, k] = splPair(g), a0 = a[k], b0 = b[k], x0 = e.clientX, y0 = e.clientY;
    const span = k === 'w' ? grid.clientWidth : grid.clientHeight;
    e.preventDefault();
    SPL.el.classList.add('spl-live');
    g.classList.add('on');
    const move = (ev) => splNudge(g, a, b, k, a0, b0, ((k === 'w' ? ev.clientX - x0 : ev.clientY - y0)) / span);
    const up = () => { removeEventListener('pointermove', move); SPL.el.classList.remove('spl-live'); splitSave(); splPlace(); };
    addEventListener('pointermove', move);
    addEventListener('pointerup', up, { once: true });
    addEventListener('pointercancel', up, { once: true });
    return;
  }
  const th = e.target.closest('.spl-th');
  if (!th || e.target.closest('button')) return;
  const tile = th.closest('.spl-t'), from = tile.dataset.k, x0 = e.clientX, y0 = e.clientY;
  let on = false, drop = null;
  const mark = document.createElement('div');
  mark.className = 'spl-drop';
  const move = (ev) => {
    const dx = ev.clientX - x0, dy = ev.clientY - y0;
    if (!on) {
      if (Math.hypot(dx, dy) < 5) return;
      on = true;
      SPL.el.classList.add('spl-moving');
      tile.classList.add('lifted');
      grid.append(mark);
    }
    tile.style.transform = `translate(${dx}px, ${dy}px)`;
    drop = splTarget(grid, from, ev.clientX, ev.clientY);
    mark.hidden = !drop;
    if (drop) Object.assign(mark.style, drop.rect);
  };
  const up = () => {
    removeEventListener('pointermove', move);
    if (!on) return;
    SPL.el.classList.remove('spl-moving');
    tile.classList.remove('lifted');
    tile.style.transform = '';
    mark.remove();
    if (drop) { splMove(from, drop.k, drop.side); splitSave(); }
    splPlace();
  };
  addEventListener('pointermove', move);
  addEventListener('pointerup', up, { once: true });
  addEventListener('pointercancel', up, { once: true });
}
// where a tile dropped at (x, y) goes: onto the tile under it (the middle swaps them), or beside it (an edge).
// Left and right split that tile's width; top and bottom make a row of its own above or below that tile's row.
function splTarget(grid, from, x, y) {
  const g = grid.getBoundingClientRect();
  for (const [ri, r] of SPL.rows.entries()) for (const t of r.t) {
    if (t.k === from) continue;
    const el = $(`.spl-t[data-k="${CSS.escape(t.k)}"]`, grid), b = el.getBoundingClientRect();
    if (x < b.left || x > b.right || y < b.top || y > b.bottom) continue;
    const px = (x - b.left) / b.width, py = (y - b.top) / b.height;
    const edge = Math.min(px, 1 - px, py, 1 - py);
    const side = edge > 0.25 ? 'swap' : edge === px ? 'left' : edge === 1 - px ? 'right' : edge === py ? 'top' : 'bottom';
    const rel = (l, t, w, h) => ({ left: l - g.left + 'px', top: t - g.top + 'px', width: w + 'px', height: h + 'px' });
    // the row's band, full width, for a new row
    const rowTop = b.top, rowH = b.height;
    const rect = side === 'swap' ? rel(b.left, b.top, b.width, b.height)
      : side === 'left' ? rel(b.left, b.top, b.width / 2, b.height)
      : side === 'right' ? rel(b.left + b.width / 2, b.top, b.width / 2, b.height)
      : side === 'top' ? rel(g.left, rowTop, g.width, rowH / 2)
      : rel(g.left, rowTop + rowH / 2, g.width, rowH / 2);
    return { k: t.k, ri, side, rect };
  }
  return null;
}
// Moves a chat tile onto another's side, or swaps the two.
function splMove(from, to, side) {
  const at = (k) => { for (const r of SPL.rows) { const i = r.t.findIndex((t) => t.k === k); if (i >= 0) return [r, i]; } return []; };
  if (side === 'swap') {
    const [ra, ia] = at(from), [rb, ib] = at(to);
    [ra.t[ia].k, rb.t[ib].k] = [rb.t[ib].k, ra.t[ia].k];
  } else {
    const [ra, ia] = at(from);
    ra.t.splice(ia, 1); // its space goes to its row's others (splNorm)
    if (!ra.t.length) SPL.rows.splice(SPL.rows.indexOf(ra), 1);
    const [rb, ib] = at(to);
    // it comes in with an average share, the others giving way in proportion: halving the tile it lands by would
    // leave two too small to use once a few are dropped on the same spot
    const avg = (xs) => xs.reduce((a, v) => a + v, 0) / xs.length;
    if (side === 'left' || side === 'right') rb.t.splice(side === 'left' ? ib : ib + 1, 0, { k: from, w: avg(rb.t.map((t) => t.w)) });
    else SPL.rows.splice(SPL.rows.indexOf(rb) + (side === 'top' ? 0 : 1), 0, { h: avg(SPL.rows.map((r) => r.h)), t: [{ k: from, w: 1 }] });
  }
  splNorm();
  SPL.keys = SPL.rows.flatMap((r) => r.t.map((t) => t.k));
}
// each tile's name and state, from the terminal list
async function splitLabels() {
  const panes = await allPanes(15000);
  for (const t of SPL.el?.querySelectorAll('.spl-t') || []) {
    const x = panes.find((p) => p.key === t.dataset.k);
    $('.spl-tt', t).textContent = x ? x.label : t.dataset.k;
    $('em', t).textContent = x ? x.session : '';
    const st = x?.p.state || '';
    $('.nq-dot', t).className = 'nq-dot ' + (['asking', 'waiting'].includes(st) ? 'warn' : ['working', 'background', 'stalled', 'running'].includes(st) ? 'busy' : '');
  }
}
setInterval(() => { if (SPL.el && !SPL.el.hidden && !document.hidden) splitLabels(); }, 10000);

function splitSet(keys) {
  SPL.keys = [...new Set(keys)].slice(0, 12);
  if (!SPL.keys.includes(SPL.max)) SPL.max = '';
  splitSave();
  if (S.view !== 'term') setView('term');
  else splitShow();
}
const splitAdd = (k) => splitSet([...SPL.keys, k]);
function splitDrop(k) {
  SPL.keys = SPL.keys.filter((x) => x !== k);
  if (SPL.max === k) SPL.max = '';
  splitSave();
  splitShow();
}

// "Add a chat": the terminals with an agent in them that aren't on screen yet, by name, narrowed as you type
async function splitPick() {
  const panes = (await allPanes(15000)).filter((x) => !SPL.keys.includes(x.key) && (x.p.agent || x.p.kind === 'claude' || x.p.kind === 'codex'));
  let box = $('.spl-pick', SPL.el);
  if (box) return box.remove();
  box = document.createElement('div');
  box.className = 'spl-pick';
  box.innerHTML = '<input type="search" placeholder="Find a chat" aria-label="Find a chat" autocomplete="off" spellcheck="false"><div class="spl-pl"></div>';
  $('.spl-bar', SPL.el).append(box);
  const draw = (q = '') => {
    q = q.trim().toLowerCase();
    $('.spl-pl', box).innerHTML = panes.filter((x) => !q || `${x.label} ${x.session}`.toLowerCase().includes(q)).slice(0, 40)
      .map((x) => `<button type="button" class="seek-hit" data-spl-pick="${esc(x.key)}">${ic('chat-round-dots')}<span>${esc(x.label)}</span><em>${esc(x.session)}</em></button>`).join('') ||
      '<div class="faint" style="padding:.5rem .625rem">No other chats</div>';
  };
  draw();
  const q = $('input', box);
  q.addEventListener('input', () => draw(q.value));
  q.addEventListener('keydown', (e) => { if (e.key === 'Escape') box.remove(); if (e.key === 'Enter') $('[data-spl-pick]', box)?.click(); });
  q.focus();
}

function splitClick(e) {
  const b = e.target.closest('button');
  if (!b) return;
  const k = b.closest('.spl-t')?.dataset.k;
  if (b.matches('[data-spl-add]')) return splitPick();
  if (b.matches('[data-spl-pick]')) { $('.spl-pick', SPL.el)?.remove(); return splitAdd(b.dataset.splPick); }
  if (b.matches('[data-spl-exit]')) return splitSet([]);
  if (b.matches('[data-spl-even]')) { splEven(); splitSave(); return splPlace(); }
  if (b.matches('[data-spl-x]')) return splitDrop(k);
  if (b.matches('[data-spl-max]')) { SPL.max = SPL.max === k ? '' : k; return splPlace(); }
  if (b.matches('[data-spl-solo]')) { SPL.el.hidden = true; document.body.classList.remove('split-on'); return jumpGo({ key: k }); }
}

// what a tile says about itself: it closed (its back button), or it switched to another chat
addEventListener('message', (e) => {
  if (e.origin !== location.origin || !e.data?.cpSplit) return;
  const { cpSplit, key, was } = e.data;
  if (cpSplit === 'close') splitDrop(key);
  if (cpSplit === 'key' && was && was !== key && SPL.keys.includes(was)) {
    SPL.keys = SPL.keys.map((x) => (x === was ? key : x));
    for (const r of SPL.rows) for (const t of r.t) if (t.k === was) t.k = key;
    const t = SPL.el && $(`.spl-t[data-k="${CSS.escape(was)}"]`, SPL.el);
    if (t) t.dataset.k = key;
    splitSave();
    splitLabels();
  }
});

// a chat's "Split": it becomes a tile, and the picker asks for the one beside it
$('#dr-beside').addEventListener('click', () => {
  const k = D.key;
  if (!k) return;
  closePane();
  splitSet([...SPL.keys.filter((x) => x !== k), k]);
  if (SPL.keys.length < 2) splitPick();
});
// a feature (a zellij session): every agent in it, side by side
$('#view').addEventListener('click', async (e) => {
  const b = e.target.closest('[data-feature]');
  if (!b) return;
  e.stopPropagation();
  const xs = (await allPanes(0)).filter((x) => x.session === b.dataset.feature && (x.p.agent || x.p.kind === 'claude' || x.p.kind === 'codex'));
  if (xs.length) splitSet(xs.map((x) => x.key));
}, true);
