'use strict';

// ---------- assistant ----------
// The owner's Hermes agent (projects/vm-agent). One chat for every channel, in time order, like a thread of
// events: each message says who sent it, through which channel (Telegram, Panel) and when. A JARVIS turn shows
// its work as a trace (thinking, what it says while working, each tool call with its input and result) above
// the answer, which can carry tables, diagrams, charts and files (ai-rich.js). What you type here goes out
// through the Panel. Connections (the plug in the composer): the channels' state, the Google sign-in file and
// Hermes' own dashboard (/hermes/). The send box's glow drifts faster while JARVIS works.

const AI_CH = { panel: 'Panel', telegram: 'Telegram', agent: 'Agent', thread: 'Thread' }; // agent: a Claude agent's `jarvis` messages
const AI = {
  conn: false, tab: 'msgs', files: [], queue: [], models: [], threads: [], items: [], more: false, live: null, state: null, es: null, google: null, loadingMore: false, stale: false,
  v: 0, drawn: '', open: new Set(), // items version, what the history shows, which trace steps are expanded
};
S.data.ai = {}; // the view draws its own content: render() only needs it present
VIEWS.ai = () => '<div id="ai" class="ai" data-own></div>';

const aiTime = (t) => new Date(t * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
function aiDay(t) { // "Today", "Yesterday", "Wed 29 Apr"
  const d = new Date(t * 1000);
  const days = Math.round((new Date().setHours(0, 0, 0, 0) - new Date(d).setHours(0, 0, 0, 0)) / 864e5);
  return days === 0 ? 'Today' : days === 1 ? 'Yesterday' : d.toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short' });
}
// who · channel · time, the sender's side first (the agents' task threads' meta row)
const aiMeta = (who, ch, t) => `<div class="ai-meta"><b>${esc(who)}</b>${ch ? `<span class="ai-ch ch-${esc(ch)}">${esc(AI_CH[ch] || ch)}</span>` : ''}<span>${t}</span></div>`;
const aiAct = (attr, k, icon, label) => `<button type="button" class="abtn" data-${attr}="${esc(k)}" title="${label}" aria-label="${label}">${ic(icon)}</button>`;

// the send box is uimaxx's Neon Prompt Bar (uimaxx.ing/r/neon-prompt-bar.json): its drifting glow (in place of the
// old aurora canvas), the aurora ring, a plus, a chip, and the orb with Phosphor's navigation arrow (light) to send
const NPB_GLOW = '<div class="npb-glow" aria-hidden="true"><i></i><i></i><i></i><i></i></div>';
const NPB_SEND = '<svg class="i npb-arrow" viewBox="0 0 256 256" aria-hidden="true"><path fill="currentColor" d="M236.65,108.1,60.72,42.83l-.13,0A14,14,0,0,0,42.78,60.59s0,.09,0,.13L108.1,236.65A13.77,13.77,0,0,0,121.28,246h.26a13.8,13.8,0,0,0,13.14-9.88l0-.15,22.14-79.1L236,134.73l.15,0a14,14,0,0,0,.53-26.58Zm-4,15.1-82.26,23a6,6,0,0,0-4.16,4.16l-23,82.26a1.85,1.85,0,0,1-1.86,1.36,1.82,1.82,0,0,1-1.92-1.35.61.61,0,0,0,0-.12L54.11,56.62a2,2,0,0,1,2.51-2.51l175.91,65.26.12,0a2,2,0,0,1,0,3.79Z"/></svg>';

// ---- shell: the channel header (Messages, Threads, Files), history, the turn in progress, composer (built once per
// visit); a thread opens in a pane on the right, as in Slack (outside main: main's container query would pin it) ----
function aiMount(view) {
  const root = $('#ai', view);
  // Messages · Threads · Files: in the top bar, on the title's line
  if (!$('#ai-tabs')) { $('#title').insertAdjacentHTML('afterend', '<nav id="ai-tabs" class="views ai-tabs" role="tablist" aria-label="Show"></nav>'); $('#ai-tabs').addEventListener('click', aiClick); }
  $('.top').classList.add('ai-top');
  if (!root || root.dataset.on) return;
  root.dataset.on = '1';
  root.innerHTML = '<div id="ai-body" class="ai-body"><div id="ai-hist" class="ai-hist"></div><div id="ai-now" class="ai-hist"></div><div id="ai-conns" hidden></div><div id="ai-all" class="aic-body" hidden></div></div>' +
    '<div id="ai-foot" class="ai-foot">' + NPB_GLOW +
    `<form id="ai-form" class="composer" autocomplete="off"><button type="button" id="ai-down" class="tobottom" aria-label="Jump to the latest" title="Jump to the latest">${ic('arrow-down')}</button>` +
    `<div id="ai-files" class="files" hidden></div>` +
    `<textarea id="ai-text" rows="1" placeholder="Message JARVIS" aria-label="Message JARVIS"></textarea>` +
    `<div class="ctools"><button type="button" class="cbtn npb-add" data-aiattach aria-label="Attach files" title="Attach files or photos (or drop, or paste)">${ic('plus')}</button>` +
    `<button type="button" class="npb-chip" data-aiconn aria-pressed="false" aria-label="Settings" title="Settings: model, channels, memory, skills, tools, jobs, logs">${ic('tuning')}Settings</button>` +
    `<button type="button" id="ai-clear" class="cbtn cclear" data-aiclear aria-label="Clear" title="Clear what you've written" hidden>${ic('xmark')}</button>` +
    `<button type="submit" id="ai-send" class="cbtn send" aria-label="Send" disabled>${NPB_SEND}</button></div></form></div>`;
  root.addEventListener('click', (e) => { if (!e.composedPath().some((n) => n.id === 'th-pane')) aiClick(e); }); // the open thread has its own (its path, not the target: a button it redrew is no longer in it)
  root.addEventListener('toggle', aiToggle, true);
  const ta = $('#ai-text', root);
  try { ta.value = sessionStorage.getItem('aiDraft') || ''; } catch { /* no storage */ }
  ta.addEventListener('input', () => { aiGrow(ta); aiSendState(); try { sessionStorage.setItem('aiDraft', ta.value); } catch { /* no storage */ } });
  ta.addEventListener('keydown', (e) => { if (e.key === 'Escape' && AI_RE.ai) { e.preventDefault(); e.stopPropagation(); aiReplyTo('', false); } });
  ta.addEventListener('keydown', (e) => { // Enter sends on a keyboard; on a phone it's a new line (the button sends)
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && !matchMedia('(pointer: coarse)').matches) { e.preventDefault(); aiSubmit(); }
  });
  $('#ai-form', root).addEventListener('submit', (e) => { e.preventDefault(); aiSubmit(); });
  ta.addEventListener('paste', (e) => { const fs = [...(e.clipboardData?.files || [])]; if (fs.length) { e.preventDefault(); aiAddFiles(fs); } });
  root.insertAdjacentHTML('beforeend', '<input id="ai-file" type="file" multiple hidden>');
  $('#ai-file', root).addEventListener('change', (e) => { aiAddFiles([...e.target.files]); e.target.value = ''; });
  aiFilesRender();
  aiGrow(ta);
  AI.items = [];
  AI.more = false;
  AI.drawn = '';
  AI.tab = 'msgs';
  root.classList.toggle('ai-room', AI.conn);
  aiConnect();
  DIG.at = 0; digLoad(); // the readable versions of the agents' messages
  aiRender(true);
  let th = AI.goThread || '';
  AI.goThread = '';
  let solo = false;
  if (!th) try { th = sessionStorage.getItem('aiThread') || ''; solo = sessionStorage.getItem('aiThreadSolo') === '1'; } catch { /* no storage */ }
  if (th && solo) aiTab('threads');
  if (th) aiOpenThread(th, '', solo);
}
const aiToggle = (e) => { const k = e.target.dataset?.k; if (k) { if (e.target.open) AI.open.add(k); else AI.open.delete(k); } };

// leaving the view: no streams, no aurora, the open thread kept aside (it comes back on return), the title back
function aiLeave() {
  aiStop();
  thStop();
  thPark();
  const p = $('#th-pane');
  if (p) p.hidden = true;
  aurora(null);
  $('#ai-tabs')?.remove();
  $('.top').classList.remove('ai-top');
}

function aiGrow(ta) { ta.style.height = 'auto'; ta.style.height = Math.min(200, ta.scrollHeight) + 'px'; }

// the composer's plug: Connections in place of the chat, and back
function aiConnToggle(on = !AI.conn) {
  if (on === AI.conn) return;
  AI.conn = on;
  if (on) AI.tab = 'msgs';
  $('#ai')?.classList.toggle('ai-room', on);
  if (on) { aicLoad(); if (AIC.tab === 'tools') aicLoad('mcp'); }
  aicFollow();
  aiRender(true);
}

function aiConnect() {
  aiStop();
  if (document.hidden || S.view !== 'ai') return;
  const es = new EventSource('/api/ai/stream');
  es.onmessage = (e) => aiMessage(JSON.parse(e.data));
  es.onerror = () => { if (es.readyState === EventSource.CLOSED) { AI.es = null; setTimeout(() => { if (S.view === 'ai' && !AI.es) aiConnect(); }, 3000); } };
  AI.es = es;
}
function aiStop() { if (AI.es) { AI.es.close(); AI.es = null; } }

function aiMessage(m) {
  const first = m.kind === 'init' && !AI.items.length; // the history arriving on entry jumps; a reconnect's (back on the tab after a while) keeps your place
  if (m.kind === 'init') { AI.items = m.items || []; AI.more = !!m.more; AI.v++; }
  else if (m.items?.length) { const have = new Set(AI.items.map((i) => i.k)); AI.items.push(...m.items.filter((i) => !have.has(i.k))); AI.v++; }
  if ('live' in m) AI.live = m.live;
  if ('state' in m) AI.state = m.state;
  if ('queue' in m) AI.queue = m.queue || [];
  if ('threads' in m) {
    AI.threads = m.threads || []; AI.drawn = ''; thRender();
    // the sidebar's thread list (app.js navQuick polls it every 20 s) follows at once: a status change shows there too
    NQ.threads = AI.threads.filter((t) => t.status !== 'archived' && t.status !== 'done').sort((a, b) => (b.created || 0) - (a.created || 0)).slice(0, 10)
      .map((t) => ({ id: t.id, title: t.title, status: t.status, state: thState(t), live: !!t.live, created: t.created }));
    store.set('navThreads', JSON.stringify(NQ.threads));
    navDraw();
  }
  aiRender(first);
}

// ---- threads: Main JARVIS is your executive assistant; a thread is a product lead JARVIS for one feature, with its
// team of agents in a zellij session of its own (threads.go). Each shows as a card under the message it grew from:
// where it stands (an orb, as on Terminals) and what its lead says it's doing now or needs from you. A card drops the
// thread open right under it, in the chat: its conversation and a box to write to its lead. ----
const TH_ST = { planning: 'Planning', working: 'Working', needs_you: 'Needs you', done: 'Done', archived: 'Archived' };
const TH = { id: '', parent: '', items: [], live: null, queue: [], es: null, v: 0, files: false, team: null, solo: false }; // solo: open in the Threads tab, as a chat of its own
const aiAgo = (t) => (t ? fmtAgo(now() - t) : '');
function fmtAgo(s) { return s < 60 ? 'just now' : s < 3600 ? `${Math.floor(s / 60)} min ago` : s < 86400 ? `${Math.floor(s / 3600)} h ago` : `${Math.floor(s / 86400)} d ago`; }
const thOf = (id) => AI.threads.find((t) => t.id === id);
// the chat item a thread's card sits under: the message it grew from, or, when JARVIS opened it mid-turn (its parent is
// then a step: a tool call or a thought), that turn's answer (while the turn still runs, the message before it)
function thAnchor(k, its = AI.items) {
  const i = its.findIndex((x) => x.k === k);
  if (i < 0) return '';
  const msg = (x) => x.role === 'user' || x.role === 'assistant';
  if (msg(its[i])) return k;
  const j = its.findIndex((x, n) => n > i && msg(x));
  return j >= 0 && its[j].role === 'assistant' ? its[j].k : its.findLast((x, n) => n < i && msg(x))?.k || '';
}
const thLast = (t) => Math.max(t.last || 0, t.updated || 0);
// what it's doing now (threads.go State: from what runs, not the status its lead set): working, waiting (on you), done, archived
const thState = (t) => t.state || (t.status === 'needs_you' ? 'waiting' : t.live || t.status === 'working' || t.status === 'planning' ? 'working' : t.status);
const thOrbK = (t) => ({ waiting: 'input', done: 'done', archived: 'stopped', working: 'working' })[thState(t)] || 'running';
const thSt = (t) => (thState(t) === 'working' ? 'Working now' : thState(t) === 'waiting' ? TH_ST.needs_you || 'Waiting on you' : TH_ST[t.status] || t.status);
const thOrb = (t, size) => orbHTML(thOrbK(t), size, thSt(t));
const thNote = (t) => t.note || (t.status === 'planning' ? 'Its lead is reading the brief and writing the plan.' : 'No update from its lead yet.');
// open ones first (needs you at the top), the latest moved first; archived ones apart
function aiThreadsShown() {
  const rank = { needs_you: 0, working: 1, planning: 1, done: 2 };
  const open = AI.threads.filter((t) => t.status !== 'archived').sort((a, b) => rank[a.status] - rank[b.status] || thLast(b) - thLast(a));
  return [open, AI.threads.filter((t) => t.status === 'archived').sort((a, b) => thLast(b) - thLast(a))];
}

// what you haven't seen: a thread that moved since you last had it open (on the first visit, all of it counts as seen)
const TH_SEEN = (() => { try { return JSON.parse(localStorage.getItem('thSeen')); } catch { return null; } })() || { _: Date.now() / 1000 };
try { localStorage.setItem('thSeen', JSON.stringify(TH_SEEN)); } catch { /* no storage */ } // the first visit's baseline stays
const thUnread = (t) => TH.id !== t.id && thLast(t) > (TH_SEEN[t.id] || TH_SEEN._) + 1;
function thSeen(t) {
  if (!t || thLast(t) <= (TH_SEEN[t.id] || 0)) return;
  TH_SEEN[t.id] = thLast(t);
  try { localStorage.setItem('thSeen', JSON.stringify(TH_SEEN)); } catch { /* no storage */ }
}
// a thread's timeline (threads.go logEvent): the lead's status changes and notes, its team's news; the newest one
// shimmers while it works
const TH_EV = { status: 'clock-circle', note: 'sparkle', team: 'people', plan: 'check-circle-f' };
function thTimeline(t, n = 3, glow = true) {
  const busy = glow && thState(t) === 'working';
  const log = (t.log || []).slice(-n);
  if (!log.length) log.push({ kind: 'note', text: thNote(t), t: t.updated });
  return log.map((e, i) => {
    const what = e.kind === 'status' ? `moved it to ${TH_ST[e.text] || e.text}` : e.text;
    return `<div class="thc-ev">${ic(TH_EV[e.kind] || 'sparkle')}<span class="thc-evt"><b>${e.kind === 'team' ? 'Team' : 'Lead'}</b><span class="thc-w">${busy && i === log.length - 1 ? shim(what) : esc(what)}</span></span><span class="thc-t">${aiAgo(e.t)}</span></div>`;
  }).join('');
}

