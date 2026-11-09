'use strict';
// ---------- a feature's page (v8 layout) ----------
// Drawn from the real record: featFacts (dossier, team, trees, PRs, thread) plus featFacts.page, the page data the
// back end assembles (featv8page.go). A part the panel doesn't track yet says so; nothing is invented.
// Order: header · Since you looked · Team · Needs you · Plan (Milestones | Objectives) · Code delivery · Work log.
// Owner actions reuse the existing routes and their handlers in terms.js (data-fp*); the context chat posts to the
// feature's manager thread only (POST /api/term/feature/message), deduped by its client id.
var FV = { filter: 'all', trend: 'since', lrange: 'all', sel: null, q: null, ptab: 'ms', mso: new Set(), vb: null, hover: null,
  prf: { repo: 'all', state: 'all', failing: false }, pr: null, br: null, wl: 'all', wlc: false, glin: null,
  sent: new Set(), seenQ: new Set(), acked: new Set(), seenBad: new Map(), seenTry: 0, seenPause: false, reviewed: {}, drafts: {}, comp: null, msgs: {}, msgN: 0 };
const fvNow = () => Date.now() / 1000;
const FV_STALE = 120; // s without a team sample before the page says it may be out of date (the sampler runs every 2-10 s)
const fvT = (t) => { if (!t) return '–'; const d = new Date(t * 1000); return d.toLocaleDateString(undefined, { weekday: 'short' }) + ' ' + d.toTimeString().slice(0, 5); };
const fvHM = (s) => { const m = Math.max(0, Math.round(s / 60)); return m < 60 ? m + 'm' : Math.floor(m / 60) + 'h ' + String(m % 60).padStart(2, '0') + 'm'; };
const fvNT = (why) => `<div class="fv-nt">${ic('info-circle')}<span>${why}</span></div>`; // "not tracked yet"

// ---- the model: what the page needs, from the record ----
// Until the back end assembles page.*, the standalone owner routes fill sessions/relations and branches/releases/prx.
// They serve the panel's cached snapshot (no model, nothing run for the request); null/absent there = not tracked.
const FVX = {};
async function fvExtra(name) {
  const x = (FVX[name] ||= { at: 0 }); if (x.busy || Date.now() - x.at < 12000) return; x.busy = true;
  const q = '?session=' + encodeURIComponent(name), get = (p) => api('/api/term/feature/' + p + q).catch(() => null);
  const [c, s] = await Promise.all([get('code'), get('sessions')]);
  const was = JSON.stringify([x.code, x.sess, x.ev?.length]); x.code = c?.status === 200 ? c : null; x.sess = s?.status === 200 ? s : null;
  // events: ascending pages from the last cursor (only what's new is read again); a failed read keeps what we had
  for (let i = 0; i < 25; i++) { // 25 x 200 = the back end's per-feature cap (5000)
    const r = await api(`/api/term/feature/events${q}&after=${encodeURIComponent(x.cur || 'ev-0')}&limit=200`).catch(() => null);
    if (r?.status !== 200) { if (r?.status === 404) x.ev ??= null; break; }
    x.ev = (x.ev || []).concat(r.events || []); if (r.events?.length) x.cur = r.events[r.events.length - 1].id;
    if (!r.more) break;
  }
  x.busy = false; x.at = x.code?.refreshing ? Date.now() - 10000 : Date.now(); // the server is reading git/GitHub: look again in 2 s
  if (JSON.stringify([x.code, x.sess, x.ev?.length]) !== was && TV.feature === name && S.view === 'term') render();
}
const fvSeq = (v) => (v == null || v === '' ? null : Number.isFinite(+String(v).replace(/^ev-/, '')) ? +String(v).replace(/^ev-/, '') : null);
const fvAt = (t) => (typeof t === 'string' ? Date.parse(t) / 1000 || 0 : t || 0); // the API's times: unix s, or RFC3339 from Go's time.Time
function fvModel(name, d, t) {
  const X = FVX[name] || {}, raw = d.page || {}, pg = { ...raw };
  if (!('sessions' in raw) && X.sess) { pg.sessions = X.sess.sessions; pg.relations ??= X.sess.relations; pg.observedAt ??= X.sess.observedAt; }
  if (!('branches' in raw) && X.code) { pg.codeReading = X.code.refreshing && !X.code.branches; pg.branches = X.code.branches; pg.releases ??= X.code.releases; pg.prx ??= X.code.prx; }
  if (!('events' in raw) && X.ev) pg.events = X.ev;
  { const a = fvSeq(raw.seen?.frontier), b = FV.fr?.[name] ?? null; if (a != null || b != null) pg.seen = { ...(raw.seen || {}), frontier: Math.max(a ?? -1, b ?? -1) }; } // 'ev-N' or N; the latest the panel told us
  if (pg.events) pg.events = pg.events.map((e) => ({ ...e, at: fvAt(e.at), seq: e.seq || +String(e.id).slice(3) || 0 }));
  if (pg.sessions) pg.sessions =pg.sessions.map((s) => ({ ...s, k: s.id || s.sid, from: s.from || 0, to: s.to ?? null }));
  const dz = d.dossier || { milestones: [] }, steps = [];
  (dz.milestones || []).forEach((m, i) => (m.steps || []).forEach((x, j) => steps.push({ ...x, ms: 'M' + (i + 1), msTitle: fvMsT(m.title, 'M' + (i + 1)), n: `${i + 1}.${j + 1}` })));
  const stepOf = (k) => steps.find((x) => x.key === k || x.n === k);
  const observed = pg.observedAt || 0, stale = !observed || fvNow() - observed > FV_STALE;
  const panes = t ? termPanes(t).filter((x) => x.s?.name === name) : [];
  const live = (tab) => (d.team || []).find((a) => a.tab === tab);
  const sessions = pg.sessions || null, tickets = fvTickets(pg, steps);
  return { name, d, pg, dz, steps, stepOf, observed, stale, panes, live, sessions, tickets, rels: pg.relations || [], evidence: pg.evidence || {} };
}
// tickets: the record's own (page.tickets), else derived from the steps exactly as the existing owner routes see them
function fvTickets(pg, steps) {
  if (Array.isArray(pg.tickets)) return pg.tickets.map((x) => ({ ...x, real: true }));
  const out = [];
  for (const x of steps) {
    if (x.state === 'asking' && x.ask && !x.answer) out.push({ id: 'ask:' + x.key, kind: 'ask', step: x.key, title: x.ask, reason: x.ask, decision: 'Answer it: its lead resumes the step.', who: x.owner });
    if (x.check && x.cwd && !x.approval?.check) out.push({ id: 'approve:' + x.key, kind: 'approve', step: x.key, title: `Approve the check for ${x.n}`, reason: `Proposed check: ${x.check} in ${x.cwd}`, decision: 'Approve exactly this command, folder and wording as its proof.', who: x.owner });
    if (x.blocked) out.push({ id: 'hold:' + x.key, kind: 'hold', step: x.key, title: `${x.owner || 'A tab'} is held`, reason: x.blocked, decision: 'Release the hold so its lead re-plans, or leave it held.', who: x.owner });
    else if (x.state === 'claimed') out.push({ id: 'verify:' + x.key, kind: 'verify', step: x.key, title: `Is ${x.n} done?`, reason: x.why || 'Ticked, with no receipt for this revision.', decision: 'Verify it yourself, or leave it to its check.', who: x.owner });
  }
  return out;
}
const fvSessState = (M, s) => {
  if (s.to) return M.stepOf(s.step)?.state === 'done' ? 'done' : 'idle';
  if (M.stale) return 'unknown';
  if (s.blocked || M.tickets.some((k) => (k.sid === s.sid || k.session === s.sid) && k.kind === 'hold') || M.stepOf(s.step)?.blocked) return 'held';
  if (M.tickets.some((k) => k.sid === s.sid || k.session === s.sid)) return 'needs';
  return 'processing';
};
const FV_ORB = { processing: 'working', needs: 'input', held: 'blocked', done: 'done', idle: 'idle', unknown: 'stopped' };
const FV_L = { processing: 'Processing', needs: 'Needs you', held: 'Held', done: 'Done', idle: 'Ended', unknown: 'Unknown' };
const fvOrb = (st) => orb(FV_ORB[st] || 'idle', 14);

// ---- the page ----
function featPageV8(t, name, d) {
  if (!d.page || !('sessions' in d.page) || !('branches' in d.page) || !('events' in d.page)) fvExtra(name);
  const M = fvModel(name, d, t); FV.M = M; fvOutResume();
  const s = t.sessions.find((x) => x.name === name), th = d.thread || FEAT.s[name]?.thread;
  return `<div class="fp fv" data-k="fv:${esc(name)}">${fvHead(M, s, th)}${fvBanners(M, th)}` +
    `<section class="card fv-c" data-k="fv-since" aria-label="Since you looked">${fvSince(M)}</section>` +
    `<section class="card fv-c" data-k="fv-team" id="fv-team" aria-label="Team timeline">${fvTeam(M)}</section>` +
    `<section class="card fv-c" data-k="fv-needs" id="needs" aria-label="Needs you">${fvNeeds(M)}</section>` +
    `<section class="card fv-c" data-k="fv-plan" id="fv-plan" aria-label="Plan and milestones">${fvPlan(M)}</section>` +
    `<section class="card fv-c" data-k="fv-code" id="fv-code" aria-label="Code delivery">${fvCode(M)}</section>` +
    `<section class="card fv-c" data-k="fv-log" id="fv-log" aria-label="Work log">${fvLog(M)}</section></div>`;
}
function fvHead(M, s, th) {
  const st = M.steps, ver = st.filter((x) => x.state === 'done').length, pct = st.length ? Math.round((ver / st.length) * 100) : 0;
  const bk = (x) => (x.blocked ? 'held' : x.state === 'done' ? (x.label === 'owner verified' ? 'owner' : 'verified') : x.state);
  const seg = ['verified', 'owner', 'claimed', 'doing', 'asking', 'held', 'todo'].map((k) => { const n = st.filter((x) => bk(x) === k).length; return n ? `<span class="s-${k}" data-flex="${n}"></span>` : ''; }).join('');
  const ev = M.pg.events || [], fr = M.pg.seen?.frontier ?? null, since = fr == null ? [] : ev.filter((e) => e.seq > fr && (e.kind === 'verified' || e.kind === 'attested'));
  const agents = M.panes.filter((x) => x.kind === 'agent');
  const c = (k) => (M.sessions ? [...new Set(M.sessions.filter((x) => !x.to).map((x) => x.tab))].filter((tab) => k === 'all' || M.sessions.filter((x) => x.tab === tab && !x.to).some((x) => fvSessState(M, x) === k)).length : 0);
  return `<header class="fv-head" data-k="fvh"><button type="button" class="icon-btn ghost" data-tfeatx aria-label="Back to all sessions" title="All sessions">${ic('angle-left')}</button><h2>${esc(M.name)}</h2>` +
    `<div class="fv-vp" title="${esc(st.length ? `${ver} of ${st.length} jobs verified (receipt or your attestation; claims never count)` : 'No PLAN.md yet')}"><div class="t"><b>${st.length ? pct + '%' : '–'}</b> ${st.length ? `verified · ${ver}/${st.length} jobs` : 'no plan yet'}</div><div class="fv-vbar">${seg}</div></div>` +
    `<span class="spacer"></span>${featOwner(s, th)}` +
    (fr == null ? `<span class="fv-since0" title="The page records what you have seen only from rows that were on your screen; nothing has been recorded yet">since you looked: not recorded yet</span>`
      : `<span class="fv-since${since.length ? '' : ' zero'}" title="${esc(since.length ? since.map((e) => e.text).join('\n') : 'Nothing newly verified since your seen frontier')}">${since.length ? '+' + since.length + ' verified' : 'nothing new'} since you looked</span>`) +
    `<span class="seg sm" role="group" aria-label="Filter tabs">${[['all', 'All'], ['needs', 'Needs you'], ['held', 'Held']].map(([k, l]) => `<button type="button" data-fvf="${k}" aria-pressed="${FV.filter === k}">${l}<span>${c(k)}</span></button>`).join('')}</span>` +
    (agents.length ? `<button type="button" class="icon-btn ghost" data-tfeatb="${esc(M.name)}" title="Send every agent the feature's folder: what must be true, the plan, the tests">${ic('document-code')}</button><button type="button" class="icon-btn ghost" data-feature="${esc(M.name)}" title="Every agent's chat, side by side">${ic('grid4')}</button>` : '') + `</header>`;
}
function fvBanners(M, th) {
  const dz = M.dz, w = [];
  if (FP.err?.s === M.name) w.push(['warn', `<b>Not refreshed.</b> The last read failed (${FP.err.status ? 'error ' + FP.err.status : 'no answer'}): this is the record as last read.`]);
  if (M.pg.teamUnsampled) w.push(['warn', '<b>Team not sampled yet.</b> The panel hasn\'t looked at the terminals since it started, so who is working now is unknown. The plan and log below are the record as saved.']);
  else if (!M.observed) w.push(['warn', 'This page has no observation time from the panel yet: what it shows may not be current.']);
  else if (M.stale) w.push(['warn', `<b>May be out of date.</b> The team was last observed ${ago(M.observed)}${M.pg.reconciledAt ? `, the plan last reconciled ${ago(M.pg.reconciledAt)}` : ''}. Open sessions show as unknown.`]);
  for (const c of dz.conflicts || []) w.push(['warn', esc(c)]);
  if (dz.doneStale) w.push(['warn', 'Marked done, but that no longer holds since the record changed: ' + esc(dz.doneStale)]);
  if (dz.doneHistorical) w.push(['warn', 'Historical, unverified: marked done before the panel checked evidence. By today\'s checks: ' + esc(dz.doneHistorical)]);
  const bind = !th ? `<div class="fp-bind fld" data-k="fvbind">${ic('people')}<span class="faint">No manager thread runs this feature. Attach one by its exact id:</span><input data-fpbindv placeholder="thread id" aria-label="Thread id"><button type="button" class="btn sm" data-fpbind="${esc(M.name)}">Attach</button></div>` : '';
  return bind + w.map(([k, h], i) => `<div class="fp-warn" data-k="fvw${i}" role="status">${ic('alert-triangle')}<span>${h}</span></div>`).join('');
}

// ---- Since you looked: the full-width activity graph (activity is not progress) ----
const FV_TR = { since: 'Since you looked', '24h': '24h', all: 'All' };
const FV_EVK = { verified: 'ver', attested: 'own', ask: 'ask', hold: 'hold', pr_merged: 'pr', ci_failed: 'ci', report: 'rep', lead: 'mgr', transition: 'mgr', assign: 'mgr', reopen: 'hold', wake: 'mgr', delivery: 'mgr', message: 'rep' };
function fvSince(M) {
  const ev = (M.pg.events || []).filter((e) => e.at > 0).sort((a, b) => a.at - b.at), now = fvNow(), fr = M.pg.seen?.frontier; // an event without a readable time isn't placed on the time axis
  const lookedAt = fr != null ? ev.find((e) => e.seq === fr)?.at : null;
  const t0 = FV.trend === 'since' && lookedAt ? lookedAt : FV.trend === 'all' && ev.length ? Math.min(ev[0].at, now - 3600) : now - 86400, t1 = now;
  const X = (t) => ((Math.max(t0, Math.min(t1, t)) - t0) / (t1 - t0 || 1)) * 1000, pct = (t) => (X(t) / 10).toFixed(2) + '%';
  const A = M.pg.activity, H = 110, known = M.observed || now;
  let curve = '', calls = null;
  if (A?.calls?.length) {
    const pts = A.calls.map((v, i) => [A.from + i * A.bucketS + A.bucketS / 2, v]).filter((p) => p[0] >= t0 - A.bucketS && p[0] <= known), vmax = Math.max(1, ...pts.map((p) => p[1]));
    calls = A.calls.reduce((a, v, i) => a + (A.from + i * A.bucketS >= t0 ? v : 0), 0);
    if (pts.length > 1) { const d = pts.map((p, i) => `${i ? 'L' : 'M'}${X(p[0]).toFixed(1)},${(H - 6 - (p[1] / vmax) * (H - 18)).toFixed(1)}`).join(''); curve = `<path class="area" d="${d}L${X(pts[pts.length - 1][0])},${H}L${X(pts[0][0])},${H}Z"/><path class="curve" d="${d}"/>`; }
  }
  const vis = ev.filter((e) => e.at >= t0), shown = FV.hover ? vis.find((e) => e.id === FV.hover) : vis.filter((e) => e.kind === 'verified' || e.kind === 'attested').pop() || vis[vis.length - 1];
  const yOf = (i) => 40 + ((i * 37) % 50);
  const nv = vis.filter((e) => e.kind === 'verified' || e.kind === 'attested').length;
  const attr = (e) => (e.step ? ` data-fvgo="${esc(e.step)}"` : e.pr ? ` data-fvpr="${e.pr}"` : e.ticket ? ` data-fvq="${esc(e.ticket)}"` : '');
  return `<div class="h3"><div><h3>Since you looked</h3><div class="fv-sincel">Since ${fvT(t0)} <span class="pill">${fvHM(t1 - t0)}</span>` +
    `<span class="seg sm" role="group" aria-label="Window">${Object.entries(FV_TR).map(([k, l]) => `<button type="button" data-fvtr="${k}" aria-pressed="${FV.trend === k}"${k === 'since' && !lookedAt ? ' disabled title="No seen frontier recorded yet"' : ''}>${l}</button>`).join('')}</span></div></div>` +
    `<div class="fv-sums"><span>${fvOrb('done')}<b>${nv}</b> verified</span><span>${fvOrb('needs')}<b>${M.tickets.filter((x) => x.kind !== 'hold').length}</b> need you</span><span>${fvOrb('held')}<b>${M.tickets.filter((x) => x.kind === 'hold').length}</b> held</span><span class="faint">${calls == null ? 'tool calls: not tracked yet' : calls + ' tool calls'}</span></div></div>` +
    `<div class="fv-plot" data-fvplot><svg viewBox="0 0 1000 ${H}" preserveAspectRatio="none" height="${H}" aria-label="Activity over time: activity, not progress">${[0.25, 0.5, 0.75].map((f) => `<line class="gl" x1="0" x2="1000" y1="${H * f}" y2="${H * f}"/>`).join('')}${curve}</svg>` +
    (known < t1 - FV_STALE ? `<span class="unk" data-fvx="${pct(known)}" title="Not observed since ${fvT(known)}"></span>` : '') +
    vis.map((e, i) => `<button type="button" class="ev ${FV_EVK[e.kind] || 'mgr'}${shown === e ? ' on' : ''}" data-fvx="${pct(e.at)}" data-fvy="${yOf(i)}" data-fvev="${esc(e.id)}"${attr(e)} aria-label="${esc(fvT(e.at) + ' · ' + e.text)}"></button>`).join('') +
    (shown ? `<span class="xh" data-fvx="${pct(shown.at)}"></span><span class="ann" data-fvx="${pct(shown.at)}" data-fvy="${yOf(vis.indexOf(shown))}" data-fva="${(X(shown.at) / 10).toFixed(1)}">${esc(fvT(shown.at))} · ${esc(shown.kind)}<small>${esc(shown.text || '')}</small></span>` : '') +
    (M.pg.eventsMore ? `<span class="fv-more faint">latest ${(M.pg.events || []).length} events shown; older ones aren't drawn</span>` : '') + (!vis.length ? `<span class="fv-plot0">${M.pg.events ? 'No events in this window.' : 'Feature events are not tracked yet.'}</span>` : '') +
    `</div><div class="xax"><span>${fvT(t0)}</span><span>${fvT(t1)} · now</span></div>` +
    `<div class="key"><span><i class="ln"></i>tool calls: activity, not progress</span><span><i class="d ver"></i>check receipt</span><span><i class="d own"></i>owner attested</span><span><i class="pr"></i>PR merged</span><span><i class="ci"></i>CI failed</span><span><i class="ask"></i>needs you</span><span><i class="hold"></i>held</span><span><i class="mgr"></i>lead / supervisor</span><span>click a dot to open its job / PR / item</span></div>`;
}

