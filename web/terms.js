'use strict';
// ---------- the Terminals page ----------
// Every zellij session with its tabs and panes, each pane wearing an orb for its state (orbs.js); the agents that
// can't go on without you on top; views, search and a Filter menu over the sessions (in your order: drag a header); a click
// on a pane opens its chat or screen in the drawer. It runs inside app.js's live view: every
// update re-renders this HTML and morph patches the page in place, so everything here is a pure function of the
// data plus TV, the page's own state (kept in this browser).
// Loaded after app.js: it uses app.js's helpers (esc, ic, render, openPane, api…) and its own listeners run in the
// capture phase, so a click on this page's controls never reaches app.js's handlers.

const tvGet = (k, d) => { try { const v = store.get('tv.' + k, null); return v == null ? d : JSON.parse(v); } catch { return d; } };
const TV = {
  view: tvGet('view', 'all'), fStatus: new Set(tvGet('fStatus', [])), fSession: new Set(tvGet('fSession', [])), q: '',
  order: tvGet('order', []), closed: new Set(tvGet('closed', [])), tabSel: {},
  folding: null, drag: null, peekW: tvGet('peekWidth', 0), ren: null, feature: store.get('tv.feature', '') || '',
};
function tvSave() {
  const v = { view: TV.view, fStatus: [...TV.fStatus], fSession: [...TV.fSession], order: TV.order, closed: [...TV.closed] };
  for (const [k, x] of Object.entries(v)) store.set('tv.' + k, JSON.stringify(x));
}
const TKINDS = { agent: ['Agents', 'sparkle'], service: ['Services', 'server'], shell: ['Shells', 'terminal-square'] };
const TURG = ['blocked', 'input', 'error', 'slow', 'working', 'done', 'running', 'stopped', 'idle'];
const stLabel = (k) => ORB_STATES[k]?.label || k;
const STIP = {
  working: 'Working: thinking or running a tool', input: 'Input: needs a quick answer, a choice or a permission',
  blocked: "Blocked: can't go on until you do something", done: 'Done: finished what it was asked',
  running: 'Running', slow: 'Slow: its CPU has been busy', stopped: 'Stopped', error: 'Error: errors in its newest output', idle: 'Idle: a shell at its prompt',
};
const orb = (k, size = 20) => orbHTML(k, size, STIP[k]);
const procName = (cmd) => String(cmd || '').split(' ')[0].split('/').pop().replace(/^\.\//, '');
const sessOn = (n) => !TV.fSession.size || TV.fSession.has(n);
// renaming: a pencil by a session's or a tab's name turns it into a box (Enter or a click away saves, Esc doesn't)
const renaming = (sn, tab = null) => TV.ren?.session === sn && TV.ren.tab === tab;
const renBtn = (sn, tab, label) => `<button class="icon-btn sm ghost ren" type="button" data-tren="${esc(sn)}|${tab ?? ''}" aria-label="Rename ${esc(label)}" title="Rename">${ic('pen')}</button>`;
const renIn = (val, label) => `<input id="ren-in" class="ren-in" data-k="ren" value="${esc(val)}" maxlength="64" autocomplete="off" spellcheck="false" aria-label="New name for ${esc(label)}">`;

// every pane, with what the page shows about it
function termPanes(t) {
  const out = [];
  for (const s of t.sessions || []) for (const tb of s.tabs || []) for (const p of tb.panes) {
    const kind = termKind(p), one = tb.panes.length === 1;
    let name = one ? tb.name : p.agent?.title || p.title || 'pane ' + p.id;
    if (!one && kind === 'service' && p.proc) name = procName(p.proc);
    out.push({ key: s.name + ':' + p.id, s, tb, p, kind, one, name, st: termState(p) });
  }
  return out;
}
function termDetail(x) {
  const p = x.p, out = p.output ? 'output ' + ago(p.output) : '';
  if (x.kind === 'agent') {
    if (x.st === 'working') return [p.detail || 'Working', p.since ? fmtDur(now() - p.since) : ''].filter(Boolean).join(' · ');
    if (p.state === 'asking') return [p.detail, p.agent?.prompt?.q].filter(Boolean).join(': ');
    return [p.since ? `${stLabel(x.st)} ${ago(p.since)}` : '', askOf(p.agent?.last).text || p.agent?.title || p.detail].filter(Boolean).join(' · ');
  }
  if (x.kind === 'service') return [p.proc || p.detail, p.since && p.state === 'running' ? 'up ' + fmtDur(now() - p.since) : '', out].filter(Boolean).join(' · ');
  return ['Shell prompt', out].filter(Boolean).join(' · ');
}
function termMatch(x) {
  if (!sessOn(x.s.name) || (TV.view !== 'all' && x.kind !== TV.view) || (TV.fStatus.size && !TV.fStatus.has(x.st))) return false;
  const q = TV.q.trim().toLowerCase().replace(/^:/, '');
  return !q || [x.name, x.tb.name, x.s.name, x.p.git?.branch, x.p.proc, x.p.detail, x.p.agent?.title, stLabel(x.st), ...(x.p.ports || [])]
    .some((v) => String(v ?? '').toLowerCase().includes(q));
}
const urgOf = (x) => TURG.indexOf(x.st);
const worstOf = (xs) => xs.map((x) => x.st).sort((a, b) => TURG.indexOf(a) - TURG.indexOf(b))[0] || 'idle';
// what can move to another session (the server says the same in movable): a one-pane tab whose Claude isn't
// working or asking, or a shell at its prompt
function termMovable(tb) {
  if (tb.panes.length !== 1) return 'Only a tab with one pane can move for now.';
  const p = tb.panes[0];
  if (p.agent) return ['working', 'background', 'stalled', 'starting'].includes(p.state) ? "It's working: move it once it stops." : p.state === 'asking' ? "It's asking you something: answer it first." : '';
  if (p.kind === 'claude' || p.kind === 'codex') return "Its agent isn't tracked yet: try again in a moment.";
  return p.fg || p.state === 'running' ? `${procName(p.proc) || 'What runs in it'} would restart: stop it first.` : '';
}

// ---------- the view ----------
function viewTerm(t) {
  for (const s of t.sessions || []) s.tabs ||= []; // a session with no readable layout: no tabs, never a crash
  featLoad();
  if (TV.feature && (t.sessions || []).some((s) => s.name === TV.feature)) return featPage(t, TV.feature);
  const all = termPanes(t), vis = all.filter(termMatch);
  return termNeeds(all) + '<div>' + termTools(all, t) + termChips() + termSessions(t, all, vis) + '</div>';
}

// ---------- features: each session as a feature ----------
// /api/term/features: the JARVIS thread that runs a session (its status, note, plan) and each agent's own to-do list
// (what it's on and what's left, in its words). Fetched every 15 s while the page is open; drawn from what's known.
var FEAT = { at: 0, busy: false, s: {} };
async function featLoad() {
  if (FEAT.busy || Date.now() - FEAT.at < 15000) return;
  FEAT.busy = true;
  FEAT.at = Date.now();
  const r = await api('/api/term/features').catch(() => null);
  FEAT.busy = false;
  if (r?.status !== 200) return;
  const was = JSON.stringify(FEAT.s);
  FEAT.s = r.sessions || {};
  if (S.view === 'term' && JSON.stringify(FEAT.s) !== was) render();
}
// ---------- a feature's page: its report (a PM's ticket, written by Claude from the facts: featreport.go) ----------
// Click a session to open it. The facts are the panel's own (team, worktrees, PRs, lines, the thread's plan); the
// report (objective, requirements, plan and owners, risks, testing, what's left, merges, timeline) is rewritten on
// "Rewrite the report", or by itself when there's none yet.
var FP = { s: '', d: null, at: 0, busy: false, asked: {}, edit: '', draft: '' };
const FP_FILES = { feature: 'FEATURE.md', plan: 'PLAN.md', tests: 'TESTS.md' };
function fpTemplate(doc, name) {
  const main = FP.d?.trees?.find((w) => w.main)?.root;
  if (doc === 'feature') return `# ${name}\n\n## What must be true\n- \n\n## Objective\nMetric: \nBaseline: \nTarget: \nBy: \n\n## Requirements\n- \n\n## Definition of done\n- \n\n## Out of scope\n- \n\nMain worktree: ${main ? tilde(main) : ''}\n`;
  if (doc === 'plan') return `## M1 \n- [ ]  @${FP.d?.team?.[0]?.tab || 'Tab name'}\n\n## M2 \n- [ ] \n`;
  return '- [ ] T1 : how it is run\n';
}
async function fpSave(doc) {
  const r = await api('/api/term/feature/doc', { session: FP.s, doc, text: FP.draft, rev: FP.rev }).catch(() => ({ status: 0 }));
  if (r.status !== 200) return toast(r.error || 'Couldn\'t save it', true);
  FP.d.dossier = r.dossier;
  FP.edit = '';
  toast(`${FP_FILES[doc]} saved: the team reads it from now on`);
  render();
}
async function fpLoad(name, force) {
  if (FP.busy || (!force && FP.s === name && Date.now() - FP.at < (FP.d?.busy ? 4000 : 12000))) return;
  FP.busy = true;
  const r = await api('/api/term/feature?session=' + encodeURIComponent(name)).catch(() => null);
  FP.busy = false;
  FP.at = Date.now();
  if (r?.status !== 200) { // kept: the last good record (if any) stays, marked as not refreshed; never shown as empty
    const was = FP.err; FP.err = { status: r?.status || 0, error: r?.error || '', s: name };
    if (S.view === 'term' && TV.feature === name && JSON.stringify(was) !== JSON.stringify(FP.err)) render(); return; }
  FP.err = null;
  const { status, ...d } = r;
  const was = FP.s === name ? JSON.stringify(FP.d) : '';
  FP.s = name; FP.d = d;
  if (S.view === 'term' && TV.feature === name && JSON.stringify(d) !== was) render();
}
function featPage(t, name) { // the feature page (featpage.js), drawn from what the panel holds: opening it never runs a report or a model
  fpLoad(name);
  const d = FP.s === name ? FP.d : null;
  if (!d && FP.err?.s === name) return `<div class="fp fv" data-k="fv:${esc(name)}"><div class="fv-head"><button type="button" class="icon-btn ghost" data-tfeatx aria-label="Back to all sessions">${ic('angle-left')}</button><h2>${esc(name)}</h2></div><div class="fp-warn" role="alert">${ic('alert-triangle')}<span>Couldn't read this feature's record (${FP.err.status ? 'error ' + FP.err.status : 'no answer from the panel'}${FP.err.error ? ': ' + esc(FP.err.error) : ''}). Nothing is shown rather than an empty page; it is asked again shortly.</span></div></div>`;
  if (!d) return `<div class="fp fv" data-k="fv:${esc(name)}"><div class="fv-head"><button type="button" class="icon-btn ghost" data-tfeatx aria-label="Back to all sessions">${ic('angle-left')}</button><h2>${esc(name)}</h2></div><div class="skel"></div><div class="skel"></div></div>`;
  return featPageV8(t, name, d);
}

// FEATURE.draft.md (an agent's proposal) against FEATURE.md, line by line (LCS, O(n*m): fine at a page's size)
function fpDiff(a, b) {
  a = a.split('\n'); b = b.split('\n');
  const n = a.length, m = b.length, L = Array.from({ length: n + 1 }, () => new Int32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) L[i][j] = a[i] === b[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1]);
  const out = [];
  for (let i = 0, j = 0; i < n || j < m;) {
    if (i < n && j < m && a[i] === b[j]) { out.push([' ', a[i]]); i++; j++; }
    else if (i < n && (j === m || L[i + 1][j] >= L[i][j + 1])) out.push(['-', a[i++]]);
    else out.push(['+', b[j++]]);
  }
  return out;
}
function fpDraft(dz) {
  if (!(dz.draft || '').trim()) return '';
  const rows = fpDiff(dz.feature || '', dz.draft).map(([k, l]) => `<div class="${k === '+' ? 'add' : k === '-' ? 'del' : ''}">${k} ${esc(l)}</div>`).join('');
  return `<div class="fp-draft" data-k="fpdraft"><div class="fp-warn">${ic('alert-triangle')}<span>A draft of new requirements waits on you. Adopting it changes the approved requirements (FEATURE.md), and the outcomes need accepting again.</span></div>` +
    `<div class="fp-diff">${rows}</div><div class="fp-draft-b"><button type="button" class="btn sm" data-fppromote="1">Adopt draft</button><button type="button" class="btn sm ghost" data-fpdiscard="1">Discard draft</button></div></div>`;
}

