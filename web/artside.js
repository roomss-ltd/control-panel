// An artefact beside the chat: an agent's HTML page opened from a JARVIS chat (Main or a thread) or a terminal's chat
// shows in a panel on the right, with the browser's annotate tools in its header: comment (a pin), highlight text,
// draw, arrow, box, text. The marks live in the page (review.js, injected by /api/fs/raw?review=1) and are kept in
// "<page>.comments.json"; each one goes to the chat it was opened from the moment it's saved. The panel watches the
// file (its X-Mtime, every 1.5 s) and reloads the page the moment the agent rewrites it, at the same scroll.
// It belongs to the chat it was opened from: switch to another chat and it closes.
const AS = { here: 0, path: '', title: '', ctx: null, marks: [], tool: '', mtime: '', y: 0, busy: false, timer: 0, pending: false, sending: false, again: false };
const asRaw = (p, review) => '/api/fs/raw?path=' + encodeURIComponent(p) + (review ? '&review=1' : '');
const AS_TOOLS = [['pin', 'comment', 'Comment: click anywhere on the page'], ['hl', 'pen', 'Highlight: select text, say what to change'],
  ['pen', 'brush', 'Draw'], ['arrow', 'arrow-up-right', 'Arrow'], ['box', 'br-rect', 'Box'], ['text', 'text', 'Text on the page']];