// ---- Team: one row per tab, one bar per session; colour = state (blue processing to now, red needs you/held, green done) ----
const FV_LR = { '12h': 43200, '24h': 86400, all: 0 };
const FV_REL = { handoff: 'hands off to', blocks: 'blocks', pair: 'works alongside', resume: 'resumed as' };
function fvTeam(M) {
  const now = fvNow(), S = M.sessions;
  const head = (n) => `<div class="h3"><h3>Team</h3>${n}<span class="sp"></span><label class="fv-chk"><input type="checkbox" data-fvall${FV.all ? ' checked' : ''}> show all links</label>` +
    `<span class="seg sm" role="group" aria-label="Team time range">${Object.keys(FV_LR).map((k) => `<button type="button" data-fvlr="${k}" aria-pressed="${FV.lrange === k}">${k === 'all' ? 'All' : 'Last ' + k}</button>`).join('')}</span></div>`;
  if (!S) { // no session history yet: today's agents, honestly without bars
    const rows = (M.d.team || []).map((a) => `<div class="fv-trow"><span class="who">${orb(a.state === 'working' ? 'working' : a.state || 'idle', 14)}<b>${esc(a.tab)}</b><small>${esc(a.kind || '')}${a.lastAt ? ' · last seen ' + ago(a.lastAt) : ''}</small></span><span class="faint">${esc(a.title || a.last || '')}</span></div>`).join('');
    return head(`<span class="src">${plural((M.d.team || []).length, 'tab')}</span>`) + (rows || '') + fvNT('Session history (who worked on which job, when) is not tracked yet, so there are no bars or links.');
  }
  if (!S.length) return head(`<span class="src">0 sessions</span>`) + fvNT("No sessions are recorded for this feature yet.") + fvDeps(M);
  const first = S.reduce((m, x) => (x.from ? Math.min(m, x.from) : m), now - 3600), span = FV_LR[FV.lrange] || now - first + 1, t0 = Math.max(first, now - span), t1 = now + span * 0.02;
  const x = (t) => ((Math.max(t0, Math.min(t1, t)) - t0) / (t1 - t0)) * 100, known = M.observed || now;
  const tabs = [...new Set(S.map((s) => s.tab))], tst = (tab) => { const o = S.filter((s) => s.tab === tab && !s.to).map((s) => fvSessState(M, s)); for (const k of ['unknown', 'held', 'needs', 'processing']) if (o.includes(k)) return k; return S.filter((s) => s.tab === tab).every((s) => M.stepOf(s.step)?.state === 'done') ? 'done' : 'idle'; };
  const rows = tabs.filter((tab) => FV.filter === 'all' || tst(tab) === FV.filter);
  const step = span <= 43200 ? 7200 : span <= 86400 ? 14400 : 43200, ticks = []; for (let t = now - step; t > t0; t -= step) ticks.unshift(t);
  const grid = ticks.map((t) => `<span class="gridl" data-fvl="${x(t)}"></span>`).join('') + `<span class="nowl" data-fvl="${x(now)}"></span>` + (known < now - FV_STALE ? `<span class="unk" data-fvl="${x(known)}"></span>` : '');
  const bar = (s) => { const tk = !s.to && M.tickets.find((t) => t.sid ? t.sid === s.sid : t.step === s.step), st = fvSessState(M, s), j = M.stepOf(s.step), end = s.to || Math.min(now, M.stale ? known : now), msC = j ? j.ms : '';
    return `<button type="button" class="ss ${fvMc(msC)} st-${st}${FV.msHi && msC !== FV.msHi ? ' dim' : ''}${s.to ? ' done' : ' open'}${s.from ? '' : ' nofrom'}${FV.sel === s.k ? ' sel' : ''}" data-sess="${esc(s.k)}" data-fvl="${x(s.from)}" data-fvw="${Math.max(0.4, x(end) - x(s.from))}" data-fl="${esc(`${msC} · ${j?.n || s.step || '—'}|${j?.text || 'no job'}|${s.kind || ''} · ${FV_L[st]}`)}" aria-label="${esc(`${s.tab}, ${s.kind || ''} session ${s.sid}: job ${j ? j.n + ' ' + j.text : s.step || 'none'}, ${fvT(s.from)} to ${s.to ? fvT(s.to) : 'now'}, ${FV_L[st]}`)}">` +
      `<span class="mk">${esc(msC || '—')} · ${esc(j?.n || s.step || '—')}</span>${tk ? `<span class="tag ${tk.kind === 'hold' ? 'held' : 'needs'}" data-fvq="${esc(tk.id)}">${tk.kind === 'hold' ? 'Held' : 'Needs you'}</span>` : ''}</button>`; };
  const lrow = rows.map((tab) => { const ss = S.filter((s) => s.tab === tab && s.from < t1 && (s.to || now) > t0), st = tst(tab), cur = ss.filter((s) => !s.to);
    return `<div class="lrow${FV.msHi && !ss.some((s) => M.stepOf(s.step)?.ms === FV.msHi) ? ' dim' : ''}" data-k="lr:${esc(tab)}"><span class="who">${fvOrb(st)}<span class="nm">${esc(tab)}</span><span class="l2">${FV_L[st]}${cur.length ? ' · ' + cur.map((s) => (s.kind || '') + ' · ' + (M.stepOf(s.step)?.n || s.step || '—')).join(', ') : ''}</span></span><div class="trk">${grid}${ss.map(bar).join('')}</div></div>`; }).join('');
  const axis = `<div class="lrow ax"><span class="axpad"></span><div class="trk">${ticks.map((t) => `<span class="axl" data-fvl="${x(t)}">−${fvHM(now - t)}</span>`).join('')}<span class="axl now" data-fvl="${x(now)}">now</span></div></div>`;
  const cnt = (k) => tabs.filter((tb) => tst(tb) === k).length;
  return head(`<span class="src">${fvOrb('processing')}${cnt('processing')} processing</span><span class="src">${fvOrb('needs')}${cnt('needs') + cnt('held')} need you</span><span class="src">${plural(tabs.length, 'tab')}</span>${FV.msHi ? `<span class="src">Highlighting ${fvMref(M, FV.msHi)} <button type="button" class="btn ghost" data-fvmshi="">Clear</button></span>` : ''}`) +
    `<div class="lwrap"><div class="lanes${FV.sel ? ' focus' : ''}" id="fv-lanes">${axis}${lrow || `<div class="fv-nt">No tabs are “${esc(FV_L[FV.filter] || FV.filter)}”.</div>`}<svg class="deps" id="fv-deps" aria-hidden="true"></svg><div class="flab" id="fv-flab" aria-hidden="true"></div></div></div>` +
    fvDeps(M) + `<div class="lkey"><span><i class="lsw proc"></i>processing (reaches now)</span><span><i class="lsw bad"></i>blocked / needs you</span><span><i class="lsw ok"></i>done</span><span><i class="lsw idle"></i>ended, job not verified</span>` +
    `<span>Hover, focus or select a bar to see only its links · ← → ↑ ↓ move · Esc clears</span><span>links come only from recorded relationships${M.rels.length ? '' : ' (none recorded yet)'}</span></div>` + fvDetail(M);
}
// recorded dependencies that can't be drawn as a link between two bars (an end with no session yet), and invalid
// prerequisites: listed with why, linked to their jobs, never hidden and never drawn as an invented bar or edge
function fvDeps(M) {
  const open = M.rels.filter((r) => !r.from || !r.to || !M.sessions.some((x) => x.k === r.from) || !M.sessions.some((x) => x.k === r.to));
  const bad = M.steps.filter((x) => (x.afterInvalid || []).length);
  if (!open.length && !bad.length) return '';
  const job = (k) => (k ? fvKref(M, k, true) : '<span class="faint">no step</span>'), need = (k) => { const t = k && M.tickets.find((x) => x.step === k); return t ? ` <button type="button" class="tag needs" data-fvq="${esc(t.id)}" aria-label="${esc('Needs you: ' + t.title)}">Needs you →</button>` : ''; };
  return `<div class="fv-deps" data-k="fvdeps" aria-label="Dependencies not drawn"><h5>Dependencies not drawn on the timeline</h5><ul>` +
    open.map((r) => `<li data-rel-open="${esc(r.id)}">${job(r.toStep)} ${esc(FV_REL[r.kind] === 'blocks' ? 'waits on' : FV_REL[r.kind] || r.kind)} ${job(r.fromStep)}${need(r.toStep)}<span class="faint"> · ${esc(r.why || (!r.from ? 'no session on the prerequisite yet' : 'no session on this job yet'))}${r.src ? ' · ' + esc(r.src) : ''}</span></li>`).join('') +
    bad.map((x) => `<li data-dep-issue="${esc(x.key)}">${job(x.key)}: invalid prerequisite ${x.afterInvalid.map((d) => `<code>${esc(d.ref)}</code> (${esc(d.why)})`).join(', ')}${need(x.key)}<span class="faint"> · the gate treats it as not ready</span></li>`).join('') + `</ul></div>`;
}
function fvDetail(M) {
  const s = FV.sel && M.sessions?.find((x) => x.k === FV.sel); if (!s) return '';
  const st = fvSessState(M, s), j = M.stepOf(s.step), rel = M.rels.filter((r) => r.from === s.k || r.to === s.k), o = (id) => M.sessions.find((x) => x.k === id);
  const chip = (r, x, lab) => x ? `<button type="button" class="fl-c k-${r.kind}" data-sess="${esc(x.k)}"><small>${lab}</small>${esc(x.tab)} · ${esc(M.stepOf(x.step)?.n || x.step || '—')}</button>` : '';
  const ins = rel.filter((r) => r.to === s.k && r.kind !== 'pair'), outs = rel.filter((r) => r.from === s.k && r.kind !== 'pair'), al = rel.filter((r) => r.kind === 'pair');
  const pane = M.panes.find((p) => p.tb?.name === s.tab && p.kind === 'agent');
  const prs = (M.d.prs || []).filter((p) => p.session === s.sid);
  return `<div class="detail" role="region" aria-label="Session detail" data-k="fvdet"><h5>${fvOrb(st)}${esc(s.tab)} <span class="faint">${esc(s.kind || '')} session <code>${esc(s.sid)}</code>${s.model ? ' · ' + esc(s.model) : ''} · ${FV_L[st]}</span><button type="button" class="btn sm ghost x" data-fvunsel>Close</button></h5>` +
    (s.blocked ? `<div class="row"><span>Blocked</span><span class="warn">${esc(s.blocked)}</span></div>` : '') +
    ((s.depIssues || []).length ? `<div class="row"><span>Prerequisites</span><span>${s.depIssues.map((d) => `<code>${esc(d.ref)}</code>: ${esc(d.why)}`).join(' · ')} <span class="faint">(invalid after: refs, not drawn)</span></span></div>` : '') +
    `<div class="row"><span>Job</span><span>${j ? `${fvJref(j, true)} <span class="faint">${esc(j.msTitle)}</span>` : esc(s.step || 'none recorded')}</span></div>` +
    `<div class="row"><span>Time</span><span>${s.from ? fvT(s.from) : "start not recorded"} → ${s.to ? fvT(s.to) : "now"}${s.from ? " · " + fvHM((s.to || fvNow()) - s.from) : ""}${s.end ? ' · ' + esc(s.end) : ''}</span></div>` +
    `<div class="row"><span>Flow</span><span class="flow">${ins.map((r) => chip(r, o(r.from), FV_REL[r.kind] + ' this')).join('') || '<span class="faint">nothing recorded before</span>'}<span class="arw">→</span><span class="fl-c self"><small>this session</small>${esc(s.tab)} · ${esc(j?.n || '—')}</span><span class="arw">→</span>${outs.map((r) => chip(r, o(r.to), 'this ' + FV_REL[r.kind])).join('') || '<span class="faint">nothing recorded after</span>'}${al.length ? `<span class="faint">alongside</span>${al.map((r) => chip(r, o(r.from === s.sid ? r.to : r.from), 'works alongside')).join('')}` : ''}</span></div>` +
    `<div class="row"><span>Relationships</span><span>${rel.length ? `<ul>${rel.map((r) => `<li>${esc(FV_REL[r.kind])}: ${esc(r.why || '')}<br><span class="faint">record ${esc(r.id)} · ${esc(r.src || '')} · ${fvT(r.at)}</span></li>`).join('')}</ul>` : 'None recorded. Bars that merely sit near each other are not related.'}</span></div>` +
    `<div class="row"><span>Code</span><span>${s.branch ? `<code>${esc(s.branch)}</code>` : 'No branch recorded'}${prs.map((p) => ` · <a href="${esc(p.url)}" target="_blank" rel="noopener">#${p.number} ${esc((p.state || '').toLowerCase())}</a>`).join('')}</span></div>` +
    (pane ? `<div class="row"><span>Chat</span><span><button type="button" class="btn sm ghost" data-tchat="${esc(pane.key)}">Open ${esc(s.tab)}'s chat</button></span></div>` : '') + `</div>`;
}
const fvJref = (j, full) => `<button type="button" class="ref" data-fvgo="${esc(j.key)}" title="${esc(`${j.ms} ${j.msTitle} → ${j.n}\n${j.text}`)}">${esc(j.n)}${full ? ' ' + esc(j.text) : ''}</button>`;
const fvKref = (M, k, full) => { const j = M.stepOf(k); return j ? fvJref(j, full) : esc(k || '—'); };