// the plan its lead and you agreed on (thread_update plan), as uimaxx's Linear Progress (a bar whose fill warms from
// indigo to peach, a sheen at its tip) and Task List Card (Now pinned, then Next and Done; the step in hand lit)
const thPlanN = (t) => [(t.plan || []).filter((x) => x.state === 'done').length, (t.plan || []).length];
function thBar(t) {
  const [d, n] = thPlanN(t), p = n ? Math.max(4, Math.round((100 * d) / n)) : 0;
  return `<span class="lp" role="progressbar" aria-label="Plan" aria-valuemin="0" aria-valuemax="${n}" aria-valuenow="${d}"><i style="width:${p}%;background-size:${(100 * 100) / p}% 100%"></i></span>`;
}
function thPlanLine(t) {
  if (!t.plan?.length) return '';
  const [d, n] = thPlanN(t), now = t.plan.find((x) => x.state === 'doing') || t.plan.find((x) => x.state === 'todo');
  return `<div class="thc-plan">${thBar(t)}<span><b>${d} of ${n}</b>${now ? ` · ${now.state === 'doing' ? 'now' : 'next'}: ${esc(now.text)}` : ' · all done'}</span></div>`;
}
// an agreed plan: uimaxx's Issue Activity Card (uimaxx.ing/r/issue-activity-card.json). Its title row with the
// activity glyph says how far along it is; each step is a row of its timeline: done (the card's filled round badge,
// a Phosphor check), now (Phosphor's clock, its text shimmering while the lead works), next (the activity glyph, dim)
const IAC_ACT = '<svg class="i" viewBox="0 0 16 16" fill="none" aria-hidden="true"><circle cx="4.5" cy="3.5" r="1.6" fill="currentColor"/><path d="M4.5 3.5v8.5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/><circle cx="4.5" cy="12" r="1.6" fill="currentColor"/><path d="M11.5 6.5v6" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/><circle cx="11.5" cy="12.5" r="1.4" fill="currentColor"/></svg>';
const IAC_CLOCK = '<svg class="i" viewBox="0 0 256 256" aria-hidden="true"><path fill="currentColor" d="M128,24A104,104,0,1,0,232,128,104.11,104.11,0,0,0,128,24Zm0,192a88,88,0,1,1,88-88A88.1,88.1,0,0,1,128,216Zm64-88a8,8,0,0,1-8,8H128a8,8,0,0,1-8-8V72a8,8,0,0,1,16,0v48h48A8,8,0,0,1,192,128Z"/></svg>';
const IAC_CHECK = '<svg viewBox="0 0 256 256" aria-hidden="true"><path fill="currentColor" d="M232.49,80.49l-128,128a12,12,0,0,1-17,0l-56-56a12,12,0,1,1,17-17L96,183,215.51,63.51a12,12,0,0,1,17,17Z"/></svg>';
// ---- a takeover proposal (```proposal, scout.go): JARVIS found a terminal session of the owner's stalled and offers
// to carry it to shipped. The plan card (Issue Activity Card), its sections what / plan / updates / proof / needs /
// blockers, and the owner's three answers (POST /api/ai/scout); once answered, the card says which ----
RICH_BLOCKS.proposal = function thProposal(p, reply = '') {
  RICH_BLOCKS.proposal.specs[String(p.session || '')] = p;
  const L = (x) => (Array.isArray(x) ? x : x ? [x] : []).filter(Boolean);
  const sec = (icon, title, items, cls = '') => (L(items).length ? `<div class="iac-r iac-h iac-sub${cls}"><span class="iac-ic">${icon}</span><p>${title}</p></div>` +
    `<div class="iac-tl">${L(items).map((x) => `<div class="iac-r st-todo"><span class="iac-ic">${IAC_ACT}</span><p><span>${mdInline(String(x))}</span></p></div>`).join('')}</div>` : '');
  const se = String(p.session || ''), r = String(reply || '');
  const said = r.includes(`take over "${se}"`) ? 'taken' : r.includes(`Not now for "${se}"`) ? 'later' : r.includes(`Never take over "${se}"`) ? 'never' : '';
  const acts = said ? `<div class="iac-r prop-said"><span class="iac-ic">${said === 'taken' ? `<span class="iac-ok">${IAC_CHECK}</span>` : IAC_CLOCK}</span><p>${{ taken: 'You approved it: a thread is taking it over', later: 'Not now: it asks again tomorrow if still stalled', never: 'Never: JARVIS leaves it to you' }[said]}</p></div>`
    : `<div class="prop-acts"><button type="button" class="btn sm primary" data-scout="take">Take it over</button><button type="button" class="btn sm" data-scout="later">Not now</button><button type="button" class="btn sm ghost" data-scout="never">Never for this one</button></div>`;
  return `<div class="iac prop" data-session="${esc(se)}" data-title="${esc(p.title || '')}">` +
    `<div class="iac-r iac-h"><span class="iac-ic">${ic('bolt')}</span><p>Takeover proposal · <b>${esc(p.title || se)}</b> <span class="iac-k">${esc(se)}${p.stalled ? ' · stalled ' + esc(p.stalled) : ''}</span></p></div>` +
    (p.what ? `<div class="prop-what">${mdInline(String(p.what))}</div>` : '') +
    sec(IAC_ACT, 'The plan', p.plan) + sec(ic('chat-round-dots'), 'How you will hear from me', p.updates) + sec(IAC_CHECK.replace('<svg', '<svg class="i"'), 'Proof it is done', p.proof) +
    sec(ic('people'), 'What I need from you', p.needs) + sec(ic('alert-triangle'), 'Blockers', p.blockers, ' prop-warn') + acts + '</div>';
};
RICH_BLOCKS.proposal.specs = {}; // the last drawn proposal per session: Take it over sends it as the new thread's brief
async function thScout(b) {
  const card = b.closest('.prop');
  card.querySelectorAll('[data-scout]').forEach((x) => { x.disabled = true; });
  const r = await api('/api/ai/scout', { session: card.dataset.session, title: card.dataset.title, act: b.dataset.scout, plan: RICH_BLOCKS.proposal.specs[card.dataset.session] || null }).catch(() => ({ status: 0, error: "The panel can't be reached" }));
  if (r.status !== 200) { card.querySelectorAll('[data-scout]').forEach((x) => { x.disabled = false; }); return toast(r.error || `Error ${r.status}`, true); }
  b.textContent = 'Sent';
}
function thPlanHTML(t) {
  if (!t) return '';
  const busy = thState(t) === 'working';
  const crew = (TH.id === t.id && TH.crew) || [], lists = crew.filter((a) => a.todos?.length);
  if (!t.plan?.length && !lists.length) return ''; // not every task needs a plan: no plan, no card
  const row = (x, who) => {
    const [icon, word] = x.state === 'done' ? [`<span class="iac-ok">${IAC_CHECK}</span>`, 'Done'] : x.state === 'doing' ? [IAC_CLOCK, 'Now'] : x.state === 'claimed' ? [IAC_ACT, 'Claimed, unverified'] : x.state === 'asking' ? [IAC_CLOCK, 'Waits on you'] : [IAC_ACT, 'Next'];
    return `<div class="iac-r st-${esc(x.state)}" title="${esc(x.text)}"><span class="iac-ic">${icon}</span><p><b>${who || word}</b> ${x.state === 'doing' && busy ? shim(x.text) : `<span>${esc(x.text)}</span>`}</p></div>`;
  };
  // planned: the lead's plan, the one you agreed
  const [d, n] = thPlanN(t);
  const planned = t.plan?.length ? `<div class="iac-r iac-h"><span class="iac-ic">${IAC_ACT}</span><p>Planned · <b>${d} of ${n}</b> steps verified <span class="iac-k">the feature page's PLAN.md: one record for you, the lead and the team</span></p></div>` +
    `<div class="iac-tl">${t.plan.map((x) => row(x)).join('')}</div>`
    : `<div class="iac-r iac-h"><span class="iac-ic">${IAC_ACT}</span><p>Planned · <span class="iac-warn">its lead hasn't published the plan it agreed with you</span></p></div>`;
  // followed: each agent's own to-do list, read from its session
  const st = { completed: 'done', in_progress: 'doing' };
  const agent = (a) => {
    const todos = a.todos.map((x) => ({ text: x.content, state: st[x.status] || 'todo' })), done = todos.filter((x) => x.state === 'done').length;
    const now = todos.find((x) => x.state === 'doing') || todos.find((x) => x.state === 'todo');
    const what = now ? `${now.state === 'doing' ? 'now' : 'next'}: ${now.text}` : 'all done';
    return `<details class="iac-ag" data-k="crew:${esc(a.pane)}"${aiOpen('crew:' + a.pane)}><summary class="iac-r st-${now ? now.state : 'done'}"><span class="iac-ic">${ic('terminal-square')}</span>` +
      `<p><b>${esc(a.who)}</b> <span class="iac-k">${a.kind === 'codex' ? 'Codex' : 'Claude'} · ${done} of ${todos.length}</span> ${now?.state === 'doing' && busy ? shim(what) : `<span>${esc(what)}</span>`}</p></summary>` +
      `<div class="iac-tl iac-in">${todos.map((x) => row(x)).join('')}</div></details>`;
  };
  const followed = lists.length ? `<div class="iac-r iac-h iac-sub"><span class="iac-ic">${ic('people')}</span><p>Followed · <span class="iac-k">what each agent on its team is working through, from its own session</span></p></div>` +
    `<div class="iac-tl">${lists.map(agent).join('')}</div>` : '';
  return `<div class="iac">${planned}${followed}</div>`;
}

// a thread's card: title and where it stands, its plan's progress, what happened lately, the branch, how many messages
function thCard(t, from = '') {
  const n = t.replies || 0;
  return `<button type="button" class="thc st-${esc(t.status)}${TH.id === t.id && !TH.solo ? ' on' : ''}" data-aith="${esc(t.id)}">` +
    `<div class="thc-h">${thOrb(t, 18)}<b>${esc(t.title)}</b><span class="thc-st">${esc(thSt(t))}</span>${thUnread(t) ? '<i class="thc-new">New</i>' : ''}<span class="thc-t">${aiAgo(thLast(t))}</span>${ic('angle-down')}</div>` +
    (from ? `<div class="thc-from">${esc(from)}</div>` : '') +
    thPlanLine(t) + `<div class="thc-tl">${thTimeline(t)}</div>` +
    `<div class="thc-f">${t.branch ? `<span>${ic('hierarchy')}${esc(t.branch)}</span>` : ''}<span>${ic('chat-round-dots')}${n} ${n === 1 ? 'message' : 'messages'}</span></div></button>`;
}