// ctx: {kind: 'pane', key, session, pane} | {kind: 'thread', id} | {kind: 'main'}
function artSide(path, title, ctx) {
  let el = $('#art-side');
  if (!el) {
    document.body.insertAdjacentHTML('beforeend', `<aside id="art-side" class="art-side" aria-label="Artefact"><div class="as-grip" role="separator" aria-orientation="vertical" aria-label="Drag to resize; double-click to reset" title="Drag to resize · double-click to reset"></div><button type="button" class="as-strip" data-as="show" title="Show the artefact" aria-label="Show the artefact">${ic('angle-left')}<span></span></button><header class="as-h"></header><div class="as-tools" role="toolbar" aria-label="Annotate"></div><div class="as-list" hidden></div><div class="as-body"></div></aside>`);
    el = $('#art-side');
    el.addEventListener('click', asClick);
    asGrip($('.as-grip', el));
  }
  asSize(store.get('artW', ''), '');
  Object.assign(AS, { path, title: title || path.split('/').pop(), ctx, marks: [], tool: '', y: 0, busy: false, pending: false, mtime: '' });
  el.hidden = false;
  document.body.classList.add('art-open');
  asHead();
  $('.as-list', el).hidden = true;
  $('.as-body', el).innerHTML = `<iframe class="as-f" allow="clipboard-write" sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox" src="${esc(asRaw(path, true))}" title="${esc(AS.title)}"></iframe>`;
  clearInterval(AS.timer); clearInterval(AS.here);
  AS.timer = setInterval(asWatch, 1500);
  AS.here = setInterval(asHere, 250);
  asWatch();
}
function artSideClose() {
  clearInterval(AS.timer); clearInterval(AS.here);
  document.body.classList.remove('art-max', 'art-min');
  AS.path = '';
  const el = $('#art-side');
  if (el) { el.hidden = true; $('.as-body', el).innerHTML = ''; }
  document.body.classList.remove('art-open');
}
const asTo = () => AS.ctx?.kind === 'pane' ? (findPane(AS.ctx.session, AS.ctx.pane)?.tb?.name || AS.ctx.key) : AS.ctx?.kind === 'thread' ? (typeof thOf === 'function' && thOf(AS.ctx.id)?.title) || 'the thread' : 'Main JARVIS';
const asOpen = () => AS.marks.filter((m) => !m.done);
function asHead() {
  const el = $('#art-side'), n = asOpen().length, max = document.body.classList.contains('art-max');
  $('.as-strip span', el).textContent = AS.title;
  $('.as-h', el).innerHTML = `${ic('pen-tool')}<div class="as-t"><b title="${esc(AS.path)}">${esc(AS.title)}</b><span>Comments go straight to ${esc(asTo())}</span></div>` +
    `<span class="as-new"${AS.pending ? '' : ' hidden'}><button type="button" class="btn sm" data-as="reload">${ic('refresh')}New version</button></span>` +
    `<button type="button" class="icon-btn ghost sm" data-as="min" title="Hide: the chat takes the screen" aria-label="Hide the artefact">${ic('angle-right')}</button>` +
    `<button type="button" class="icon-btn ghost sm" data-as="max" title="${max ? 'Back beside the chat' : 'Full screen'}" aria-label="${max ? 'Back beside the chat' : 'Full screen'}">${ic(max ? 'minimize' : 'maximize')}</button>` +
    `<a class="icon-btn ghost sm" href="${esc(asRaw(AS.path))}" target="_blank" rel="noopener" title="Open in a tab" aria-label="Open in a tab">${ic('arrow-up-right')}</a>` +
    `<button type="button" class="icon-btn ghost sm" data-as="close" title="Close" aria-label="Close">${IC.x}</button>`;
  $('.as-tools', el).innerHTML = AS_TOOLS.map(([k, i, l]) => `<button type="button" data-tool="${k}" title="${l}" aria-label="${l}" aria-pressed="${AS.tool === k}">${ic(i)}</button>`).join('') +
    `<span class="as-sep"></span><button type="button" class="as-cnt" data-as="list" aria-pressed="${!$('.as-list', el).hidden}" title="Your marks">${n} open</button>` +
    (n && !AS.sending ? `<span class="spacer"></span><button type="button" class="as-send" data-as="send">${ic('direct-send')}<span>Send ${n}</span></button>` : '');
}
function asList() {
  const box = $('#art-side .as-list');
  box.innerHTML = AS.marks.length ? AS.marks.map((m, i) => `<button type="button" class="as-it${m.done ? ' done' : ''}" data-mark="${esc(m.id)}"><span class="n">${i + 1}</span><span class="as-b"><span>${esc(m.text || '(a drawing)')}</span><small>${esc(m.quote ? '“' + m.quote + '”' : m.target || '')}</small></span></button>`).join('')
    : '<div class="empty">Pick a tool above and mark the page.</div>';
}
async function asClick(e) {
  const t = e.target.closest('[data-tool]');
  if (t) { AS.tool = AS.tool === t.dataset.tool ? '' : t.dataset.tool; asPost({ op: 'tool', tool: AS.tool }); return asHead(); }
  const mk = e.target.closest('[data-mark]');
  if (mk) { $('#art-side .as-list').hidden = true; asHead(); return asPost({ op: 'show', id: mk.dataset.mark }); }
  const a = e.target.closest('[data-as]')?.dataset.as;
  if (a === 'close') return artSideClose();
  if (a === 'max' || a === 'min' || a === 'show') { asSize('', a === 'show' ? '' : document.body.classList.contains('art-' + a) ? '' : a); return asHead(); }
  if (a === 'reload') return asReload();
  if (a === 'list') { const l = $('#art-side .as-list'); l.hidden = !l.hidden; asList(); return asHead(); }
  if (a === 'send') return asSend();
}
// its width: dragged (kept for next time), full screen (the artefact over everything) or tucked to a strip (the chat
// takes the screen); dragging past the edges does the same
function asSize(w, mode) {
  const b = document.body;
  b.classList.toggle('art-max', mode === 'max');
  b.classList.toggle('art-min', mode === 'min');
  if (w) document.documentElement.style.setProperty('--art-w', w);
  else if (w === null) document.documentElement.style.removeProperty('--art-w');
}
// The drag handle between the chat and the artefact panel: drag to resize, double-click for the default width.
function asGrip(g) {
  g.addEventListener('dblclick', () => { store.set('artW', ''); asSize(null, ''); asHead(); });
  g.addEventListener('pointerdown', (e) => {
    e.preventDefault();
    g.setPointerCapture(e.pointerId);
    document.body.classList.add('art-drag'); // the iframe would eat the moves
    const move = (ev) => {
      const w = innerWidth - ev.clientX;
      if (w > innerWidth - 120) return asSize('', 'max');
      if (w < 200) return asSize('', 'min');
      asSize(Math.round(w) + 'px', '');
    };
    const up = () => {
      g.removeEventListener('pointermove', move); g.removeEventListener('pointerup', up); g.removeEventListener('pointercancel', up);
      document.body.classList.remove('art-drag');
      const v = document.documentElement.style.getPropertyValue('--art-w');
      if (v && !document.body.classList.contains('art-min') && !document.body.classList.contains('art-max')) store.set('artW', v);
      asHead();
    };
    g.addEventListener('pointermove', move); g.addEventListener('pointerup', up); g.addEventListener('pointercancel', up);
  });
}
// it belongs to the chat it was opened from: leave that chat (another terminal, thread, Main, page) and it closes
function asHere() {
  const c = AS.ctx;
  if (!c || !AS.path) return;
  if (c.kind === 'thread' && c.id === 'new' && TH.id && TH.id !== 'new') c.id = TH.id;
  const here = c.kind === 'pane' ? D.key === c.key
    : c.kind === 'thread' ? S.view === 'ai' && !D.key && TH.id === c.id
    : S.view === 'ai' && !D.key && !TH.id;
  if (!here) artSideClose();
}
const asFrame = () => $('#art-side .as-f');
const asPost = (o) => asFrame()?.contentWindow?.postMessage({ type: 'cp-comments', ...o }, '*');