// ---- Needs you: the open items, oldest first; the selected one with its reply as a conversation ----
const FV_TK = { ask: 'Waits on your answer', approve: 'Check to approve', verify: 'Claimed, not verified', hold: 'Held', missed: 'Missed checkpoint', draft: 'Requirements draft', outcomes: 'Outcomes to accept' };
function fvNeeds(M) {
  const N = M.tickets.slice().sort((a, b) => (a.openedAt || a.at || 0) - (b.openedAt || b.at || 0));
  if (!N.some((x) => x.id === FV.q)) FV.q = N[0]?.id || null;
  const src = M.pg.tickets ? '' : '<span class="src">from the plan\'s open asks, proposed checks, claims and holds</span>';
  if (!N.length) return `<div class="h3"><h3>Needs you</h3><span class="tag idle">0</span>${src}</div><div class="fv-nt">${M.steps.length ? 'Nothing needs you. The lead handles the rest.' : 'No plan yet, so nothing to decide.'}</div>`;
  const tk = N.find((x) => x.id === FV.q), tkS = tk.sid || tk.session ? M.sessions?.filter((s) => s.sid === (tk.sid || tk.session)).sort((a, b) => !a.to - !b.to || b.from - a.from)[0] : null, j = M.stepOf(tk.step), who = tk.who || (tk.tab && '@' + tk.tab) || M.sessions?.find((s) => s.sid === (tk.sid || tk.session))?.tab || j?.owner || 'its agent';
  const acts = tk.real ? (tk.options || []).map((o, i) => `<button type="button" class="btn sm${i ? ' ghost' : ' primary'}" data-opt="${esc(o.id)}"${o.needsText ? ' data-needs-text' : ''} title="${esc(o.effect || o.consequence || '')}">${esc(o.label)}${o.needsText ? ' <small>(with your text)</small>' : ''}</button>`).join('') + (tk.real && !(tk.options || []).length ? '<span class="faint">Nothing to choose here: the panel offers no action for this item.</span>' : '')
    : tk.kind === 'approve' ? `<button type="button" class="btn sm primary" data-fpapprove="${esc(tk.step)}">Approve check</button>`
    : tk.kind === 'verify' ? `<button type="button" class="btn sm" data-fpverify="${esc(tk.step)}">Mark verified (your judgement)</button>`
    : tk.kind === 'hold' ? `<button type="button" class="btn sm" data-fprelease="${esc(j?.owner || '')}">Release hold</button>` : '';
  const ifs = tk.real ? [...(tk.options || []).map((o) => [o.label, o.effect || o.consequence]), tk.ifNone && ['Do nothing', tk.ifNone]].filter(Boolean) : [];
  const isAsk = !tk.real && tk.kind === 'ask';
  return `<div class="h3"><h3>Needs you</h3><span class="tag needs">${N.length}</span><span class="src">oldest first</span>${src}</div><div class="nq">` +
    `<div class="qlist" role="listbox" aria-label="Open items" data-keep-scroll="fvq">${N.map((x) => `<button type="button" class="qi k-${esc(x.kind)}${x.id === FV.q ? ' on' : ''}" data-q="${esc(x.id)}" role="option" aria-selected="${x.id === FV.q}"><span class="tt">${fvOrb(x.kind === 'hold' ? 'held' : 'needs')}${esc(x.title)}</span><span class="wt"><span>${esc(FV_TK[x.kind] || x.kind)}</span>${x.openedAt || x.at ? `·<span>${fvHM(fvNow() - (x.openedAt || x.at))}</span>` : ''}${x.step ? `·<span>${esc(M.stepOf(x.step)?.n || x.step)}</span>` : ''}</span></button>`).join('')}</div>` +
    `<article class="tk" aria-label="${esc(tk.title)}"><h4>${fvOrb(tk.kind === 'hold' ? 'held' : 'needs')}${esc(tk.title)}</h4><dl>` +
    `<dt>Who</dt><dd>${esc(who)}${tkS ? ` · <button type="button" class="lnk" data-sess="${esc(tkS.k)}">${esc(tk.sid)} on the timeline ↑</button>` : ''}</dd>` +
    (j ? `<dt>Where</dt><dd>${fvMref(M, j.ms)} ${esc(j.msTitle)} → ${fvJref(j, true)}</dd>` : '') +
    (tk.openedAt || tk.at ? `<dt>Waiting</dt><dd>${fvHM(fvNow() - (tk.openedAt || tk.at))} · since ${fvT(tk.openedAt || tk.at)}</dd>` : '') +
    `<dt>Reason</dt><dd>${esc(tk.reason || '')}</dd><dt>Decision</dt><dd>${esc(tk.decision || '')}</dd>` +
    (ifs.length ? `<dt>If you…</dt><dd><ul>${ifs.map(([o, c]) => `<li><b>${esc(o)}</b>: ${esc(c || '')}</li>`).join('')}</ul></dd>` : '') + `</dl>` +
    (acts ? `<div class="acts">${acts}</div>` : '') +
    `<div class="ib"><div class="ib-msg"><span class="ib-who">${fvOrb(tk.kind === 'hold' ? 'held' : 'needs')}${esc(who)}${tk.openedAt || tk.at ? ' · ' + fvT(tk.openedAt || tk.at) : ''}</span><p>${esc(tk.reason || tk.title)}</p>${tk.decision ? `<p><b>${esc(tk.decision)}</b></p>` : ''}</div>` +
    (isAsk ? `<div class="ib-in"><textarea data-fpansk="${esc(tk.step)}" rows="1" placeholder="Your answer to ${esc(who)}…" aria-label="Answer: ${esc(tk.title)}"></textarea><button type="button" class="btn sm primary" data-fpans="${esc(tk.step)}" data-qid="${esc(j?.qid || '')}" data-resume="1">Answer &amp; resume</button></div>`
      : tk.real ? (() => { const nt = (tk.options || []).some((o) => o.needsText);
        const th = fvThread(), H = fvOutOf(M.name, 'tk:' + tk.id).at(-1), openH = fvOpenAt(M.name, tk.id).length > 0, sep = openH && FV.tkSep?.[tk.id], full = fvOutFull(), off = !th || full || (openH && !sep);
        return `<div class="ib-in"><textarea data-reply="${esc(tk.id)}" rows="1" maxlength="4000" placeholder="${nt ? `Your answer (then an option above), or a message to the lead…` : `A message to the lead about this…`}" aria-label="${nt ? 'Answer or message' : 'Message'}: ${esc(tk.title)}">${esc(FV.drafts[tk.id] || '')}</textarea>` +
          `<button type="button" class="btn sm" data-send="${esc(tk.id)}"${off ? ' disabled aria-disabled="true"' : ''} title="${!th ? 'No manager thread runs this feature: nobody to send it to' : full ? esc(FV_FULL) : openH && !sep ? 'An earlier message about this item is not confirmed yet: Retry it, or choose to send a separate one' : `To ${esc(th.title)} (the manager thread) about this item. A message, not an answer or an approval.`}">${sep ? 'Send separate message' : 'Send as message'}</button></div>` +
          (openH ? `<div class="fvc-w fv-tksep" role="status">${sep ? `This sends a second, separate message. The earlier one may still arrive too, so the lead may get both. <button type="button" class="lnk" data-tksep="${esc(tk.id)}">Cancel</button>` : `An earlier message about this item is not confirmed yet and may still arrive, so Send is off. <button type="button" class="lnk" data-tksep="${esc(tk.id)}">Send a separate message…</button>`}</div>` : '') +
          (full ? `<div class="fvc-w" role="status">${esc(FV_FULL)}</div>` : '') +
          (H?.state ? `<div class="fv-tkmsg"><span class="ss6 ${esc(H.state)}" role="status">${FV_SS[H.state] || esc(H.state)}${H.why ? ': ' + esc(H.why) : ''}</span>${fvOpen(H) && (H.state === 'failed' || H.state === 'unknown') && H.sent ? ' <button type="button" class="lnk" data-tkretry="' + esc(tk.id) + '">Retry (same message)</button>' : ''}${H.state === 'held' && H.newThread && !H.maybe ? ` <button type="button" class="lnk" data-tkconfirm="${esc(tk.id)}">Send it to ${esc(H.newThread.title || H.newThread.thread)} instead</button>` : ''}</div>` : ''); })() : '') +
    (FV.tkErr?.[tk.id] ? `<div class="fp-warn" role="alert">${ic('alert-triangle')}<span>${esc(FV.tkErr[tk.id])}</span></div>` : '') +
    `<div class="ib-foot"><span>${isAsk ? 'Your answer goes to that step only, through the existing answer route' : tk.real ? `Only the options the panel offers act. A message goes to the lead and decides nothing.` : 'Act with the buttons above (the existing owner routes)'}</span>${tk.real ? `<details><summary>Route</summary><div>ticket <code>${esc(tk.id)}</code>${tk.rev ? ` @<code>${esc(tk.rev)}</code>` : ''} · <code>POST /api/term/feature/ticket/reply</code> · refused if the item changed or its agent was replaced</div></details>` : ''}</div></div></article></div>`;
}

// ---- Plan: Milestones | Objectives ----
function fvObjectives(M) { // objectives with ids, the steps that cover them and their proof state
  const o = M.pg.objectives || (M.d.dossier?.dod ? { items: M.d.dossier.dod.map((x) => ({ id: x.id, text: x.req, doneWhen: x.done, steps: x.covers })) } : null);
  return o;
}
const fvItName = (it) => it.line != null && !it.kind ? it.line : `${FV_IK[it.kind] || it.kind || ''} ${it.title || it.ref || ''}`.trim(); // every evidence entry has a name
const fvProof = (M, k) => M.evidence[k] || M.stepOf(k)?.proof || null;
// evidenced = a receipt (check) or your attestation. A model assessment, a claim or claimed items are never more than claimed.
const fvPState = (M, k) => { const j = M.stepOf(k), p = M.evidence[k]; if (!j) return 'none';
  if (p ? (p.check || p.attest) && !p.staleWhy && !p.check?.stale : j.state === 'done' && (j.receipt || j.label === 'owner verified')) return 'evidenced';
  if (j.state === 'claimed' || j.state === 'done' || p?.claim || p?.items?.length || p?.assessment) return 'claimed'; return 'none'; };