// the tabs, in the top bar: Messages · Threads (how many wait on you, else how many are open) · Files
function aiChanRender() {
  const el = $('#ai-tabs');
  if (!el) return;
  const [open] = aiThreadsShown();
  // waiting on you (amber, by name in its tip) and working, apart; done ones don't count
  const need = open.filter((t) => thState(t) === 'waiting'), busy = open.filter((t) => thState(t) === 'working');
  const k = JSON.stringify([AI.tab, AI.conn, need.map((t) => t.title), busy.length]);
  if (el.dataset.k === k) return;
  el.dataset.k = k;
  const tab = (id, label, n = '') => `<button type="button" data-aitab="${id}" role="tab" aria-selected="${AI.tab === id && !AI.conn}">${label}${n}</button>`;
  const badges = (need.length ? `<span class="n warn" title="${esc('Waiting on you: ' + need.map((t) => t.title).join(', '))}">${need.length} waiting</span>` : '') +
    (busy.length ? `<span class="n" title="${esc('Working: ' + busy.map((t) => t.title).join(', '))}">${busy.length} working</span>` : '');
  el.innerHTML = tab('msgs', 'Messages') + tab('threads', 'Threads', badges) + tab('files', 'Files');
}
function aiTab(id) {
  if (TH.solo && TH.id && id !== 'threads') thClose(true); // a thread open in the Threads tab stays there
  AI.tab = id;
  $('#ai')?.classList.toggle('tab-threads', id === 'threads');
  if (id === 'files') aiFilesLoad();
  aiConnToggle(false);
  $('#ai')?.classList.toggle('ai-room', id !== 'msgs');
  aiRender(true);
}
// the Threads tab: waiting on you, in progress, done; archived folded away. Each card says the message it grew from.
function aiThreadsHTML() {
  const [open, arch] = aiThreadsShown();
  const card = (t) => { const a = thAnchor(t.parent), p = a && AI.items.find((x) => x.k === a); return thCard(t, p ? aiFrom(p.text || '')[2].replace(/\[([^\]]*)\]\([^)]*\)/g, '$1').replace(/[*_`#>]+/g, '').replace(/\s+/g, ' ').slice(0, 200) : ''); };
  const group = (title, list) => (list.length ? `<div class="aic-cat">${title} · ${list.length}</div><div class="thl">${list.map(card).join('')}</div>` : '');
  if (!AI.threads.length) return `<div class="empty ai-empty">${ic('chat-round-dots')}<b>No threads yet</b><span>Hover a message and start a thread from it: a JARVIS of its own leads that work, with a team of agents, and reports here.</span></div>`;
  return group('Waiting on you', open.filter((t) => thState(t) === 'waiting')) + group('In progress', open.filter((t) => thState(t) === 'working')) +
    group('Done', open.filter((t) => t.status === 'done')) +
    (arch.length ? `<details class="th-arch" data-k="th:arch"${aiOpen('th:arch')}><summary>${ic('angle-right')}Archived · ${arch.length}</summary><div class="thl">${arch.map(card).join('')}</div></details>` : '');
}

// ---- the open thread: it sits in a slot under its card in Main's history (aiHistHTML). That history redraws as a
// whole, so the pane steps out to <body> first (thPark) and goes back into the new slot after (thPlace), keeping what
// you're typing ----
let thKept = null;
function thPark() {
  const p = $('#th-pane'), ta = $('#thp-text');
  if (!p || p.parentElement === document.body) return;
  thKept = { sel: ta && document.activeElement === ta ? [ta.selectionStart, ta.selectionEnd] : null };
  document.body.append(p);
}
// Puts the open thread's pane in its slot: inline in the history, or full width when it is the only thing shown.
function thPlace() {
  const p = $('#th-pane');
  if (!p || !TH.id) return;
  thTopBar();
  const slot = $(TH.solo ? '#ai-all [data-thslot]' : '#ai-hist [data-thslot]');
  if (slot && p.parentElement !== slot) slot.append(p);
  const shown = !p.hidden;
  p.hidden = !slot;
  if (!slot) return;
  if (!shown) thRender(true);
  if (thKept) {
    if (thKept.sel) { const ta = $('#thp-text'); ta.focus({ preventScroll: true }); ta.setSelectionRange(...thKept.sel); }
    thKept = null;
  }
  if (TH.scroll && !TH.loaded && !TH.solo && TH.id !== 'new') { p.style.height = '0px'; p.style.overflow = 'clip'; } // shut until its history is in: then it parts open (no flash of it first)
  if (TH.scroll && (TH.loaded || TH.id === 'new')) { // once it has its history: it unfolds, and you ride down to its latest
    TH.scroll = false;
    const top = slot.getBoundingClientRect().top + scrollY;
    if (TH.solo) scrollTo(0, document.documentElement.scrollHeight);
    else { const s0 = scrollY; thPart(p, true, (cur, k) => scrollTo(0, s0 + (Math.max(top - 90, top + cur - innerHeight + 260) - s0) * k)); } // the page glides there with it, no jump first
  }
  thBox();
}
// the chat parting around an open thread: it grows to its height, pushing what's below down, while follow(height)
// keeps its newest end in view (what's above rides up); closing, it shrinks back and the chat closes over it
function thPart(p, open, follow) {
  p.getAnimations().forEach((x) => x.cancel());
  p.style.height = p.style.opacity = p.style.overflow = '';
  if (open) { // a long thread opens on its newest screenful ("Show earlier messages" above it): a short, calm parting, never a race past thousands of pixels
    p.classList.remove('folded');
    if (TH.unfolded !== TH.id && p.offsetHeight > innerHeight * 0.85) p.classList.add('folded');
  }
  const h = p.offsetHeight;
  const run = (p.thRun = (p.thRun || 0) + 1); // a newer parting (another thread opened) takes over: this one lets go
  const rail = () => { cancelAnimationFrame(RAIL.raf); aiRail(); };
  if (matchMedia('(prefers-reduced-motion: reduce)').matches) { follow?.(h, 1); rail(); return Promise.resolve(); }
  const ease = (x) => (x < 0.5 ? 4 * x * x * x : 1 - Math.pow(-2 * x + 2, 3) / 2); // in and out: it eases off, travels, and settles
  const dur = open ? 680 : 480, t0 = performance.now();
  let n = 0;
  p.style.overflow = 'clip';
  return new Promise((done) => {
    const step = (now) => {
      if (p.thRun !== run) return done();
      const k = ease(Math.min(1, Math.max(0, (now - t0) / dur))), cur = Math.round(open ? h * k : h * (1 - k));
      p.style.height = cur + 'px';
      p.style.opacity = String(open ? 0.25 + 0.75 * k : 1 - 0.8 * k);
      follow?.(cur, k);
      if (++n % 3 === 0) rail(); // the scrollbar's band and marks grow and shrink with it (every third frame: it costs a layout)
      if (k < 1) requestAnimationFrame(step);
      else { p.style.height = p.style.overflow = p.style.opacity = ''; follow?.(p.offsetHeight, 1); rail(); done(); }
    };
    step(t0); // the first frame now: no full-size flash before it starts
  });
}
// "Show earlier messages": the rest of the thread opens above, and what you were reading stays where it is
function thUnfold() {
  const p = $('#th-pane'), b = $('#thp-body');
  if (!p || !b) return;
  TH.unfolded = TH.id;
  const y0 = b.getBoundingClientRect().bottom;
  p.classList.remove('folded');
  scrollBy(0, b.getBoundingClientRect().bottom - y0);
  $('.thp-fold')?.animate([{ opacity: 0.4 }, { opacity: 1 }], { duration: 400, easing: 'ease-out' });
}
// the panel's top bar can change height (layout, phone or computer): the thread's sticky header goes right under it
function thTopBar() {
  const bar = document.querySelector('header.top'), ai = $('#ai');
  if (!bar || !ai || thTopBar.on) return;
  thTopBar.on = true;
  const set = () => ai.style.setProperty('--th-top', Math.max(0, Math.round(bar.getBoundingClientRect().bottom) + 14) + 'px');
  new ResizeObserver(set).observe(bar);
  addEventListener('resize', set);
  set();
}
function thPane() {
  let p = $('#th-pane');
  if (!p) {
    document.body.insertAdjacentHTML('beforeend', '<section id="th-pane" class="th-pane" aria-label="Thread" hidden></section>');
    p = $('#th-pane');
    p.addEventListener('click', aiClick);
    // one box to write in: Main's, until Reply moves it into the thread; scrolled away from the thread, it goes back
    new IntersectionObserver(([e]) => {
      const on = e.isIntersecting && !p.hidden;
      $('#ai')?.classList.toggle('th-vis', on);
      thBox();
    }).observe(p);
    p.addEventListener('toggle', aiToggle, true);
  }
  return p;
}
function thShell() {
  thPane().innerHTML = `<div class="thp-end thp-start">Start of thread</div><div class="thp-main"><header class="thp-h"><span id="thp-orb"></span><b id="thp-sub" class="thp-t"></b><span id="thp-st" class="thp-st"></span><div id="thp-meta" class="thp-meta"></div><div id="thp-plan" class="thp-plan"></div>` +
    `<button type="button" class="thp-close" data-aith="" title="Close the thread" aria-label="Close the thread">${ic('xmark')}<span>Close</span></button></header>` +
    '<button type="button" class="thp-more" data-thmore>' + ic('arrow-up') + 'Show earlier messages</button>' +
    '<div class="thp-fold"><div id="thp-tl" class="thp-tl"></div><div id="thp-body" class="thp-body"><div id="thp-hist"></div><div id="thp-now"></div></div></div></div>' + // .thp-main: the title is pinned only over its messages, never over the reply box
    `<form id="thp-form" class="thp-foot" autocomplete="off">${NPB_GLOW}<div class="composer"><button type="button" id="thp-down" class="tobottom" data-thdown aria-label="Jump to the latest" title="Jump to the latest">${ic('arrow-down')}</button><div id="thp-files" class="files" hidden></div><textarea id="thp-text" rows="1" placeholder="Message its lead" aria-label="Message the thread's lead"></textarea>` +
    `<div class="ctools"><button type="button" class="cbtn npb-add" data-aiattach aria-label="Attach files" title="Attach files or photos (or drop, or paste)">${ic('plus')}</button><span id="thp-to" class="npb-chip thp-to"></span><span class="thp-hint"></span><button type="submit" id="thp-send" class="cbtn send" aria-label="Send" disabled>${NPB_SEND}</button></div></div></form>` +
    '<div class="thp-end">End of thread</div>';
  const ta = $('#thp-text');
  ta.addEventListener('input', () => { aiGrow(ta); thSendState(); });
  ta.addEventListener('paste', (e) => { const fs = [...(e.clipboardData?.files || [])]; if (fs.length) { e.preventDefault(); aiAddFiles(fs); } });
  ta.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && AI_RE.th) { e.preventDefault(); e.stopPropagation(); return aiReplyTo('', true); }
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && !matchMedia('(pointer: coarse)').matches) { e.preventDefault(); thSubmit(); }
  });
  $('#thp-form').addEventListener('submit', (e) => { e.preventDefault(); thSubmit(); });
}
// open a thread under its card ('new': a thread about to start from Main's message parent); '' closes it
// from outside the chat (the sidebar's quick access, a terminal, a link): in the Threads tab, as a chat of its own
function aiShowThread(id, tries = 0) {
  if (D.key) closePane(); // a terminal's chat open over the page (opened from JARVIS's page, it never left it): it steps aside
  if (!AI.threads.length && tries < 40) return setTimeout(() => aiShowThread(id, tries + 1), 75); // entering the page, the threads are still on their way
  aiTab('threads');
  aiOpenThread(id, '', true);
}
function aiOpenThread(id, parent = '', solo = false) {
  if (!id) return thClose();
  if (TH.solo !== solo && TH.id) { thStop(); TH.id = ''; } // moving between the Threads tab and Main: drawn afresh
  TH.solo = solo;
  thPane().classList.toggle('solo', solo);
  if (id !== 'new' && AI.threads.length && !thOf(id)) return toast('That thread is gone', true);
  if (TH.id !== id || (id === 'new' && TH.parent !== parent)) {
    thStop();
    if (TH.id !== 'new') { thPane().classList.remove('reply'); $('#ai')?.classList.remove('th-on'); } // a new thread's first message keeps its box
    Object.assign(TH, { id, parent: id === 'new' ? parent : thOf(id)?.parent || '', items: [], loaded: false, live: null, queue: [], files: false, team: null, crew: null, crewK: '' });
    TH.v++;
    thShell();
    thPane().hidden = true; // thPlace shows it, drawn, in its slot
  }
  try { sessionStorage.setItem('aiThread', id === 'new' ? '' : id); sessionStorage.setItem('aiThreadSolo', solo ? '1' : ''); } catch { /* no storage */ }
  thPane().classList.toggle('new', id === 'new');
  if (id !== 'new' && !TH.es) thConnect();
  if (id !== 'new') thCrewLoad();
  TH.scroll = true; // thPlace brings its card to the top of the screen
  if (!solo && (AI.tab !== 'msgs' || AI.conn)) aiTab('msgs'); // from a notification: back in the chat, where it is
  AI.drawn = '';
  aiRender();
  thBox();
  if (id === 'new') requestAnimationFrame(() => { thBox(); $('#thp-text')?.focus({ preventScroll: true }); }); // starting one: write its first message
}
// the thread's team, each agent with the to-do list it works through (GET /api/ai/thread/team)
async function thCrewLoad() {
  const id = TH.id;
  if (!id || id === 'new') return;
  const r = await fetch('/api/ai/thread/team?id=' + encodeURIComponent(id)).then((x) => (x.ok ? x.json() : null)).catch(() => null);
  if (TH.id !== id || !r) return;
  const k = JSON.stringify(r.agents);
  if (k !== TH.crewK) { TH.crew = r.agents; TH.crewK = k; }
  if (r.agents?.length) thRender(); // its strip's clocks move on too
}
setInterval(() => { if (TH.id && !document.hidden && S.view === 'ai') thCrewLoad(); }, 5e3);
// the team at work, live, above the thread's box: each agent its lead runs, what it's doing this minute and how long
// since it last moved; a tap opens its chat. Busy ones first.
const CREW_WORK = ['working', 'background', 'running', 'starting'], CREW_WAIT = ['asking', 'waiting'];
function thCrewHTML() {
  const crew = TH.crew || [];
  if (!crew.length || TH.files) return '';
  const rank = (a) => (CREW_WAIT.includes(a.state) ? 0 : CREW_WORK.includes(a.state) ? 1 : 2);
  const busy = crew.filter((a) => CREW_WORK.includes(a.state)).length, wait = crew.filter((a) => CREW_WAIT.includes(a.state)).length;
  const row = (a) => {
    const work = CREW_WORK.includes(a.state), w8 = CREW_WAIT.includes(a.state);
    const mark = work ? '<i class="nq-run" aria-label="Working"><b></b><b></b><b></b></i>' : w8 ? '<i class="nq-wait" aria-label="Waiting"></i>' : '<i class="nq-dot"></i>';
    const what = w8 ? (a.state === 'asking' ? 'Asking a question' : 'Waiting for its next instruction') + (a.doing && !/^(Thinking|Working)\b/i.test(a.doing) ? ' · ' + a.doing : '') : a.doing || (work ? 'working' : a.state === 'stalled' ? 'stalled' : 'idle');
    const moved = a.out || a.since;
    return `<button type="button" class="crew-r${work ? ' on' : w8 ? ' w8' : ''}" data-aiopen="pane:${esc(a.pane)}" title="Open ${esc(a.who)}'s chat">${mark}<b>${esc(a.who)}</b>` +
      `<span class="crew-k">${a.kind === 'codex' ? 'Codex' : 'Claude'}</span><span class="crew-d">${work ? shim(what) : esc(what)}</span>` +
      `${moved ? `<em>${work ? 'moved ' : ''}${aiAgo(moved)}</em>` : ''}${ic('angle-right')}</button>`;
  };
  let min = false; try { min = localStorage.getItem('thCrewMin') === '1'; } catch { /* no storage */ } // minimised: its header and counts only (remembered)
  return `<div class="crew${min ? ' min' : ''}"><button type="button" class="crew-h" data-crewmin aria-expanded="${!min}" title="${min ? 'Show' : 'Minimise'} the team">${ic('people')}<span>Team</span>` +
    `${busy ? `<b class="on">${busy} working</b>` : ''}${wait ? `<b class="w8">${wait} waiting</b>` : ''}${!busy && !wait ? '<b>all idle</b>' : ''}${ic('angle-down')}</button>` +
    (min ? '' : [...crew].sort((a, b) => rank(a) - rank(b)).map(row).join('')) + '</div>';
}
// one send box (the owner's rule): scrolled to the end of the chat, it's Main's, at the bottom; scrolled up, with an
// open thread on screen, it's the thread's (sticky at the bottom while the thread fills the screen, at its end when
// it's short). When the open thread is the last thing in the chat, its end is the chat's end: the box stays the
// thread's (fold the thread to talk to Main). Switching, the new box rises in; what you were typing keeps focus.
function thBox() {
  const p = $('#th-pane'), ai = $('#ai');
  if (!p || !ai) return;
  let on = false;
  if (TH.solo) on = !!TH.id && !p.hidden && AI.tab === 'threads';
  else if (TH.id && !p.hidden && p.parentElement !== document.body && AI.tab === 'msgs' && !AI.conn) {
    const r = p.getBoundingClientRect();
    const end = scrollY + innerHeight >= document.documentElement.scrollHeight - 24 && r.bottom < innerHeight - 200; // Main below it, in view
    on = !end && r.bottom > 120 && r.top < innerHeight - 120;
  }
  const to = $('#thp-to');
  if (to) to.innerHTML = `${ic('chat-round-dots')}<span>${TH.id === 'new' ? 'New thread' : esc(thOf(TH.id)?.title || 'This thread')}</span>`;
  if (p.classList.contains('reply') === on) return;
  const typing = ['ai-text', 'thp-text'].includes(document.activeElement?.id);
  const from = $(on ? '#ai-foot .composer' : '#thp-form .composer')?.getBoundingClientRect(); // where the box was
  p.classList.toggle('reply', on);
  ai.classList.toggle('th-on', on);
  aiFilesRender(); // what's attached goes with the box
  const box = $(on ? '#thp-form .composer' : '#ai-foot .composer'), dst = box?.getBoundingClientRect();
  // it floats from where it was to where it goes (the thread's end ↔ the bottom), quick but eased in to rest
  if (box && from?.height && dst?.height && !matchMedia('(prefers-reduced-motion: reduce)').matches) {
    const dy = Math.max(-innerHeight, Math.min(innerHeight, from.top - dst.top));
    box.animate([{ transform: `translate(${from.left - dst.left}px, ${dy}px) scale(${from.width / dst.width}, 1)`, transformOrigin: 'left top', opacity: 0.7 }, { transform: 'none', transformOrigin: 'left top', opacity: 1 }], { duration: 420, easing: 'cubic-bezier(.22, 1, .36, 1)' });
    box.parentElement.querySelector('.npb-glow')?.animate([{ transform: `translate(${from.left - dst.left}px, ${dy}px)`, opacity: 0 }, { transform: 'none', opacity: 1 }], { duration: 420, easing: 'cubic-bezier(.22, 1, .36, 1)' }); // its glow travels with it
  } else box?.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 200 });
  if (typing) $(on ? '#thp-text' : '#ai-text')?.focus({ preventScroll: true });
}
function thClose(now) {
  const p0 = $('#th-pane');
  if (!now && !TH.solo && p0 && !p0.hidden && p0.parentElement !== document.body) { // the chat closes back over it
    if (!p0.dataset.closing) { p0.dataset.closing = '1'; thPart(p0, false).then(() => { delete p0.dataset.closing; thClose(true); }); }
    return;
  }
  thStop();
  const was = TH.id;
  TH.id = '';
  const p = $('#th-pane');
  if (p) { p.hidden = true; p.style.opacity = p.style.height = p.style.overflow = ''; p.classList.remove('reply', 'solo'); }
  TH.solo = false;
  $('#ai')?.classList.remove('th-on', 'th-vis');
  try { sessionStorage.setItem('aiThread', ''); } catch { /* no storage */ }
  AI.drawn = '';
  aiRender();
  const card = $(`#ai-hist [data-aith="${CSS.escape(was)}"]`);
  card?.scrollIntoView({ block: 'nearest' }); // folded: its card where you are, settling in
  card?.animate([{ opacity: 0, transform: 'translateY(0.5rem) scale(.98)' }, { opacity: 1, transform: 'none' }], { duration: 300, easing: 'cubic-bezier(.22, 1, .36, 1)' });
}
function thConnect() {
  thStop();
  const id = TH.id;
  if (!id || id === 'new' || document.hidden || S.view !== 'ai') return;
  const es = new EventSource('/api/ai/stream?thread=' + encodeURIComponent(id));
  es.onmessage = (e) => thMessage(JSON.parse(e.data));
  es.onerror = () => { if (es.readyState === EventSource.CLOSED) { TH.es = null; setTimeout(() => { if (S.view === 'ai' && TH.id === id && !TH.es) thConnect(); }, 3000); } };
  TH.es = es;
}
function thStop() { if (TH.es) { TH.es.close(); TH.es = null; } }
function thMessage(m) {
  if (m.kind === 'init') { TH.items = m.items || []; TH.loaded = true; TH.v++; }
  else if (m.items?.length) { const have = new Set(TH.items.map((i) => i.k)); TH.items.push(...m.items.filter((i) => !have.has(i.k))); TH.v++; }
  if ('live' in m) TH.live = m.live;
  if ('queue' in m) TH.queue = m.queue || [];
  thRender(m.kind === 'init');
  thPlace();
}
// the header's actions: its team, branch, files, archive
function thMetaHTML(t) {
  if (!t) return '';
  return '<div class="th-xs">' +
    (t.branch ? `<span class="th-x" title="${esc(t.repo || '')}">${ic('hierarchy')}${esc(t.branch)}</span>` : '') +
    (thPlanHTML(t) ? `<button type="button" class="th-x${TH_PLAN.on ? ' on' : ''}" data-thplan aria-pressed="${TH_PLAN.on}" title="${TH_PLAN.on ? 'Hide' : 'Show'} the plan">${ic('list-check')}<span class="thx-l">Plan</span>${t.plan?.length ? `<span class="thx-n">${thPlanN(t).join('/')}</span>` : ''}</button>` : '') +
    (t.team ? `<button type="button" class="th-x" data-thfeat="${esc(t.team)}" title="The feature page: the one record (plan, evidence, blockers) the owner, this lead and its team read and write">${ic('list-check')}<span class="thx-l">Feature page</span></button>` : '') +
    `<button type="button" class="th-x${TH.files ? ' on' : ''}" data-thfiles>${ic('folder')}<span class="thx-l">Files</span></button>` +
    (t.status !== 'archived' ? `<button type="button" class="th-x th-arch-b${AIC.arm === 'archive' ? ' armed' : ''}" data-aiarchive="${esc(t.id)}" title="Its lead wraps up, stops the team and closes its session">${AIC.arm === 'archive' ? 'Tap again to archive' : 'Archive'}</button>` : '') + '</div>';
}
// the plan's panel, shown or hidden (remembered): it hangs from the thread's pinned title, so it's there at any point in the thread
const TH_PLAN = { on: (() => { try { return localStorage.getItem('thPlan') !== '0'; } catch { return true; } })() };
// the plan's card fades at its foot while there's more below; scrolled to its end (or all of it fits), it doesn't
function thPlanEnd() { const c = $('#thp-plan'); if (c) c.classList.toggle('at-end', c.scrollTop + c.clientHeight >= c.scrollHeight - 4); }
document.addEventListener('scroll', (e) => { if (e.target.id === 'thp-plan') thPlanEnd(); }, { capture: true, passive: true });
function thPlanToggle() {
  TH_PLAN.on = !TH_PLAN.on;
  try { localStorage.setItem('thPlan', TH_PLAN.on ? '1' : '0'); } catch { /* no storage */ }
  thRender(true);
  const el = $('#thp-plan .iac');
  if (el && !matchMedia('(prefers-reduced-motion: reduce)').matches) el.animate([{ opacity: 0, transform: 'translateY(-0.5rem) scale(.98)' }, { opacity: 1, transform: 'none' }], { duration: 260, easing: 'cubic-bezier(.22, 1, .36, 1)' });
}
function thRender(jump) {
  const p = $('#th-pane');
  if (!p || p.hidden || !TH.id) return;
  const t = thOf(TH.id);
  $('#thp-sub').textContent = TH.id === 'new' ? 'New thread' : t?.title || '';
  const ok = TH.id === 'new' ? 'new' : t ? thOrbK(t) : '';
  if ($('#thp-orb').dataset.o !== ok) { $('#thp-orb').innerHTML = t ? thOrb(t, 22) : ''; $('#thp-orb').dataset.o = ok; }
  const meta = $('#thp-meta'), mk = JSON.stringify([TH.id, t, TH.files, AIC.arm, TH.crewK, TH_PLAN.on, Math.floor(Date.now() / 60000)]);
  if (meta.dataset.k !== mk) {
    meta.innerHTML = thMetaHTML(t);
    $('#thp-st').textContent = t ? `${thSt(t)} · ${aiAgo(thLast(t))}` : '';
    $('.thp-h').style.setProperty('--p', t?.plan?.length ? thPlanN(t)[0] / thPlanN(t)[1] : 0);
    const pl = $('#thp-plan'), py = pl.scrollTop; // redrawn every minute: where you'd scrolled it stays
    pl.innerHTML = t && TH_PLAN.on ? thPlanHTML(t) : '';
    pl.scrollTop = py;
    thPlanEnd();
    $('#thp-tl').innerHTML = TH.id === 'new' ? '<p class="thp-new">Your message starts a thread from this one. A JARVIS of its own leads the work as its product lead, with a team of agents, and keeps you posted here.</p>' : t ? thTimeline(t, 4, false) : '';
    meta.dataset.k = mk;
  }
  thSeen(t);
  const hist = $('#thp-hist'), nowEl = $('#thp-now');
  const near = innerHeight + scrollY >= document.documentElement.scrollHeight - 160 && !TH.scroll;
  if (TH.files) {
    const k = 'files:' + JSON.stringify(TH.team) + TH.v;
    if (hist.dataset.k !== k) { hist.innerHTML = aiFilesHTML(TH); hist.dataset.k = k; }
    nowEl.innerHTML = '';
    thSendState();
    return;
  }
  const key = `${TH.id}:${TH.v}:${!!TH.live}:${AI.v}:${t?.replies}:${!!AI.state}`;
  if (hist.dataset.k !== key) { artKeep(hist, () => { hist.innerHTML = aiHistHTML(TH); }); richEnhance(hist); hist.dataset.k = key; }
  nowEl.innerHTML = aiNowHTML(TH) + thCrewHTML();
  if (near && !jump) scrollTo(0, document.documentElement.scrollHeight);
  thSendState();
}
function thSendState() {
  const b = $('#thp-send'), ta = $('#thp-text');
  if (!b || !ta) return;
  const has = !!ta.value.trim() || (aiBoxTh() && AI.files.length > 0), stop = !!TH.live && !has;
  b.classList.toggle('stop', stop);
  b.innerHTML = stop ? ic('stop-f') : NPB_SEND;
  b.setAttribute('aria-label', stop ? 'Stop' : TH.live ? 'Queue it: sends when the lead is done' : 'Send');
  b.title = b.getAttribute('aria-label');
  b.disabled = !stop && (!has || (aiBoxTh() && AI.files.some((f) => !f.path))); // photos still uploading
  ta.placeholder = TH.id === 'new' ? 'What should its lead do?' : TH.live ? 'Message its lead (sends when it is done)' : 'Message its lead';
}
async function thSubmit() {
  const ta = $('#thp-text');
  const text = ta.value.trim();
  const paths = (doc) => AI.files.filter((f) => !!f.doc === doc).map((f) => f.path).filter(Boolean);
  const images = paths(false), files = paths(true);
  if (!text && !images.length && !files.length) { if (TH.live) await api('/api/ai/stop', { thread: TH.id }).catch(() => {}); return; }
  if (await thSend(TH.id === 'new' ? text : aiReplyWrap('th', text), images, files)) {
    ta.value = ''; aiGrow(ta); aiReplyTo('', true);
    for (const f of AI.files) aiRmURL(f);
    AI.files = [];
    aiFilesRender();
  }
}
// a message into the open thread (the reply box, or a button in one of its answers); the first one starts it
async function thSend(text, images = [], files = []) {
  const fail = () => ({ status: 0, error: "The panel can't be reached" });
  $('#thp-send').disabled = true;
  if (TH.id === 'new') {
    const r = await api('/api/ai/thread', { parent: TH.parent, text, images, files }).catch(fail);
    if (r.status !== 200) { thSendState(); toast(r.error || `Error ${r.status}`, true); return false; }
    AI.threads = [...AI.threads.filter((t) => t.id !== r.thread.id), { ...r.thread, replies: 0, last: 0, live: true }];
    aiOpenThread(r.thread.id);
    return true;
  }
  const r = await api('/api/ai/send', { thread: TH.id, text, images, files }).catch(fail);
  if (r.status !== 200) { thSendState(); toast(r.error || `Error ${r.status}`, true); return false; }
  TH.files = false;
  return true;
}
async function thFiles() {
  TH.files = !TH.files;
  TH.team = null;
  thRender(true);
  if (!TH.files) return;
  const r = await api('/api/ai/thread/files?id=' + encodeURIComponent(TH.id)).catch(() => null);
  TH.team = r?.status === 200 ? r.files || [] : [];
  thRender(true);
}
// "Start a thread from this" on a Main message: its thread if it has one, else a new one from it
function aiReplyThread(k) {
  const mine = AI.threads.filter((x) => thAnchor(x.parent) === k);
  const t = mine.find((x) => x.status !== 'archived') || mine[0];
  aiOpenThread(t ? t.id : 'new', t ? '' : k);
}
async function aiArchive(btn) {
  if (AIC.arm !== 'archive') { AIC.arm = 'archive'; thRender(); setTimeout(() => { if (AIC.arm === 'archive') { AIC.arm = ''; thRender(); } }, 4000); return; }
  AIC.arm = '';
  btn.disabled = true;
  const r = await api('/api/ai/thread/archive', { id: btn.dataset.aiarchive }).catch(() => ({ status: 0, error: "The panel can't be reached" }));
  if (r.status !== 200) { btn.disabled = false; return toast(r.error || `Error ${r.status}`, true); }
  toast('Archived: its lead is wrapping up and closing its team');
}
// who sent a thread's message (the panel and vm-agent mark it): [who, kind or '', the text without the mark, context]
function aiFrom(text) {
  let ctx = '';
  text = text.replace(/^\[Thread context\]\n([\s\S]*?)\n\[\/Thread context\]\n*/, (_, c) => { ctx = c; return ''; });
  const marks = [[/^\[From the owner, in the panel\]\n?/, '', ''], [/^\[From the owner, passed on by Main JARVIS from Telegram\]\n?/, '', 'telegram'],
    [/^\[(?:From Main JARVIS|Brief from Main JARVIS[^\]]*)\]\n?/, 'JARVIS', 'main'], [/^\[Team update from the panel\]\n?/, 'Team', 'team'],
    [/^\[From the Claude agent in ([^,\]]+)[^\]]*\]\n?/, 'Claude', 'agent']];
  for (const [re, who, kind] of marks) { const m = re.exec(text); if (m) return [m[1] ? `${who} · ${m[1]}` : who, kind, text.slice(m[0].length), ctx]; }
  return ['', '', text, ctx];
}