// the desired outcomes take part in done: the owner accepts them as they stand (a changed set needs accepting again)
function fpAccept(dz) {
  const n = (dz.outcomes || []).length;
  if (!n) return '';
  return `<div class="fp-accept" data-k="fpacc">${dz.accepted
    ? `${ic('check-circle-f')}<span>You accepted these ${plural(n, 'outcome')} as met.</span>`
    : `${ic('alert-triangle')}<span class="faint">The feature can't be done until you accept its ${plural(n, 'desired outcome')} as they stand.</span><button type="button" class="btn sm" data-fpaccept="1" title="Accept every outcome listed above as met. Editing them later needs accepting again.">Accept outcomes</button>`}</div>`;
}

const FEAT_ST = { planning: 'Planning', working: 'Working', needs_you: 'Needs you', done: 'Done', stopped: 'Stopped' };
// who runs it: a JARVIS thread (its lead), or you
// a step and what backs it: its evidence and result (checked by the panel), why a tick isn't done yet, a hold,
// a missed checkpoint, and the owner's question with a box to answer that step only
function featOwner(s, th) {
  if (!th) return `<span class="fown you" title="You started ${esc(s.name)}: no JARVIS thread runs it">${ic('hand')}You</span>`;
  return `<button type="button" class="fown jv" data-tthread="${esc(th.id)}" title="Run by JARVIS in the thread “${esc(th.title)}”: open it">` +
    `<i class="nq-dot ${th.live ? 'busy' : th.status === 'needs_you' ? 'warn' : th.status === 'done' ? 'ok' : ''}"></i>JARVIS · ${esc(th.title)}</button>`;
}
// the thread's agreed plan as the feature's progress: steps done of all, the step being worked on now
function featPlan(th) {
  if (!th) return '';
  const plan = th.plan || [], done = plan.filter((x) => x.state === 'done').length, now = plan.find((x) => x.state === 'doing');
  const pct = plan.length ? Math.round((done / plan.length) * 100) : 0;
  return `<div class="fplan" data-k="fp:${esc(th.id)}">` +
    `<span class="fst ${esc(th.status)}">${esc(FEAT_ST[th.status] || th.status)}</span>` +
    (plan.length ? `<span class="fbar" title="${done} of ${plan.length} plan steps done"><i data-w="${pct}"></i></span><b class="fn">${done}/${plan.length}</b>` : '<span class="faint">No plan agreed yet</span>') +
    (now ? `<span class="fnow" title="${esc(now.text)}">${ic('arrow-right')}${esc(now.text)}</span>` : th.note ? `<span class="fnow" title="${esc(th.note)}">${esc(th.note)}</span>` : '') +
    (th.branch ? `<span class="brc">${IC.branch}${esc(th.branch)}</span>` : '') + '</div>';
}
// an agent's own to-do list: what it's on now and how far through it is; a click unfolds the list
function featTodos(x) {
  const set = FEAT.s[x.s.name]?.agents?.[x.key], ts = set?.todos || [];
  if (!ts.length) return '';
  const done = ts.filter((t) => t.status === 'completed').length, now = ts.find((t) => t.status === 'in_progress');
  const open = S.subsOpen.has('todo:' + x.key);
  return `<button type="button" class="tdo${open ? ' on' : ''}" data-ttodo="${esc(x.key)}" aria-expanded="${open}" title="Its own to-do list: ${done} of ${ts.length} done${set.t ? ' · updated ' + ago(set.t) : ''}">` +
    `<span class="fbar sm"><i data-w="${Math.round((done / ts.length) * 100)}"></i></span><b>${done}/${ts.length}</b>` +
    `<span class="tdo-t">${esc(now ? now.content : done === ts.length ? 'All done' : ts.find((t) => t.status !== 'completed')?.content || '')}</span>${caret}</button>`;
}
function featTodoList(x) {
  const ts = FEAT.s[x.s.name]?.agents?.[x.key]?.todos || [];
  if (!S.subsOpen.has('todo:' + x.key) || !ts.length) return '';
  return `<ol class="tdo-list" data-k="tl:${esc(x.key)}">` + ts.map((t) => `<li class="${esc(t.status)}">${ic(t.status === 'completed' ? 'check-circle-f' : t.status === 'in_progress' ? 'arrow-right' : 'check-circle')}<span>${esc(t.content)}</span></li>`).join('') + '</ol>';
}
// what can't go on without you: Blocked first, then Input, oldest first
function termNeeds(all) {
  const need = all.filter((x) => x.kind === 'agent' && (x.st === 'blocked' || x.st === 'input') && sessOn(x.s.name))
    .sort((a, b) => urgOf(a) - urgOf(b) || (a.p.since || 0) - (b.p.since || 0));
  if (!need.length) return '';
  return `<div class="tneeds">${sec('Waiting on you', `${need.length} · what each needs from you`, '', 'bell')}<div class="tstrip-w inbox-w"><div class="inbox">` + need.map((x) => {
    const ask = x.p.state === 'asking' ? askOf('', x.p.agent?.prompt?.q || x.p.detail) : askOf(x.p.agent?.last);
    return `<div class="ibx ${x.st}" role="button" tabindex="0" data-k="n:${esc(x.key)}" data-tpeek="${esc(x.key)}" title="Open ${esc(x.name)}">` +
      `<div class="ibx-h">${orb(x.st, 24)}<b>${esc(x.name)}</b><span class="w">${x.p.since ? fmtDur(now() - x.p.since).replace(/ \d+s$/, '') : ''}</span></div>` +
      `<p class="ask">${esc(ask.text || x.p.agent?.title || '')}</p>` +
      `<span class="where"><i class="dotk k-agent"></i>${esc(x.s.name)} · #${x.tb.pos + 1}${x.p.git?.branch ? ' · ' + esc(x.p.git.branch) : ''}</span>` +
      `<div class="ibx-a"><button class="btn sm" type="button" data-treply="${esc(x.key)}">${ic('chat-round-dots')}Reply</button></div></div>`;
  }).join('') + '</div></div></div>';
}
function termTools(all, t) {
  const inView = (k) => all.filter((x) => (k === 'all' || x.kind === k) && sessOn(x.s.name)).length;
  const n = (TV.fStatus.size ? 1 : 0) + (TV.fSession.size ? 1 : 0), ss = t.sessions || [];
  return '<div class="tools thead" data-k="tools">' +
    `<h2 class="th-t">${ic('window2')}Features<span class="muted">${plural(ss.length, 'session')} · ${plural(ss.reduce((k, s) => k + s.tabs.length, 0), 'tab')}</span></h2>` +
    '<div class="views" role="tablist" aria-label="Show">' +
    [['all', 'All'], ['agent', 'Agents'], ['service', 'Services'], ['shell', 'Shells']].map(([k, l]) => `<button type="button" role="tab" data-tview="${k}" aria-selected="${TV.view === k}">${l}<span class="n">${inView(k)}</span></button>`).join('') +
    `</div><div class="tbar">` +
    `<button type="button" class="tbtn" data-tpop="filter" aria-haspopup="dialog" aria-expanded="${TPOP.kind === 'filter'}">${ic('list-check')}<span class="lb">Filter</span>${n ? `<span class="cnt">${n}</span>` : ''}</button>` +
    `<button type="button" class="btn sm" data-new="any" title="A new session, or a tab in one">${ic('plus')}New</button></div></div>`;
}
function termChips() {
  const c = [];
  if (TV.fStatus.size) c.push(`<span class="tfchip">Status is <b>${[...TV.fStatus].map(stLabel).join(', ')}</b><button type="button" data-tunf="status" aria-label="Remove the status filter">${ic('xmark')}</button></span>`);
  if (TV.fSession.size) c.push(`<span class="tfchip">Session is <b>${[...TV.fSession].map(esc).join(', ')}</b><button type="button" data-tunf="session" aria-label="Remove the session filter">${ic('xmark')}</button></span>`);
  if (TV.q.trim()) c.push(`<span class="tfchip">Matches <b>“${esc(TV.q.trim())}”</b><button type="button" data-tunf="q" aria-label="Clear the search">${ic('xmark')}</button></span>`);
  return `<div class="tfchips">${c.length ? c.join('') + '<button type="button" class="linkbtn" data-tclear>Clear all</button>' : ''}</div>`;
}
// sessions in your order (drag a header to change it); new ones go last
function orderedSessions(t) {
  const ss = [...(t.sessions || [])];
  for (const s of ss) if (!TV.order.includes(s.name)) TV.order.push(s.name);
  return ss.sort((a, b) => TV.order.indexOf(a.name) - TV.order.indexOf(b.name));
}
function termSessions(t, all, vis) {
  const filtered = TV.fStatus.size || TV.q.trim() || TV.view !== 'all';
  const shown = orderedSessions(t).filter((s) => sessOn(s.name) && (!filtered || vis.some((x) => x.s === s)));
  if (!shown.length) return `<div class="card empty">Nothing matches the filters. <button type="button" class="linkbtn" data-tclear>Clear them</button></div>`;
  return '<div class="sessions" data-flip>' + shown.map((s) => termSession(s, all.filter((x) => x.s === s), vis.filter((x) => x.s === s))).join('') + '</div>';
}
function termSession(s, mine, seen) {
  const counts = TURG.map((k) => [k, mine.filter((x) => x.st === k).length]).filter(([, n]) => n);
  const closed = TV.closed.has(s.name), folding = TV.folding === s.name;
  const [pur, why] = termPurpose(mine.map((x) => x.p));
  const th = FEAT.s[s.name]?.thread;
  const tabs = [...s.tabs].sort((a, b) => a.pos - b.pos), selTab = tabs.find((tb) => tb.id === TV.tabSel[s.name]);
  const strip = `<div class="tstrip-w"><div class="tstrip" role="tablist" aria-label="Tabs in ${esc(s.name)}">` +
    `<button type="button" class="tchip" role="tab" data-ttab="${esc(s.name)}|" aria-selected="${!selTab}"><span class="tn">All tabs</span><span class="c">${seen.length}</span></button>` +
    tabs.map((tb) => { const w = worstOf(mine.filter((x) => x.tb === tb)), n = seen.filter((x) => x.tb === tb).length;
      return `<button type="button" class="tchip${n ? '' : ' dim'}" role="tab" data-k="t:${tb.id}" draggable="true" data-tdrag="tab" data-ttab="${esc(s.name)}|${tb.id}" aria-selected="${selTab === tb}" title="${esc(tb.name)} · ${stLabel(w)}">${orb(w, 14)}<span class="tp">#${tb.pos + 1}</span><span class="tn">${esc(tb.name)}</span>${tb.panes.length > 1 ? `<span class="c">${tb.panes.length}</span>` : ''}</button>`; }).join('') +
    `<button type="button" class="icon-btn sm ghost tadd" data-new="tab" data-session="${esc(s.name)}" aria-label="New tab in ${esc(s.name)}" title="New tab">${ic('plus')}</button></div></div>`;
  let body = '';
  for (const tb of tabs) {
    if (selTab && tb !== selTab) continue;
    const rows = seen.filter((x) => x.tb === tb);
    if (!rows.length) continue;
    if (tb.panes.length === 1) { body += termRow(rows[0]); continue; }
    body += `<div class="tgroup" data-k="g:${tb.id}"><div class="ttab"><span class="tab">#${tb.pos + 1}</span>${renaming(s.name, tb.id) ? renIn(tb.name, 'tab ' + tb.name) : `<b>${esc(tb.name)}</b>${renBtn(s.name, tb.id, 'tab ' + tb.name)}`}<span class="faint">${tb.panes.length} panes in one tab</span>${termMap(tb, s.name, mine)}<span class="spacer"></span>` +
      `<button class="icon-btn sm" type="button" data-new="pane" data-session="${esc(s.name)}" data-tab="${tb.id}" data-tabname="${esc(tb.name)}" aria-label="New pane in ${esc(tb.name)}" title="New pane">${ic('plus')}</button>` +
      `<button class="icon-btn sm" type="button" data-menu="tab" data-session="${esc(s.name)}" data-tab="${tb.id}" aria-label="More for tab ${esc(tb.name)}" aria-haspopup="menu">${ic('more-h')}</button></div>` +
      rows.map((x) => termRow(x)).join('') + '</div>';
  }
  const hidden = mine.length - seen.length;
  if (!body) body = `<div class="stub">${ic('eye')}<span>${selTab ? `Nothing in #${selTab.pos + 1} ${esc(selTab.name)} matches` : mine.length ? `${plural(hidden, 'pane')} hidden by the filters` : 'No panes.'}</span>${hidden ? '<span class="spacer"></span><button class="btn sm ghost" type="button" data-tclear>Show all</button>' : ''}</div>`;
  else if (hidden && !selTab) body += `<div class="stub">${ic('eye')}<span>${plural(hidden, 'more pane')} hidden by the filters</span><span class="spacer"></span><button class="btn sm ghost" type="button" data-tclear>Show all</button></div>`;
  return `<section class="card flush session tsess${closed ? ' closed' : ''}" data-k="s:${esc(s.name)}" data-session="${esc(s.name)}"><div class="sbar">${mine.map((x) => `<i class="o-${x.st}" title="${esc(x.name)}: ${stLabel(x.st)}"></i>`).join('')}</div>` +
    `<div class="sh" data-tfold="${esc(s.name)}" title="Open ${esc(s.name)} as a feature: its report · drag to reorder"><span class="grip" aria-hidden="true"></span>` +
    `<button class="icon-btn sm ghost" type="button" data-tfold="${esc(s.name)}" aria-expanded="${!closed}" aria-label="${closed ? 'Open' : 'Fold'} ${esc(s.name)}">${ic('angle-down', 'fold-ic')}</button>` +
    (renaming(s.name) ? renIn(s.name, 'session ' + s.name) : `<b class="name">${esc(s.name)}</b>${renBtn(s.name, null, 'session ' + s.name)}`) + `${pur ? `<span class="purp ${pur}" title="${esc(why)}">${pur}</span>` : ''}` + featOwner(s, th) +
    `<span class="sum">${counts.map(([k, n]) => `<span class="oc" title="${esc(STIP[k])}">${orb(k, 13)}<b>${n}</b></span>`).join('')}</span><span class="spacer"></span>` +
    (s.tabs.some((tb) => tb.panes.some((p) => p.agent || p.kind === 'claude' || p.kind === 'codex')) ? `<button class="btn sm" type="button" data-feature="${esc(s.name)}" title="Every agent in ${esc(s.name)} side by side">${ic('grid4')}Side by side</button>` : '') +
    `<button class="icon-btn sm" type="button" data-menu="session" data-session="${esc(s.name)}" aria-label="More for session ${esc(s.name)}" aria-haspopup="menu">${ic('more-h')}</button></div>` +
    `${s.err ? `<div class="empty err">${esc(s.err)}</div>` : ''}<div class="sfold"${closed && !folding ? ' hidden' : ''}>${featPlan(th)}${strip}<div class="sbody">${body}</div></div></section>`;
}
// a tab with several panes: a map of its split, each square opening its pane
function termMap(tb, sn, mine) {
  const g = tb.panes.filter((p) => p.geo);
  if (g.length < 2) return '';
  const W = Math.max(...g.map((p) => p.geo[0] + p.geo[2])), H = Math.max(...g.map((p) => p.geo[1] + p.geo[3]));
  return `<span class="mm" role="group" aria-label="How #${tb.pos + 1} is split">` + g.map((p) => {
    const x = mine.find((y) => y.p === p);
    return `<button type="button" class="mmp o-${x.st}" data-tpeek="${esc(x.key)}" data-geo="${p.geo.join(',')},${W},${H}" title="${esc(x.name)} · ${stLabel(x.st)}" aria-label="Open ${esc(x.name)}">${orb(x.st, 10)}</button>`;
  }).join('') + '</span>';
}
const ring = (a, x) => { const c = a?.ctx; if (!c?.used) return ''; const pct = Math.min(100, Math.round((c.used / c.window) * 100));
  const cfg = S.data.term?.compact, lim = a.compact?.off ? 'never' : (a.compact?.auto || cfg?.auto || 0) + '%';
  if (x && x.p.kind !== 'codex') return `<button type="button" class="ctx-m" data-cpop="${esc(x.s.name)}|${x.p.id}" title="Context ${fmtTok(c.used)} of ${fmtTok(c.window)} · compacts by itself at ${lim}${a.compact?.auto ? ' (its own limit)' : ''}: click to change">` +
    `<svg class="ring" viewBox="0 0 20 20" aria-hidden="true"><circle class="bg" cx="10" cy="10" r="7.5" pathLength="100"/><circle class="fg" cx="10" cy="10" r="7.5" pathLength="100" stroke-dasharray="${pct} 100"/></svg>${pct}%${a.compact?.auto || a.compact?.off ? `<em>${a.compact.off ? 'no auto' : '@' + a.compact.auto}</em>` : ''}</button>`;
  return `<span class="ctx-m" title="Context ${fmtTok(c.used)} of ${fmtTok(c.window)}"><svg class="ring" viewBox="0 0 20 20" aria-hidden="true"><circle class="bg" cx="10" cy="10" r="7.5" pathLength="100"/><circle class="fg" cx="10" cy="10" r="7.5" pathLength="100" stroke-dasharray="${pct} 100"/></svg>${pct}%</span>`; };