const fvOStatus = (M, o) => { if (o.status) return o.status; if (typeof o.met === 'boolean') { if (o.met) return 'met'; if (!(o.steps || []).length) return 'unknown'; return (o.steps || []).some((k) => fvPState(M, k) === 'evidenced') ? 'part' : 'unmet'; } const st = (o.steps || []).map((k) => fvPState(M, k)); if (!st.length) return 'unknown'; const n = st.filter((x) => x === 'evidenced').length; return n === st.length ? 'met' : n ? 'part' : 'unmet'; };
const FV_OS = { met: 'Met', part: 'Partly evidenced', unmet: 'Unmet', unknown: 'Not linked to jobs' };
function fvPlan(M) {
  const tabs = `<span class="seg sm" role="tablist" aria-label="Plan view">${[['ms', 'Milestones'], ['obj', 'Objectives']].map(([k, l]) => `<button type="button" role="tab" data-fvpt="${k}" aria-selected="${FV.ptab === k}" aria-pressed="${FV.ptab === k}">${l}</button>`).join('')}</span>`;
  const ed = (doc) => FP.edit === doc && FP.s === M.name;
  const editor = (doc) => `<textarea class="fp-ed" data-k="fped:${doc}" data-fped="${doc}" spellcheck="false" aria-label="${FP_FILES[doc]}">${esc(FP.draft)}</textarea>` +
    `<div class="fp-eda"><span class="faint">${esc(M.dz.dir ? tilde(M.dz.dir) : '')}/${FP_FILES[doc]} · every agent on this feature reads it</span><span class="spacer"></span><button type="button" class="btn sm ghost" data-fpcancel>Cancel</button><button type="button" class="btn sm primary" data-fpsave="${doc}">Save</button></div>`;
  if (FV.ptab === 'obj') {
    const O = fvObjectives(M);
    const body = ed('feature') ? editor('feature') : O?.items?.length ? fvObjList(M, O) : `${fvNT('No objectives with ids yet. Write them in FEATURE.md as <code>- [ ] D1: requirement — done when: …</code> and link jobs with <code>- covers: D1</code>.')}` +
      ((M.dz.outcomes || []).length ? `<ol class="dod">${M.dz.outcomes.map((x) => `<li><div class="r1"><span class="oid">D?</span><span class="rq">${esc(x)}</span><span class="st6 unknown">not linked to jobs</span></div></li>`).join('')}</ol>` : '');
    return `<div class="p-h"><h3>Plan</h3>${tabs}${O?.items?.length ? `<span class="src">${O.items.filter((o) => fvOStatus(M, o) === 'met').length}/${O.items.length} objectives met</span>` : ''}<span class="sp"></span>${ed('feature') ? '' : `<button type="button" class="btn sm ghost" data-fpedit="feature">Edit FEATURE.md</button>`}</div><div role="tabpanel">${body}${fpDraft(M.dz)}${fpAccept(M.dz)}</div>`;
  }
  if (ed('plan')) return `<div class="p-h"><h3>Plan</h3>${tabs}</div>${editor('plan')}`;
  const ms = M.dz.milestones || [];
  if (!M.d.dossier) return `<div class="p-h"><h3>Plan</h3>${tabs}</div>${fvNT('The plan is unknown right now: the panel sent no record of it (not the same as no plan). It is asked again shortly.')}`;
  if (!ms.length) return `<div class="p-h"><h3>Plan</h3>${tabs}<span class="sp"></span><button type="button" class="btn sm" data-fpedit="plan">Write PLAN.md</button></div>${fvNT('No PLAN.md yet: milestones as <code>## headings</code>, jobs as <code>- [ ] job @Tab</code>. The lead writes it; you can edit it; every session reads the same file.')}`;
  const ver = M.steps.filter((x) => x.state === 'done').length, cur = ms.findIndex((m) => m.done < m.steps.length);
  return `<div class="p-h"><h3>Plan</h3>${tabs}<span class="src">${ver}/${M.steps.length} jobs verified${(M.pg.arts || []).length ? ` · ${M.pg.arts.filter(fvArtOk).length} visual artifacts${M.pg.arts.some((a) => !fvArtOk(a)) ? ` (${M.pg.arts.filter((a) => !fvArtOk(a)).length} unavailable)` : ''}` : ''}</span><span class="sp"></span><button type="button" class="btn sm ghost" data-fvexp>${ms.every((m, i) => FV.open?.has('M' + (i + 1))) ? 'Collapse all' : 'Expand all'}</button><button type="button" class="btn sm ghost" data-fpedit="plan">Edit PLAN.md</button></div><div role="tabpanel">` +
    ms.map((m, i) => { const id = 'M' + (i + 1), xs = M.steps.filter((x) => x.ms === id), n = xs.filter((x) => x.state === 'done').length, open = FV.open?.has(id) || (i === cur && !FV.open?.has('!' + id));
      const working = (M.sessions || []).filter((s) => !s.to && M.stepOf(s.step)?.ms === id), arts = (M.pg.arts || []).filter((a) => fvArtMs(M, ms, a) === id);
      return `<details class="ms${i === cur ? ' cur' : ''}" data-ms="${id}" data-k="fvms:${id}"${open ? ' open' : ''}><summary><span class="car">▸</span><i class="mi ${fvMc(id)}" aria-hidden="true"></i><b>${id} ${esc(fvMsT(m.title, id))}</b>` +
        (working.length ? `<span class="working">${working.map((s) => `${fvOrb(fvSessState(M, s))}${esc(s.tab)}`).join(' ')}</span>` : '') +
        `${arts.length ? `<span class="src" title="${plural(arts.length, 'visual artifact')} linked to ${id}">▣ ${arts.length}</span>` : ''}<button type="button" class="btn ghost" data-fvmshi="${id}" aria-pressed="${FV.msHi === id}" title="Highlight ${id}'s sessions on the team timeline">on timeline ↑</button><span class="mbar" aria-hidden="true">${['done', 'claimed', 'doing', 'asking', 'todo'].map((k) => { const c = xs.filter((x) => x.state === k).length; return c ? `<span class="s-${k}" data-flex="${c}"></span>` : ''; }).join('')}</span><span class="fn">${n}/${xs.length}</span></summary>` +
        fvMso(M, id) + `<ol class="steps">${xs.map((x) => fvStep(M, x)).join('')}</ol>` +
        (arts.length ? `<div class="arts">${arts.map(fvArt).join('')}</div>` : '') + `</details>`; }).join('') + `</div>` +
    ((un) => un.length ? `<div class="arts-un"><span class="faint">Artifacts not placed: their job isn't in this plan, or their milestone title isn't (or names more than one milestone)</span><div class="arts">${un.map(fvArt).join('')}</div></div>` : '')((M.pg.arts || []).filter((a) => !fvArtMs(M, ms, a)));
}
// page.arts: a job's artifact sits in its job's milestone; a milestone's own in the one milestone with exactly that title
const fvArtMs = (M, ms, a) => { if (a.step) return M.stepOf(a.step)?.ms || null; const i = ms.findIndex((m) => m.title === a.milestone); return i >= 0 && ms.filter((m) => m.title === a.milestone).length === 1 ? 'M' + (i + 1) : null; };
const fvArtOk = (a) => !!a.url && (a.state === 'ok' || a.state === 'link');
function fvArt(a) { // usable: a link to the existing preview, or an https page (opened only when you click it); else inert, with why
  const pp = a.purpose === 'ui' ? ['ui', 'UI design'] : a.purpose === 'issue' ? ['issue', 'Issue explanation'] : ['other', a.purpose || 'no purpose given'];
  let host = ''; try { host = new URL(a.url).host; } catch { /* a preview path */ }
  const head = `<span class="pp ${pp[0]}">${esc(pp[1])}</span><span class="ti">${esc(a.title || '(untitled)')}</span>`;
  if (!fvArtOk(a)) return `<div class="art off" aria-disabled="true" data-fvart="${esc(a.id)}">${head}<span class="me">unavailable: ${esc(a.state === 'ok' ? 'the Files preview doesn\'t serve this file' : a.why || a.state || 'unknown')}</span></div>`;
  if (a.state === 'link') return `<a class="art" href="${esc(a.url)}" target="_blank" rel="noopener noreferrer" data-fvart="${esc(a.id)}">${head}<span class="me">link · opens ${esc(host)} · not fetched until you open it</span></a>`;
  return `<a class="art" href="${esc(a.url)}" target="_blank" rel="noopener" data-fvart="${esc(a.id)}">${head}<span class="me">${esc(a.kind || '')}${a.comments ? ' · ' + plural(a.comments, 'comment') : ''}</span></a>`;
}
// milestone colours: four base hues, then three more (validated for colour-blind separation); past 7, grey. Always beside the id
const fvMc = (id) => { const n = +String(id || '').replace(/\D/g, ''); return n >= 1 && n <= 7 ? 'mc' + n : 'mc0'; };
const fvMref = (M, id) => { if (!id) return ''; const m = (M.dz.milestones || [])[+id.slice(1) - 1]; return `<button type="button" class="ref ms ${fvMc(id)}" data-fvms="${esc(id)}" title="${esc(`${id} ${fvMsT(m?.title, id)}`)}">${esc(id)}</button>`; };
const fvMsT = (t, id) => String(t || '').replace(new RegExp('^' + id + '\\b[\\s:.-]*'), ''); // PLAN.md titles often start with their own id ("## M1 Diagnose stalls"): shown once
function fvMso(M, id) {
  const O = fvObjectives(M); if (!O?.items?.length) return '';
  const os = O.items.filter((o) => (o.steps || []).some((k) => M.stepOf(k)?.ms === id)); if (!os.length) return '';
  const mini = (k) => { const ps = fvPState(M, k); return `<button type="button" class="pm ${ps}" data-proof-go="${esc(M.stepOf(k)?.key || k)}">${ps === 'evidenced' ? 'evidenced' : ps === 'claimed' ? 'claimed' : 'no proof'}</button>`; };
  return `<details class="mso" data-mso="${id}"${FV.mso.has(id) ? ' open' : ''}><summary><span class="car">▸</span>Objectives this milestone covers <span class="faint">${os.map((o) => esc(o.id)).join(' · ')}</span></summary><ul>` +
    os.map((o) => { const mine = o.steps.filter((k) => M.stepOf(k)?.ms === id), other = o.steps.filter((k) => !mine.includes(k)), st = fvOStatus(M, o);
      return `<li><div><button type="button" class="lnk oid" data-obj-go="${esc(o.id)}">${esc(o.id)}</button> ${esc(o.text)} <span class="st6 ${st}">${FV_OS[st]}</span></div><div class="mso-j">${id} covers it with ${mine.map((k) => `${fvKref(M, k)} ${mini(k)}`).join(' · ')}${other.length ? `<span class="faint"> · it also needs ${other.map((k) => fvKref(M, k)).join(', ')} (other milestones)</span>` : ''}</div></li>`; }).join('') + `</ul></details>`;
}
function fvObjList(M, O) {
  const jrow = (k) => { const j = M.stepOf(k), p = fvProof(M, k), ps = fvPState(M, k);
    const ev = ps === 'evidenced' ? ((p?.items || []).map((it) => `<button type="button" class="lnk" data-proof-go="${esc(j.key)}">${esc(fvItName(it))}</button>`).join(' · ') || `<button type="button" class="lnk" data-proof-go="${esc(j.key)}">${esc(j.label || 'receipt')}</button>`)
      : ps === 'claimed' ? `<span class="miss">Claimed, no evidence.</span> <button type="button" class="lnk" data-proof-go="${esc(j?.key || k)}">what's missing</button>` : `<span class="miss">Missing:</span> <span class="faint">${esc(p?.required?.text || 'no proof named yet')}</span>`;
    return `<div>${fvKref(M, k)} <span class="faint ell">${esc(j?.text || 'not in PLAN.md')}</span><span><span class="st6 ${ps === 'evidenced' ? 'met' : ps === 'claimed' ? 'part' : 'unmet'}">${ps === 'evidenced' ? 'evidenced' : ps === 'claimed' ? 'claimed' : 'no proof'}</span></span><span>${ev}</span></div>`; };
  return `<div class="oos">From FEATURE.md <code>## Objective</code>${O.rev ? ' · ' + esc(O.rev) : ''} · an objective is met only when every job under it has evidence</div>` + (O.text || M.d.dossier?.objective ? `<p class="fv-goal">${esc(O.text || M.d.dossier.objective)}</p>` : '') +
    ((O.unknown || []).length ? `<div class="fp-warn" role="status">${ic('alert-triangle')}<span>Jobs say they cover ${O.unknown.map((x) => `<code>${esc(x)}</code>`).join(', ')}, which no objective has: that coverage counts for nothing.</span></div>` : '') +
    `<ol class="dod" aria-label="Objectives and definition of done">${O.items.map((o) => { const st = fvOStatus(M, o);
      return `<li data-obj="${esc(o.id)}" data-k="fvo:${esc(o.id)}" tabindex="-1"><div class="r1"><span class="oid">${esc(o.id)}</span><span class="rq">${esc(o.text)}</span><span class="st6 ${st}">${FV_OS[st]}</span></div>` +
        (o.doneWhen ? `<div class="dd"><b>Done when</b>${esc(o.doneWhen)}</div>` : '') + (o.proof ? `<div class="dd"><b>Proof you require</b>${esc(o.proof)} <span class="faint">(binds)</span></div>` : '') +
        ((o.missing || []).length && !o.met ? `<div class="dd miss-l"><b>Not met because</b><ul>${o.missing.map((m) => `<li>${esc(m)}</li>`).join('')}</ul></div>` : '') + `<div class="cov">${(o.steps || []).map(jrow).join('') || '<div class="faint">No job covers it yet (add <code>- covers: ' + esc(o.id) + '</code> under a job).</div>'}</div></li>`; }).join('')}</ol>` +
    (O.outOfScope ? `<div class="oos">Out of scope: ${esc(O.outOfScope)}</div>` : '');
}
function fvStep(M, x) {
  const p = fvProof(M, x.key), ps = fvPState(M, x.key), sess = (M.sessions || []).filter((s) => s.step === x.key || s.step === x.n);
  const badge = ps === 'evidenced' ? `<button type="button" class="vb r-check" data-fvvb="${esc(x.key)}" aria-expanded="${FV.vb === x.key}">proof · ${esc([p?.check && 'check', p?.assessment && (p.assessment.author ? 'assessed by author' : p.assessment.author === false ? 'assessed' : 'assessed (author unknown)'), (p?.attest || x.label === 'owner verified') && 'you attested'].filter(Boolean).join(' + ') || x.label || 'receipt')} ▾</button>`
    : ps === 'claimed' ? `<button type="button" class="vb r-claim" data-fvvb="${esc(x.key)}" aria-expanded="${FV.vb === x.key}">claimed · no evidence ▾</button>`
    : x.state !== 'done' && p?.required ? `<button type="button" class="vb r-need" data-fvvb="${esc(x.key)}" aria-expanded="${FV.vb === x.key}">proof needed ▾</button>` : '';
  const b = [x.blocked && '<i class="b bad">held</i>', x.missed ? `<i class="b bad">missed ${esc(x.due)}</i>` : x.due && x.state !== 'done' ? `<i class="b">due ${esc(x.due)}</i>` : '', x.state === 'asking' && '<i class="b warn">waits on you</i>'].filter(Boolean).join('');
  const tk = M.tickets.find((k) => k.step === x.key);
  const sub = [(x.after || []).length && `waits on ${x.after.map((k) => fvKref(M, k)).join(', ')}`,
    (x.afterInvalid || []).length && `<span class="warn" data-after-invalid>invalid prerequisite ${x.afterInvalid.map((d) => `<code>${esc(d.ref)}</code> (${esc(d.why)})`).join(', ')}: not ready until fixed</span>`,
    x.check && `check <code>${esc(x.check)}</code> ${x.approval?.check ? '<span class="ok">approved</span>' : '<span class="warn">not approved</span>'}`, x.state === 'claimed' && x.why && `<span class="warn">${esc(x.why)}</span>`, x.blocked && `<span class="bad">${esc(x.blocked)}</span>`,
    ...(M.d.prs || []).filter((pr) => pr.step === x.key).map((pr) => `<a href="${esc(pr.url)}" target="_blank" rel="noopener">#${pr.number} ${esc((pr.state || '').toLowerCase())}</a>`)].filter(Boolean);
  const kinds = ['read', 'build', 'test', 'check'];
  const acts = [kinds.includes(x.kind) && !x.approval?.kind && `<button type="button" class="btn sm ghost" data-fpsafe="${esc(x.key)}" title="If its agent stops mid-way, it may be resumed to redo this step">Approve as safe to redo</button>`].filter(Boolean);
  const resp = sess.length ? sess.map((s) => `<button type="button" class="${s.to ? '' : 'cur'}" data-sess="${esc(s.k)}" title="${esc(`${s.tab} · ${s.kind || ''} ${s.sid}\nShow on the timeline`)}">${fvOrb(fvSessState(M, s))}${esc(s.tab)}${s.sid ? ` <span class="faint">${esc(s.sid)}</span>` : ''}</button>`).join('') : x.owner ? `<em>${esc(x.owner)}</em>` : '<em class="none">no owner</em>';
  return `<li class="${esc(x.state)}" data-key="${esc(x.key)}" data-k="fvst:${esc(x.key)}" tabindex="-1"><span class="sd">${fvOrb(x.state === 'done' ? 'done' : x.blocked ? 'held' : x.state === 'asking' || x.state === 'claimed' ? 'needs' : x.state === 'doing' ? 'processing' : 'idle')}</span>` +
    `<span class="stx"><span><b>${esc(x.n)}</b> ${esc(x.text)}${badge}${b}${(M.pg.arts || []).filter((a) => a.step === x.key).map((a) => `<button type="button" class="artchip" data-fvartgo="${esc(a.id)}" title="${esc(a.title || '')}">▣ ${esc(a.purpose === 'ui' ? 'UI' : a.purpose || 'artifact')}</button>`).join('')}${tk ? ` <button type="button" class="tag needs" data-fvq="${esc(tk.id)}" aria-label="${esc('Needs you: ' + (tk.title || x.text))}">Needs you →</button>` : ''}</span>${sub.length ? `<small class="evd">${sub.join(' · ')}</small>` : ''}${acts.length ? `<span class="fp-acts">${acts.join('')}</span>` : ''}${fvProofBox(M, x)}</span><span class="resp">${resp}</span></li>`;
}
const FV_IK = { log: 'log', doc: 'doc', db: 'db', image: 'image', ui: 'UI', link: 'link', run: 'run', file: 'file' };
function fvProofBox(M, x) {
  if (FV.vb !== x.key) return '';
  const p = fvProof(M, x.key) || {}, ps = fvPState(M, x.key), O = fvObjectives(M), objs = (O?.items || []).filter((o) => (o.steps || []).includes(x.key) || (o.steps || []).includes(x.n)).map((o) => o.id);
  const a = p.assessment, req = p.required;
  const item = (it, i) => { const ext = /^https?:\/\//.test(it.url || it.ref || ''), body = it.line != null && !it.kind ? `<code>${esc(it.line)}</code>` // a line the panel can't resolve: the writer's words only
      : it.kind === 'db' && it.rows ? `<table><thead><tr>${(it.cols || []).map((c) => `<th>${esc(c)}</th>`).join('')}</tr></thead><tbody>${it.rows.map((r) => `<tr>${r.map((c) => `<td>${esc(c)}</td>`).join('')}</tr>`).join('')}</tbody></table>`
      : it.refused ? (it.refused === 'missing' ? `<code>${esc(tilde(it.ref || ''))}</code>` : '<span class="faint">(path not shown)</span>') // outside, secret or unreadable: never named
      : ext ? `<a href="${esc(it.url || it.ref)}" target="_blank" rel="noopener noreferrer">${esc(it.url || it.ref)}</a>` // a link, never fetched by the page
      : (it.kind === 'image' || it.kind === 'ui') && it.path && it.current?.sha256 ? `<a href="/api/fs/raw?path=${encodeURIComponent(it.path)}" target="_blank" rel="noopener"><img src="/api/fs/raw?path=${encodeURIComponent(it.path)}" alt="${esc(it.title || it.ref || '')}" loading="lazy"></a>`
      : it.body ? `<pre>${esc(it.body)}</pre>` : it.path ? `<code>${esc(tilde(it.path))}</code>` : it.ref ? `<code>${esc(tilde(it.ref))}</code>` : '';
    const sl = it.seal, cu = it.current, pv = it.provenance, mn = it.mention, sh = (x) => esc((x || '').slice(0, 10)) + '…';
    const rows = [
      `<div class="prov warn" data-pv="claim">claimed by its writer${it.refused ? '' : it.path ? ': the panel found the file; that says nothing about whether it is right' : ': the panel hasn\'t checked this ' + esc(FV_IK[it.kind] || it.kind || 'line')}</div>`,
      it.refused && `<div class="prov warn" data-pv="refused">${it.refused === 'missing' ? 'missing: not found in the feature\'s folders' : 'not read: ' + esc(it.refused)}</div>`,
      it.stale && `<div class="prov warn" data-pv="stale">stale: ${esc(it.staleWhy || 'changed since sealed')}</div>`,
      sl && `<div class="prov" data-pv="seal">sealed sha256 ${sh(sl.sha256)} · ${esc(sl.size)} B · first seen ${fvT(sl.firstSeen)}${sl.submittedBy ? ' · via ' + esc(sl.submittedBy) : ''} — integrity only: the same bytes since then, not that they are correct</div>`,
      it.path && !sl && `<div class="prov" data-pv="seal">not sealed: no record of these bytes when first submitted</div>`,
      cu && `<div class="prov" data-pv="current">${cu.sha256 ? `now sha256 ${sh(cu.sha256)} (${esc(cu.how)}${cu.checkedAt ? ' ' + fvT(cu.checkedAt) : ''})` : `bytes now not checked${cu.why ? ': ' + esc(cu.why) : ''}`}</div>`,
      pv ? `<div class="prov" data-pv="provenance">written by an agent's ${esc(pv.tool)} at ${esc(pv.at)}${pv.bytesMatch === true ? ' (same bytes as sealed)' : pv.bytesMatch === false ? ' (<b class="warn">different bytes than sealed</b>)' : ''} — where it came from, not that it is right</div>` : it.provenanceNone && `<div class="prov" data-pv="provenance">no recorded agent write of it after its criterion was set</div>`,
      mn && `<div class="prov" data-pv="mention">a ${esc(mn.tool || 'Bash')} command named it at ${esc(mn.at)}: a correlation only, not provenance or proof</div>`,
      !it.kind && it.line == null && it.ref == null && '<div class="prov">(an evidence entry with nothing to show)</div>'].filter(Boolean).join('');
    return `<figure class="pfi${it.against ? ' against' : ''}${it.stale || it.refused ? ' stale' : ''}" data-fvpfi="${i}"><figcaption><span class="k">${esc(FV_IK[it.kind] || it.kind || 'claim')}</span>${esc(it.title || '')}${it.against ? '<span class="tag fail">counter-evidence</span>' : ''}</figcaption>${body}${rows}${it.prov || it.sha256 || it.at ? `<div class="prov">${esc([it.prov, it.sha256 && 'sha256 ' + it.sha256.slice(0, 10) + '…', it.at && fvT(it.at)].filter(Boolean).join(' · '))}</div>` : ''}</figure>`; };
  const cr = p.criterion, mech = req?.mechanical;
  const crit = cr || mech?.length ? `<div class="pf-req${cr?.met === false ? ' warn' : ''}" data-fvcrit>${cr?.how === 'owner attested' ? 'Criterion: met by your attestation.' : cr?.how === 'needs your attestation' || (!cr && !mech?.length) ? 'Criterion: no part of it is checked mechanically; only your attestation meets it.'
      : `Criterion ${cr?.met === true ? 'met' : cr?.met === false ? '<b>not met</b>' : 'not decided'} by the mechanical check, which checks only: ${(mech || []).map(esc).join('; ')}. That is not proof the objective holds.${cr?.missing?.length ? ` Missing: ${cr.missing.map(esc).join('; ')}.` : ''}`}</div>` : '';
  const legacy = !p.items && (x.receipt || x.evidence || x.proof); // a step from before evidence entries: what the record has
  return `<div class="pf" role="region" aria-label="Proof for ${esc(x.n)}"><div class="pf-h"><b>Proof for ${esc(x.n)}</b>${objs.length ? `<span class="faint">serves</span>${objs.map((o) => `<button type="button" class="lnk" data-obj-go="${esc(o)}">${esc(o)}</button>`).join(' ')}` : ''}<span class="spacer"></span><span class="st6 ${ps === 'evidenced' ? 'met' : ps === 'claimed' ? 'part' : 'unmet'}">${ps === 'evidenced' ? 'evidence-backed' : ps === 'claimed' ? 'claimed · not evidenced' : 'not done · no evidence yet'}</span></div>` +
    (req ? `<div class="pf-req">Required proof · <b>${req.by === 'owner' ? 'named by you (binds)' : 'chosen by the agent (you named none)'}</b>: ${esc(req.text)}</div>` : '') +
    crit +
    (p.claim || p.staleWhy || (x.state === 'claimed' && x.why) ? `<div class="pf-req warn">${esc([...new Set([p.claim, p.staleWhy, x.state === 'claimed' && x.why].filter(Boolean))].join(' '))}</div>` : '') +
    (ps === 'none' && !legacy ? '' : `<div class="pf-res"><div><h6>Check result${p.check?.source ? ' · ' + esc(p.check.source) : ''}</h6>${p.check ? `<span>${esc(p.check.text)}</span><span class="faint">${fvT(p.check.at)}${p.check.rev ? ' · job rev ' + esc(p.check.rev) : ''}${p.check.stale ? ' · <b class="warn">stale</b>' : ''}</span>` : x.receipt ? `<span>receipt ${esc(x.receipt)}${x.evidence ? ' · ' + esc(x.evidence) : ''}</span>` : '<span class="none">None recorded</span>'}</div>` +
      `<div><h6>Model assessment</h6>${a ? `<span><b>${esc(a.verdict)}</b> · ${esc(a.why || '')}</span><span class="faint">by ${esc(a.by || 'unknown')} · <b class="${a.author ? 'warn' : ''}">${a.author === true ? 'the author assessed its own work' : a.author === false ? 'another session' : 'author unknown: independence not claimed' + (a.claimedSid ? ` (claimed ${esc(a.claimedSid)})` : '')}</b>${a.cites?.length ? ' · cites ' + a.cites.map((c) => '#' + (c + 1)).join(', ') : ''}</span>` : '<span class="none">None</span>'}</div>` +
      `<div><h6>Owner attestation</h6>${p.attest ? `<span>${esc(p.attest.text)}</span><span class="faint">${fvT(p.attest.at)}${p.attest.rev ? ' · rev ' + esc(p.attest.rev) : ''}</span>` : x.label === 'owner verified' ? `<span>${esc(x.proof || 'You marked it verified')}</span>` : '<span class="none">None</span>'}</div></div>`) +
    ((p.items || []).length ? `<div class="pf-items">${p.items.map(item).join('')}</div>` : `<div class="pf-req">${ps === 'evidenced' ? 'The record has a receipt or your attestation for it, but no evidence items to show.' : 'No evidence recorded yet. It stays unmet until the proof above exists.'}</div>`) +
    `<div class="pf-fx">Provenance shows where evidence came from, not that the objective holds.</div></div>`;
}