// ---- files: every file a conversation produced (links in answers, files its tools wrote) and, in a thread, every
// file its team's agents wrote; newest first, with when ----
function aiConvFiles(c) {
  const seen = new Map();
  const add = (p, t, how) => { p = p.replace(/^~(?=\/)/, window.cpHome || '~'); if (!seen.has(p) || seen.get(p).t < t) seen.set(p, { path: p, t, how }); };
  for (const it of c.items) {
    if (it.role === 'tool' && /^(write_file|patch)$/.test(it.name) && /^[~/]\S+$/.test(it.text || '')) add(it.text, it.t, it.name === 'patch' ? 'Edited' : 'Written');
    if (it.role === 'assistant') for (const m of (it.text || '').matchAll(RICH_LOCAL)) add(m[3] || m[4], it.t, 'Shared');
  }
  return [...seen.values()].sort((a, b) => b.t - a.t);
}
const aiFileRow = (p, t, meta) => { const u = '/api/ai/file?path=' + encodeURIComponent(p); const name = p.split('/').pop();
  return `<a class="th-file" href="${esc(u)}" target="_blank" rel="noopener">${fxBadge(name)}<span class="th-file-m"><b>${esc(name)}</b><small>${esc(tilde(p).replace(/[^/]*$/, ''))}</small></span>` +
    `<span class="th-file-w">${esc(meta)}<small>${aiDay(t)} · ${aiTime(t)}</small></span></a>`; };
const teamFile = (f) => ({ path: f.path, t: f.last, how: (f.created ? 'Created' : 'Edited') + ' by ' + (f.who || f.pane) });
// the Files tab: the last month's files, the main chat's, then each thread's (its lead's and its team's); a search on top
const AI_FILES_DAYS = 30;
async function aiFilesLoad() {
  const since = Date.now() / 1000 - AI_FILES_DAYS * 86400, ts = AI.threads.filter((t) => thLast(t) >= since);
  const rs = await Promise.all(ts.map((t) => api('/api/ai/thread/files?id=' + encodeURIComponent(t.id)).catch(() => null)));
  AI.tfiles = ts.map((t, i) => { const r = rs[i]?.status === 200 ? rs[i] : {}; return { t, files: [...aiConvFiles({ items: r.lead || [] }), ...(r.files || []).map(teamFile)] }; });
  aiRender();
}
function aiFilesTabHTML() {
  const since = Date.now() / 1000 - AI_FILES_DAYS * 86400, q = (AI.fq || '').trim().toLowerCase();
  const keep = (fs) => { const m = new Map(); for (const f of fs) if (f.t >= since && (!q || f.path.toLowerCase().includes(q)) && !(m.get(f.path)?.t >= f.t)) m.set(f.path, f); return [...m.values()].sort((a, b) => b.t - a.t); };
  const groups = [['Main chat', ic('sparkle'), keep(aiConvFiles(AI))], ...(AI.tfiles || []).map(({ t, files }) => [t.title, thOrb(t, 14), keep(files)])].filter((g) => g[2].length);
  const list = groups.map(([title, mark, fs]) => `<div class="aic-cat ai-fg">${mark}${esc(title)} · ${fs.length}</div><div class="card flush th-files">${fs.map((f) => aiFileRow(f.path, f.t, f.how)).join('')}</div>`).join('');
  return list + (AI.tfiles ? '' : '<div class="skel"></div>') + (list || !AI.tfiles ? '' : `<div class="empty">${q ? `No file matches “${esc(AI.fq.trim())}” in the last month` : 'No files in the last month'}</div>`);
}
function aiFilesHTML(c) {
  const row = aiFileRow;
  const own = aiConvFiles(c), pane = c === TH, lead = pane ? 'its lead' : 'JARVIS';
  return `<div class="aic-cat">From ${lead}</div>` + (own.length ? `<div class="card flush th-files">${own.map((f) => row(f.path, f.t, f.how)).join('')}</div>` : `<div class="empty">Nothing yet: files ${lead} writes or links show up here, with when.</div>`) +
    (pane ? '<div class="aic-cat">From its team</div>' + (TH.team === null ? '<div class="skel"></div>' : TH.team.length
      ? `<div class="card flush th-files">${TH.team.map(teamFile).map((f) => row(f.path, f.t, f.how)).join('')}</div>`
      : '<div class="empty">Nothing yet: what its agents write shows up here.</div>') : '');
}