function termRow(x) {
  const p = x.p, a = p.agent, subs = a?.subagents || [], open = S.subsOpen.has(x.key) && subs.length > 0, d = termDetail(x);
  const kindTxt = x.kind === 'agent' ? p.kind : x.kind === 'service' ? procName(p.proc) || p.kind : 'shell';
  const prs = (a?.prs || []).slice(0, 2).map((n) => `<span class="prn">#${n}</span>`).join('') + ((a?.prs || []).length > 2 ? `<span class="prn">+${a.prs.length - 2}</span>` : '');
  const alerts = x.kind === 'service' ? (p.errs ? `<span class="alert bad">${ic('alert-triangle')}${plural(p.errs, 'error')}</span>` : '') + (p.warns ? `<span class="alert warn">${ic('alert-triangle')}${plural(p.warns, 'warning')}</span>` : '') : '';
  const ren = x.one && renaming(x.s.name, x.tb.id);
  let h = `<div class="trow s-${x.st}${TV.peek === x.key ? ' sel' : ''}" role="button" tabindex="0" data-k="p:${esc(x.key)}" data-open="${esc(x.key)}" draggable="${!ren}" data-tdrag="pane">` +
    `${orb(x.st, 22)}<div class="t"><span class="nm">${x.one ? `<span class="tab">#${x.tb.pos + 1}</span>` : ''}${ren ? renIn(x.tb.name, 'tab ' + x.tb.name) : esc(x.name)}</span>${x.one && !ren ? renBtn(x.s.name, x.tb.id, 'tab ' + x.tb.name) : ''}<span class="tx">` +
    `<span class="kind ${x.kind}">${ic(TKINDS[x.kind][1])}${esc(kindTxt)}</span>${p.git?.branch ? `<span class="brc">${IC.branch}${esc(p.git.branch)}</span>` : ''}` +
    (a && (a.add || a.del) ? `<span class="nq-ln" title="Lines it wrote and removed with its edit tools (and its subagents'), live"><b>+${a.add || 0}</b><s>−${a.del || 0}</s></span>` : '') +
    `${(p.ports || []).map((n) => `<span class="port">:${n}</span>`).join('')}${prs}</span></div>` +
    `<div class="act"><span class="chips">${alerts}${x.kind === 'agent' ? compactBtn(a, x.s.name, p.id, 'sm') : ''}${ring(a, x)}<span class="use">${p.n ? `${fmtPct(p.cpu)} · ${fmtB(p.rss)}` : ''}</span>` +
    (subs.length ? `<button class="chip subs" data-subs="${esc(x.key)}" aria-expanded="${open}">${ic('people')}${plural(subs.length, 'subagent')}${caret}</button>` : '') +
    `</span><span class="hov">${x.kind === 'agent' ? `<button class="icon-btn sm" type="button" data-tchat="${esc(x.key)}" aria-label="Open the chat of ${esc(x.name)}" title="Chat">${ic('chat-round-dots')}</button>` : ''}` +
    `<button class="icon-btn sm" type="button" data-tscreen="${esc(x.key)}" aria-label="Open the terminal of ${esc(x.name)}" title="Terminal">${ic('terminal-square')}</button>` +
    `<button class="icon-btn sm" type="button" data-menu="tpane" data-session="${esc(x.s.name)}" data-pane="${p.id}" aria-label="More for ${esc(x.name)}" aria-haspopup="menu">${DOTS}</button></span></div>` +
    `<div class="d">${x.st === 'working' && d ? shim(d) : esc(d) || '&nbsp;'}</div>${x.kind === 'agent' ? featTodos(x) : ''}</div>` + featTodoList(x);
  if (open) {
    h += '<div class="subs-list">' + subs.map((sa) => `<div class="sa" role="button" tabindex="0" data-opensub="${esc(x.key + '|' + sa.id)}" title="See what it is doing"${dropAttr('s:' + x.key)}><div class="t"><i class="spin"></i>${esc(sa.desc || sa.id)}</div>` +
      `<div class="d">${esc([sa.type, sa.model, sa.bg ? 'background' : '', sa.doing || 'Thinking', fmtDur(now() - sa.started)].filter(Boolean).join(' · '))}</div></div>`).join('') + '</div>';
  }
  return h;
}
// after each render (app.js calls it): sizes through the CSSOM, the scrolling strips' edge fades, the peek kept current
function termAfter(el) {
  for (const i of $$('[data-flex]', el)) i.style.flex = i.dataset.flex;
  for (const b of $$('.mmp[data-geo]', el)) {
    const [x, y, w, h, W, H] = b.dataset.geo.split(',').map(Number);
    Object.assign(b.style, { left: (x / W) * 100 + '%', top: (y / H) * 100 + '%', width: `calc(${(w / W) * 100}% - 3px)`, height: `calc(${(h / H) * 100}% - 3px)` });
  }
  for (const w of $$('.tstrip-w', el)) fades(w);
  if (TV.peek) renderPeek();
  if (TV.feature) featPageV8After(el);
}
function fades(w) { const t = w.firstElementChild; w.classList.toggle('fade-l', t.scrollLeft > 2); w.classList.toggle('fade-r', t.scrollLeft + t.clientWidth < t.scrollWidth - 2); }