// ---- Code delivery: release strip (deployed ≠ branch HEAD), branch graph, PRs ----
function fvCode(M) {
  const src = (M.d.prs || []).length ? `<span class="src">PRs: GitHub, as the panel last fetched them</span>` : '';
  return `<div class="h3"><h3>Code delivery</h3>${src}</div>${fvRelease(M)}${fvGraph(M)}${fvPRs(M)}`;
}
function fvRelease(M) {
  const R = M.pg.releases;
  if (R && !R.length) return `<div class="relstrip none" data-k="fvrel">${fvNT('No repository with a deploy source is linked to this feature.')}</div>`;
  if (!R && M.pg.codeReading) return `<div class="relstrip none" data-k="fvrel">${fvNT('Reading deploys from GitHub…')}</div>`;
  if (!R) return `<div class="relstrip none" data-k="fvrel">${fvNT('Release versions are not tracked yet: no deploy source is configured (FEATURE.md <code>deploy:</code>). Merged is never shown as deployed.')}</div>`;
  const cell = (r, role, f) => { const id = `rel:${r.repo}:${role}`;
    if (!f) return `<div class="relc none" data-relc="${esc(id)}" tabindex="0"><span class="rvv">—</span><small>${esc(r.why || (role === 'prep' ? 'no release named' : 'not deployed'))}</small></div>`;
    if (role === 'br' && !f.name) return `<div class="relc warn" data-relc="${esc(id)}" tabindex="0"><span class="rvv">unknown</span><small>${esc(f.note || 'no worktree of this repo')}</small></div>`;
    if (role !== 'br' && (f.state === 'unknown' || f.unknown)) return `<div class="relc warn" data-relc="${esc(id)}" tabindex="0"><span class="rvv">unknown</span><small>${esc(f.why || 'no source')}${f.version ? ` · last known ${esc(f.version)}${f.at ? ' at ' + fvT(f.at) : ''}` : ''}</small></div>`;
    if (role === 'prep' && !f.version) return `<div class="relc none" data-relc="${esc(id)}" tabindex="0"><span class="rvv">—</span><small>${esc(f.why || f.note || 'no release named')}</small></div>`;
    if (role === 'prep') return `<div class="relc" data-relc="${esc(id)}" tabindex="0"><span class="rvv">${esc(f.version)}</span><small>${esc(f.note || '')}</small></div>`;
    if (role === 'br') return `<div class="relc" data-relc="${esc(id)}" tabindex="0"><span class="rvv"><code>${esc(f.name)}</code></span><small>HEAD <code>${esc(f.head || '?')}</code> · ${f.deployed === true ? 'deployed' + (f.deployedIn ? ' in ' + esc(f.deployedIn) : '') : f.deployed === false ? 'HEAD is not deployed' : 'deployed: unknown'}${f.note ? ' · ' + esc(f.note) : ''}</small></div>`;
    const old = f.state === 'stale' || (f.fetchedAt && fvNow() - f.fetchedAt > 3600);
    return `<div class="relc${old ? ' warn' : ''}" data-relc="${esc(id)}" tabindex="0"><span class="rvv">${esc(f.version)} <code>${esc(f.sha || '')}</code></span><small>${f.state === 'stale' ? 'last known · ' : ''}deployed ${fvT(f.at)} · ${esc(f.source || '?')} · ${f.fetchedAt ? 'fetched ' + ago(f.fetchedAt) : 'fetch time unknown'}</small>${f.prs?.length ? `<small class="n">has ${f.prs.map((n) => '#' + n).join(', ')}</small>` : ''}${f.attempt ? `<small class="n warn">newer deploy ${f.attempt.state === 'fail' ? 'failed' : 'running'} <code>${esc(f.attempt.sha || '')}</code></small>` : ''}</div>`; };
  return `<div class="relstrip" role="table" aria-label="Release status per repository" data-k="fvrel"><div class="relh" role="row"><span>Repo</span><span>Production · deployed</span><span>Stage · deployed</span><span>Preparing</span><span>Feature branch</span></div>` +
    R.map((r) => `<div class="relr" role="row"><span class="relrepo">${esc(r.repo)}${r.primary ? ' <span class="tag open">primary</span>' : ''}</span>${cell(r, 'prod', r.prod)}${cell(r, 'stage', r.stage)}${cell(r, 'prep', r.preparing)}${cell(r, 'br', r.branch)}</div>`).join('') + `</div>`;
}
function fvGraph(M) {
  const B = M.pg.branches;
  const tr = (M.d.trees || []).slice().sort((a, b) => b.main - a.main), trees = () => (tr.length ? `<div class="fv-trees">${tr.map((w) => `<div class="fp-row fp-tree${w.main ? ' main' : ''}" data-fvtree="${esc(w.root)}"><span class="brc">${IC.branch}${esc(w.branch || 'detached')}</span>${w.main ? '<span class="fp-st merged">main worktree</span>' : w.otherRepo ? '<span class="fp-st closed">another repo</span>' : w.checked ? (w.ahead ? `<span class="fp-st warn">${w.ahead} not in main worktree</span>` : '<span class="fp-st merged">in main worktree</span>') : '<span class="fp-st closed" title="the panel hasn\'t measured this tree against the main worktree yet: not 0, unknown">not measured yet</span>'}<span class="mono faint fp-path">${esc(tilde(w.root))}</span><em>${esc((w.agents || []).join(', '))}</em></div>`).join('')}</div>` : ''); // the worktrees the panel sees: shown whatever the branch list says
  if (B && !B.length) return fvNT('No feature branches found for this feature.') + trees(); // [] = none found, not unknown: the trees still show
  if (!B && M.pg.codeReading) return fvNT('Reading branches from git…') + trees();
  if (!B) return trees() +
      fvNT('Branch ancestry (which branch each came from, where it merged) is not tracked yet: worktrees are listed as the panel sees them.');
  for (const b of B) b.commits?.sort((a, c) => (a.at || 0) - (c.at || 0)); // oldest first (git log serves newest first)
  const now = fvNow(), t0 = Math.min(...B.map((b) => b.from || (b.commits?.[0]?.at) || now)), X = (t) => ((Math.max(t0, Math.min(now, t)) - t0) / (now - t0 || 1)) * 100;
  const repos = [...new Set(B.map((b) => b.repo))], bo = branchOrder(B), dep = new Map(bo.map((x) => [x.b.id, x.depth])); // a parent with no row: a root (lib.js)
  const depth = (b) => dep.get(b.id) || 0;
  const prOf = (n) => (M.d.prs || []).find((p) => p.number === n);
  const rows = repos.map((r) => { const bs = bo.filter((x) => x.b.repo === r).map((x) => x.b);
    return `<div class="grow repo"><span class="rh">${esc(r)}</span></div>` + bs.map((b) => { const p = b.pr && prOf(b.pr), par = b.parent && B.find((x) => x.id === b.parent);
      const right = p ? `<a class="tag ${esc((p.state || '').toLowerCase())}" href="${esc(p.url)}" target="_blank" rel="noopener">#${p.number} ${p.state === 'MERGED' ? 'merged' : p.state === 'CLOSED' ? 'closed' : p.draft ? 'draft' : 'open'}</a>${fvCi(p)}` : !b.parent ? `<span class="faint">${esc(b.commits?.slice(-1)[0]?.sha || '')}</span>` : (b.commits || []).some((c) => c.kind === 'unpushed') ? `<span class="tag unknown">${b.commits.filter((c) => c.kind === 'unpushed').length} unpushed · no PR</span>` : '';
      return `<div class="grow" data-row="${esc(b.id)}" data-k="gr:${esc(b.id)}"><button type="button" class="bl" data-fvbr="${esc(b.id)}" data-depth="${depth(b)}"><b><i class="bsw" data-bc="${esc(b.color || (b.parent ? '' : '#8a8a8a'))}"></i><span title="${esc(b.name)}">${esc(branchLabel(b.name, par?.name))}</span></b><small>${par ? `from ${esc(par.name)} @ ${esc(b.src || '?')}` : 'trunk'}${b.by ? ' · ' + esc(b.by) : ''}</small></button>` +
        `<div class="gtrk" data-trk="${esc(b.id)}">${(b.commits || []).map((c) => `<button type="button" class="cm${c.kind === 'merge' ? ' merge' : c.kind === 'unpushed' ? ' unpushed' : ''}" data-fvl="${X(c.at)}" title="${esc(`${b.name} @ ${c.sha} · ${fvT(c.at)}${c.msg ? '\n' + c.msg : ''}${c.kind === 'unpushed' ? '\nnot pushed' : ''}`)}" aria-label="${esc('commit ' + c.sha + ' on ' + b.name)}"></button>`).join('')}</div><div class="st2">${right}</div></div>`; }).join(''); }).join('');
  const sel = FV.br && B.find((b) => b.id === FV.br);
  return `<div class="gwrap"><div class="graph" id="graph">${rows}<svg class="gsvg" id="fv-gsvg" aria-hidden="true"></svg></div></div>` +
    `<div class="key"><span>○ commit · ● merge commit</span><span>line down from a parent = checked out from that commit</span><span>line up with arrow = merged into its destination</span><span>dotted tail = still open</span><span>✕ = PR closed without merging</span><span class="warn">dashed amber = not pushed</span><span>hover a branch to trace its lineage · click for its checkout source</span></div>` +
    (sel ? `<div class="detail" data-k="fvbrd"><h5><code>${esc(sel.name)}</code> <span class="faint">${esc(sel.repo)}</span><button type="button" class="btn sm ghost x" data-fvunbr>Close</button></h5><div class="row"><span>Checked out from</span><span>${sel.parent ? `${esc(B.find((x) => x.id === sel.parent)?.name || sel.parent)} @ <code>${esc(sel.src || '?')}</code>` : 'trunk'}</span></div>` +
      (sel.wt ? `<div class="row"><span>Worktree</span><span><code>${esc(tilde(sel.wt))}</code>${sel.by ? ' · by ' + esc(sel.by) : ''}</span></div>` : '') +
      `<div class="row"><span>Goes into</span><span>${sel.into ? `${esc(B.find((x) => x.id === sel.into)?.name || sel.into)} · ${esc(sel.intoState || '')}` : 'nowhere yet'}</span></div><div class="row"><span>Commits</span><ol>${(sel.commits || []).map((c) => `<li><code>${esc(c.sha)}</code> ${fvT(c.at)}${c.msg ? ' · ' + esc(c.msg) : ''}${c.kind === 'unpushed' ? ' <span class="tag unknown">not pushed</span>' : ''}</li>`).join('')}</ol></div></div>` : '');
}
const fvCiState = (p) => { const c = p.checks || []; if (!c.length) return null; if (c.some((x) => /fail|error|cancel/i.test(x.conclusion || x.state || ''))) return 'fail'; if (c.some((x) => /pend|progress|queued|running/i.test(x.status || x.state || ''))) return 'running'; return 'pass'; };
const fvCi = (p) => { const s = fvCiState(p); return s ? `<span class="tag ${{ pass: 'ok', fail: 'fail', running: 'run' }[s]}">CI ${s === 'pass' ? 'passed' : s === 'fail' ? 'failing' : 'running'}</span>` : '<span class="tag idle">no CI</span>'; };
// Shipped by: the session the panel linked to the PR (its tab, kind, id and job), or — when none is linked
function fvShipped(M, p) { const s = p.session && (M.sessions || []).find((x) => x.sid === p.session || x.k === p.session), j = M.stepOf(s?.step || p.step);
  if (!s && !p.session) return '<td><span class="faint">—</span></td>';
  return `<td>${s ? `${fvOrb(fvSessState(M, s))}${esc(s.tab)}` : `<span class="faint" title="a session the panel has no record of">${esc(p.session)}</span>`}<span class="t2">${esc([s?.kind, s?.sid || p.session].filter(Boolean).join(' '))}${j ? ` · ${fvMref(M, j.ms)} ${fvJref(j)}` : ''}</span></td>`; }
// Deployed: only from the PR's deploy records (CI passing is not a deploy)
function fvDeployed(p) { const d = (p.deploys || []).filter((x) => x.state === 'live'), st = p.state === 'MERGED';
  return `<td class="hide-m">${d.length ? d.map((x) => `<span class="tag stage">on ${esc(x.env)}</span>`).join(' ') : st ? '<span class="tag idle">not deployed</span>' : '<span class="faint">—</span>'}</td>`; }
function fvPRs(M) {
  const P = M.d.prs || [];
  if (!P.length) return `<div class="prbar"><h4>Pull requests</h4></div>${fvNT('No pull requests linked to this feature yet.')}`;
  const f = FV.prf, st = (p) => (p.state === 'MERGED' ? 'merged' : p.state === 'CLOSED' ? 'closed' : 'open'), repos = [...new Set(P.map((p) => p.repo).filter(Boolean))];
  const list = P.filter((p) => (f.repo === 'all' || p.repo === f.repo) && (f.state === 'all' || st(p) === f.state) && (!f.failing || fvCiState(p) === 'fail'));
  const cnt = (k) => P.filter((p) => (f.repo === 'all' || p.repo === f.repo) && (k === 'all' || st(p) === k)).length;
  return `<div class="prbar"><h4>Pull requests</h4><span class="faint">${list.length} of ${P.length}</span><span class="spacer"></span>${repos.length > 1 ? `<label>Repo <select data-prf aria-label="Repository"><option value="all">All repos</option>${repos.map((r) => `<option value="${esc(r)}"${f.repo === r ? ' selected' : ''}>${esc(r)}</option>`).join('')}</select></label>` : ''}` +
    `<span class="seg sm" role="group" aria-label="PR state">${[['all', 'All'], ['merged', 'Merged'], ['open', 'Open'], ['closed', 'Closed, not merged']].map(([k, l]) => `<button type="button" data-prs="${k}" aria-pressed="${f.state === k}">${l}<span>${cnt(k)}</span></button>`).join('')}</span>` +
    `<label class="fv-chk"><input type="checkbox" data-prfail${f.failing ? ' checked' : ''}> Failing CI only</label></div>` +
    `<div class="prscroll" data-keep-scroll="fvpr"><table class="prs"><thead><tr><th>PR</th><th>Change</th><th>Shipped by</th><th>State</th><th>CI</th><th class="hide-m">Deployed</th><th></th></tr></thead><tbody>${list.map((p) => { const s2 = fvCiState(p), open = FV.pr === p.number;
      return `<tr class="pr${s2 === 'fail' && st(p) === 'open' ? ' bad' : ''}${open ? ' sel' : ''}" data-open="${p.number}" data-k="pr:${p.number}"><td class="num"><a href="${esc(p.url)}" target="_blank" rel="noopener">#${p.number}</a><span class="t2">${esc(p.repo || '')}</span></td><td>${esc(p.title)}<span class="t2"><code>${esc(p.head || '')}</code> → <code>${esc(p.base || '')}</code></span></td>` + fvShipped(M, p) +
        `<td><span class="tag ${st(p) === 'merged' ? 'merged' : st(p) === 'closed' ? 'closed' : p.draft ? 'draft' : 'open'}">${st(p) === 'closed' ? 'closed, not merged' : p.draft && st(p) === 'open' ? 'draft' : st(p)}</span><span class="t2">${p.mergedAt ? 'merged ' + ago(Date.parse(p.mergedAt) / 1000) : p.closedAt ? 'closed ' + ago(Date.parse(p.closedAt) / 1000) : p.createdAt ? 'opened ' + ago(Date.parse(p.createdAt) / 1000) : ''}</span></td><td>${fvCi(p)}</td>${fvDeployed(p)}<td><button type="button" class="chev" data-fvdrill="${p.number}" aria-expanded="${open}" aria-label="Details for #${p.number}">${open ? '▴' : '▾'}</button></td></tr>` +
        (open ? `<tr class="drill"><td colspan="7"><div class="drillg"><div><h6>Checks</h6><ul>${(p.checks || []).map((c) => `<li>${esc(c.name || '')}: ${esc(c.conclusion || c.status || c.state || '')}${c.url ? ` · <a href="${esc(c.url)}" target="_blank" rel="noopener">log</a>` : ''}</li>`).join('') || '<li>none</li>'}</ul></div><div><h6>Review</h6><p>${esc(p.review || '—')}</p></div><div><h6>Deploys</h6><p>${esc((p.deploys || []).map((x) => x.env || x).join(', ') || 'none recorded')}</p></div></div></td></tr>` : ''); }).join('')}</tbody></table></div>`;
}

// ---- Work log: authored LOG.md lines, as a server-like log ----
const FV_LK = /^\s*(?:[-*]\s*)?(?:(\d{4}-\d\d-\d\d[ T]\d\d:\d\d)\s*)?(?:\[?([^\]:]*?)\]?\s+)?(DECISION|ACTION|EVIDENCE|BLOCKER|RESEARCH)\b\s*[:—-]?\s*(.*)$/;
function fvLogEntries(M) {
  if (Array.isArray(M.pg.log)) return M.pg.log;
  return (M.dz.log || '').split('\n').map((l, i) => { const m = FV_LK.exec(l); if (!m) return null; return { id: 'logline:' + i, at: m[1] ? Date.parse(m[1].replace(' ', 'T')) / 1000 : 0, who: (m[2] || '').trim() || '—', kind: m[3], title: m[4], raw: true }; }).filter(Boolean).reverse();
}
function fvLog(M) {
  const L = fvLogEntries(M), who = ['all', ...new Set(L.map((l) => l.who))], shown = L.filter((l) => FV.wl === 'all' || l.who === FV.wl).sort((a, b) => (b.at || 0) - (a.at || 0));
  // a raw LOG.md line (no event id) is shown but has no read identity: it is never receipted or counted as unseen
  const seen = new Set(M.pg.seen?.ids || []), fr = M.pg.seen?.frontier, raw = L.some((l) => !fvRid(l));
  const isSeen = (l) => !fvRid(l) || (fr != null && l.seq <= fr) || seen.has(l.id) || FV.acked.has(l.id);
  const sv = M.pg.supervisor ? `<span class="faint">supervisor: last sample ${M.pg.supervisor.lastSample ? fvT(M.pg.supervisor.lastSample) : 'unknown'} · next check ${esc(M.pg.supervisor.next || 'unknown')}</span>` : '<span class="faint">supervisor: not reported</span>';
  const line = (l) => { const b = [];
    for (const c of l.considered || []) b.push(['considered', `${c.option}: ${c.tradeoff || ''}`]);
    if (l.chose) b.push(['chose', `${l.chose.option}${l.chose.why ? ': ' + l.chose.why : ''}`]);
    for (const r of l.ran || []) b.push(['ran', `${r.ok === true ? '✓' : r.ok === false ? '✗' : '?'} ${r.cmd}${r.result ? ' → ' + r.result : ''}`, r.ok === false, 'the writer reports it ran this and how it went: not run or checked by the panel']);
    for (const u of l.ranUnparsed || []) b.push(['ran?', u, false, 'not in the ok=,cmd=,result= form: shown as written']);
    if (l.next) b.push(['next', l.next]);
    const links = [l.stale && `<span class="tag warn" data-stale title="${esc(`Written against plan ${l.rev || '?'}; the plan is ${M.dz.rev || '?'} now`)}">plan changed since</span>`,
      l.claimedRev && l.claimedRev !== l.rev && `<span class="faint" title="the writer's own word, not checked">says rev ${esc(l.claimedRev)}</span>`,
      l.step && fvKref(M, l.step), !l.step && l.stepClaimed && `<span class="faint" title="names no step of this plan">claims step ${esc(l.stepClaimed)}</span>`, ...(l.objectives || []).map((o) => `<button type="button" class="lnk" data-obj-go="${esc(o)}">${esc(o)}</button>`), l.proofStep && `<button type="button" class="lnk" data-proof-go="${esc(l.proofStep)}">proof</button>`, !l.proofStep && l.proofStepClaimed && `<span class="faint" title="names no step of this plan">claims proof for ${esc(l.proofStepClaimed)}</span>`,
      ...(l.objectivesClaimed || []).map((o) => `<span class="faint" title="FEATURE.md has no objective with this id">claims ${esc(o)}</span>`),
      l.pr && (l.prKnown === false ? `<span class="faint" title="not one of this feature's PRs">claims #${esc(l.pr)}</span>` : `<span>#${esc(l.pr)}</span>`)].filter(Boolean).join(' ');
    const rs = FV.reviewed[l.id] || (M.pg.reviewed && l.id in M.pg.reviewed ? (M.pg.reviewed[l.id] === (l.rev || '') ? 'ok' : 'changed') : null);
    const rv = l.review ? (rs === 'ok' ? '<span class="lg-rv done">✓ reviewed</span>' : rs === 'warn' ? `<span class="lg-rv warn" title="${esc(FV.revWarn?.[l.id] || '')}">reviewed · not fully recorded: ${esc(FV.revWarn?.[l.id] || '')}</span>` : rs === 'changed' ? '<span class="lg-rv warn">changed since: look again</span>' : `<button type="button" class="lnk lg-rv" data-review="${esc(l.id)}" data-rev="${esc(l.rev || '')}">mark reviewed</button>`) : '';
    return `<li class="lg${isSeen(l) ? '' : ' unseen'}" data-log="${esc(l.id)}" data-seen="${isSeen(l) || FV.sent.has(l.id) ? '' : esc(l.id)}"${FV.seenBad.has(l.id) ? ` data-seen-refused="${esc(FV.seenBad.get(l.id))}"` : ''} data-k="lg:${esc(l.id)}" tabindex="0"><div class="lg-h"><time>${l.at ? fvT(l.at) : '—'}</time><span class="lg-who">${esc(l.who)}${l.session ? ` <span class="faint"${l.sessionClaimed ? ' title="the session the writer named (X-Agent-Sid): its word, not checked"' : ''}>${esc(l.session)}${l.sessionClaimed ? ' (claimed)' : ''}</span>` : ''}</span><span class="lg-k ${esc(String(l.kind).toLowerCase())}">${esc(l.kind)}</span><span class="lg-t">${esc(l.title)}</span><span class="lg-l">${links}</span>${rv}</div>` +
      (FV.wlc || !b.length ? '' : `<ul class="lg-b">${b.map(([k, t, bad, tip]) => `<li${tip ? ` title="${esc(tip)}"` : ''}><span class="bk${bad ? ' bad' : k === 'chose' ? ' ch' : ''}">${esc(k)}</span><span class="bt">${esc(t)}</span></li>`).join('')}${(l.ran || []).length || (l.ranUnparsed || []).length ? '<li class="faint">ran: as reported by the writer, not executed or verified by the panel</li>' : ''}</ul>`) + `</li>`; };
  const un = L.filter((l) => !isSeen(l)).length, bad = L.filter((l) => FV.seenBad.has(l.id)).length, pend = L.filter((l) => !isSeen(l) && FV.sent.has(l.id) && !FV.seenBad.has(l.id)).length;
  return `<div class="h3"><h3>Work log</h3><span class="src">decisions, actions and evidence each agent wrote in LOG.md · public summaries, not private reasoning</span><span class="sp"></span>` +
    `<span class="src" title="Seen: on your screen for at least 1 s while this tab was visible and focused. Opening the page marks nothing. Reviewed: you clicked mark reviewed.${raw ? ' Lines read straight from LOG.md have no event id, so they are not tracked as seen.' : ''}">${un} unseen${pend ? ` · ${pend} not saved yet${FV.seenPause ? ' (sign in again)' : FV.seenOff ? ' (this panel does not record seen yet)' : ''}` : ''}${bad ? ` · ${bad} refused by the panel` : ''}${FV.seenWarn ? ` · <span class="warn" title="${esc(FV.seenWarn)}">seen saved, with a warning</span>` : ''}</span><button type="button" class="btn sm ghost" data-fvwlc>${FV.wlc ? 'Show reasons' : 'Headlines only'}</button></div>` +
    `<div class="lg-bar"><span class="seg sm" role="group" aria-label="Filter by team member">${who.map((w) => `<button type="button" data-wl="${esc(w)}" aria-pressed="${FV.wl === w}">${w === 'all' ? 'Everyone' : esc(w)}</button>`).join('')}</span>${sv}</div>` +
    (L.length ? `<ol class="lg-list" data-keep-scroll="fvlog" aria-label="Work log, newest first">${shown.map(line).join('')}</ol>` : fvNT('Nothing logged yet. Agents add typed lines to LOG.md: <code>DECISION: … | considered: … | chose: …</code>, <code>ACTION</code>, <code>EVIDENCE</code>, <code>BLOCKER</code>, <code>RESEARCH</code>.'));
}