// ---- starting over, and the model. When a conversation keeps failing (a refusal sticks to its history), a new one
// goes to JARVIS without that history; the chat keeps it, above a divider (aisession.go). /new and /model work too ----
async function aiNewChat() {
  const r = await api('/api/ai/session', {}).catch(() => ({ status: 0 }));
  if (r.status !== 200) return toast(r.error || "Couldn't start a new conversation", true);
  aiConnToggle(false);
  if (AI.tab !== 'msgs') aiTab('msgs');
  toast('New conversation: JARVIS starts fresh, keeping its memory and your threads');
}
async function aiSetModel(m) {
  const r = await api('/api/ai/model', { model: m }).catch(() => ({ status: 0 }));
  if (r.status !== 200) return toast(r.error || "Couldn't switch the model", true);
  toast(m ? `The panel chat now uses ${m}` : 'The panel chat uses Hermes’ default model');
}
const aiStuckHTML = () => `<div class="ai-btns"><div class="ai-btns-r"><button type="button" class="ai-btn primary" data-ainew>${ic('arrow-rotate')}New conversation</button>` +
  `<button type="button" class="ai-btn" data-aiopen="control:overview">${ic('tuning')}Change model</button></div></div>`;

// ---- files and photos for JARVIS: attach, drop anywhere on the page, or paste. A photo is scaled down here (the model reads
// up to ~1568 px; Hermes takes 10 MB a request) and uploaded at once; sending hands the panel their paths, and
// it gives them to JARVIS as images (assistant.go aiAttach) ----
const AI_IMG_PX = 1568;
async function aiPrepImage(file) {
  const plain = /^image\/(png|jpeg|gif|webp)$/.test(file.type) && file.size <= 1.5e6;
  let bmp;
  try { bmp = await createImageBitmap(file); } catch { if (plain) return file; throw new Error(`${file.name || 'That image'}: can't read it`); }
  const k = Math.min(1, AI_IMG_PX / Math.max(bmp.width, bmp.height));
  if (plain && k === 1) { bmp.close(); return file; }
  const c = document.createElement('canvas');
  c.width = Math.round(bmp.width * k);
  c.height = Math.round(bmp.height * k);
  c.getContext('2d').drawImage(bmp, 0, 0, c.width, c.height);
  bmp.close();
  const blob = await new Promise((ok) => c.toBlob(ok, 'image/jpeg', 0.86));
  if (!blob) throw new Error("Couldn't prepare that image");
  return new File([blob], (file.name || 'photo').replace(/\.\w+$/, '') + '.jpg', { type: 'image/jpeg' });
}
// Any other file (a spreadsheet, a PDF, a zip…) goes as it is, and JARVIS opens it from its path; so does a photo
// this browser can't read (it can't be scaled down for the model to see)
function aiAddFiles(list) {
  // no box on screen (the Threads list): Main's is the one they go in, so show it rather than fill a hidden one
  if (!aiBoxTh() && !$('#ai-foot')?.offsetParent) aiTab('msgs');
  for (const file of list) {
    const img = /^image\//.test(file.type) && file.type !== 'image/svg+xml';
    if (AI.files.length >= 10) { toast('Up to 10 files a message', true); break; }
    if (img && AI.files.filter((f) => !f.doc).length >= 6) { toast('Up to 6 photos a message', true); continue; }
    if (file.size > 25 << 20) { toast(`${file.name}: files up to 25 MB`, true); continue; }
    const f = img ? { url: URL.createObjectURL(file), path: '' } : { doc: true, name: file.name || 'file', size: file.size, url: '', path: '' };
    const asFile = () => { aiRmURL(f); Object.assign(f, { doc: true, name: file.name || 'image', size: file.size }); return file; };
    f.up = (img ? aiPrepImage(file).catch(asFile) : Promise.resolve(file)).then(uploadFile)
      .then((p) => { f.path = p; aiFilesRender(); }, (e) => { toast(e.message, true); aiRmFile(f); });
    AI.files.push(f);
  }
  aiFilesRender();
}
const aiRmURL = (f) => { if (f.url) URL.revokeObjectURL(f.url); f.url = ''; };
function aiRmFile(f) { aiRmURL(f); AI.files = AI.files.filter((x) => x !== f); aiFilesRender(); }
const aiBoxTh = () => !!$('#ai')?.classList.contains('th-on'); // the open thread's box is the one up
function aiFilesRender() {
  const th = aiBoxTh(), box = $(th ? '#thp-files' : '#ai-files'), other = $(th ? '#ai-files' : '#thp-files');
  if (other) { other.hidden = true; other.innerHTML = ''; }
  if (!box) return;
  box.hidden = !AI.files.length;
  const rm = (i) => `<button type="button" data-airmfile="${i}" aria-label="Remove">${ic('xmark')}</button>`;
  box.innerHTML = AI.files.map((f, i) => (f.doc
    ? `<div class="fchip doc${f.path ? '' : ' up'}">${fxBadge(f.name)}<span>${esc(f.name)}</span><small>${fmtB(f.size)}</small>${rm(i)}</div>`
    : `<div class="fchip${f.path ? '' : ' up'}"><img src="${esc(f.url)}" alt="">${rm(i)}</div>`)).join('');
  aiSendState();
  thSendState();
}
let aiDragN = 0;
const aiDragOK = (e) => S.view === 'ai' && !!$('#ai-foot') && !e.target.closest?.('#drawer') && [...(e.dataTransfer?.types || [])].includes('Files');
const aiDropHint = (on) => { $('#ai-foot')?.classList.toggle('dropping', on && !aiBoxTh()); $('#thp-form')?.classList.toggle('dropping', on && aiBoxTh()); };
document.addEventListener('dragenter', (e) => { if (aiDragOK(e) && aiDragN++ === 0) aiDropHint(true); });
document.addEventListener('dragleave', (e) => { if (aiDragOK(e) && --aiDragN <= 0) { aiDragN = 0; aiDropHint(false); } });
document.addEventListener('dragover', (e) => { if (aiDragOK(e)) e.preventDefault(); });
document.addEventListener('drop', (e) => { if (!aiDragOK(e)) return; aiDragN = 0; aiDropHint(false); e.preventDefault(); aiAddFiles([...e.dataTransfer.files]); });
// "[Attached image: <path>]" and "[Attached file: <path>]" lines (assistant.go aiAttach) show as the photos and
// the files above your message
function aiAttached(text) {
  const imgs = [], docs = [];
  const rest = text.replace(/\n*\[Attached (image|file): (\/[^\]\n]+)\]/g, (_, k, p) => { (k === 'image' ? imgs : docs).push(p); return ''; });
  return [rest.trim(), imgs, docs];
}

// ---- drawing ----
function aiRender(jump) {
  const body = $('#ai-body');
  if (!body) return;
  $('#ai-foot')?.classList.toggle('busy', !!AI.live); // the bar's glow drifts faster while JARVIS works
  $('[data-aiconn]')?.setAttribute('aria-pressed', String(AI.conn));
  aiChanRender();
  cancelAnimationFrame(RAIL.raf); RAIL.raf = requestAnimationFrame(aiRail);
  if (held() && !jump && AI.drawn) { AI.stale = true; return; } // flushStale (app.js) draws it when the finger or scroll stops; a redraw asked for (a thread opened or closed: AI.drawn cleared) isn't held
  AI.stale = false;
  const nearEnd = innerHeight + scrollY >= document.documentElement.scrollHeight - 160;
  const hist = $('#ai-hist'), nowEl = $('#ai-now'), conns = $('#ai-conns'), all = $('#ai-all');
  const away = AI.conn || AI.tab !== 'msgs'; // settings, the threads or the files, in place of the chat
  hist.hidden = nowEl.hidden = away;
  conns.hidden = !AI.conn;
  all.hidden = AI.conn || AI.tab === 'msgs';
  keepScroll([window], () => {
    if (AI.conn) { // the control room (ai-control.js) redraws when its data changes, so its fields keep focus
      const ck = AIC.v + ':' + AIC.tab + ':' + (AIC.tab === 'overview' ? JSON.stringify([AI.state, AI.google, AI.tg, AI.models]) : '');
      const typing = conns.contains(document.activeElement) && document.activeElement.matches('input, textarea, select');
      if (conns.dataset.k !== ck && !typing) { conns.innerHTML = aicHTML(); aicAfter(conns); conns.dataset.k = ck; }
      return;
    }
    if (AI.tab === 'files') { // its search stays put (and keeps focus) while the list under it redraws
      if (!$('#ai-fq')) {
        all.innerHTML = `<label class="aic-q ai-fq">${ic('search')}<input id="ai-fq" type="search" placeholder="Search files from the last month" autocomplete="off" spellcheck="false" aria-label="Search files"></label><div id="ai-flist"></div>`;
        all.dataset.k = 'files';
        const q = $('#ai-fq');
        q.value = AI.fq || '';
        q.addEventListener('input', () => { AI.fq = q.value; aiRender(); });
      }
      const list = $('#ai-flist'), k = JSON.stringify([AI.v, AI.fq, AI.tfiles?.map((x) => x.files.length), aiDay(Date.now() / 1000)]);
      if (list.dataset.k !== k) { list.innerHTML = aiFilesTabHTML(); list.dataset.k = k; }
      return;
    }
    if (AI.tab !== 'msgs') {
      const k = AI.tab + JSON.stringify(AI.tab === 'threads' ? [AI.threads, Math.floor(now() / 60), [...AI.open]] : [AI.v]) + aiDay(Date.now() / 1000); // "Today" moves on
      const solo = AI.tab === 'threads' && TH.solo && TH.id ? 'solo:' + TH.id : '';
      if (solo) { // one thread, as a chat of its own: a way back, then the thread (thPlace puts it in the slot)
        if (all.dataset.k !== solo) { thPark(); all.innerHTML = `<button type="button" class="th-back" data-thlist>${ic('arrow-left')}All threads</button><div class="th-slot" data-thslot></div>`; all.dataset.k = solo; }
        return;
      }
      if (all.dataset.k !== k) { thPark(); all.innerHTML = aiThreadsHTML(); all.dataset.k = k; }
      return;
    }
    const key = `${TH.id}:${TH.parent}:${AI.v}:${AI.more}:${AI.loadingMore}:${!!AI.state}:${AI.state?.err || ''}:${!!AI.live}:${Math.floor(now() / 60)}`;
    if (key !== AI.drawn) { // the history only redraws when it changed: its diagrams and charts stay put
      thPark();
      artKeep(hist, () => { hist.innerHTML = aiHistHTML(AI); });
      richEnhance(hist);
      AI.drawn = key;
    }
    nowEl.innerHTML = aiNowHTML(AI);
  });
  // a thread open in the Threads tab keeps its place (at its end when it opened)
  if (away) { if (jump && !(TH.solo && TH.id)) scrollTo(0, 0); } else if (jump || nearEnd) scrollTo(0, document.documentElement.scrollHeight);
  thPlace();
  aiSendState();
  aiDown();
}

// More than a screen above the latest: the jump-down button by the composer shows (as in the terminal chats)
function aiDown() {
  $('#ai-down')?.classList.toggle('on', !AI.conn && AI.tab === 'msgs' && document.documentElement.scrollHeight - scrollY - innerHeight > innerHeight);
  $('#thp-down')?.classList.toggle('on', aiBoxTh() && thEnd() - scrollY - innerHeight > innerHeight); // the thread's box is up: its own
}
// where an open thread's latest is: the page's end in the Threads tab, the thread's own end in Main's chat
function thEnd() {
  const p = $('#th-pane');
  if (TH.solo || !p) return document.documentElement.scrollHeight;
  return p.getBoundingClientRect().bottom + scrollY + 40;
}
addEventListener('scroll', () => { if (S.view === 'ai') { aiDown(); aiRailAt(); thBox(); } }, { passive: true });