// ---------- the peek: a sheet over the page with a pane at a glance, its latest turn drawn as its chat draws it ----------
// An agent: what it needs from you, its numbers, where it works, its latest turn by the chat's own renderer (your
// message, its work folded under one line, its reply, the files it made), every file its session touched, and a reply
// box that takes photos. A server or a shell: the last lines on its screen. A second click on the row (or Open the
// chat) opens the full drawer; the left edge drags to any width; Esc closes it. Full screen on a phone.
const peekEl = $('#tpeek');
const PK = { key: null, items: null, turn: [], screen: null, files: null, want: '', at: 0, filesAt: 0, busy: false, photos: [] };
if (TV.peekW) peekEl.style.setProperty('--pk-w', TV.peekW + 'px');
function closePeek() {
  if (!TV.peek) return;
  TV.peek = PK.key = null;
  dropPhotos();
  peekEl.classList.remove('open');
  setTimeout(() => { if (!TV.peek) { peekEl.hidden = true; peekEl.innerHTML = ''; } }, 260);
  for (const r of $$('#view .trow.sel')) r.classList.remove('sel');
}
const peekQS = (x) => `session=${encodeURIComponent(x.s.name)}&pane=${x.p.id}`;
// what it reads beside the live data: an agent's latest chat items (again when its state or reply changes, every
// 3 s while it works) and the files its session touched (every 10 s); anything else's screen (every 5 s)
async function loadPeek(x) {
  const chat = !!x.p.agent?.chat, t = Date.now(), want = `${x.key}|${x.p.agent?.lastAt || ''}|${x.p.state}`;
  if (!PK.busy && (PK.want !== want || t - PK.at > (chat ? (x.st === 'working' ? 3000 : 30000) : 5000))) {
    Object.assign(PK, { want, at: t, busy: true });
    try {
      const r = await api(chat ? `/api/pane/chat?${peekQS(x)}&tail=${x.st === 'working' ? 'live' : 'turn'}` : `/api/pane/peek?${peekQS(x)}`);
      if (PK.key === x.key && r.status === 200) { if (chat) PK.items = r.items || []; else PK.screen = r.screen || []; renderPeek(); }
    } catch { /* the next update asks again */ } finally { PK.busy = false; }
  }
  if (chat && t - PK.filesAt > 10000) {
    PK.filesAt = t;
    api(`/api/pane/files?${peekQS(x)}`).then((r) => { if (PK.key === x.key) { PK.files = r.status === 200 ? r.files || [] : []; renderPeek(); } }).catch(() => {});
  }
}
// the latest turn, by the chat's renderer: from your last message on. A long turn in progress shows its last steps
// (the rest are a click away, in the chat).
function peekTurn(x) {
  const items = PK.items;
  if (!items) return `<div class="muted">${shim('Reading the chat…')}</div>`;
  if (!items.length) return '<div class="muted">No messages yet.</div>';
  const from = Math.max(0, items.findLastIndex((it) => it.k === 'user'));
  let turn = items.slice(from), you = '', skipped = 0;
  const cut = turn[0].k !== 'user'; // the turn began before what was read: there are more steps than these
  if (x.st === 'working' && turn.length > 14) {
    if (turn[0].k === 'user') { you = userMsg(turn[0], ''); turn = turn.slice(1); }
    const last = turn.slice(-10), tools = new Set(last.filter((it) => it.k === 'tool').map((it) => it.id));
    const keep = last.filter((it) => it.k !== 'result' || tools.has(it.id)); // a result whose call scrolled off would stand alone
    skipped = turn.length - keep.length;
    turn = keep;
  }
  PK.turn = turn;
  const h = chatHTML(x.p, { key: x.key, items: turn, before: 0, seen: 0, qs: peekQS(x) }).split(CHUNK).join('');
  return (from > 0 ? `<button type="button" class="pk-earlier" data-tchat="${esc(x.key)}">${ic('history')}Earlier in this chat</button>` : '') +
    `<div class="chat pk-chat">${you}${skipped ? `<button type="button" class="pk-skip" data-tchat="${esc(x.key)}">${ic('more-h')}${cut ? 'Earlier steps' : plural(skipped, 'earlier step')}</button>` : ''}${h}</div>`;
}
// every file the session touched, newest first: a click opens it in the chat's preview
function peekFiles(x) {
  const fs = PK.files;
  if (!fs?.length) return '';
  const root = x.p.git?.root || '';
  const where = (p) => { const d = p.replace(/[^/]*$/, ''); return root && d.startsWith(root + '/') ? d.slice(root.length + 1) || './' : tilde(d); };
  const row = (f) => `<button type="button" class="pk-f${f.exists ? '' : ' gone'}" data-openfile="${esc(f.path)}" title="${esc(tilde(f.path))}"><span class="pk-fi">${fxBadge(f.path)}</span>` +
    `<span class="pk-fn"><b>${esc(f.path.split('/').pop())}</b><span>${esc(where(f.path))}</span></span><span class="pk-fm">${f.created ? '<em>new</em>' : ''}` +
    `${f.add ? `<span class="add">+${f.add}</span>` : ''}${f.del ? `<span class="del">−${f.del}</span>` : ''}<span class="faint">${f.last ? fmtDur(now() - f.last).replace(/ \d+s$/, '') : ''}</span></span></button>`;
  return `<section class="pk-sec"><h3>Files<span class="n">${fs.length}</span></h3><div class="pk-files">${fs.slice(0, 8).map(row).join('')}</div>` +
    (fs.length > 8 ? `<button type="button" class="linkbtn" data-tfiles>All ${fs.length} files</button>` : '') + '</section>';
}
function renderPeek() {
  const t = S.data.term, x = t && termPanes(t).find((y) => y.key === TV.peek);
  if (!x) return closePeek();
  loadPeek(x);
  const p = x.p, a = p.agent, chat = !!a?.chat;
  const facts = [];
  if (p.n) facts.push(['CPU', fmtPct(p.cpu)], ['Memory', fmtB(p.rss)]);
  if (a?.ctx?.used) facts.push(['Context', `${ctxPct(a.ctx)}%`, `${fmtTok(a.ctx.used)} of ${fmtTok(a.ctx.window)} tokens · compacting limits`, `${x.s.name}|${p.id}`]);
  if (a?.cost) facts.push(['Cost so far', '$' + a.cost.toLocaleString(undefined, { maximumFractionDigits: a.cost >= 100 ? 0 : 2 })]);
  if (x.kind === 'service' && p.since) facts.push(['Up', fmtDur(now() - p.since)]);
  if (p.output) facts.push(['Last output', ago(p.output)]);
  const ask = x.kind === 'agent' && (x.st === 'blocked' || x.st === 'input') ? (p.state === 'asking' ? askOf('', a?.prompt?.q || p.detail) : askOf(a?.last)) : null;
  const chip = (icon, text, cls = '') => `<span class="pk-chip${cls}">${icon}<span>${esc(text)}</span></span>`;
  let body = (ask ? `<div class="pk-ask o-${x.st}">${orb(x.st, 20)}<span>${esc(ask.text)}</span></div>` : '') +
    `<div class="facts">${facts.map(([k, v, tip, cpop]) => cpop
      ? `<button type="button" class="fact" data-cpop="${esc(cpop)}" title="${esc(tip)}" aria-haspopup="dialog"><span>${k}</span><b>${esc(v)}</b></button>`
      : `<div class="fact"${tip ? ` title="${esc(tip)}"` : ''}><span>${k}</span><b>${esc(v)}</b></div>`).join('')}</div>` +
    `<div class="pk-where">${chip(ic('window2'), `${x.s.name} › #${x.tb.pos + 1} ${x.tb.name}${x.one ? '' : ' › ' + x.name}`)}` +
    (a?.cwd ? chip(ic('folder'), a.cwd) : '') + (p.git?.branch ? chip(IC.branch, p.git.branch + (p.git.worktree ? ' (worktree)' : '')) : '') +
    (a?.prs || []).slice(0, 4).map((n) => `<span class="prn">#${n}</span>`).join('') + '</div>';
  if (chat) {
    body += `<section class="pk-sec"><h3>Latest</h3>${peekTurn(x)}</section>` + peekFiles(x) +
      `<form class="pk-comp" id="tp-form">${PK.photos.length ? `<div class="pk-ph">${PK.photos.map((f, i) => `<span class="pk-phi"><img src="${f.url}" alt="Photo to send"><button type="button" data-tunphoto="${i}" aria-label="Remove this photo">${ic('xmark')}</button></span>`).join('')}</div>` : ''}` +
      `<div class="pk-row"><button type="button" class="icon-btn sm ghost" data-tattach aria-label="Attach photos" title="Attach photos">${ic('paperclip')}</button>` +
      `<label class="sr" for="tp-text">Reply to ${esc(x.name)}</label><textarea id="tp-text" rows="1" placeholder="${x.st === 'working' ? 'Queue a message for when it finishes' : 'Reply to ' + esc(x.name)}"></textarea>` +
      `<button class="cbtn send" type="submit" aria-label="Send">${ic('arrow-up')}</button></div><input id="tp-file" type="file" accept="image/*" multiple hidden></form>` +
      `<div class="pk-acts"><button class="btn" type="button" data-tchat="${esc(x.key)}">${ic('chat-round-dots')}Open the chat</button><button class="btn" type="button" data-tscreen="${esc(x.key)}">${ic('terminal-square')}Terminal</button>${compactBtn(a, x.s.name, p.id, 'pk')}</div>`;
  } else {
    const ls = PK.screen || [];
    const cls = (l) => (/\bERR\b|\bERROR\b|\bFATAL\b|\bpanic:/.test(l) ? 'bad' : /\bWARN(ING)?\b/.test(l) ? 'warn' : '');
    body += `<section class="pk-sec"><h3>On screen</h3><pre class="logs">${!PK.screen ? 'Reading…' : ls.length ? ls.map((l) => `<span class="${cls(l)}">${esc(l)}</span>`).join('\n') : 'Nothing on screen.'}</pre></section>` +
      `<div class="pk-acts">${p.fg ? `<button class="btn" type="button" data-tstop="${esc(x.key)}">${ic('stop-f')}Stop ${esc(procName(p.proc))}…</button>` : ''}<button class="btn" type="button" data-tscreen="${esc(x.key)}">${ic('terminal-square')}Terminal</button></div>`;
  }
  const head = `<div class="pk-h"><span class="pk-ic k-${x.kind}">${ic(TKINDS[x.kind][1])}</span><div class="tt"><h2>${esc(x.name)}</h2><div class="crumb">${esc(x.s.name)} · #${x.tb.pos + 1} · ${stLabel(x.st)}</div></div>${orb(x.st, 28)}` +
    `<button class="icon-btn sm ghost" type="button" data-${chat ? 'tchat' : 'tscreen'}="${esc(x.key)}" aria-label="Open it full" title="Open ${chat ? 'the chat' : 'the terminal'}">${ic('maximize')}</button>` +
    `<button class="icon-btn sm ghost" type="button" data-menu="tpane" data-session="${esc(x.s.name)}" data-pane="${p.id}" aria-label="More" aria-haspopup="menu">${DOTS}</button>` +
    `<button class="icon-btn sm ghost" type="button" data-tclose aria-label="Close" title="Close (Esc)">${IC.x}</button></div>`;
  morph(peekEl, '<div class="pk-grip" title="Drag to resize · double-click for the default width"></div>' + head + `<div class="pk-body">${body}</div>`);
}
// photos for the reply: picked, pasted into the box; sent with it (the chat's own upload path)
function addPhotos(list) {
  for (const f of list) if (/^image\//.test(f.type) && PK.photos.length < 10) { f.url = URL.createObjectURL(f); PK.photos.push(f); }
  renderPeek();
}
function dropPhotos() { for (const f of PK.photos) URL.revokeObjectURL(f.url); PK.photos = []; }
peekEl.addEventListener('change', (e) => { if (e.target.id === 'tp-file') { addPhotos(e.target.files); e.target.value = ''; } });
peekEl.addEventListener('paste', (e) => { const fs = [...(e.clipboardData?.files || [])].filter((f) => /^image\//.test(f.type)); if (e.target.id === 'tp-text' && fs.length) { e.preventDefault(); addPhotos(fs); } });
peekEl.addEventListener('submit', (e) => {
  e.preventDefault();
  const ta = $('#tp-text', peekEl), text = ta?.value.trim(), x = TV.peek && termPanes(S.data.term).find((y) => y.key === TV.peek);
  if ((!text && !PK.photos.length) || !x) return;
  queueSend(x.key, x.s.name, x.p.id, text || '', PK.photos); // the photos stay with the message (its chat shows them)
  PK.photos = [];
  ta.value = '';
  toast(`Sent to ${x.name}`);
  PK.at = 0; // read the chat again: your message shows in the turn
  renderPeek();
});
peekEl.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.target.id === 'tp-text' && !coarse) { e.preventDefault(); peekEl.querySelector('#tp-form').requestSubmit(); } });
// a fold opened in the peek stays open across its refreshes (the chat keeps them in D.openTools too)
peekEl.addEventListener('toggle', (e) => { const id = e.target.dataset?.id; if (id) e.target.open ? D.openTools.add(id) : D.openTools.delete(id); }, true);
// its left edge drags to any width (kept in this browser); a double-click goes back to the default
peekEl.addEventListener('pointerdown', (e) => {
  const g = e.target.closest('.pk-grip');
  if (!g) return;
  e.preventDefault();
  g.setPointerCapture(e.pointerId);
  peekEl.classList.add('sizing');
  const move = (ev) => { TV.peekW = Math.round(Math.min(innerWidth - 240, Math.max(380, innerWidth - ev.clientX))); peekEl.style.setProperty('--pk-w', TV.peekW + 'px'); };
  g.addEventListener('pointermove', move);
  g.addEventListener('lostpointercapture', () => { g.removeEventListener('pointermove', move); peekEl.classList.remove('sizing'); store.set('tv.peekWidth', JSON.stringify(TV.peekW)); }, { once: true });
});
peekEl.addEventListener('dblclick', (e) => { if (!e.target.closest('.pk-grip')) return; TV.peekW = 0; peekEl.style.removeProperty('--pk-w'); store.set('tv.peekWidth', '0'); });
// the full drawer, in chat or terminal
function openMode(key, mode) {
  closePeek();
  openPane(key);
  if (D.key === key && D.mode !== mode) { D.mode = mode; D.view = D.raw = null; renderDrawer(); connectPane(); }
}

// ---------- renaming a session or a tab ----------
function renStart(session, tab) {
  if (TV.ren) renDone(true);
  TV.ren = { session, tab };
  render();
  const i = $('#ren-in');
  if (i) { i.focus(); i.select(); }
}
// the new name shows at once; zellij's own comes with the next update (or the old one comes back, with why)
async function renDone(save) {
  const r = TV.ren, name = ($('#ren-in')?.value || '').trim();
  if (!r) return;
  TV.ren = null;
  const s = (S.data.term?.sessions || []).find((x) => x.name === r.session), tb = r.tab === null ? null : s?.tabs.find((t) => t.id === r.tab);
  const old = tb ? tb.name : r.session;
  if (!save || !name || name === old || !s || (r.tab !== null && !tb)) return render();
  const put = (from, to) => { if (tb) tb.name = to; else renameLocally(from, to); render(); };
  put(old, name);
  const res = await api('/api/term/rename', { session: r.session, ...(tb ? { tab: r.tab } : {}), name }).catch(() => ({ status: 0, error: 'The panel is offline' }));
  if (res.status !== 200) { toast(res.error || 'Rename failed', true); put(name, old); }
}
// a session's new name: its place in your order, folded, filters and selected tab go with it
function renameLocally(from, to) {
  const s = (S.data.term?.sessions || []).find((x) => x.name === from);
  if (s) s.name = to;
  TV.order = TV.order.map((n) => (n === from ? to : n));
  for (const set of [TV.closed, TV.fSession]) if (set.delete(from)) set.add(to);
  if (from in TV.tabSel) { TV.tabSel[to] = TV.tabSel[from]; delete TV.tabSel[from]; }
  if (TV.peek?.startsWith(from + ':')) closePeek();
  tvSave();
}
$('#view').addEventListener('keydown', (e) => { if (e.target.id === 'ren-in' && e.key === 'Enter') { e.preventDefault(); renDone(true); } }, true);
$('#view').addEventListener('focusout', (e) => { if (e.target.id === 'ren-in' && TV.ren) renDone(true); });

// ---------- Filter: a menu under the toolbar ----------
const TPOP = { kind: '', el: $('#tpop') };
function openPop(btn, kind) {
  TPOP.kind = kind;
  drawPop();
  const m = TPOP.el;
  m.hidden = false;
  const r = btn.getBoundingClientRect(), w = m.offsetWidth, h = m.offsetHeight;
  m.style.left = Math.max(8, Math.min(r.right - w, innerWidth - w - 8)) + 'px';
  m.style.top = (r.bottom + 6 + h > innerHeight - 8 ? Math.max(8, r.top - h - 6) : r.bottom + 6) + 'px';
  for (const b of $$('[data-tpop]')) b.setAttribute('aria-expanded', String(b.dataset.tpop === kind));
  $('#tpop-q', m)?.focus({ preventScroll: true });
}
function closePop() { if (!TPOP.kind) return; TPOP.kind = ''; TPOP.el.hidden = true; for (const b of $$('[data-tpop]')) b.setAttribute('aria-expanded', 'false'); }
function drawPop() {
  const m = TPOP.el, all = termPanes(S.data.term || {});
  const q = ($('#tpop-q', m)?.value || '').toLowerCase();
  const opt = (fx, on, label, n, lead) => (!q || label.toLowerCase().includes(q)
    ? `<button type="button" class="opt" role="menuitemcheckbox" aria-checked="${on}" data-fx="${fx}"><span class="ck"></span>${lead}${esc(label)}<span class="n">${n}</span></button>` : '');
  const st = TURG.filter((k) => all.some((x) => x.st === k) || TV.fStatus.has(k)).map((k) => opt('st:' + k, TV.fStatus.has(k), stLabel(k), all.filter((x) => x.st === k).length, orb(k, 16))).join('');
  const ss = (S.data.term?.sessions || []).map((s) => opt('ss:' + s.name, TV.fSession.has(s.name), s.name, all.filter((x) => x.s === s).length, ic('window2'))).join('');
  const bodyHTML = `<div class="mh">Status</div>${st || '<div class="none">No status matches.</div>'}<div class="mh">Session</div>${ss || '<div class="none">No session matches.</div>'}` +
    (TV.fStatus.size || TV.fSession.size ? '<hr><button type="button" class="opt" data-fx="clear">Clear filters</button>' : '');
  if ($('#tpop-q', m)) $('#tpop-body', m).innerHTML = bodyHTML;
  else m.innerHTML = `<label class="tpq">${ic('search')}<span class="sr">Filter options</span><input id="tpop-q" placeholder="Filter by status or session…" autocomplete="off"></label><div id="tpop-body">${bodyHTML}</div>`;
}
TPOP.el.addEventListener('input', (e) => { if (e.target.id === 'tpop-q') drawPop(); });
TPOP.el.addEventListener('click', (e) => {
  const b = e.target.closest('[data-fx]');
  if (!b) return;
  const [k, v] = b.dataset.fx.split(/:(.*)/s);
  if (k === 'st') TV.fStatus.has(v) ? TV.fStatus.delete(v) : TV.fStatus.add(v);
  else if (k === 'ss') TV.fSession.has(v) ? TV.fSession.delete(v) : TV.fSession.add(v);
  else if (k === 'clear') { TV.fStatus.clear(); TV.fSession.clear(); }
  tvSave(); render(); drawPop();
});
addEventListener('pointerdown', (e) => { if (TPOP.kind && !e.target.closest('#tpop, [data-tpop]')) closePop(); }, true);

// ---------- a row's ⋯: move its tab to another session, stop what runs, close it ----------
function termMenu(btn, session, pane) {
  const f = findPane(session, pane);
  if (!f) return;
  const why = termMovable(f.tb), others = (S.data.term?.sessions || []).filter((s) => s.name !== session);
  const item = (icon, label, data, cls = '', tip = '') => `<button type="button" role="menuitem" class="mi ${cls}"${tip ? ` title="${esc(tip)}"` : ''}${cls.includes('off') ? ' aria-disabled="true"' : ''} ${Object.entries(data).map(([k, v]) => `data-${k}="${esc(v)}"`).join(' ')}>${ic(icon)}<span>${esc(label)}</span></button>`;
  const items = [];
  if (others.length) {
    items.push(`<div class="mh">${f.tb.panes.length > 1 ? `Move tab #${f.tb.pos + 1} to` : 'Move this tab to'}</div>`);
    for (const o of others) items.push(item('arrow-right', o.name, why ? {} : { mi: 'tmove', session, tab: f.tb.id, to: o.name }, why ? 'off' : '', why));
    if (why) items.push(`<div class="mnote">${esc(why)}</div>`);
  }
  if (f.p.agent?.ctx) {
    items.push(`<div class="mh">Context ${ctxPct(f.p.agent.ctx)}% full</div>`);
    if (!['pausing', 'compacting'].includes(f.p.agent.compact?.phase)) items.push(item('layers-alt', 'Compact now', { mi: 'compact', session, pane }));
    items.push(item('tuning', 'Compacting limits…', { mi: 'cpop', session, pane }));
  }
  if (f.p.fg && termKind(f.p) !== 'agent') items.push(item('stop-f', `Stop ${procName(f.p.proc)}…`, { mi: 'close', what: 'process', session, pane, pid: f.p.fg }, 'danger'));
  items.push(item('xmark', 'Close pane…', { mi: 'close', what: 'pane', session, pane }, 'danger'));
  showMenu(btn, items.join(''));
}
// the move, confirmed in the panel's own dialog
function termMove(session, tab, to) {
  const { tb } = findTab(session, tab);
  if (!tb) return;
  const why = termMovable(tb);
  if (why) return toast(why, true);
  const p = tb.panes[0];
  modalIcon('arrow-right');
  pending = { url: '/api/term/move', body: { session, tab, to }, done: `Moved ${tb.name} to ${to}`, after: () => { if (tb.panes.some((q) => PK.key === session + ':' + q.id)) closePeek(); if (tb.panes.some((q) => D.key === session + ':' + q.id)) closePane(); } };
  $('#modal-title').textContent = `Move ${tb.name} to ${to}?`;
  $('#modal-msg').textContent = p.agent
    ? `Claude closes here and resumes the same conversation in a new tab in ${to}, in ${p.agent.cwd || 'its folder'}.`
    : `A new shell opens in ${to}, in the same folder, and this tab closes.`;
  $('#modal-ok').textContent = 'Move';
  $('#modal-ok').className = 'btn primary';
  $('#modal-err').textContent = '';
  $('#modal').showModal();
}
$('#menu').addEventListener('click', (e) => {
  const b = e.target.closest('[data-mi="tmove"]');
  if (!b) return;
  e.stopImmediatePropagation();
  closeMenu();
  termMove(b.dataset.session, +b.dataset.tab, b.dataset.to);
}, true);

// ---------- a session folds and opens, animated ----------
// The body's height glides from where it is (mid-fold too: a second click turns it around) to its end, and holds
// there (fill) until the end state is set in the same step: no frame at its natural height in between, no re-render.
function termFold(name) {
  const sec = $(`#view .tsess[data-session="${CSS.escape(name)}"]`), body = sec && $('.sfold', sec);
  const closing = !TV.closed.has(name);
  closing ? TV.closed.add(name) : TV.closed.delete(name);
  tvSave();
  if (!body || calm.matches) return render();
  const from = body.hidden ? 0 : body.getBoundingClientRect().height, op = body.hidden ? 0 : +getComputedStyle(body).opacity;
  body._fold?.cancel();
  body.hidden = false;
  sec.classList.toggle('closed', closing);
  const to = closing ? 0 : body.scrollHeight;
  TV.folding = name;
  const a = body._fold = body.animate([{ height: from + 'px', opacity: op }, { height: to + 'px', opacity: closing ? 0 : 1 }],
    { duration: Math.round(180 + 140 * Math.min(1, Math.abs(to - from) / 500)), easing: 'cubic-bezier(.4, 0, .2, 1)', fill: 'forwards' });
  a.onfinish = () => {
    if (body._fold !== a) return;
    body._fold = null;
    if (TV.folding === name) TV.folding = null;
    body.hidden = closing;
    a.cancel(); // the end state is in place: let go of the held frame
  };
}

// ---------- clicks: this page's controls (capture phase: app.js's view handler never sees them) ----------
function termClick(e) {
  const inPeek = e.currentTarget === peekEl;
  if (S.view !== 'term' && !inPeek) return;
  const t = e.target, hit = (sel) => t.closest(sel);
  let b;
  const stop = () => { e.stopPropagation(); e.preventDefault(); };
  if (Date.now() - SORT.endedAt < 400 && hit('.tsess .sh')) return stop(); // the end of a drag, not a click
  if ((b = hit('[data-tview]'))) { stop(); TV.view = b.classList.contains('chip') && TV.view === b.dataset.tview ? 'all' : b.dataset.tview; tvSave(); return render(); }
  if ((b = hit('[data-tsolo]'))) { stop(); const n = b.dataset.tsolo; TV.fSession = TV.fSession.size === 1 && TV.fSession.has(n) ? new Set() : new Set([n]); tvSave(); return render(); }
  if ((b = hit('[data-tunf]'))) { stop(); const k = b.dataset.tunf; if (k === 'status') TV.fStatus.clear(); if (k === 'session') TV.fSession.clear(); if (k === 'q') TV.q = ''; tvSave(); return render(); }
  if (hit('[data-tclear]')) { stop(); TV.view = 'all'; TV.q = ''; TV.fStatus.clear(); TV.fSession.clear(); TV.tabSel = {}; tvSave(); return render(); }
  if ((b = hit('[data-tren]'))) { stop(); const [sn, tab] = b.dataset.tren.split('|'); return renStart(sn, tab === '' ? null : +tab); }
  if ((b = hit('[data-tpop]'))) { stop(); return TPOP.kind === b.dataset.tpop ? closePop() : openPop(b, b.dataset.tpop); }
  if ((b = hit('[data-ttab]'))) { stop(); const [sn, id] = b.dataset.ttab.split('|'); TV.tabSel[sn] = id ? +id : null; return render(); }
  if ((b = hit('[data-treply]'))) { stop(); return openMode(b.dataset.treply, 'chat'); }
  if ((b = hit('[data-tfeatx]'))) { stop(); TV.feature = ''; store.set('tv.feature', ''); return render(); }
  if ((b = hit('[data-fpedit]'))) { stop(); const doc = b.dataset.fpedit, dz = FP.d?.dossier || {}; FP.edit = doc; FP.rev = dz.revs?.[doc] || ''; FP.draft = (dz[doc] || '').trim() ? dz[doc] : fpTemplate(doc, FP.s); render(); return setTimeout(() => $('.fp-ed')?.focus(), 0); }
  if (hit('[data-fpcancel]')) { stop(); FP.edit = ''; return render(); }
  if ((b = hit('[data-fpsave]'))) { stop(); return fpSave(b.dataset.fpsave); }
  const fpAct = (url, body, ok) => api(url, { session: FP.s, ...body }).then((r) => {
    if (r.status !== 200) return toast(r.error || 'Couldn\'t do it', true);
    FP.d.dossier = r.dossier;
    toast(r.warning || ok, !!r.warning);
    render();
  }, () => toast('The panel is offline', true));
  const fpFind = (key) => (FP.d?.dossier?.milestones || []).flatMap((m) => m.steps).find((x) => x.key === key);
  if ((b = hit('[data-fpans]'))) {
    stop();
    const text = $(`[data-fpansk="${b.dataset.fpans}"]`)?.value.trim();
    if (!text) return toast('Write your answer first', true);
    const resume = b.dataset.resume === '1';
    return fpAct('/api/term/feature/answer', { key: b.dataset.fpans, qid: b.dataset.qid, rev: FP.d.dossier.rev, text, resume }, resume ? 'Answered: its lead resumes that step' : 'Answered: the step waits for its lead');
  }
  if ((b = hit('[data-fpapprove]'))) {
    stop();
    const x = fpFind(b.dataset.fpapprove);
    const all = (FP.d?.dossier?.milestones || []).flatMap((m) => m.steps).filter((y) => y.check && y.cwd && !y.approval?.check);
    const items = (b.dataset.all ? all : [x]).map((y) => ({ key: y.key, check: y.check, cwd: y.cwd }));
    return fpAct('/api/term/feature/approve', { items }, `Approved ${items.length} check${items.length > 1 ? 's' : ''}: a run of exactly that command, there, verifies its step`);
  }
  if ((b = hit('[data-fpsafe]'))) { stop(); const x = fpFind(b.dataset.fpsafe); return fpAct('/api/term/feature/approve', { items: [{ key: x.key, kind: x.kind }] }, 'Approved as safe to redo after an interruption'); }
  if (hit('[data-fppromote]')) { stop(); const dz = FP.d.dossier; return fpAct('/api/term/feature/promote', { draftRev: dz.revs.draft, featureRev: dz.revs.feature }, 'Draft adopted as FEATURE.md: accept the outcomes again once verified'); }
  if (hit('[data-fpdiscard]')) { stop(); return fpAct('/api/term/feature/discard', { draftRev: FP.d.dossier.revs.draft }, 'Draft discarded'); }
  if ((b = hit('[data-fpaccept]'))) { stop(); return fpAct('/api/term/feature/accept', { rev: FP.d.dossier.revs.feature }, 'Outcomes accepted as they stand'); }
  if ((b = hit('[data-fpverify]'))) { stop(); return fpAct('/api/term/feature/verify', { key: b.dataset.fpverify, rev: FP.d.dossier.rev }, 'Marked owner verified'); }
  if ((b = hit('[data-fprelease]'))) { stop(); return fpAct('/api/term/feature/release', { tab: b.dataset.fprelease, rev: FP.d.dossier.rev }, 'Hold released: its lead re-plans (nothing new is authorized)'); }
  if ((b = hit('[data-fpbind]'))) {
    stop();
    const thread = $('[data-fpbindv]')?.value.trim();
    if (!thread) return toast('Type the thread\'s id', true);
    return api('/api/term/feature/bind', { session: b.dataset.fpbind, thread }).then((r) => {
      if (r.status !== 200) return toast(r.error || 'Couldn\'t attach it', true);
      toast(`${r.thread.title} manages ${b.dataset.fpbind} now${r.warning ? ', but: ' + r.warning : ''}`, !!r.warning);
      FP.at = 0;
      fpLoad(b.dataset.fpbind, true);
    }, () => toast('The panel is offline', true));
  }
  if ((b = hit('[data-fpms]'))) { const k = 'ms:' + b.dataset.fpms; S.subsOpen.has(k) ? S.subsOpen.delete(k) : S.subsOpen.add(k); return; }
  if ((b = hit('[data-tfeatb]'))) {
    stop();
    b.disabled = true;
    return api('/api/term/feature/brief', { session: b.dataset.tfeatb }).then((r) => {
      b.disabled = false;
      if (r.status !== 200) return toast(r.error || 'Couldn\'t brief them', true);
      toast(`Briefed ${r.sent.length ? r.sent.join(', ') : 'nobody'}${r.skipped.length ? ` · not now: ${r.skipped.join(', ')}` : ''}${r.warning ? ' · but: ' + r.warning : ''}`, !r.sent.length || !!r.warning);
      FP.at = 0;
    }, () => { b.disabled = false; toast('The panel is offline', true); });
  }
  if ((b = hit('[data-ttodo]'))) { stop(); const k = 'todo:' + b.dataset.ttodo; S.subsOpen.has(k) ? S.subsOpen.delete(k) : S.subsOpen.add(k); return render(); }
  if ((b = hit('[data-tthread]'))) { stop(); const id = b.dataset.tthread; setView('ai'); return setTimeout(() => aiShowThread(id), 0); }
  if ((b = hit('[data-tchat]'))) { stop(); return openMode(b.dataset.tchat, 'chat'); }
  if ((b = hit('[data-tscreen]'))) { stop(); return openMode(b.dataset.tscreen, 'screen'); }
  if ((b = hit('[data-tstop]'))) { stop(); const f = findPane(...splitKey(b.dataset.tstop)); return f && openClose('process', f.s.name, f.tb.id, f.p.id, f.p.fg); }
  if (hit('[data-tclose]')) { stop(); return closePeek(); }
  if ((b = hit('[data-menu="tpane"]'))) { stop(); return termMenu(b, b.dataset.session, +b.dataset.pane); }
  if (inPeek) { // the peek's own: photos, files, and the chat's buttons in its latest turn
    const key = PK.key;
    if (hit('[data-tattach]')) { stop(); return $('#tp-file', peekEl).click(); }
    if ((b = hit('[data-tunphoto]'))) { stop(); const [f] = PK.photos.splice(+b.dataset.tunphoto, 1); if (f) URL.revokeObjectURL(f.url); return renderPeek(); }
    if (hit('[data-tfiles]')) { stop(); openMode(key, 'chat'); return openFilesPanel(); }
    if ((b = hit('[data-openfile]'))) { stop(); const path = b.dataset.openfile; openMode(key, 'chat'); return openFile(path); }
    if ((b = hit('[data-copyi]'))) { stop(); const it = PK.turn[+b.dataset.copyi]; return it && navigator.clipboard?.writeText(it.text).then(() => toast('Copied'), () => {}); }
    if ((b = hit('[data-quotei]'))) { stop(); const it = PK.turn[+b.dataset.quotei], ta = $('#tp-text', peekEl); if (it && ta) { ta.value = it.text.split('\n').map((l) => '> ' + l).join('\n') + '\n\n' + ta.value; ta.focus(); } return; }
    if ((b = hit('[data-ulong]'))) { stop(); const id = b.dataset.ulong; D.openTools.has(id) ? D.openTools.delete(id) : D.openTools.add(id); return renderPeek(); }
    if (hit('[data-sub], [data-more], [data-unqueue]')) { stop(); return openMode(key, 'chat'); }
    return;
  }
  if ((b = hit('[data-tpeek]'))) { stop(); const k = b.dataset.tpeek, f = findPane(...splitKey(k)); return openMode(k, f?.p.agent?.chat ? 'chat' : 'screen'); }
  if ((b = hit('button[data-tfold]'))) { stop(); return termFold(b.dataset.tfold); }
  if ((b = hit('[data-tfold]')) && !hit('button, a, input')) { stop(); TV.feature = b.dataset.tfold; store.set('tv.feature', TV.feature); window.scrollTo(0, 0); return render(); } // a session: its feature page
  // a pane: the first click peeks, the next opens it full (its chat, or its terminal)
  if ((b = hit('[data-open]')) && !hit('button, a, input')) {
    stop();
    const key = b.dataset.open, x = termPanes(S.data.term).find((y) => y.key === key);
    return openMode(key, x?.p.agent?.chat ? 'chat' : 'screen'); // straight to its chat (or terminal)
  }
}
$('#view').addEventListener('click', termClick, true);
peekEl.addEventListener('click', termClick, true);
$('#view').addEventListener('keydown', (e) => {
  if (S.view !== 'term' || (e.key !== 'Enter' && e.key !== ' ')) return;
  const el = e.target.closest('[data-tpeek]');
  if (el && el === e.target) { e.preventDefault(); el.click(); }
}, true);
$('#view').addEventListener('input', (e) => { if (e.target.id === 'tq') { TV.q = e.target.value; render(); } });
addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  if (TV.ren) { e.stopImmediatePropagation(); renDone(false); return; }
  if (TPOP.kind) { e.stopImmediatePropagation(); closePop(); return; }
  if (SORT.st?.on) { e.stopImmediatePropagation(); land(false); return; }
  if (TV.peek && !D.key && $('#menu').hidden && !$('dialog[open]')) { e.stopImmediatePropagation(); closePeek(); }
}, true);
// the tab strips and Waiting on you scroll sideways under the wheel; their edges fade where more is
$('#view').addEventListener('wheel', (e) => {
  const t = e.target.closest('.tstrip, .inbox');
  if (!t || t.scrollWidth <= t.clientWidth || Math.abs(e.deltaX) > Math.abs(e.deltaY)) return;
  const before = t.scrollLeft;
  t.scrollLeft += e.deltaY;
  if (t.scrollLeft !== before) e.preventDefault();
}, { passive: false });
$('#view').addEventListener('scroll', (e) => { if (e.target.matches?.('.tstrip, .inbox')) fades(e.target.parentElement); }, true);