// ---- after each render: positions, relation links, branch graph, seen tracking ----
function featPageV8After(el) {
  const root = $('.fv', el); if (!root) return;
  for (const n of $$('[data-fvl]', root)) { n.style.left = n.dataset.fvl + '%'; if (n.dataset.fvw) n.style.width = `max(8px, calc(${n.dataset.fvw}% - 3px))`; }
  for (const n of $$('[data-fvx]', root)) { n.style.left = n.dataset.fvx; if (n.dataset.fvy) n.style.top = n.dataset.fvy + 'px'; if (n.dataset.fva) n.style.transform = `translate(-${n.dataset.fva}%, -125%)`; }
  for (const n of $$('[data-depth]', root)) n.style.setProperty('--depth', n.dataset.depth);
  for (const n of $$('[data-flex]', root)) n.style.flex = n.dataset.flex;
  fvDrawRels(); fvDrawGraph(); fvObserve(root);
  if (FV.sel) fvHi(FV.sel);
  if (FV.comp) fvPlaceChat(); // a page render never rebuilds the chat (typing, focus and caret stay)
}
function fvDrawRels() {
  const M = FV.M, svg = $('#fv-deps'), box = $('#fv-lanes'); if (!svg || !box || !M) return;
  const b0 = box.getBoundingClientRect(), trk = $('.lrow:not(.ax) .trk', box); if (!trk) { svg.innerHTML = ''; return; }
  const tb = trk.getBoundingClientRect();
  Object.assign(svg.style, { left: tb.left - b0.left + 'px', width: tb.width + 'px', height: b0.height + 'px' });
  const rp = (P) => { let d = `M${P[0][0]},${P[0][1]}`; for (let i = 1; i < P.length - 1; i++) { const [x0, y0] = P[i - 1], [x, y] = P[i], [x2, y2] = P[i + 1], r = Math.min(6, (Math.abs(x - x0) || Math.abs(y - y0)) / 2, (Math.abs(x2 - x) || Math.abs(y2 - y)) / 2); d += ` L${x - Math.sign(x - x0) * r},${y - Math.sign(y - y0) * r} Q${x},${y} ${x + Math.sign(x2 - x) * r},${y + Math.sign(y2 - y) * r}`; } const L = P[P.length - 1]; return d + ` L${L[0]},${L[1]}`; };
  svg.innerHTML = `<defs>${['handoff', 'blocks', 'pair', 'resume'].map((k) => `<marker id="fva-${k}" viewBox="0 0 10 10" refX="9" refY="5" markerUnits="userSpaceOnUse" markerWidth="10" markerHeight="10" orient="auto"><path d="M0,0 L10,5 L0,10 z" class="mk-${k}"/></marker>`).join('')}</defs>` +
    M.rels.map((r) => { const eu = $(`.ss[data-sess="${CSS.escape(r.from)}"]`, box), ev = $(`.ss[data-sess="${CSS.escape(r.to)}"]`, box); if (!eu || !ev) return '';
      const ru = eu.getBoundingClientRect(), rv = ev.getBoundingClientRect(), x1 = ru.right - tb.left, y1 = ru.top + ru.height / 2 - b0.top, x2 = rv.left - tb.left, y2 = rv.top + rv.height / 2 - b0.top; let P, lx, ly;
      if (r.kind === 'pair') { const x = Math.max(ru.left, rv.left) + 14 - tb.left, dn = rv.top > ru.top; P = [[x, (dn ? ru.bottom : ru.top) - b0.top], [x, (dn ? rv.top : rv.bottom) - b0.top]]; lx = x + 6; ly = (P[0][1] + P[1][1]) / 2; }
      else if (Math.abs(y1 - y2) < 2) { P = [[x1, y1], [Math.max(x2, x1 + 8), y2]]; lx = (x1 + x2) / 2; ly = y1 - 12; }
      else if (x2 - x1 >= 20) { P = [[x1, y1], [x1 + 10, y1], [x1 + 10, y2], [x2, y2]]; lx = x1 + 16; ly = (y1 + y2) / 2; }
      else { const gy = y2 + (y1 < y2 ? -1 : 1) * (rv.height / 2 + 6); P = [[x1, y1], [x1 + 8, y1], [x1 + 8, gy], [x2 - 10, gy], [x2 - 10, y2], [x2, y2]]; lx = Math.min(x1, x2) + 4; ly = gy; }
      const t = FV_REL[r.kind] || r.kind, w = t.length * 5.6 + 12;
      return `<path class="${esc(r.kind)}${FV.all ? ' all' : ''}" data-rel="${esc(r.id)}" d="${rp(P)}" marker-end="url(#fva-${esc(r.kind)})"/><g class="lbl" data-rel-t="${esc(r.id)}"><rect x="${lx}" y="${ly - 8}" width="${w}" height="16" rx="8"/><text x="${lx + 6}" y="${ly + 3.5}">${esc(t)}</text></g>`; }).join('');
}
function fvHi(id) {
  const box = $('#fv-lanes'); if (!box) return;
  for (const e of $$('.ss.rel, .ss.fx', box)) e.classList.remove('rel', 'fx');
  for (const p of $$('#fv-deps .on', box)) p.classList.remove('on');
  box.classList.toggle('focus', !!id); if (!id) return;
  $(`.ss[data-sess="${CSS.escape(id)}"]`, box)?.classList.add('fx');
  for (const r of FV.M?.rels || []) if (r.from === id || r.to === id) { $(`[data-rel="${CSS.escape(r.id)}"]`, box)?.classList.add('on'); $(`[data-rel-t="${CSS.escape(r.id)}"]`, box)?.classList.add('on'); $(`.ss[data-sess="${CSS.escape(r.from === id ? r.to : r.from)}"]`, box)?.classList.add('rel'); }
}
function fvFlab(bar) {
  const fl = $('#fv-flab'), box = $('#fv-lanes'); if (!fl || !box) return; if (!bar) { fl.classList.remove('on'); return; }
  const [a, b, c] = bar.dataset.fl.split('|'), r = bar.getBoundingClientRect(), b0 = box.getBoundingClientRect();
  fl.innerHTML = `<b>${esc(a)}</b> ${esc(b)} <small>${esc(c)}</small>`; fl.style.left = Math.max(4, Math.min(r.left - b0.left, b0.width - fl.offsetWidth - 4)) + 'px'; fl.style.top = r.top - b0.top - 30 + 'px'; fl.classList.add('on');
}
function fvDrawGraph() {
  const M = FV.M, B = M?.pg.branches, svg = $('#fv-gsvg'), box = $('#graph'); if (!svg || !box || !B) return;
  const b0 = box.getBoundingClientRect(), any = $('[data-trk]', box); if (!any) return;
  const tb = any.getBoundingClientRect(), W = tb.width, H = b0.height, now = fvNow(), t0 = Math.min(...B.map((b) => b.from || b.commits?.[0]?.at || now)), X = (t) => ((Math.max(t0, Math.min(now, t || t0)) - t0) / (now - t0 || 1)) * W, R = 9; // no time known (from null, no commits): the start
  Object.assign(svg.style, { left: tb.left - b0.left + 'px', width: W + 'px', height: H + 'px' });
  const yOf = (id) => { const r = $(`[data-trk="${CSS.escape(id)}"]`, box)?.getBoundingClientRect(); return r ? r.top + r.height / 2 - b0.top : null; };
  const prOf = (n) => (M.d.prs || []).find((p) => p.number === n), lin = FV.glin ? fvLin(B, FV.glin) : null;
  let out = `<defs><marker id="fvgm" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0,1 L9,5 L0,9 z" class="mk-merge"/></marker></defs><line class="gnow" x1="${X(now)}" x2="${X(now)}" y1="0" y2="${H}"/>`;
  for (const b of B) {
    const y = yOf(b.id); if (y == null) continue;
    const p = b.pr && prOf(b.pr), merged = b.intoState === 'merged' || p?.state === 'MERGED', closed = b.intoState === 'closed' || p?.state === 'CLOSED', trunk = !b.parent, cs = b.commits || [], last = cs[cs.length - 1], fu = cs.findIndex((c) => c.kind === 'unpushed');
    const dest = merged && B.find((x) => x.id === b.into), dc = dest && (dest.commits || []).find((c) => c.kind === 'merge' && c.at >= (last?.at || 0)), mAt = dc?.at || (p?.mergedAt ? Date.parse(p.mergedAt) / 1000 : last?.at || now);
    const xm = merged ? Math.max(X(mAt), X(last?.at || b.from) + R + 4) : 0; let xs = X(b.from || cs[0]?.at || t0), g = '';
    if (b.parent) { const py = yOf(b.parent), sc = (B.find((x) => x.id === b.parent)?.commits || []).find((c) => c.sha === b.src), xp = X(sc?.at || b.from) + (sc?.kind === 'merge' ? 7 : 0); xs = Math.max(xs, xp + R);
      if (py != null) { const dn = y > py ? 1 : -1; g += `<path class="fork" d="M${xp},${py + dn * 6} V${y - dn * R} Q${xp},${y} ${xp + R},${y} H${xs}"/>`; } }
    const lx = X(last?.at || b.from || t0), endC = closed ? X(p?.closedAt ? Date.parse(p.closedAt) / 1000 : last?.at || now) : 0;
    const solidEnd = trunk ? X(now) : merged ? xm - R : closed ? endC : fu > 0 ? X(cs[fu - 1].at) : fu === 0 ? xs : lx;
    g += `<path class="${trunk ? 'body trunk' : 'body'}" d="M${xs},${y} H${solidEnd}"/>`;
    if (fu >= 0) g += `<path class="unp" d="M${solidEnd},${y} H${lx}"/>`;
    if (!trunk && !merged && !closed) g += `<path class="tail" d="M${lx + 6},${y} H${X(now)}"/>`;
    if (merged) { const dy = yOf(b.into); if (dy != null) { const up = dy < y ? -1 : 1; g += `<path class="mrg" d="M${xm - R},${y} Q${xm},${y} ${xm},${y + up * R} V${dy - up * 8}" marker-end="url(#fvgm)"/>`; } }
    if (closed) g += `<path class="closedx" d="M${endC - 4},${y - 4} L${endC + 4},${y + 4} M${endC + 4},${y - 4} L${endC - 4},${y + 4}"/>`;
    out += `<g class="gb${closed ? ' closed' : ''}${lin ? (lin.has(b.id) ? ' on' : ' off') : ''}" data-b="${esc(b.id)}" data-bc="${esc(b.color || '')}">${g}</g>`;
  }
  svg.innerHTML = out;
  for (const n of $$('[data-bc]', box.parentElement)) if (n.dataset.bc) n.style.setProperty('--bc', n.dataset.bc);
}
function fvLin(B, id) { const s = new Set(); let b = B.find((x) => x.id === id); while (b) { s.add(b.id); if (b.into) s.add(b.into); b = b.parent && B.find((x) => x.id === b.parent); } const dn = (x) => B.filter((c) => c.parent === x).forEach((c) => { s.add(c.id); dn(c.id); }); dn(id); return s; }
function fvSetLin(id) {
  if (FV.glin === id) return; FV.glin = id; const g = $('#graph'), B = FV.M?.pg.branches; if (!g || !B) return; const L = id ? fvLin(B, id) : null;
  g.classList.toggle('lin', !!id); for (const r of $$('[data-row]', g)) r.classList.toggle('on', !!L && L.has(r.dataset.row));
  for (const x of $$('.gb', g)) { x.classList.toggle('on', !!L && L.has(x.dataset.b)); x.classList.toggle('off', !!L && !L.has(x.dataset.b)); }
}

// seen: a row counts as seen only after >= 60% of it was on screen for 1 s while this tab was visible and focused.
// Receipts go out in small batches (idempotent); nothing is marked on opening the page.
let fvIO = null; const fvTim = new Map(); let fvFlushT = 0;
function fvObserve(root) {
  if (fvIO) fvIO.disconnect(); fvTim.forEach(clearTimeout); fvTim.clear();
  fvIO = new IntersectionObserver((ents) => { for (const en of ents) { const id = en.target.dataset.seen; if (!id) continue;
    if (en.isIntersecting && en.intersectionRatio >= 0.6) { if (!fvTim.has(id)) fvTim.set(id, setTimeout(() => { fvTim.delete(id); if (document.visibilityState === 'visible' && document.hasFocus()) fvSeen(id, en.target); }, 1000)); }
    else { clearTimeout(fvTim.get(id)); fvTim.delete(id); } } }, { threshold: [0, 0.6, 1] });
  for (const n of $$('[data-seen]:not([data-seen=""])', root)) fvIO.observe(n);
}
const fvRid = (l) => /^ev-\d+$/.test(l.id || '') && l.seq > 0; // only a server-issued feature event id can carry a seen receipt
function fvSeen(id, el) {
  if (FV.sent.has(id)) return; FV.sent.add(id); FV.seenQ.add(id); el.dataset.seen = ''; fvIO?.unobserve(el); // stays unseen until the panel records it
  clearTimeout(fvFlushT); fvFlushT = setTimeout(fvFlushSeen, 600);
}
// /seen is atomic: 200 records the batch · 400 records nothing and names the bad ids (+reason): those are dropped and shown
// refused, the valid ones stay pending and go again · 401/403 paused (kept) until the tab is shown again · 409 reload the record
// and re-observe · no answer / 5xx: kept, backoff 15 s doubling for 5 tries, then shown pending until the tab is shown again.
async function fvFlushSeen() {
  const M = FV.M, ids = [...FV.seenQ]; if (!ids.length || !M || FV.seenPause || FV.seenOff) return; FV.seenQ.clear();
  const r = await api('/api/term/feature/seen', { session: M.name, ids }).catch(() => ({ status: 0 }));
  const retry = (xs) => { xs.forEach((id) => FV.seenQ.add(id)); clearTimeout(fvFlushT); if (++FV.seenTry <= 5) fvFlushT = setTimeout(fvFlushSeen, 15000 * 2 ** (FV.seenTry - 1)); };
  if (r.status === 200) { FV.seenTry = 0; FV.seenWarn = r.warning || ''; ids.forEach((id) => FV.acked.add(id)); const fr = fvSeq(r.frontier); if (fr != null) (FV.fr ||= {})[M.name] = Math.max(FV.fr[M.name] ?? -1, fr); for (const id of ids) for (const n of $$(`.lg[data-log="${CSS.escape(id)}"]`)) n.classList.remove('unseen'); return render(); }
  if (r.status === 400) {
    const why = new Map((r.bad || []).map((b) => (typeof b === 'string' ? [b, ''] : [b.id, b.reason || '']))), bad = new Set(why.keys()), ok = ids.filter((id) => !bad.has(id));
    bad.forEach((id) => { if (ids.includes(id)) FV.seenBad.set(id, why.get(id) || 'refused'); }); // malformed | unknown | compacted
    if (!bad.size) ids.forEach((id) => FV.seenBad.set(id, r.reason || r.error || 'refused')); // no ids named: nothing from this batch is claimed
    else if (ok.length) { FV.seenTry = 0; retry(ok); }
    FP.at = 0; fpLoad(M.name, true); return render();
  }
  if (r.status === 404 || r.status === 405) { ids.forEach((id) => FV.seenQ.add(id)); FV.seenOff = true; return render(); } // this panel doesn't record seen yet: kept pending, said so, not retried
  if (r.status === 401 || r.status === 403) { ids.forEach((id) => FV.seenQ.add(id)); FV.seenPause = true; return render(); }
  if (r.status === 409) { ids.forEach((id) => FV.sent.delete(id)); FP.at = 0; return fpLoad(M.name, true); }
  retry(ids); render();
}
document.addEventListener('visibilitychange', () => { if (document.visibilityState !== 'visible') { fvTim.forEach(clearTimeout); fvTim.clear(); } else if ($('.fv')) { FV.seenPause = false; FV.seenTry = 0; fvFlushSeen(); fvObserve($('.fv')); } });