// ---- the rail: the chat's history as ticks down the right edge (yours long, JARVIS's short, a violet one where a
// thread was opened, a blue one where you are). Hover: the day and what the chat was about there; click: go there.
// Laid out from the drawn chat (a ResizeObserver redraws it as the chat grows); a computer's, not a phone's.
const RAIL = { ticks: [], ro: null, raf: 0, chap: {}, chapAt: 0 }; // chap: the server's one line per stretch (chapters.go)
// The chat's time rail: a tick per stretch of the conversation, with the server's one-line chapters, to jump around a long chat.
function aiRail() {
  const hist = $('#ai-hist'), ai = $('#ai');
  if (!hist || !ai) return;
  let r = $('#ai-rail');
  if (!r) {
    ai.insertAdjacentHTML('beforeend', '<nav id="ai-rail" class="ai-rail" aria-label="Where you are in the chat"><div class="rail-ts"></div><i class="rail-at"></i><div class="rail-tip" hidden></div></nav>');
    r = $('#ai-rail');
    r.addEventListener('pointermove', (e) => aiRailTip(e.clientY));
    r.addEventListener('pointerleave', () => aiRailTip(null));
    r.addEventListener('click', (e) => { const t = aiRailNear(e.clientY); if (t) scrollTo({ top: Math.max(0, t.y - innerHeight / 3), behavior: 'smooth' }); });
    RAIL.ro = new ResizeObserver(() => { cancelAnimationFrame(RAIL.raf); RAIL.raf = requestAnimationFrame(aiRail); });
    RAIL.ro.observe(hist);
    addEventListener('resize', () => { cancelAnimationFrame(RAIL.raf); RAIL.raf = requestAnimationFrame(aiRail); });
  }
  r.hidden = AI.tab !== 'msgs' || !!AI.conn;
  if (r.hidden) return;
  if (Date.now() - RAIL.chapAt > 60e3) { // what each stretch was about, refreshed a minute at a time
    RAIL.chapAt = Date.now();
    fetch('/api/ai/chapters').then((x) => (x.ok ? x.json() : null)).then((m) => { if (m) { RAIL.chap = m; aiRail(); } }).catch(() => {});
  }
  const H = document.documentElement.scrollHeight, h = r.clientHeight;
  let about = '';
  const ticks = [];
  for (const el of $$('#ai-hist .msg-u, #ai-hist .ai-a, #ai-hist .thc')) {
    if (!el.offsetParent) continue; // hidden (an open thread's card)
    const kind = el.classList.contains('thc') ? 'th' : el.classList.contains('msg-u') && !el.classList.contains('from-o') ? 'you' : 'ai';
    const said = kind === 'you' ? RAIL.chap[el.dataset.k] || (el.querySelector('.bubble')?.textContent || '').trim().split('\n')[0] : '';
    if (said) about = said;
    const day = el.closest('.ai-dayg')?.querySelector('.ai-day')?.textContent || '';
    const inTh = !!el.closest('#th-pane');
    if (inTh && !aiRailShown(el)) continue; // clipped away (parting, or folded above its newest part)
    const tt = kind === 'th' ? thOf(el.dataset.aith) : null; // a thread starts its own line here: its title and where it stands
    const tip = tt ? `Thread opened: ${tt.title}${tt.note ? ' — ' + tt.note : ''}` : (inTh ? (thOf(TH.id)?.title || 'Thread') + ' · ' : '') + (about || 'JARVIS');
    const y = el.getBoundingClientRect().top + scrollY;
    const at = Math.round((y / H) * h);
    const last = ticks[ticks.length - 1];
    const rank = { th: 3, you: 2, ai: 1 };
    if (last && at - last.at < 4) { if (rank[kind] > rank[last.kind]) Object.assign(last, { kind, tip, day, y, inTh }); continue; } // too close: the weightier one
    ticks.push({ at, kind, tip, day, y, inTh });
  }
  RAIL.ticks = ticks;
  // the open thread's own history branches off the main line: its ticks step aside, joined by a line
  // the open thread: a band from exactly where it starts to where it ends in the chat (the pane's own box)
  const pane = $('#ai-hist #th-pane:not([hidden])');
  let brl = '';
  if (pane) {
    const pr = pane.getBoundingClientRect(), a0 = Math.round(((pr.top + scrollY) / H) * h), a1 = Math.round(((pr.bottom + scrollY) / H) * h);
    brl = `<i class="rail-th" style="top:${a0}px;height:${Math.max(4, a1 - a0)}px"></i>`;
    ticks.push({ at: a0, kind: 'thx', tip: `Thread starts: ${thOf(TH.id)?.title || 'New thread'}`, day: '', y: pr.top + scrollY }, { at: a1, kind: 'thx', tip: `Thread ends: ${thOf(TH.id)?.title || 'New thread'}`, day: '', y: pr.bottom + scrollY - innerHeight * 0.6 });
  }
  r.querySelector('.rail-ts').innerHTML = brl + ticks.filter((t) => t.kind !== 'thx').map((t) => `<b class="t-${t.kind}${t.inTh ? ' in-th' : ''}" style="top:${t.at}px"></b>`).join('');
  aiRailAt();
}
function aiRailShown(el) {
  const f = el.closest('.thp-fold') || el.closest('#th-pane'), r = el.getBoundingClientRect(), c = f.getBoundingClientRect(), p = $('#th-pane').getBoundingClientRect();
  return r.top >= Math.max(c.top, p.top) - 1 && r.top <= Math.min(c.bottom, p.bottom);
}
function aiRailAt() {
  const r = $('#ai-rail');
  if (!r || r.hidden) return;
  r.querySelector('.rail-at').style.top = Math.round(((scrollY + innerHeight / 2) / document.documentElement.scrollHeight) * r.clientHeight) + 'px';
}
function aiRailNear(cy) {
  const r = $('#ai-rail');
  const y = cy - r.getBoundingClientRect().top;
  let best = null;
  for (const t of RAIL.ticks) if (!best || Math.abs(t.at - y) < Math.abs(best.at - y)) best = t;
  return best && Math.abs(best.at - y) < 24 ? best : null;
}
function aiRailTip(cy) {
  const r = $('#ai-rail'), tip = r?.querySelector('.rail-tip');
  if (!tip) return;
  const t = cy == null ? null : aiRailNear(cy);
  r.querySelectorAll('.rail-ts b.on').forEach((b) => b.classList.remove('on'));
  tip.hidden = !t;
  if (!t) return;
  r.querySelector(`.rail-ts b[style="top:${t.at}px"]`)?.classList.add('on');
  tip.className = 'rail-tip k-' + t.kind;
  tip.style.top = t.at + 'px';
  tip.innerHTML = `<small>${esc(t.day)}</small>${esc(t.tip.length > 110 ? t.tip.slice(0, 110) + '…' : t.tip)}`;
}

// the agents' messages to each other, rewritten for you to read (GET /api/ai/digests, digest.go): fetched with the
// chat, every minute while it's open; when new ones arrive, both chats redraw
const DIG = { m: {}, k: '', at: 0 };
async function digLoad() {
  if (Date.now() - DIG.at < 60e3) return;
  DIG.at = Date.now();
  const m = await fetch('/api/ai/digests').then((x) => (x.ok ? x.json() : null)).catch(() => null);
  const k = m && Object.keys(m).length + ':' + Object.keys(m).slice(-1)[0];
  if (!m || k === DIG.k) return;
  DIG.m = m; DIG.k = k; AI.v++; TH.v++; AI.drawn = '';
  aiRender(); if (TH.id) thRender(true);
}
setInterval(() => { if (S.view === 'ai' && !document.hidden) digLoad(); }, 15e3);

// ---- replying to one message (as on Telegram or WhatsApp): Reply on any message sets it on that chat's box (Main's,
// or the thread's), a strip above the text says which; sent, the message carries Hermes' own reply pointer
// ("[Replying to your previous message: "…"]" for JARVIS's or the lead's own, "[Replying to: "…"]" for anyone else's),
// the same thing a Telegram reply puts in front of JARVIS, so Main and the leads (SOUL.md, LEAD.md) read it as an
// answer to THAT message ----
const AI_RE = { ai: null, th: null };
// Starts a reply to a message: quotes it into the box (the main chat's, or the open thread's).
function aiReplyTo(k, inThread) {
  const box = inThread ? 'th' : 'ai', it = k && (inThread ? TH.items : AI.items).find((x) => x.k === k);
  if (it) {
    const own = it.role === 'assistant';
    const [from, kind, unmarked] = aiFrom(it.text || '');
    const text = own ? it.text || '' : aiAttached(aiQuote(unmarked)[1])[0];
    const who = own ? (inThread ? 'its lead' : 'JARVIS') : kind === 'main' ? 'Main JARVIS' : kind === 'team' ? 'the team update' : from || 'yourself';
    AI_RE[box] = { k, own, who, text };
  } else AI_RE[box] = null;
  const form = $(inThread ? '#thp-form' : '#ai-form'), ta = $(inThread ? '#thp-text' : '#ai-text');
  if (!form || !ta) return;
  const comp = form.matches('.composer') ? form : form.querySelector('.composer');
  let strip = comp.querySelector('.npb-rep');
  const r = AI_RE[box];
  if (!r) { strip?.remove(); return; }
  if (!strip) { strip = document.createElement('div'); strip.className = 'npb-rep'; ta.before(strip); strip.animate([{ opacity: 0, transform: 'translateY(0.375rem)' }, { opacity: 1, transform: 'none' }], { duration: 220, easing: 'cubic-bezier(.22, 1, .36, 1)' }); }
  const plain = r.text.replace(/```[\s\S]*?```/g, ' [block] ').replace(/[*_`#>|]+/g, '').replace(/\s+/g, ' ').trim();
  strip.innerHTML = `<span class="npb-rep-ic">${ic('undo')}</span><button type="button" class="npb-rep-t" data-aiq title="Go to that message"><b>Replying to ${esc(r.who)}</b><span>${esc(plain.slice(0, 160) || 'that message')}</span></button>` +
    `<button type="button" class="npb-rep-x" data-airex aria-label="Don't reply to it" title="Don't reply to it (Esc)">${ic('xmark')}</button>`;
  strip.querySelector('[data-aiq]').dataset.k = r.k;
  if (inThread) $('#th-pane')?.classList.add('reply'); // the thread's own box, even scrolled to the end
  else if ($('#ai')?.classList.contains('th-on')) scrollTo({ top: document.documentElement.scrollHeight, behavior: 'smooth' }); // a thread's box is up: Main's is at the end
  ta.focus({ preventScroll: true });
}
function aiReplyWrap(box, text) {
  const r = AI_RE[box];
  if (!r) return text;
  const q = r.text.length > 4000 ? r.text.slice(0, 4000) + '…' : r.text;
  return `[Replying to${r.own ? ' your previous message' : ''}: "${q.replace(/"\]/g, '" ]')}"]\n\n${text}`;
}
// a reply's quote (or the strip): to the message it answers, which lights up for a moment
function aiReplyJump(el) {
  const inThread = !!el.closest('#th-pane'), its = inThread ? TH.items : AI.items, root = inThread ? $('#thp-hist') : $('#ai-hist');
  let k = el.dataset.k;
  if (!k) {
    const q = el.textContent.replace(/…$/, '').trim().slice(0, 80);
    k = q && its.find((x) => x.role !== 'user' ? (x.text || '').includes(q) : aiFrom(x.text || '')[2].includes(q) && !aiQuote(aiFrom(x.text || '')[2])[0].includes(q))?.k;
  }
  const at = k && root?.querySelector(`[data-k="${CSS.escape(k)}"], [data-aicopy="${CSS.escape(k)}"]`);
  const msg = at?.closest('.msg-u') || (at?.closest('.ai-acts')?.previousElementSibling?.matches('.ai-ans') ? at.closest('.ai-acts').previousElementSibling : at?.closest('.ai-a'));
  if (!msg) return toast("That message is further back than the chat has loaded");
  msg.scrollIntoView({ block: 'center', behavior: 'smooth' });
  msg.animate([{ background: 'rgba(154, 123, 255, .16)' }, { background: 'transparent' }], { duration: 1400, easing: 'ease-out' });
}

// "[Replying to: "…"] text" (a Telegram reply) shows the quote above the message
function aiQuote(text) {
  const m = /^\[Replying to(?: your previous message)?: "([\s\S]*?)"\]\s*/.exec(text) || /^\[Replying to: (your previous message)\]\s*/.exec(text);
  return m ? [m[1], text.slice(m[0].length)] : ['', text];
}

// a message that isn't JARVIS's: yours (a bubble on the right), or, in a thread, Main JARVIS's, the team's or one of its
// agents' (on the left, like a reply). o.tools: its actions on hover (Main: start a thread, copy)
function aiUserHTML(it, sending, extra = '', o = {}) {
  const [from, kind0, unmarked] = aiFrom(it.text || '');
  const [q, rest] = aiQuote(unmarked);
  const [text, imgs, docs] = aiAttached(rest);
  const kind = kind0 === 'main' ? 'main' : kind0 === 'team' ? 'team' : kind0 === 'agent' || it.ch === 'agent' ? 'claude' : 'you';
  const name = { main: 'Main JARVIS', team: 'Team update', claude: from || 'Claude', you: 'You' }[kind];
  const ch = kind0 === 'telegram' ? 'telegram' : (kind === 'you' || kind === 'claude') && it.ch !== 'thread' ? it.ch : '';
  const tools = o.tools && it.k && !String(it.k).startsWith('failed:') ? `<div class="ai-acts">${aiAct('aire', it.k, 'undo', 'Reply to this')}${o.thread ? '' : aiAct('aireply', it.k, 'chat-round-dots', 'Start a thread from this')}${aiAct('aicopy', it.k, 'copy', 'Copy')}</div>` : '';
  return `<div class="msg-u ai-u${sending ? ' sending' : ''}${kind === 'you' ? '' : ' from-o'}"${it.k ? ` data-k="${esc(it.k)}"` : ''}>${aiMeta(name, ch, aiTime(it.t))}` +
    `${q ? `<button type="button" class="ai-q" data-aiq title="Go to the message this answers">${esc(q.length > 280 ? q.slice(0, 280) + '…' : q)}</button>` : ''}` +
    (imgs.length ? `<div class="uimgs">${imgs.map((p) => { const u = '/api/ai/file?path=' + encodeURIComponent(p); return `<a href="${esc(u)}" target="_blank" rel="noopener"><img src="${esc(u)}" alt="Sent photo" loading="lazy"></a>`; }).join('')}</div>` : '') +
    (docs.length ? `<div class="udocs">${docs.map((p) => `<a class="udoc" href="${esc('/api/ai/file?path=' + encodeURIComponent(p))}" download>` +
      `${fxBadge(p)}<span>${esc(uploadLabel(p.split('/').pop()))}</span>${ic('download2')}</a>`).join('')}</div>` : '') +
    (text ? (kind === 'you' ? `<div class="bubble">${mdInline(text)}</div>` // between the agents (Main, the team, an agent): real markdown, all of it
      : DIG.m[it.k] ? `<div class="bubble md dig">${md(DIG.m[it.k])}<details class="dig-orig"><summary>Original message</summary><div class="md">${md(text)}</div></details></div>` // made readable (digest.go)
      : `<div class="bubble md">${md(text)}</div>`) : '') + tools + extra + '</div>';
}

// Hermes waking itself (a subagent or background process finished, …): a folded card, not your message
const AI_EV = { async_delegation_complete: ['Subagent results', 'people'], process_complete: ['Background process finished', 'terminal-square'],
  model_switch: ['Model switched', 'cpu'], skill_invocation: ['Skill', 'wand-sparkle'], auto_continue: ['Continuing', 'bolt'] };