// ---------- reorder: a session moves with the pointer, mouse or finger alike ----------
// A mouse drags its header (a click still folds it); a finger drags the handle at once, or holds the header a moment
// (a swipe still scrolls). The session itself lifts and follows, the others slide out of the way, the page scrolls at
// its edges, and it lands where you let go; Esc (or a cancelled touch) puts it back. Positions come from the layout
// (offsetTop), not the screen: the others are mid-slide.
const SORT = { st: null, endedAt: 0 };
$('#view').addEventListener('pointerdown', (e) => {
  const sh = e.target.closest?.('.tsess .sh');
  if (S.view !== 'term' || !sh || !e.isPrimary || e.button > 0 || SORT.st) return;
  const grip = !!e.target.closest('.grip');
  if (!grip && e.target.closest('button, a, input')) return;
  const st = SORT.st = { sec: sh.closest('.tsess'), id: e.pointerId, x: e.clientX, y: e.clientY, cy: e.clientY, touch: e.pointerType !== 'mouse', on: false };
  if (st.touch && grip) lift(st); // the handle: at once (a timer would lose the race to the first move)
  else if (st.touch) st.timer = setTimeout(() => lift(st), 380);
}, true);
addEventListener('pointermove', (e) => {
  const st = SORT.st;
  if (!st || e.pointerId !== st.id) return;
  if (!st.on) {
    const d = Math.hypot(e.clientX - st.x, e.clientY - st.y);
    if (st.touch) { if (d > 8) { clearTimeout(st.timer); SORT.st = null; } return; } // a finger moving before the hold: a scroll
    if (d < 5) return;
    lift(st);
  }
  st.cy = e.clientY;
  follow(st);
});
addEventListener('pointerup', (e) => { if (SORT.st?.id === e.pointerId) land(true); }, true);
addEventListener('pointercancel', (e) => { if (SORT.st?.id === e.pointerId) land(false); }, true);
$('#view').addEventListener('touchmove', (e) => { if (SORT.st?.on) e.preventDefault(); }, { passive: false }); // the page doesn't scroll under a held session
$('#view').addEventListener('contextmenu', (e) => { if (SORT.st) e.preventDefault(); });
function lift(st) {
  if (SORT.st !== st || st.on) return;
  st.on = true;
  closePop();
  S.dragging = true; // no live re-render under the drag (app.js held())
  st.grab = st.y - st.sec.getBoundingClientRect().top;
  st.sec.classList.add('lifted');
  document.body.classList.add('sorting');
  follow(st);
  const tick = () => { if (!st.on) return; edgeScroll(st); st.raf = requestAnimationFrame(tick); };
  st.raf = requestAnimationFrame(tick);
}
function follow(st) {
  const me = st.sec, list = me.parentElement, secs = [...list.children], base = list.getBoundingClientRect().top - list.offsetTop;
  const sec = secs.find((x) => st.cy < base + x.offsetTop + x.offsetHeight) || secs[secs.length - 1];
  if (sec !== me) { // over another: before it (its top half) or after it
    const ref = st.cy < base + sec.offsetTop + sec.offsetHeight / 2 ? sec : sec.nextElementSibling;
    if (ref !== me && ref !== me.nextElementSibling) {
      const others = secs.filter((x) => x !== me), was = new Map(others.map((x) => [x, x.getBoundingClientRect().top]));
      list.insertBefore(me, ref);
      if (!calm.matches) for (const x of others) {
        const dy = was.get(x) - x.getBoundingClientRect().top;
        if (Math.abs(dy) > 1) x.animate([{ transform: `translateY(${dy}px)` }, { transform: 'none' }], { duration: 260, easing: 'cubic-bezier(.4, 0, .2, 1)' });
      }
    }
  }
  me.style.transform = `translateY(${st.cy - st.grab - (base + me.offsetTop)}px) scale(${calm.matches ? 1 : 1.012})`; // under the pointer, wherever its slot is
}
// near the top or bottom of the screen (above a phone's tab bar), the page scrolls, faster the closer
function edgeScroll(st) {
  const nav = $('.nav')?.getBoundingClientRect(), bottom = nav && nav.top > innerHeight / 2 ? nav.top : innerHeight, edge = 80;
  const v = st.cy < edge + 60 ? -Math.min(18, (edge + 60 - st.cy) / 5) : st.cy > bottom - edge ? Math.min(18, (st.cy - bottom + edge) / 5) : 0;
  if (!v) return;
  const y = scrollY;
  scrollBy(0, v);
  if (scrollY !== y) follow(st);
}
function land(keep) {
  const st = SORT.st;
  SORT.st = null;
  if (!st) return;
  clearTimeout(st.timer);
  if (!st.on) return;
  st.on = false;
  cancelAnimationFrame(st.raf);
  SORT.endedAt = Date.now(); // the click that ends a mouse drag doesn't fold the header
  const me = st.sec, from = me.style.transform;
  me.classList.remove('lifted');
  me.style.transform = '';
  document.body.classList.remove('sorting');
  if (from && !calm.matches) me.animate([{ transform: from }, { transform: 'none' }], { duration: 200, easing: 'cubic-bezier(.2, .8, .2, 1)' });
  S.dragging = false;
  if (keep) { // the order on screen is the new order (sessions hidden by a filter keep their places)
    const vis = $$('#view .tsess').map((x) => x.dataset.session), q = [...vis];
    TV.order = TV.order.map((n) => (vis.includes(n) ? q.shift() : n));
    tvSave();
    flushStale();
  } else render(); // back where it was
}