// ---- the context chat: right-click (Shift+F10, the context-menu key, long-press) on content → the feature's manager thread ----
const FV_ANCH = '[data-key], .ms[data-ms] > summary, .ss[data-sess], .qi[data-q], #needs .tk, tr.pr[data-open], .pfi, [data-relc], #graph [data-row], [data-log], .dod > li[data-obj], .mso li';
function fvAnchor(el) {
  const M = FV.M; if (!M) return null;
  const j = (k) => M.stepOf(k), base = (a, kind, label, extra = {}) => ({ a, kind, label, ...extra });
  if (el.dataset.key) { const x = j(el.dataset.key); return x && base('job:' + x.key, 'Job', `${x.n} ${x.text}`, { job: x.key, ms: x.ms, rev: M.dz.rev }); }
  if (el.matches('summary')) { const id = el.parentElement.dataset.ms, m = M.dz.milestones[+id.slice(1) - 1]; return base('ms:' + id, 'Milestone', `${id} ${m?.title || ''}`, { ms: id, rev: M.dz.rev }); }
  if (el.dataset.sess) { const s = M.sessions?.find((x) => x.k === el.dataset.sess); return s && base("sess:" + s.k, 'Session', `${s.tab} · ${s.kind || ''} ${s.sid}`, { sid: s.sid, job: s.step, ms: j(s.step)?.ms }); }
  if (el.dataset.q || el.matches('.tk')) { const t = M.tickets.find((x) => x.id === (el.dataset.q || FV.q)); return t && base(t.id, 'ticket', t.title, { job: t.step, ms: j(t.step)?.ms, sid: t.sid, rev: t.rev }); }
  if (el.dataset.open) { const p = (M.d.prs || []).find((x) => x.number === +el.dataset.open); return p && base('pr:' + p.number, 'Pull request', `#${p.number} ${p.title}`, { pr: p.number, sid: p.session, job: p.step }); }
  if (el.matches('.pfi')) { const k = el.closest('[data-key]')?.dataset.key; return base(`proof:${k}:${el.dataset.fvpfi}`, 'Proof item', el.querySelector('figcaption')?.textContent || '', { job: k, ms: j(k)?.ms }); }
  if (el.dataset.relc) { const [, repo, role] = el.dataset.relc.split(':'); return base(el.dataset.relc, 'Release', `${repo} · ${role}`); }
  if (el.dataset.row) { const b = M.pg.branches?.find((x) => x.id === el.dataset.row); return b && base('br:' + b.id, 'Branch', `${b.repo} · ${b.name}`, { pr: b.pr }); }
  if (el.dataset.log) { const l = fvLogEntries(M).find((x) => x.id === el.dataset.log); return l && base('log:' + l.id, 'Work log entry', l.title, { job: l.step, sid: l.session, rev: l.rev }); }
  if (el.dataset.obj) { const O = fvObjectives(M), o = O?.items.find((x) => x.id === el.dataset.obj); return o && base('obj:' + o.id, 'Objective', `${o.id} ${o.text}`, { rev: O.rev || M.dz.revs?.feature }); }
  if (el.matches('.mso li')) { const id = $('[data-obj-go]', el)?.dataset.objGo, o = fvObjectives(M)?.items.find((x) => x.id === id); return o && base('obj:' + o.id, 'Objective', `${o.id} ${o.text}`, { ms: el.closest('[data-mso]')?.dataset.mso }); }
  return null;
}
const fvThread = () => { const M = FV.M; const m = M?.pg.manager; if (m?.thread) return { id: m.thread, title: m.title || m.thread, bound: m.bound !== false }; const th = M?.d.thread; return th ? { id: th.id, title: th.title, bound: true } : null; };
let fvRet = null;
function fvOpenChat(el, quote, at) {
  const c = fvAnchor(el); if (!c) return false;
  const sec = el.closest('section')?.getAttribute('aria-label') || 'Feature page';
  const snap = { ...c, rev: c.rev || '', text: (el.innerText || '').replace(/\s+/g, ' ').trim().slice(0, 220), quote: quote || '', loc: [sec, c.ms, c.job && FV.M.stepOf(c.job)?.n].filter(Boolean).join(' › '), url: `${location.pathname}#feature=${encodeURIComponent(FV.M.name)}&${encodeURIComponent(c.a)}`, takenAt: fvNow(), observedAt: FV.M.observed };
  fvRet = document.activeElement;
  const th0 = fvThread(), cur = fvOutOf(FV.M.name, 'chat').filter((h) => h.anchorA === c.a && (!th0 || h.thread === th0.id || h.newThread) && (fvOpen(h) || (h.state === 'held' && h.newThread && !h.maybe))).at(-1) || null; // reattach what is still open here
  FV.comp = { snap, text: '', thread: fvThread(), at: at || null, ctx: true, cur, sep: false };
  fvOutResume(); fvDrawChat(); setTimeout(() => $('.ctxchat textarea')?.focus(), 20); return true;
}
const FV_SS = { sending: 'sending…', queued: 'queued for the manager thread', delivered: '✓ delivered to the manager thread', unknown: 'not confirmed: the thread is checked before any resend', failed: 'not sent', held: 'held: the manager thread changed' };
function fvDrawChat() {
  const ta0 = document.activeElement?.matches?.('[data-ctext]') ? document.activeElement : null, sel = ta0 && [ta0.selectionStart, ta0.selectionEnd];
  try { fvDrawChat0(); } finally { if (sel) { const ta = $('[data-ctext]'); if (ta) { ta.focus(); ta.setSelectionRange(...sel); } } }
}
function fvDrawChat0() {
  let host = $('#ctxchat-host'); if (!host) { host = document.createElement('div'); host.id = 'ctxchat-host'; document.body.append(host); }
  const C = FV.comp; if (!C) { host.innerHTML = ''; return; }
  const H = C.cur, pend = fvOutOf(FV.M?.name).filter((x) => x !== H && fvOpen(x));
  const sn = C.snap, M = FV.M, th = C.thread, ch = M && sn.rev && sn.job && M.dz.rev !== sn.rev ? `the plan is now ${M.dz.rev}` : '';
  const rows = [['Item', `${esc(sn.kind)}: <b>${esc(sn.label)}</b>`], sn.ms && ['Milestone', esc(sn.ms)], sn.job && ['Job', esc(FV.M?.stepOf(sn.job)?.n || sn.job)], sn.sid && ['Session', `<code>${esc(sn.sid)}</code>`], sn.pr && ['PR', '#' + esc(sn.pr)],
    ['Record', `<code>${esc(sn.a)}${sn.rev ? '@' + esc(sn.rev) : ''}</code>`], ['Where', esc(sn.loc)], sn.quote ? ['Selected', `<q>${esc(sn.quote.slice(0, 200))}</q>`] : null].filter(Boolean);
  const off = !th ? 'No manager thread runs this feature, so there is nobody to send to. Attach one at the top of the page.' : th.bound === false ? 'The manager thread is archived or unbound: messages would be held.' : '';
  host.innerHTML = `<div class="ctxchat" role="dialog" aria-modal="false" aria-label="Message the manager thread"><div class="fvc-h"><span class="fvc-av" aria-hidden="true">${ic('chat-round-dots')}</span><span><b>${esc(th ? th.title : 'No manager thread')}</b><small>${th ? 'this feature\'s manager thread (Hermes lead)' : 'not bound'}</small></span><span class="spacer"></span><button type="button" class="icon-btn sm ghost" data-cclose aria-label="Close (Esc)">${ic('x')}</button></div>` +
    `<div class="fvc-ctx"><div class="cx-h"><span>Attached context · snapshot ${fvT(sn.takenAt)}</span><button type="button" class="lnk" data-ctxt>${C.ctx ? 'Remove' : 'Attach again'}</button></div>${C.ctx ? `<dl>${rows.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('')}</dl>${ch ? `<div class="cx-ch">Kept as sent-from: ${esc(ch)}.</div>` : ''}` : '<div class="faint">No context will be attached.</div>'}</div>` +
    (off ? `<div class="fvc-w" role="status">${esc(off)}</div>` : '') +
    (fvOpenAt(M?.name, sn.a).length ? `<div class="fvc-w fvc-sep" role="status">${C.sep ? 'This sends a second, separate message. The earlier one may still arrive too, so the lead may get both. <button type="button" class="lnk" data-csep>Cancel</button>' : 'Your earlier message about this item is not confirmed yet and may still arrive, so Send is off: Retry sends that same message. <button type="button" class="lnk" data-csep>Send a separate message…</button>'}</div>` : '') +
    (fvOutFull() ? `<div class="fvc-w" role="status">${esc(FV_FULL)}</div>` : '') +
    `<textarea data-ctext rows="3" aria-label="Message to the manager thread" placeholder="Ask the lead about this…"${th ? '' : ' disabled'}>${esc(C.text)}</textarea>` +
    (pend.length ? `<div class="fvc-pend" role="status"><b>Not confirmed yet</b><ul>${pend.map((x) => `<li data-pend="${esc(x.id)}"><q>${esc((x.text || '').slice(0, 90))}</q> <span class="ss6 ${esc(x.state)}">${FV_SS[x.state] || esc(x.state)}</span>${x.state === 'unknown' || x.state === 'failed' ? ` <button type="button" class="lnk" data-cretry="${esc(x.id)}">Retry (same message)</button>` : ''}</li>`).join('')}</ul></div>` : '') +
    `<div class="fvc-f"><span class="faint">Not an approval.</span><span class="spacer"></span>${H?.state ? `<span class="ss6 ${H.state}" role="status">${FV_SS[H.state]}${H.why ? ': ' + esc(H.why) : ''}</span>` : ''}` +
    (H && fvOpen(H) && (H.state === 'failed' || H.state === 'unknown') && H.sent ? `<button type="button" class="btn sm" data-cretry="${esc(H.id)}">Retry</button>` : '') + (H?.state === 'held' && H.newThread && !H.maybe ? `<button type="button" class="btn sm" data-cconfirm title="Sends it as a new message to the feature's manager now; the first was refused, so it was never sent">Send it to ${esc(H.newThread.title || H.newThread.thread)} instead</button>` : H?.state === 'held' && !H.maybe ? '<span class="faint">No manager runs this feature now.</span>' : '') +
    `<button type="button" class="btn sm primary" data-csend>Send</button></div>` +
    `<details data-fvcdet${C.det ? ' open' : ''}><summary>Delivery details</summary><div>To <code>${esc(th?.title || '—')}</code>${th ? ` (<code>${esc(th.id)}</code>)` : ''} · the lead routes it to agents if needed</div><div>Message id <code>${esc(H?.id || 'assigned on send')}</code> · a retry reuses it, so it never arrives twice</div></details></div>`;
  fvPlaceChat(); fvSendBtn();
}
function fvPlaceChat() {
  const el = $('.ctxchat'), C = FV.comp; if (!el || !C) return;
  const w = el.offsetWidth, h = el.offsetHeight, m = 8, at = C.at || { x: innerWidth - w - 16, y: innerHeight - h - 16 };
  let x = at.x + 4, y = at.y + 4; if (x + w > innerWidth - m) x = Math.max(m, at.x - w - 4); if (y + h > innerHeight - m) y = Math.max(m, innerHeight - h - m);
  el.style.left = Math.max(m, Math.min(x, innerWidth - w - m)) + 'px'; el.style.top = Math.max(m, y) + 'px';
}
function fvCloseChat() { FV.comp = null; fvDrawChat(); fvRet?.focus?.(); fvRet = null; }
// ---- feature messages (POST/GET /api/term/feature/message): one path for the context chat and a ticket's free text.
// The payload is {session, clientId, thread, text, anchor{a, kind, rev, label?, quote?}}. A client id is reused only for a
// byte-identical payload (a retry, safe across restarts); anything changed (the text, the context, a manager you chose
// after a rebind) is a new message with a new id, never the old one readdressed.
function fvMsgPayload(thread, text, anchor) { const a = { a: anchor.a, kind: anchor.kind, rev: anchor.rev || '' }; if (anchor.label) a.label = anchor.label.slice(0, 300); if (anchor.quote) a.quote = anchor.quote.slice(0, 1000); return { session: FV.M.name, thread, text, anchor: a }; }
// This tab's outbox: every message sent from the page, keyed feature + clientId + target thread, outside the composer
// (closing the chat or reloading never loses one). Kept in sessionStorage, so it lives as long as this tab: each tab has
// its own, and nothing here claims exactly-once across tabs (the server dedups a clientId; two tabs use two ids).
// Stored: only what a same-id retry needs (the payload you sent: your text, the item's id, rev, label and any quote you
// selected) plus its last state; nothing else (no cookie, token or page data). At most 50 unresolved (a new send is refused, with why, rather than any dropped); resolved ones are trimmed first and go after 1 h. A message that may have been accepted stays "open" until the server
// says delivered or refused it outright; while open, it is polled (2 s → 60 s, paused while the tab is hidden), shown on
// every reopen, retried only as itself (same id, same payload), and its text can't be sent again as a new message. While one
// about an item is open, Send for that item is off: a second message only through an explicit "Send a separate message…".
const FV_OUT_KEY = 'fv.out';
FV.out = (() => { try { return JSON.parse(sessionStorage.getItem(FV_OUT_KEY) || '[]'); } catch { return []; } })();
const fvOpen = (h) => !!h && h.maybe && h.state !== 'delivered';
const fvOutOf = (f, src) => FV.out.filter((h) => h.f === f && (!src || h.src === src));
const fvSameOpen = (f, text, thread) => fvOutOf(f).some((h) => fvOpen(h) && h.thread === thread && (h.text || '').trim() === (text || '').trim()); // same words to the same manager, possibly on its way
const FV_OUT_MAX = 50, FV_FULL = `${FV_OUT_MAX} messages from this tab are not confirmed yet. None is dropped, so no new message is sent until some are confirmed (they are re-checked by themselves) or retried.`;
const fvOpenAt = (f, a) => fvOutOf(f).filter((h) => fvOpen(h) && (h.sent?.anchor?.a ?? h.anchorA) === a); // unresolved messages about this item, to any manager
const fvOutFull = () => FV.out.filter(fvOpen).length >= FV_OUT_MAX;
const FV_OUT_KEEP = ['f', 'src', 'anchorA', 'id', 'sent', 'text', 'state', 'why', 'maybe', 'thread', 'newThread', 'at', 'polledFrom'];
function fvOutSave() { // an unresolved message is never evicted (new sends are refused at FV_OUT_MAX instead); resolved ones are trimmed first
  const room = Math.max(0, FV_OUT_MAX - FV.out.filter(fvOpen).length), done = room ? FV.out.filter((h) => !fvOpen(h) && Date.now() - h.at < 3600e3).slice(-room) : [];
  FV.out = FV.out.filter((h) => fvOpen(h) || done.includes(h));
  try { sessionStorage.setItem(FV_OUT_KEY, JSON.stringify(FV.out.map((h) => Object.fromEntries(FV_OUT_KEEP.filter((k) => k in h).map((k) => [k, h[k]]))))); } catch { /* storage full or off: kept in memory */ } }