function aiEventHTML(it) {
  const [label, icon] = AI_EV[it.name] || ['Notification', 'bell'];
  const text = it.text || '';
  const tasks = [...text.matchAll(/^--- (\S) TASK \d+\/\d+: (.*?) {2}\(status=/gm)]; // a fan-out's tasks: "✓ goal · ✗ goal"
  const preview = tasks.length ? tasks.map((m) => m[1] + ' ' + m[2]).join(' · ') : text.split('\n')[0].replace(/^\[|\]$/g, '');
  return `<details class="ai-ev" data-k="${esc(it.k)}"${aiOpen(it.k)}><summary><span class="tr-ic">${ic(icon)}</span><b>${label}</b>` +
    `<span class="ai-ev-p">${esc(preview)}</span><span class="ai-ch ch-${esc(it.ch)}">${esc(AI_CH[it.ch] || it.ch)}</span><span class="ai-ev-t">${aiTime(it.t)}</span>` +
    `</summary><pre class="ai-ev-b">${esc(text)}</pre></details>`;
}

// ---- the trace: a turn's thinking, narration and tool calls on one rail ----
// "mcp__vm__fleet_status" → ["fleet_status", "vm"]; built-in tools keep their name
const aiTool = (name) => { const m = /^mcp__(.+?)__(.+)$/.exec(name || ''); return m ? [m[2], m[1]] : [name || 'tool', '']; };
const AI_TOOL_IC = [[/^vm$/, 'server'], [/terminal|shell|process|exec|code/, 'terminal-square'], [/file|read|write|patch|edit/, 'document-code'],
  [/web|browser|fetch|url|search/, 'global'], [/memory|session/, 'layers'], [/deleg|agent/, 'people'], [/cron|schedule/, 'clock-circle'],
  [/skill/, 'wand-sparkle'], [/image|vision/, 'image3'], [/google|gmail|calendar|mail/, 'chat-round-dots']];
const aiToolIc = (name, src) => (AI_TOOL_IC.find(([re]) => re.test(src || name)) || [0, 'bolt'])[1];
const aiSecs = (s) => (s < 1 ? Math.max(0.1, s).toFixed(1) + 's' : s < 60 ? s.toFixed(s < 10 ? 1 : 0) + 's' : fmtDur(s));
const aiOpen = (k) => (AI.open.has(k) ? ' open' : '');

// a tool's JSON, indented and coloured. The panel cuts results at 1,500 characters, so it is often unfinished:
// laid out by walking it (string-aware), not by JSON.parse. '' when it isn't JSON.
function aiJSON(t) {
  const s = (t || '').trim();
  if (!/^[[{]/.test(s)) return '';
  try { JSON.parse(s); } catch { if (!/^[[{]\s*["[{\]}]/.test(s) || !s.endsWith('…')) return ''; }
  let o = '', d = 0, i = 0;
  const nl = () => '\n' + '  '.repeat(Math.max(d, 0));
  while (i < s.length) {
    const c = s[i];
    if (c === '"') {
      let j = i + 1;
      while (j < s.length && s[j] !== '"') j += s[j] === '\\' ? 2 : 1;
      const key = /^\s*:/.test(s.slice(j + 1, j + 9));
      o += `<span class="j-${key ? 'k' : 's'}">${esc(s.slice(i, j + 1))}</span>`;
      i = j + 1;
      continue;
    }
    if (c === '{' || c === '[') {
      const ws = s.slice(i + 1).match(/^\s*/)[0].length;
      if (s[i + 1 + ws] === (c === '{' ? '}' : ']')) { o += c + s[i + 1 + ws]; i += ws + 2; continue; }
      d++; o += c + nl();
    } else if (c === '}' || c === ']') { d--; o += nl() + c; }
    else if (c === ',') o += ',' + nl();
    else if (c === ':') o += ': ';
    else if (!/\s/.test(c)) {
      const w = s.slice(i).match(/^[^\s,:{}[\]"]+/)[0];
      o += `<span class="j-${/^(true|false|null)$/.test(w) ? 'b' : 'n'}">${esc(w)}</span>`;
      i += w.length;
      continue;
    }
    i++;
  }
  return o;
}

function aiStepHTML(s) {
  if (s.k === 'note') return `<div class="tr-s tr-note"><i class="tr-n"></i><div class="tr-t">${mdInline(s.text || '')}</div></div>`;
  if (s.k === 'think') {
    const first = (s.text || '').trim().split('\n')[0];
    return `<details class="tr-s tr-think" data-k="${esc(s.key)}"${aiOpen(s.key)}><summary><i class="tr-n"></i><span class="tr-l">Thinking</span>` +
      `<span class="tr-p">${esc(first)}</span></summary><div class="tr-b"><div class="tr-thought">${esc(s.text || '')}</div></div></details>`;
  }
  const [name, src] = aiTool(s.name);
  const st = s.state || 'ok';
  const tail = st === 'run' ? '<span class="tr-d run">running</span>' : st === 'err' ? '<span class="tr-d err">failed</span>' : s.dur ? `<span class="tr-d">${aiSecs(s.dur)}</span>` : '';
  const io = (label, text) => { const j = aiJSON(text); return `<div class="tr-io"><span>${label}</span><pre${j ? ' class="json"' : ''}>${j || esc(text)}</pre></div>`; };
  const body = (s.text ? io('in', s.text) : '') + (s.res ? io('out', s.res) : '');
  return `<details class="tr-s tr-tool ${st}" data-k="${esc(s.key)}"${aiOpen(s.key)}${body ? '' : ' data-empty'}><summary><i class="tr-n"></i>` +
    `<span class="tr-ic">${ic(aiToolIc(name, src))}</span><b>${esc(name)}</b>${src ? `<span class="tr-src">${esc(src)}</span>` : ''}` +
    `<code>${esc(s.text || '')}</code>${tail}</summary>${body ? `<div class="tr-b">${body}</div>` : ''}</details>`;
}

// a long trace folds its middle: the start and the latest steps stay in view
function aiTraceHTML(steps, live, key) {
  if (!steps.length) return '';
  let inner;
  if (!live && steps.length > 9) {
    const fk = key + ':fold';
    inner = steps.slice(0, 2).map(aiStepHTML).join('') +
      `<details class="tr-fold" data-k="${esc(fk)}"${aiOpen(fk)}><summary><i class="tr-n"></i><span>${plural(steps.length - 6, 'more step')}</span></summary>${steps.slice(2, -4).map(aiStepHTML).join('')}</details>` +
      steps.slice(-4).map(aiStepHTML).join('');
  } else inner = steps.map(aiStepHTML).join('');
  return `<div class="tr${live ? ' tr-live' : ''}">${inner}</div>`;
}

// a conversation's history (c: AI for Main, TH for the open thread), Slack-style rows
function aiHistHTML(c) {
  const pane = c === TH;
  // Hermes saves a turn as it goes (your message, then each step): while it runs, the live view below shows it,
  // so the history leaves out this conversation's items from that turn on, and takes them back once it's done
  const cut = c.live ? c.live.since - 1 : Infinity, mine = pane ? 'thread' : 'panel';
  const its = c.live ? c.items.filter((it) => !(it.ch === mine && it.t >= cut)) : c.items;
  if (!AI.state) return '<div class="skel"></div><div class="skel"></div>';
  if (pane && (TH.id === 'new' || (!its.length && !c.live))) return '';
  if (!its.length && !c.live) {
    return `<div class="empty ai-empty">${ic('sparkle')}<b>Say hi to JARVIS</b>` +
      '<span>Ask for anything on this VM: start an agent, check CI, chart your data, read your mail. Your Telegram chat shows up here too.</span>' +
      (AI.state.err ? `<span class="err">${esc(AI.state.err)}</span>` : '') + '</div>'; // no error yet: still connecting
  }
  let h = !pane && c.more ? `<div class="ai-more"><button type="button" class="btn sm" data-aimore>${AI.loadingMore ? 'Loading…' : 'Earlier messages'}</button></div>` : '';
  let day = '';
  let turn = null; // the JARVIS turn being drawn: { ch, steps, key }
  const closeTurn = () => {
    if (!turn) return;
    h += aiTraceHTML(turn.steps, false, turn.key) + '</div>';
    turn = null;
  };
  // each day is a section: its pill sticks only while the day is on screen, then the next day's takes over
  const dayPill = (t) => { const d = aiDay(t); if (d !== day) { closeTurn(); h += (day ? '</section>' : '') + `<section class="ai-dayg"><div class="ai-day"><span>${esc(d)}</span></div>`; day = d; } };
  const reply = []; // what the owner said after each item (buttons it answered show as chosen)
  for (let i = its.length - 1, next = ''; i >= 0; i--) { reply[i] = next; if (its[i].role === 'user') next = its[i].text || ''; }
  // Main's last turn failed (a refusal sticks to a conversation's history): offer the way out
  const lastErr = its.findLastIndex((x) => x.ch === mine && x.role === 'error');
  const stuck = !pane && lastErr >= 0 && !its.slice(lastErr + 1).some((x) => x.ch === mine && x.role === 'assistant') ? lastErr : -1;
  // Main: each thread's card under the message it grew from (thAnchor); one whose message isn't loaded, at the time it
  // was opened. The open thread drops down in a slot under its card (or under the message a new one starts from).
  const at = new Map(pane ? [] : AI.threads.map((t) => [t.id, thAnchor(t.parent, its)]));
  const loose = AI.threads.filter((t) => at.has(t.id) && !at.get(t.id)).sort((a, b) => a.created - b.created);
  const thr = (k) => AI.threads.filter((t) => at.get(t.id) === k);
  const slot = '<div class="th-slot" data-thslot></div>';
  const cards = (ts) => ts.map((t) => thCard(t) + (TH.id === t.id && !TH.solo ? slot : '')).join('');
  const under = (k) => { const h = cards(thr(k)) + (!pane && TH.id === 'new' && TH.parent === k ? slot : ''); return h ? `<div class="thcs">${h}</div>` : ''; };
  const looseHTML = (t) => `<div class="thcs loose">${cards([t])}</div>`;
  const who = pane ? 'Lead' : 'JARVIS';
  for (const [i, it] of its.entries()) {
    if (loose.length && loose[0].created <= it.t) { closeTurn(); while (loose.length && loose[0].created <= it.t) h += looseHTML(loose.shift()); }
    dayPill(it.t);
    if (it.role === 'user') {
      closeTurn();
      h += aiUserHTML(it, false, under(it.k), { tools: true, thread: pane || thr(it.k).length });
      continue;
    }
    if (it.role === 'event' && it.name === 'new_chat') { closeTurn(); h += '<div class="ai-new"><span>New conversation</span></div>'; continue; }
    if (it.role === 'event') { closeTurn(); h += aiEventHTML(it); continue; }
    if (turn && turn.ch !== it.ch) closeTurn(); // another channel's turn interleaved in time
    if (!turn) { turn = { ch: it.ch, steps: [], key: it.k }; h += `<div class="ai-a">${aiMeta(who, pane ? '' : it.ch, aiTime(it.t))}`; }
    if (it.role === 'think' || it.role === 'note') { turn.steps.push({ k: it.role, text: it.text, key: it.k }); continue; }
    if (it.role === 'tool') { turn.steps.push({ k: 'tool', name: it.name, text: it.text, res: it.res, state: it.run ? 'run' : 'ok', key: it.k }); continue; }
    h += aiTraceHTML(turn.steps, false, turn.key); // the work, then what it came to
    turn.steps = [];
    h += it.role === 'error' ? `<div class="ai-err">${ic('alert-triangle')}<div class="md">${md(it.text || '')}</div></div>${i === stuck ? aiStuckHTML() : ''}`
      : `<div class="md ai-ans">${richMd(it.text || '', reply[i] || (c.live && i === its.length - 1 ? c.live.user : ''))}</div>` +
        `<div class="ai-acts">${aiAct('aire', it.k, 'undo', 'Reply to this')}${pane || thr(it.k).length ? '' : aiAct('aireply', it.k, 'chat-round-dots', 'Start a thread from this')}${aiAct('aicopy', it.k, 'copy', 'Copy')}${pane ? '' : aiAct('aiquote', it.k, 'quote-up', 'Quote in your reply')}</div>` + under(it.k);
  }
  closeTurn();
  h += loose.map(looseHTML).join('');
  return h + (day ? '</section>' : '');
}

// the turn in progress: your message, the steps so far (the running one pulsing), the answer as it streams
// what you sent while JARVIS was answering: it goes out when the turn ends, in order (assistant.go's queue)
const aiWakeText = '[Your background work finished: its results are above. Answer what I asked for now.]'; // assistant.go aiWake
const aiQueueHTML = (c) => c.queue.map((q) => aiUserHTML({ text: q.text, ch: c === TH ? 'thread' : 'panel', t: q.t }, false,
  `<span class="q-l">${ic('clock-circle')}Sends when ${c === TH ? 'the lead' : 'JARVIS'} is done${c === TH ? '' : `<button type="button" class="q-x" data-aiunq="${esc(q.id)}" title="Take it back into your box">Edit</button>`}</span>`)
  .replace('class="msg-u ai-u', 'class="msg-u ai-u queued')).join('');

function aiNowHTML(c) {
  const l = c.live;
  if (!l) return aiQueueHTML(c);
  const pane = c === TH;
  const steps = (l.steps || []).map((s, i) => ({ ...s, key: 'live:' + (pane ? 't:' : '') + i }));
  const run = steps.findLast((s) => s.state === 'run');
  const status = run ? `Running ${aiTool(run.name)[0]}` : l.text ? '' : steps.length ? 'Working' : 'Thinking';
  const ch = pane ? 'thread' : 'panel';
  return (l.user === aiWakeText ? '' : aiUserHTML({ text: l.user, ch, t: l.since }, true)) +
    `<div class="ai-a ai-going">${aiMeta(pane ? 'Lead' : 'JARVIS', pane ? '' : 'panel', 'now')}${aiTraceHTML(steps, true, 'live')}` +
    (l.text ? `<div class="md ai-ans">${md(l.text)}</div>` : '') +
    (status ? `<div class="ai-status"><i class="ai-pulse"></i>${shim(status + '…')}</div>` : '') + '</div>' + aiQueueHTML(c);
}

// the control room's overview ends with the channels and connections (ai-control.js draws the rest)
function aiConnHTML() {
  const st = AI.state || {};
  const p = st.platforms || {};
  const g = AI.google;
  const row = (ok, title, detail, action = '') => `<div class="check ${ok === null ? '' : ok ? 'ok' : 'warn'}"><span class="ic">${ic(ok ? 'check-circle-f' : ok === null ? 'info-circle' : 'alert-triangle')}</span>` +
    `<div class="ai-row"><b>${title}</b><span class="muted">${detail}</span></div>${action}</div>`;
  const dash = (path, label) => `<a class="btn sm" href="/hermes${path}" target="_blank" rel="noopener">${label}${ic('arrow-up-right')}</a>`;
  return `<div class="sec-h"><h2>${ic('plug')}Channels</h2></div><div class="card flush">` +
    row(!!st.up, 'JARVIS (Hermes)', st.up ? 'Running' + (st.model ? ' · last model ' + esc(st.model) : '') : esc(st.err || 'Not running: start projects/vm-agent/run-agent.sh'), dash('/logs', 'Logs')) +
    row(p.api_server === 'connected', 'Panel chat', esc(p.api_server || 'not connected')) +
    aiTelegramHTML(p.telegram, row) +
    '</div>' +
    `<div class="sec-h"><h2>${ic('sparkle')}Models</h2><span class="muted">Pick per task with /model in any chat</span></div><div class="card flush">` +
    row(null, 'Providers and keys', 'Default: Claude Opus 5.5 (ANTHROPIC_API_KEY). Add other providers’ keys (OpenRouter, OpenAI) to switch per task with /model.', dash('/models', 'Models')) +
    '</div>' +
    `<div class="sec-h"><h2>${ic('global')}Google</h2><span class="muted">Gmail and Calendar</span></div><div class="card flush">` +
    row(g ? g.client : false, 'OAuth client', g?.client ? 'Saved' : 'Google Cloud → APIs: enable Gmail and Calendar → Credentials → OAuth client, type Desktop app → download the JSON. Publish the consent screen to production (testing sign-ins expire in 7 days).',
      `<label class="btn sm">${ic('upload2')}${g?.client ? 'Replace' : 'Upload'}<input type="file" accept="application/json,.json" data-aigoogle hidden></label>`) +
    row(g ? g.token : false, 'Signed in', g?.token ? 'JARVIS can read and send mail and manage your calendar' : 'After the upload, ask JARVIS to connect Google: it sends a sign-in link, you paste back the page it lands on.',
      g?.client && !g?.token ? `<button type="button" class="btn sm primary" data-aiask="Connect my Google account (Gmail and Calendar) with the google-workspace skill. Send me the sign-in link and tell me what to paste back.">Connect</button>` : '') +
    '</div>' +
    `<div class="sec-h"><h2>${ic('puzzle')}More connections</h2></div><div class="card flush">` +
    row(null, 'MCP servers', 'Any service with an MCP server: Notion, Linear, GitHub, Slack, databases… from the catalog or by URL/command.', dash('/mcp', 'MCP')) +
    row(null, 'Everything else', 'Hermes’ full dashboard: config, sessions, pairing, webhooks, API keys.', dash('/', 'Dashboard')) +
    '</div>';
}

// Telegram, set right here: the bot token (write-only: the panel only ever says whether one is saved) and your user
// id go to Hermes' .env; a restart of the gateway connects it. AI.tg: what /api/ai/telegram reports.
function aiTelegramHTML(state, row) {
  const tg = AI.tg || {};
  const ready = tg.token && tg.users;
  const detail = state === 'connected' ? `Connected${tg.users ? ' · only ' + esc(tg.users) + ' can talk to it' : ''}`
    : state ? esc(state) + (ready ? ' · restart if you just saved' : '')
    : ready ? 'Saved: restart JARVIS to connect' : 'Not set up yet: two things from Telegram, below';
  const restart = ready && state !== 'connected'
    ? `<button type="button" class="btn sm${AIC.arm === 'restart' ? ' danger' : ' primary'}" data-aicrestart>${ic('arrow-rotate')}${AIC.arm === 'restart' ? 'Tap again to restart' : 'Restart to connect'}</button>` : '';
  return row(state === 'connected' ? true : false, 'Telegram', detail, restart) +
    `<form class="aic-tg" data-aitgform autocomplete="off">` +
    `<label class="fld">Bot token <span class="faint">${tg.token ? '· saved' : '· from @BotFather: /newbot'}</span>` +
    `<input id="tg-token" type="password" autocomplete="off" spellcheck="false" placeholder="${tg.token ? 'Saved · type a new one to replace it' : '123456789:AA…'}"></label>` +
    `<label class="fld">Your Telegram user id <span class="faint">· from @userinfobot; only these ids can talk to JARVIS</span>` +
    `<input id="tg-users" inputmode="numeric" autocomplete="off" placeholder="e.g. 123456789" value="${esc(tg.users || '')}"></label>` +
    `<div class="aic-tg-a"><button type="submit" class="btn sm primary">${ic('check')}Save</button></div></form>`;
}

async function aiTelegramLoad() {
  const r = await api('/api/ai/telegram').catch(() => null);
  if (r?.status === 200) { AI.tg = r; if (AI.conn) aicBump(); }
}

document.addEventListener('submit', async (e) => {
  if (!e.target.matches('[data-aitgform]')) return;
  e.preventDefault();
  const token = $('#tg-token').value.trim();
  const users = $('#tg-users').value.trim();
  const body = {};
  if (token) body.token = token;
  if (users && users !== (AI.tg?.users || '')) body.users = users;
  if (!Object.keys(body).length) return toast('Nothing new to save');
  const r = await api('/api/ai/telegram', body).catch(() => ({ status: 0, error: "The panel can't be reached" }));
  if (r.status !== 200) return toast(r.error || `Error ${r.status}`, true);
  $('#tg-token').value = '';
  AI.tg = r;
  toast(r.token && r.users ? 'Saved. Restart JARVIS to connect Telegram' : 'Saved');
  aicBump();
});

// ---- actions ----
function aiSendState() {
  const b = $('#ai-send');
  if (!b) return;
  const ta = $('#ai-text'), has = !!ta.value.trim() || AI.files.length > 0;
  const stop = !!AI.live && !has; // while JARVIS answers: Stop, or, once you've typed, queue it
  b.classList.toggle('stop', stop);
  b.innerHTML = stop ? ic('stop-f') : NPB_SEND;
  b.setAttribute('aria-label', stop ? 'Stop' : AI.live ? 'Queue it: sends when JARVIS is done' : 'Send');
  b.title = b.getAttribute('aria-label');
  b.disabled = !stop && (!has || AI.files.some((f) => !f.path)); // photos still uploading
  const clr = $('#ai-clear');
  if (clr) clr.hidden = !has;
  ta.placeholder = AI.live ? 'Message JARVIS: sends when it’s done' : 'Message JARVIS';
}

async function aiSubmit() {
  const ta = $('#ai-text');
  const text = ta.value.trim();
  const paths = (doc) => AI.files.filter((f) => !!f.doc === doc).map((f) => f.path).filter(Boolean);
  const images = paths(false), files = paths(true);
  const slash = /^\/(new|reset|clear|model)\b\s*([\s\S]*)$/i.exec(text); // the panel's own: they never reach the model
  if (slash && !images.length && !files.length) {
    ta.value = '';
    try { sessionStorage.removeItem('aiDraft'); } catch { /* no storage */ }
    aiGrow(ta);
    aiSendState();
    const arg = slash[2].trim();
    if (slash[1].toLowerCase() !== 'model') return aiNewChat();
    return arg ? aiSetModel(arg === 'default' ? '' : arg) : aiGo('control:overview');
  }
  if (!text && !images.length && !files.length) { if (AI.live) await api('/api/ai/stop', {}).catch(() => {}); return; } // the empty box's button is Stop
  if (!(await aiSend(aiReplyWrap('ai', text), images, files))) return;
  aiReplyTo('', false);
  for (const f of AI.files) aiRmURL(f);
  AI.files = [];
  aiFilesRender();
  ta.value = '';
  try { sessionStorage.removeItem('aiDraft'); } catch { /* no storage */ }
  aiGrow(ta);
  aiSendState();
}

// aiSend(text): a message to JARVIS from the Panel (the composer, a reply button, a rule taught in Memory). While
// it answers, the panel queues it and sends it when that turn ends.
async function aiSend(text, images = [], files = []) {
  if (!String(text ?? '').trim() && !images.length && !files.length) { $('#ai-text')?.focus(); return false; } // nothing to send: the box, to type it
  const r = await api('/api/ai/send', { text, images, files }).catch(() => ({ status: 0, error: "The panel can't be reached" }));
  if (r.status !== 200) { toast(r.error || `Error ${r.status}`, true); return false; }
  aiConnToggle(false); // sent from settings, the threads or the files: show the chat it went to
  if (AI.tab !== 'msgs') aiTab('msgs');
  return true;
}

// a reply button's "open": a terminal's chat, a panel page, or a tab of the control room
function aiGo(target) {
  const [kind, rest] = [target.split(':')[0], target.slice(target.indexOf(':') + 1)];
  if (kind === 'thread') return aiOpenThread(rest);
  if (kind === 'control') { aiConnToggle(true); return aicTab(rest || 'overview'); }
  noteGo(target);
}

async function aiMore() {
  if (AI.loadingMore || !AI.items.length) return;
  AI.loadingMore = true;
  aiRender();
  const r = await api(`/api/ai/history?before=${encodeURIComponent(AI.items[0].k)}`).catch(() => ({ status: 0 }));
  AI.loadingMore = false;
  if (r.status === 200) {
    const h0 = document.documentElement.scrollHeight;
    AI.items = (r.items || []).concat(AI.items);
    AI.more = !!r.more;
    AI.v++;
    aiRender();
    scrollTo(0, scrollY + document.documentElement.scrollHeight - h0); // stay on the message you were reading
  } else aiRender();
}

async function aiGoogle() {
  const r = await api('/api/ai/google').catch(() => null);
  if (r?.status === 200) { AI.google = r; if (AI.conn) aiRender(); }
}

function aiClick(e) {
  const th = e.target.closest('[data-aith]');
  if (th) return th.dataset.aith && th.dataset.aith === TH.id && th.closest('#ai-hist') ? thClose() : aiOpenThread(th.dataset.aith, '', !!th.closest('#ai-all') && AI.tab === 'threads');
  if (e.target.closest('[data-thlist]')) return thClose(true); // the Threads tab: back to the list
  const rt = e.target.closest('[data-aireply]');
  if (rt) return aiReplyThread(rt.dataset.aireply);
  const tb = e.target.closest('[data-aitab]');
  if (tb) return aiTab(tb.dataset.aitab);
  if (e.target.closest('[data-thfiles]')) return thFiles();
  if (e.target.closest('[data-thplan]')) return thPlanToggle();
  const fb = e.target.closest('[data-thfeat]');
  if (fb) { TV.feature = fb.dataset.thfeat; store.set('tv.feature', TV.feature); setView('term'); return; }
  if (e.target.closest('[data-crewmin]')) { try { localStorage.setItem('thCrewMin', localStorage.getItem('thCrewMin') === '1' ? '' : '1'); } catch { /* no storage */ } return thRender(); }
  if (e.target.closest('[data-thdown]')) return scrollTo({ top: thEnd() - innerHeight, behavior: 'smooth' });
  const ao = e.target.closest('[data-artopen]');
  if (ao) return artOpen(ao.closest('.art'));
  const ar = e.target.closest('[data-aiarchive]');
  if (ar) return aiArchive(ar);
  if (AI.conn && aicClick(e)) return;
  const unq = e.target.closest('[data-aiunq]');
  if (unq) { // Edit: out of the queue, back into the box (after what's there), as ↑ does in a terminal
    unq.disabled = true;
    const q = AI.queue.find((x) => x.id === unq.dataset.aiunq);
    return api('/api/ai/unqueue', { id: unq.dataset.aiunq }).then((r) => {
      if (r.status !== 200) return toast('Already sent', true);
      const ta = $('#ai-text');
      if (q && ta) { ta.value = ta.value.trim() ? ta.value.replace(/\s+$/, '') + '\n\n' + q.text : q.text; aiGrow(ta); aiSendState(); ta.focus(); }
    });
  }
  if (e.target.closest('[data-aiclear]')) { // Clear: the box, its photos, the draft
    const ta = $('#ai-text');
    for (const f of AI.files) URL.revokeObjectURL(f.url);
    AI.files = [];
    aiFilesRender();
    ta.value = '';
    try { sessionStorage.removeItem('aiDraft'); } catch { /* no storage */ }
    aiGrow(ta);
    aiSendState();
    return ta.focus();
  }
  if (e.target.closest('[data-ainew]')) return aiNewChat();
  if (e.target.closest('#ai-down')) return scrollTo({ top: document.documentElement.scrollHeight, behavior: 'smooth' });
  const att = e.target.closest('[data-aiattach],[data-airmfile]');
  if (att) { const f = AI.files[+att.dataset.airmfile]; return att.dataset.airmfile ? f && aiRmFile(f) : $('#ai-file').click(); }
  const cp = e.target.closest('.cb-copy');
  if (cp) return copy(cp.closest('.cb').querySelector('code').textContent, cp);
  if (e.target.closest('[data-aiother]')) { const ta = $('#ai-text'); ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length); return; } // "Other": type your own answer
  const sc = e.target.closest('[data-scout]');
  if (sc) return thScout(sc);
  if (e.target.closest('[data-thmore]')) return thUnfold();
  const re = e.target.closest('[data-aire]');
  if (re) return aiReplyTo(re.dataset.aire, !!re.closest('#th-pane'));
  if (e.target.closest('[data-airex]')) return aiReplyTo('', !!e.target.closest('#th-pane'));
  const aq = e.target.closest('[data-aiq]');
  if (aq) return aiReplyJump(aq);
  const rq = e.target.closest('[data-aicopy],[data-aiquote]'); // a reply's Copy or Quote (app.js copy, quoteIntoComposer)
  if (rq) { const k = rq.dataset.aicopy ?? rq.dataset.aiquote, text = (AI.items.find((x) => x.k === k) || TH.items.find((x) => x.k === k))?.text || ''; return rq.dataset.aicopy !== undefined ? copy(text, rq) : quoteIntoComposer(text); }
  if (e.target.closest('details[data-empty] > summary')) return e.preventDefault(); // a call with nothing to show
  const t = e.target.closest('[data-aiconn],[data-aimore],[data-aiask],[data-aisend],[data-aiopen],[data-aisort]');
  if (!t) return;
  if (t.hasAttribute('data-aiconn')) aiConnToggle();
  else if (t.hasAttribute('data-aimore')) aiMore();
  else if (t.dataset.aiopen) aiGo(t.dataset.aiopen);
  else if (t.dataset.aisort) richSort(t.closest('th'));
  else (t.closest('#th-pane') && t.dataset.aisend ? thSend(t.dataset.aisend) : aiSend(t.dataset.aisend ?? t.dataset.aiask)).then((ok) => { if (ok && t.dataset.aisend) { t.classList.add('chosen'); for (const b of t.parentElement.querySelectorAll('[data-aisend]')) b.disabled = true; } });
}

document.addEventListener('change', (e) => { if (e.target.matches('[data-aicmodel]')) aiSetModel(e.target.value); });
document.addEventListener('change', async (e) => {
  if (!e.target.matches('[data-aigoogle]')) return;
  const f = e.target.files[0];
  e.target.value = '';
  if (!f) return;
  const r = await fetch('/api/ai/google', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: await f.text() })
    .then(async (x) => ({ status: x.status, ...(await x.json().catch(() => ({}))) })).catch(() => ({ status: 0 }));
  if (r.status !== 200) return toast(r.error || `Error ${r.status}`, true);
  toast('Google client saved');
  aiGoogle();
});

document.addEventListener('focusout', (e) => { if (AI.conn && e.target.closest?.('#ai-conns')) setTimeout(() => aiRender(), 0); }); // catch up on what typing held back

document.addEventListener('visibilitychange', () => {
  if (S.view !== 'ai') return;
  if (document.hidden) { aiStop(); thStop(); } else { if (!AI.es) aiConnect(); if (TH.id && !TH.es) thConnect(); }
});

// ---- artifacts: a page or picture an agent made (a design, a mockup, a report), shown in the chat by an ```artifact
// block ({"path", "title", "by", "note"}): a live preview in the message; Review opens it full screen with the comment
// layer (web/review.js, put in by /api/fs/raw?review=1): pins you drop on it, kept in "<page>.comments.json", and
// "Send to the agents" hands them to this chat (the thread's lead, or Main), who has the agent that made it act on them ----
const artURL = (p, review) => '/api/fs/raw?path=' + encodeURIComponent(p) + (review ? '&review=1' : '');
const artImg = (p) => /\.(png|jpe?g|gif|webp|avif)$/i.test(p);
RICH_BLOCKS.artifact = function artCard(a) {
  const p = String(a.path || '');
  if (!p.startsWith('/')) throw new Error('no path');
  const name = p.split('/').pop(), title = a.title || name;
  const view = artImg(p) ? `<img class="art-f" src="${esc(artURL(p))}" alt="" loading="lazy">`
    : `<iframe class="art-f" data-src="${esc(p)}" sandbox="allow-scripts" src="${esc(artURL(p))}" loading="lazy" tabindex="-1" title="${esc(title)}"></iframe>`;
  return `<div class="art" data-art="${esc(p)}" data-arttitle="${esc(title)}"><div class="art-h">${ic('pen-tool')}<div class="art-t"><b>${esc(title)}</b>` +
    `<span>${esc([a.by, name].filter(Boolean).join(' · '))}</span></div><button type="button" class="btn sm primary" data-artopen>${ic('chat-round-dots')}Review and comment</button></div>` +
    (a.note ? `<p class="art-n">${esc(a.note)}</p>` : '') + `<button type="button" class="art-v" data-artopen aria-label="Review ${esc(title)}">${view}</button></div>`;
};
// a chat redraws as a whole: its previews step aside and come back, still loaded (Element.moveBefore keeps a frame's
// state; without it they reload)
function artKeep(el, redraw) {
  const old = [...el.querySelectorAll('iframe.art-f')];
  let hold = null;
  if (old.length && el.moveBefore) { hold = document.createElement('div'); hold.hidden = true; document.body.append(hold); for (const f of old) hold.moveBefore(f, null); }
  redraw();
  if (!hold) return;
  for (const f of el.querySelectorAll('iframe.art-f')) { const o = old.find((x) => x.dataset.src === f.dataset.src && x.parentNode === hold); if (o) { f.parentNode.moveBefore(o, f); f.remove(); } }
  hold.remove();
}
// Review: the page opens beside the chat, its marks going to the chat it was shown in (artside.js)
function artOpen(card) {
  artSide(card.dataset.art, card.dataset.arttitle, card.closest('#th-pane') ? { kind: 'thread', id: TH.id } : { kind: 'main' });
}