// ---------- drag a tab, or a pane (it takes its tab), onto another session to move it (a mouse; a finger uses ⋯) ----------
$('#view').addEventListener('dragstart', (e) => {
  const el = e.target.closest?.('[data-tdrag]');
  if (S.view !== 'term' || !el) return;
  const sess = el.closest('.tsess')?.dataset.session;
  if (el.dataset.tdrag === 'tab') TV.drag = { from: sess, tab: +el.dataset.ttab.split('|')[1] };
  else { const f = findPane(...splitKey(el.dataset.open)); if (!f) return; TV.drag = { from: sess, tab: f.tb.id }; }
  S.dragging = true;
  setTimeout(() => TV.drag && el.classList.add('dragging'));
  e.dataTransfer.effectAllowed = 'move';
  e.dataTransfer.setData('text/plain', el.textContent.trim().slice(0, 80));
});
const intoAt = (e) => { const sec = e.target.closest?.('.tsess'); return sec && sec.dataset.session !== TV.drag.from ? sec : null; };
const clearDrop = () => { for (const el of $$('#view .drop-into')) el.classList.remove('drop-into'); };
$('#view').addEventListener('dragover', (e) => {
  if (!TV.drag) return;
  const sec = intoAt(e);
  if (!sec) return clearDrop();
  e.preventDefault();
  if (!sec.classList.contains('drop-into')) { clearDrop(); sec.classList.add('drop-into'); }
});
$('#view').addEventListener('drop', (e) => {
  if (!TV.drag) return;
  const sec = intoAt(e);
  clearDrop();
  if (!sec) return;
  e.preventDefault();
  termMove(TV.drag.from, TV.drag.tab, sec.dataset.session);
});
$('#view').addEventListener('dragend', () => {
  TV.drag = null;
  S.dragging = false;
  clearDrop();
  for (const el of $$('#view .dragging')) el.classList.remove('dragging');
  flushStale();
});
// what you type in a feature's document stays yours while the page keeps updating around it
$('#view').addEventListener('input', (e) => { if (e.target.matches?.('[data-fped]')) FP.draft = e.target.value; });