function fvOutChanged() { fvOutSave(); if (FV.comp) fvDrawChat(); if (S.view === 'term' && TV.feature) render(); }
function fvOutResume() { for (const h of FV.out) if (fvOpen(h) && !h.poll && !h.reading) fvMsgPoll(h); } // one chain per message
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') fvOutResume(); });
async function fvMsg(H, payload) {
  if (!FV.out.includes(H)) { if (fvOutFull()) { toast(FV_FULL, true); return; } FV.out.push(H); }
  H.f = payload.session; H.at ||= Date.now(); H.text = payload.text;
  if (payload.text.length > 4000) { H.maybe = false; H.state = 'failed'; H.why = `too long: ${payload.text.length} of at most 4000 characters`; return fvOutChanged(); }
  const key = JSON.stringify(payload); if (H.id && !H.key && H.sent) H.key = JSON.stringify(H.sent); if (!H.id || H.key !== key) { H.id = 'ow-' + Date.now().toString(36) + '-' + (++FV.msgN); H.key = key; }
  clearTimeout(H.poll); H.poll = null; H.state = 'sending'; H.why = ''; H.newThread = null; H.thread = payload.thread; H.sent = payload; H.maybe = true; H.polledFrom = Date.now(); fvOutChanged();
  const r = await api('/api/term/feature/message', { ...payload, clientId: H.id }).catch(() => ({ status: 0 }));
  if (r.status === 200) { H.state = r.state || 'unknown'; H.why = r.reason || ''; }
  else if (r.status === 409 && 'thread' in r) { H.maybe = false; H.state = 'held'; H.why = r.error || 'the feature\'s manager changed'; H.newThread = r.thread ? { thread: r.thread, title: fvThreadTitle(r.thread) } : null; } // refused before anything was recorded: provably unsent
  else if (r.status === 409) { H.maybe = false; H.state = 'failed'; H.why = r.error || 'this message id was used for another message'; }
  else if (!r.status) { H.state = 'unknown'; H.why = 'no answer from the panel: it may still have arrived. Retry sends this same message (same id), never a second one.'; }
  else if (r.status >= 500) { H.state = 'unknown'; H.why = r.error || 'error ' + r.status; }
  else { H.maybe = false; H.state = 'failed'; H.why = r.error || 'error ' + r.status; } // a 4xx refusal: nothing recorded
  if (H.state === 'delivered') H.maybe = false;
  fvOutChanged(); fvMsgPoll(H);
}
const fvPollWait = (age) => (age < 30e3 ? 2000 : age < 120e3 ? 6000 : age < 600e3 ? 20000 : 60000); // never stops while open; at most once a minute
function fvMsgPoll(H) {
  clearTimeout(H.poll); H.poll = null; if (!fvOpen(H) || !H.id || document.visibilityState !== 'visible') return; // resumed on show / open
  const wait = fvPollWait(Date.now() - (H.polledFrom || H.at));
  H.poll = setTimeout(async () => { H.poll = null; const id = H.id; H.reading = true;
    const r = await api(`/api/term/feature/message?session=${encodeURIComponent(H.f)}&id=${encodeURIComponent(id)}`).catch(() => ({ status: 0 }));
    H.reading = false; if (id !== H.id) return;
    if (r.status === 200) { H.state = r.state || 'unknown'; H.why = r.reason || ''; H.newThread = null; if (H.state === 'delivered') H.maybe = false;
      if (H.state === 'held') H.why = 'not delivered so far, but it was accepted and may still arrive: it is not sent to anyone else from here'; }
    else if (r.status === 404) { H.state = 'unknown'; H.why = 'the panel has no record of it yet, but the first try may still land. Retry sends this same message (same id), never a second one.'; }
    fvOutChanged(); fvMsgPoll(H);
  }, wait);
}
const fvThreadTitle = (id) => (FV.M?.pg.manager?.thread === id ? FV.M.pg.manager.title : (typeof thOf === 'function' && thOf(id)?.title) || id); // the panel's thread list, else its id
async function fvSendChat(toThread) {
  const C = FV.comp, ta = $('[data-ctext]'); if (ta) C.text = ta.value;
  if (toThread) { const H = C.cur; if (!H || H.maybe || H.state !== 'held' || !H.newThread) return; C.thread = { id: H.newThread.thread, title: H.newThread.title || H.newThread.thread, bound: true }; } // only a refused, provably unsent message is readdressed — as a new message
  if (!C.text.trim()) { if (ta) { ta.placeholder = 'Write something first'; ta.focus(); } return; }
  if (fvSameOpen(FV.M.name, C.text, C.thread?.id)) return; // the same words are already on their way (maybe): Retry that one
  if (!C.thread || fvOutFull()) return;
  if (!toThread && !C.sep && fvOpenAt(FV.M.name, C.snap.a).length) return; // one about this item may still arrive: only an explicit "separate message" sends another
  C.sep = false;
  const sn = C.snap, anchor = C.ctx ? { a: sn.a, kind: sn.kind, rev: sn.rev, label: sn.label, quote: sn.quote } : { a: sn.a, kind: sn.kind, rev: sn.rev };
  const H = { src: 'chat', anchorA: sn.a }; C.cur = H; return fvMsg(H, fvMsgPayload(C.thread.id, C.text, anchor));
}
function fvSendBtn() { // re-evaluated as you type
  const b = $('[data-csend]'), C = FV.comp; if (!b || !C) return; const f = FV.M?.name, same = fvSameOpen(f, C.text, C.thread?.id), other = fvOutOf(f).some(fvOpen), blk = fvOpenAt(f, C.snap.a).length > 0, full = fvOutFull();
  b.disabled = !C.thread || same || full || (blk && !C.sep); b.textContent = blk && C.sep ? 'Send separate message' : other && !same && !blk ? 'Send as a separate message' : 'Send';
  b.title = same ? 'These words are in a message that may still arrive: Retry it instead (same id)' : full ? FV_FULL : blk && !C.sep ? 'Your earlier message about this item may still arrive: Retry it, or choose "Send a separate message…"' : other || blk ? 'A message not yet confirmed exists: this sends a separate, new message' : '';
}
const fvCtxEl = (t) => !t.closest?.('.ctxchat, input, textarea, select, a[href]') && $('.fv') && t.closest?.('.fv') && t.closest(FV_ANCH);
document.addEventListener('contextmenu', (e) => { if (e.shiftKey) return; // Shift + right-click keeps the browser's menu
  const el = fvCtxEl(e.target); if (!el) return;
  const sel = String(getSelection() || '').trim(), kb = e.clientX === 0 && e.clientY === 0, r = el.getBoundingClientRect();
  if (fvOpenChat(el, sel && el.contains(getSelection().anchorNode) ? sel : '', kb ? { x: r.left, y: r.bottom } : { x: e.clientX, y: e.clientY })) e.preventDefault();
});
document.addEventListener('pointerdown', (e) => { if (FV.comp && !e.target.closest('.ctxchat') && e.button !== 2) fvCloseChat(); }, true);
let fvLp = null, fvLpAt = null;
document.addEventListener('pointerdown', (e) => { if (e.pointerType !== 'touch') return; const el = fvCtxEl(e.target); if (!el) return; fvLpAt = { x: e.clientX, y: e.clientY }; fvLp = setTimeout(() => fvOpenChat(el, '', fvLpAt), 550); });
['pointerup', 'pointercancel'].forEach((k) => document.addEventListener(k, () => clearTimeout(fvLp)));
document.addEventListener('pointermove', (e) => { if (fvLpAt && Math.hypot(e.clientX - fvLpAt.x, e.clientY - fvLpAt.y) > 10) clearTimeout(fvLp); });
addEventListener('resize', () => { fvPlaceChat(); if ($('.fv')) { fvDrawRels(); fvDrawGraph(); } });

// ---- clicks, keys, hovers on the page (capture: the page's own controls first; terms.js handles the shared data-fp* ones) ----
function fvGo(k) { const x = FV.M?.stepOf(k); if (!x) return; FV.ptab = 'ms'; FV.open ||= new Set(); FV.open.add(x.ms); FV.open.delete('!' + x.ms); render(); fvFlash($(`[data-key="${CSS.escape(x.key)}"]`)); }
function fvFlash(el) { if (!el) return; el.scrollIntoView({ behavior: calm.matches ? 'auto' : 'smooth', block: 'center' }); el.classList.remove('flash'); void el.offsetWidth; el.classList.add('flash'); setTimeout(() => el.classList.remove('flash'), 1600); }
document.addEventListener('click', (e) => {
  if (!e.target.closest?.('.fv, .ctxchat')) return;
  const h = (s) => e.target.closest(s); let b; const stop = () => { e.stopPropagation(); e.preventDefault(); };
  if ((b = h('[data-fvf]'))) { stop(); FV.filter = b.dataset.fvf; return render(); }
  if ((b = h('[data-fvtr]'))) { stop(); FV.trend = b.dataset.fvtr; FV.hover = null; return render(); }
  if ((b = h('[data-fvlr]'))) { stop(); FV.lrange = b.dataset.fvlr; return render(); }
  if ((b = h('[data-fvq]'))) { stop(); FV.q = b.dataset.fvq; render(); return fvFlash($(`[data-q="${CSS.escape(b.dataset.fvq)}"]`)); }
  if ((b = h('.ss[data-sess]'))) { stop(); FV.sel = FV.sel === b.dataset.sess ? null : b.dataset.sess; return render(); }
  if ((b = h('[data-sess]'))) { stop(); FV.sel = b.dataset.sess; FV.lrange = 'all'; FV.filter = 'all'; render(); $('#fv-team')?.scrollIntoView({ behavior: calm.matches ? 'auto' : 'smooth', block: 'start' }); return; }
  if ((b = h('[data-fvunsel]'))) { stop(); FV.sel = null; return render(); }
  if ((b = h('[data-q]'))) { stop(); FV.q = b.dataset.q; return render(); }
  if ((b = h('[data-fvgo]'))) { stop(); return fvGo(b.dataset.fvgo); }
  if ((b = h('[data-fvpr]'))) { stop(); FV.pr = +b.dataset.fvpr; FV.prf = { repo: 'all', state: 'all', failing: false }; render(); return fvFlash($(`[data-open="${b.dataset.fvpr}"]`)); }
  if ((b = h('[data-fvpt]'))) { stop(); FV.ptab = b.dataset.fvpt; return render(); }
  if ((b = h('[data-fvexp]'))) { stop(); const n = FV.M.dz.milestones.length, all = [...Array(n)].every((_, i) => FV.open?.has('M' + (i + 1))); FV.open = new Set([...Array(n)].map((_, i) => (all ? '!' : '') + 'M' + (i + 1))); return render(); }
  if ((b = h('[data-fvvb]'))) { stop(); FV.vb = FV.vb === b.dataset.fvvb ? null : b.dataset.fvvb; return render(); }
  if ((b = h('[data-proof-go]'))) { stop(); FV.vb = b.dataset.proofGo; return fvGo(b.dataset.proofGo); }
  if ((b = h('[data-obj-go]'))) { stop(); FV.ptab = 'obj'; render(); return fvFlash($(`[data-obj="${CSS.escape(b.dataset.objGo)}"]`)); }
  if ((b = h('[data-prs]'))) { stop(); FV.prf.state = b.dataset.prs; FV.pr = null; return render(); }
  if ((b = h('tr.pr[data-open]')) && !h('a, button')) { stop(); const n = +b.dataset.open; FV.pr = FV.pr === n ? null : n; return render(); }
  if ((b = h('[data-fvdrill]'))) { stop(); const n = +b.dataset.fvdrill; FV.pr = FV.pr === n ? null : n; return render(); }
  if ((b = h('[data-fvbr]'))) { stop(); FV.br = b.dataset.fvbr; return render(); }
  if ((b = h('[data-fvunbr]'))) { stop(); FV.br = null; return render(); }
  if ((b = h('[data-wl]'))) { stop(); FV.wl = b.dataset.wl; return render(); }
  if ((b = h('[data-fvwlc]'))) { stop(); FV.wlc = !FV.wlc; return render(); }
  if ((b = h('[data-review]'))) { stop(); const id = b.dataset.review;
    return api('/api/term/feature/reviewed', { session: FV.M.name, id, rev: b.dataset.rev }).then((r) => { FV.reviewed[id] = r.status === 200 ? (r.warning ? 'warn' : 'ok') : r.status === 409 ? 'changed' : FV.reviewed[id]; if (r.warning) (FV.revWarn ||= {})[id] = r.warning; if (r.status !== 200 && r.status !== 409) toast(r.error || 'Couldn\'t record it: try again', true); render(); }).catch(() => toast('Couldn\'t record it: try again', true)); }
  if ((b = h('[data-send], [data-tkretry], [data-tkconfirm]'))) { stop(); const id = b.dataset.send || b.dataset.tkretry || b.dataset.tkconfirm, t = FV.M.tickets.find((x) => x.id === id), ta = $(`[data-reply="${CSS.escape(id)}"]`);
    const H0 = fvOutOf(FV.M.name, 'tk:' + id).at(-1);
    if (b.dataset.tkretry) return H0?.sent && fvMsg(H0, H0.sent); // the same message, same id
    if (b.dataset.tkconfirm && (!H0 || H0.maybe || !H0.newThread)) return; // only a refused, provably unsent one
    const txt = b.dataset.tkconfirm ? H0.text : ta?.value || '';
    if (!txt.trim()) { if (ta) { ta.placeholder = 'Type a message first'; ta.focus(); } return; }
    if (!b.dataset.tkconfirm && fvSameOpen(FV.M.name, txt, fvThread()?.id)) { toast('These words are in a message that may still arrive: Retry it instead', true); return; }
    if (!b.dataset.tkconfirm && fvOpenAt(FV.M.name, id).length && !FV.tkSep?.[id]) return; // only an explicit "separate message"
    if (FV.tkSep) delete FV.tkSep[id];
    const th = b.dataset.tkconfirm ? H0.newThread.thread : fvThread()?.id; if (!t || !th) return;
    return fvMsg({ src: 'tk:' + id }, fvMsgPayload(th, txt, { a: t.id, kind: 'ticket', rev: t.rev, label: t.title })); }
  if ((b = h('[data-opt]'))) { stop(); const t = FV.M.tickets.find((x) => x.id === FV.q), ta = t && $(`[data-reply="${CSS.escape(t.id)}"]`), txt = (ta?.value || '').trim();
    if (b.hasAttribute('data-needs-text') && !txt) { (FV.tkErr ||= {})[t.id] = 'Write your answer first: this option sends it.'; render(); return $(`[data-reply="${CSS.escape(t.id)}"]`)?.focus(); }
    return fvTicket(t, b.dataset.opt, b.hasAttribute('data-needs-text') ? txt : ''); }
  if ((b = h('[data-fvmshi]'))) { stop(); const m = b.dataset.fvmshi; FV.msHi = !m || FV.msHi === m ? null : m; render(); if (FV.msHi) $('section[aria-label="Team timeline"]')?.scrollIntoView({ behavior: calm.matches ? 'auto' : 'smooth', block: 'start' }); return; }
  if ((b = h('[data-fvms]'))) { stop(); const id = b.dataset.fvms; (FV.open ||= new Set()).add(id); FV.ptab = 'ms'; render(); return fvFlash($(`.ms[data-ms="${CSS.escape(id)}"]`)); }
  if ((b = h('[data-fvartgo]'))) { stop(); const a = (FV.M.pg.arts || []).find((x) => x.id === b.dataset.fvartgo), ms = a && fvArtMs(FV.M, FV.M.dz.milestones || [], a); if (ms) { (FV.open ||= new Set()).add(ms); render(); } return fvFlash($(`[data-fvart="${CSS.escape(b.dataset.fvartgo)}"]`)); }
  if ((b = h('[data-tksep]'))) { stop(); const id = b.dataset.tksep; FV.tkSep ||= {}; FV.tkSep[id] = !FV.tkSep[id]; return render(); }
  if ((b = h('[data-csep]'))) { stop(); FV.comp.sep = !FV.comp.sep; return fvDrawChat(); }
  if ((b = h('[data-cclose]'))) { stop(); return fvCloseChat(); }
  if ((b = h('[data-ctxt]'))) { stop(); FV.comp.text = $('[data-ctext]').value; FV.comp.ctx = !FV.comp.ctx; return fvDrawChat(); }
  if ((b = h('[data-csend]'))) { stop(); return fvSendChat(false); }
  if ((b = h('[data-cretry]'))) { stop(); const x = FV.out.find((m) => m.id === (b.dataset.cretry || FV.comp?.cur?.id)); return x?.sent && fvMsg(x, x.sent); } // the same message, same id
  if ((b = h('[data-cconfirm]'))) { stop(); return fvSendChat(true); }
}, true);
async function fvTicket(t, option, text) {
  if (!t) return;
  if (!option) return; // free text alone isn't wired yet (the Send button is disabled and says so)
  FV.tkErr ||= {}; delete FV.tkErr[t.id];
  const r = await api('/api/term/feature/ticket/reply', { session: FV.M.name, ticket: t.id, option, text: text || undefined, rev: t.rev, sid: t.sid || undefined }).catch(() => ({ status: 0 }));
  const err = (m) => { FV.tkErr[t.id] = m; render(); toast(m, true); };
  if (r.status === 200 && r.warning) { delete FV.drafts[t.id]; toast('Sent, but: ' + r.warning, true); FP.at = 0; return fpLoad(FV.M.name, true); }
  if (r.status === 200) { delete FV.drafts[t.id]; toast(option ? 'Done: ' + (t.options?.find((o) => o.id === option)?.label || option) : `Sent to ${t.who || 'the agent'} through its lead`); FP.at = 0; return fpLoad(FV.M.name, true); }
  if (r.status === 409) { err(`Not done: ${r.error || 'it changed since you opened it'}. The page is reloading it.`); FP.at = 0; return fpLoad(FV.M.name, true); }
  if (!r.status) { err('No answer from the panel: it may or may not have been done. The page reloads it to check.'); FP.at = 0; return fpLoad(FV.M.name, true); }
  err(`Not done (${r.status}): ${r.error || 'the panel refused it'}`);
}
document.addEventListener('input', (e) => { const r = e.target.closest?.('[data-reply]'); if (r) FV.drafts[r.dataset.reply] = r.value; if (e.target.matches?.('[data-ctext]') && FV.comp) { FV.comp.text = e.target.value; fvSendBtn(); } }); // typed text survives a redraw
document.addEventListener('change', (e) => {
  if (!e.target.closest?.('.fv')) return;
  if (e.target.matches('[data-prf]')) { FV.prf.repo = e.target.value; FV.pr = null; return render(); }
  if (e.target.matches('[data-prfail]')) { FV.prf.failing = e.target.checked; FV.pr = null; return render(); }
  if (e.target.matches('[data-fvall]')) { FV.all = e.target.checked; return render(); }
});
document.addEventListener('toggle', (e) => { const d = e.target; if (d.matches?.('[data-fvcdet]') && FV.comp) FV.comp.det = d.open;
  if (d.matches?.('[data-mso]')) d.open ? FV.mso.add(d.dataset.mso) : FV.mso.delete(d.dataset.mso);
  if (d.matches?.('[data-ms]')) { FV.open ||= new Set(); const id = d.dataset.ms; FV.open.delete(id); FV.open.delete('!' + id); FV.open.add(d.open ? id : '!' + id); } }, true);
document.addEventListener('mouseover', (e) => {
  if (!e.target.closest?.('.fv')) return;
  const sb = e.target.closest('.ss[data-sess]'); fvFlab(sb); fvHi(sb ? sb.dataset.sess : FV.sel);
  const r = e.target.closest('#graph [data-row]'); fvSetLin(r ? r.dataset.row : null);
  const ev = e.target.closest('[data-fvev]'); if (ev && FV.hover !== ev.dataset.fvev) { FV.hover = ev.dataset.fvev; render(); }
});
document.addEventListener('focusin', (e) => { if (!e.target.closest?.('.fv')) return; const sb = e.target.closest('.ss[data-sess]'); if (sb) { fvFlab(sb); fvHi(sb.dataset.sess); } const r = e.target.closest('#graph [data-row]'); fvSetLin(r ? r.dataset.row : null); });
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && FV.comp) { e.stopPropagation(); return fvCloseChat(); }
  if (!e.target.closest?.('.fv')) return;
  if (e.key === 'Escape' && FV.sel) { FV.sel = null; return render(); }
  if (e.target.closest('input, textarea, select')) return;
  if ((e.key === 'F10' && e.shiftKey) || e.key === 'ContextMenu') { const el = fvCtxEl(e.target); if (el) { e.preventDefault(); const r = el.getBoundingClientRect(); fvOpenChat(el, '', { x: r.left, y: r.bottom }); } return; }
  const bar = e.target.closest('.ss[data-sess]'); if (!bar || !['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(e.key)) return;
  e.preventDefault();
  const all = $$('#fv-lanes .ss'), row = (x) => x.closest('.lrow'), rows = [...new Set(all.map(row))], cx = bar.getBoundingClientRect().left; let next;
  if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') { const same = all.filter((x) => row(x) === row(bar)).sort((a, b) => a.getBoundingClientRect().left - b.getBoundingClientRect().left); next = same[same.indexOf(bar) + (e.key === 'ArrowRight' ? 1 : -1)]; }
  else { const cand = all.filter((x) => row(x) === rows[rows.indexOf(row(bar)) + (e.key === 'ArrowDown' ? 1 : -1)]); next = cand.sort((a, b) => Math.abs(a.getBoundingClientRect().left - cx) - Math.abs(b.getBoundingClientRect().left - cx))[0]; }
  if (next) { next.focus(); fvHi(next.dataset.sess); }
}, true);