// what the agent gets: the file to change in place, and each open mark with where it is
function asText(open) {
  const lines = open.map((m) => {
    const n = AS.marks.indexOf(m) + 1;
    const what = { pin: 'Comment', hl: 'Highlighted text', pen: 'Drawing', arrow: 'Arrow', box: 'Box', text: 'Text added' }[m.kind] || 'Comment';
    return `${n}. ${m.text || '(no words: see the mark)'}\n   ${what}: ${m.target || ''}${m.quote ? `\n   the text: "${m.quote}"` : ''}`;
  });
  return `[Changes to the artefact "${AS.title}"]\nFile: ${AS.path}\nMake these changes in that file, in place: it's open beside my chat and reloads the moment it changes.\n\n${lines.join('\n')}\n\n(Every mark, with its position on the page: ${AS.path}.comments.json)`;
}
// every mark goes to the chat the moment it's saved; one that couldn't be sent stays open and Send retries it
async function asSend() {
  if (AS.sending) return (AS.again = true);
  const open = asOpen();
  if (!open.length || !AS.ctx) return;
  AS.sending = true;
  const text = asText(open), c = AS.ctx, to = asTo();
  let ok = false;
  try {
    if (c.kind === 'pane') { queueSend(c.key, c.session, c.pane, text, []); ok = true; }
    else if (c.kind === 'thread') ok = (await api('/api/ai/send', { thread: c.id, text }).catch(() => ({}))).status === 200;
    else if (typeof aiSend === 'function') ok = !!(await aiSend(text));
  } finally { AS.sending = false; }
  if (!ok) { AS.again = false; asHead(); return toast(`Couldn't reach ${to}: press Send to try again`, true); }
  const ids = new Set(open.map((m) => m.id));
  for (const m of AS.marks) if (ids.has(m.id)) m.done = true; // sent: they grey out; the agent's new version comes in on its own
  await asSave();
  asPost({ op: 'loaded', ok: true, comments: AS.marks });
  asHead();
  toast(`Sent to ${to}`);
  if (AS.again) { AS.again = false; asSend(); }
}
async function asSave() {
  const body = JSON.stringify({ page: AS.path, saved_at: new Date().toISOString(), comments: AS.marks }, null, 2);
  if (body.length > 2 << 20) return toast('Marks go up to 2 MB', true);
  const r = await fetch(`/api/fs/file?path=${encodeURIComponent(AS.path + '.comments.json')}&force=1&nolink=1`, { method: 'PUT', credentials: 'same-origin', body }).catch(() => null);
  if (r?.status !== 200) toast("Couldn't save your marks", true);
}
async function asLoad() {
  const r = await fetch(asRaw(AS.path + '.comments.json'), { credentials: 'same-origin' }).catch(() => null);
  const j = r?.ok ? await r.json().catch(() => null) : null;
  AS.marks = Array.isArray(j?.comments) ? j.comments : [];
  asPost({ op: 'loaded', ok: true, comments: AS.marks, y: AS.y });
  asPost({ op: 'tool', tool: AS.tool });
  asHead();
}

// the agent rewrote the page: reload it where you were (unless you're mid-comment: then a button says so)
async function asWatch() {
  if (!AS.path || document.hidden) return;
  const path = AS.path;
  const r = await fetch(asRaw(path), { method: 'HEAD', credentials: 'same-origin' }).catch(() => null);
  const mt = r?.ok ? r.headers.get('X-Mtime') : '';
  if (path !== AS.path || !mt) return;
  if (!AS.mtime) { AS.mtime = mt; return; }
  if (mt === AS.mtime) return;
  AS.mtime = mt;
  if (AS.busy) { AS.pending = true; return asHead(); }
  asReload();
}
function asReload() {
  AS.pending = false;
  const f = asFrame();
  if (f) f.src = asRaw(AS.path, true) + '&v=' + AS.mtime; // review.js asks for its marks again (op 'load')
  asHead();
}
addEventListener('message', async (e) => {
  const f = asFrame(), m = e.data;
  if (!f || e.source !== f.contentWindow || !m || m.type !== 'cp-comments' || !AS.path) return;
  if (m.op === 'load') return asLoad();
  if (m.op === 'save') { AS.marks = Array.isArray(m.comments) ? m.comments.slice(0, 500) : []; asHead(); if (!$('#art-side .as-list').hidden) asList(); await asSave(); return asSend(); }
  if (m.op === 'busy') { AS.busy = !!m.busy; if (!AS.busy && AS.pending) asReload(); return; }
  if (m.op === 'scrolled') AS.y = +m.y || 0;
});
addEventListener('keydown', (e) => { if (e.key === 'Escape' && AS.path && AS.tool && !e.target.closest?.('input, textarea')) { AS.tool = ''; asPost({ op: 'tool', tool: '' }); asHead(); } });
