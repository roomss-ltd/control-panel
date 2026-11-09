// Feature-page acceptance, browser half, FIXTURE PROOF: the real page (web/*) on the real panel and session, but
// GET /api/term/feature and the v8 writes are answered by a deterministic featFacts.page fixture. Proves the page's
// behaviour against its data contract, independent of the back end (the actual-API proof is ui.test.mjs).
// Run: acceptance/v8/run.sh '' '' ui-fixture.test.mjs
// Two tests need files kept outside the repo and are skipped without them: F-V1 (V8QA_REAL_PLAN, a real PLAN.md
// with milestones M1..M7) and F-V2 (V8QA_DESIGN_HTML, a design mock-up page, as a path or file:// URL).
import { test, before, after, afterEach } from 'node:test';
import assert from 'node:assert/strict';
import { SECTIONS, SEL, FIXTURE_STRINGS, openFeatureJS, R } from './names.mjs';
import { S, B, E, F, OUT, sleep, agent, feature, lines, api, seedFeature, hermesMode, turns, restartPanel, steps, launchChromium } from './lib.mjs';
import { readFileSync, existsSync, appendFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { pathToFileURL, fileURLToPath } from 'node:url';
import { resolve } from 'node:path';

const now = Math.floor(Date.now() / 1000), h = 3600;
const K = { a: 'a1b2c3d4e5f6', b: 'b2c3d4e5f6a1', c: 'c3d4e5f6a1b2', d: 'd4e5f6a1b2c3', e: 'e5f6a1b2c3d4' };
const step = (key, text, state, x = {}) => ({ key, text, state, owner: 'Worker', ...x });
function fixture(withPage = true) {
  const d = {
    session: S, thread: { id: 'th-orbit', title: 'Orbit manager', status: 'working' },
    team: [{ tab: 'Worker', kind: 'claude', state: 'working', lastAt: now - 20 }],
    trees: [{ root: '/tmp/orbit', branch: 'feature/orbit', main: false }],
    prs: [
      { number: 11, title: 'Orbit sync', state: 'OPEN', repo: 'orbit/core', url: 'https://github.com/orbit/core/pull/11', head: 'feature/orbit', base: 'main', checks: [{ name: 'test', conclusion: 'FAILURE' }], session: 's-b', createdAt: now - 3 * h },
      { number: 10, title: 'Orbit schema', state: 'MERGED', repo: 'orbit/core', url: 'https://github.com/orbit/core/pull/10', checks: [{ name: 'test', conclusion: 'SUCCESS' }], mergedAt: new Date((now - 4 * h) * 1000).toISOString() },
      { number: 9, title: 'Orbit tools probe', state: 'CLOSED', repo: 'orbit/tools', url: 'https://github.com/orbit/tools/pull/9', checks: [], closedAt: new Date((now - 2 * h) * 1000).toISOString() }],
    dossier: { rev: 'r1', revs: { feature: 'f1' }, dir: '/tmp/orbit-rec', objective: 'Orbit syncs every night and shows its log.', log: '',
      milestones: [
        { title: 'Schema', done: 1, steps: [step(K.a, 'Write the schema', 'done', { label: 'verified', receipt: 'r', proof: 'go test ok' }), step(K.b, 'Sync job', 'doing')] },
        { title: 'Ship', done: 0, steps: [step(K.c, 'Nightly timer', 'claimed', { why: 'Ticked, no receipt' }), step(K.d, 'Pick the log format', 'asking', { ask: 'JSON or text?' }), step(K.e, 'Smoke check', 'todo', { check: 'make smoke', cwd: '/tmp/orbit' })] }] },
  };
  if (!withPage) return d;
  const ev = (n, at, kind, text, x = {}) => ({ id: 'ev-' + n, seq: n, at: new Date(at * 1000).toISOString(), kind, text, ...x }) // the EVENT WIRE: at is an RFC3339 UTC string;
  d.page = {
    observedAt: now - 5, reconciledAt: now - 30,
    events: [ev(1, now - 5 * h, 'assign', 'Worker on 1.1'), ev(2, now - 4 * h, 'verified', '1.1 verified', { step: K.a }), ev(3, now - 3 * h, 'lead', 'Chose JSON lines'),
      ev(4, now - 2 * h, 'ask', 'Log format?', { step: K.d }), ev(5, now - h, 'ci_failed', 'CI failed on #11', { pr: 11 }), ev(6, now - 600, 'transition', 'Sync job started')],
    seen: { frontier: 2, ids: [] }, reviewed: {},
    sessions: [{ id: 'live:orbit:3:s-d', sid: 's-d', tab: 'Tester', kind: 'claude', step: K.e, stepSource: 'plan owner', from: 0, fromSource: 'unknown', to: null, end: null }, { sid: 's-a', tab: 'Worker', kind: 'claude', step: K.a, from: now - 5 * h, to: now - 3 * h, end: 'done' },
      { sid: 's-b', tab: 'Worker', kind: 'claude', step: K.b, from: now - 3 * h, to: null }, { sid: 's-c', tab: 'Reviewer', kind: 'codex', step: K.d, from: now - 2 * h, to: null }],
    relations: [{ id: 'rel-1', kind: 'handoff', from: 's-a', to: 's-b', at: now - 3 * h, why: 'schema done', src: 'ev-1' }],
    tickets: [{ id: 'tk-1', kind: 'ask', step: K.d, tab: 'Reviewer', sid: 's-c', title: 'Log format?', reason: 'JSON or text?', rev: 'r1',
      options: [{ id: 'answer', label: 'Answer and let it go on', consequence: 'the step goes back to doing', needsText: true }, { id: 'answer-wait', label: 'Answer, keep it waiting', consequence: 'the step goes back to todo', needsText: true }], ifNone: 'it waits' },
    { id: 'tk-2', kind: 'approve', step: K.e, at: now - h, title: 'Approve the smoke check', reason: 'make smoke in /tmp/orbit', decision: 'Approve as proof', rev: 'r1', options: [{ id: 'opt-approve', label: 'Approve check' }] }],
    objectives: { rev: 'f1', items: [{ id: 'D1', text: 'Schema exists', doneWhen: 'tests pass', steps: [K.a, K.b] }, { id: 'D2', text: 'Runs nightly', doneWhen: 'timer fires', steps: [K.c] }] },
    evidence: { [K.a]: { required: { by: 'owner', text: 'test log' }, check: { text: 'go test passed', at: now - 4 * h, rev: 'r1', source: 'receipt' },
      assessment: { verdict: 'pass', why: 'log shows ok', by: 's-b', author: null, claimedSid: 's-b' }, items: [{ kind: 'log', title: 'test log', body: 'ok  orbit 0.4s' }] } },
    branches: [{ id: 'b-main', repo: 'orbit/core', name: 'main', from: now - 6 * h, commits: [{ at: now - 6 * h, sha: '1111aaa', msg: 'base' }, { at: now - 4 * h, sha: '2222bbb', msg: 'merge #10', kind: 'merge' }] },
      { id: 'b-orbit', repo: 'orbit/core', name: 'feature/orbit', parent: 'b-main', src: '1111aaa', from: now - 5 * h, pr: 11, into: 'b-main', intoState: 'open',
        commits: [{ at: now - 3 * h, sha: '3333ccc', msg: 'sync' }, { at: now - 1800, sha: '4444ddd', msg: 'wip', kind: 'unpushed' }] }],
    releases: [{ repo: 'orbit/core', primary: true, prod: { version: 'v1.4.0', sha: '5555eee', at: now - 20 * h, source: 'deploy: version endpoint', fetchedAt: now - 60 },
      stage: { version: 'v1.5.0-rc1', sha: '6666fff', at: now - 2 * h, source: 'deploy: version endpoint', fetchedAt: now - 60, prs: [10] }, preparing: { version: 'v1.5.0', note: 'release PR' },
      branch: { name: 'feature/orbit', head: '4444ddd', deployed: false } },
    { repo: 'orbit/tools', stage: { unknown: true, why: 'no deploy source', version: 'v0.2.0', at: now - 30 * h }, why: 'test-only repo' }],
    log: [{ id: 'ev-3', seq: 3, at: now - 3 * h, who: 'Worker', session: 's-b', kind: 'DECISION', step: K.d, title: 'Chose JSON lines', considered: [{ option: 'JSON', tradeoff: 'parseable' }, { option: 'text', tradeoff: 'readable' }], chose: { option: 'JSON' }, review: true, rev: 'lr1' },
      { id: 'ev-5', seq: 5, at: now - h, who: 'Worker', kind: 'ACTION', title: 'Ran the sync once' }, { id: 'ev-6', seq: 6, at: now - 600, who: 'Reviewer', kind: 'EVIDENCE', title: 'Smoke log attached' }],
    arts: [{ id: 'art-1', step: K.b, milestone: 'Schema', purpose: 'ui', title: 'Sync screen', kind: 'image', path: '/tmp/orbit.png', url: '/api/fs/raw?path=%2Ftmp%2Forbit.png', comments: 0, state: 'ok', source: 'PLAN.md', by: '' }], // page.arts as featv8arts.go serves it
    supervisor: { lastSample: now - 5, next: 'on the next panel sample' },
    manager: { thread: 'th-orbit', title: 'Orbit manager', bound: true },
  };
  return d;
}

let br; const errs = [], calls = [], reports = [];
// Expected HTTP failures: each one is registered by the scenario that causes it (an injected answer, or a real one the
// test provokes). A browser "Failed to load resource" line must match an unconsumed one by URL path AND status;
// anything else is an error, and an expectation never seen fails F-L6.
const expected = [];
const expectFail = (path, status, why) => expected.push({ path, status, why, seen: false });
function onConsole(m) {
  if (m.type() !== 'error') return;
  const f = /^Failed to load resource: (?:the server responded with a status of (\d+)|net::\w+)/.exec(m.text());
  if (f) { const path = (() => { try { return new URL(m.location().url).pathname; } catch { return '?'; } })(), st = f[1] ? +f[1] : 'net';
    const e = expected.find((x) => !x.seen && x.path === path && x.status === st); if (e) { e.seen = true; return; }
    return errs.push(`console: unexpected ${st} from ${path}`); }
  errs.push('console: ' + m.text());
}
const ans = {}; // route -> list of {status, body} to answer in turn (else 200 {})
const openCtx = []; // every context a test opened: closed after each test, pass or fail, so nothing of one test runs into the next
afterEach(async () => { for (const c of openCtx.splice(0)) await c.close().catch(() => {}); for (const k of Object.keys(ans)) delete ans[k]; });
async function fresh(opts = {}, data = fixture(), extra = null, realWrites = false, sess = S) {
  const { clock, ...copts } = opts; const c = await br.newContext({ viewport: { width: 1440, height: 1000 }, ...copts }); openCtx.push(c);
  await c.request.post(B + '/api/login', { data: { pin: '482913' } });
  const p = await c.newPage(); if (clock) await p.clock.install();
  p.on('pageerror', (e) => errs.push(String(e))); p.on('console', onConsole);
  if (data) await p.route((u) => u.pathname === '/api/term/feature', (r) => r.fulfill({ json: data })); // data null = the real record
  if (extra) await p.route((u) => /^\/api\/term\/feature\/(code|sessions|events)$/.test(u.pathname), (r) => { const v = extra[new URL(r.request().url()).pathname.split("/").pop()]; if (v == null) { const path = new URL(r.request().url()).pathname; expectFail(path, 404, 'route answered "not tracked" (404) by the scenario'); return r.fulfill({ status: 404, json: {} }); } return r.fulfill({ json: v }); });
  if (!realWrites) await p.route((u) => /^\/api\/term\/feature\/(seen|reviewed|message|ticket\/reply|report)$/.test(u.pathname), async (r) => {
    const path = new URL(r.request().url()).pathname; if (path.endsWith("report")) reports.push(path); calls.push({ path, body: JSON.parse(r.request().postData() || '{}'), t: Date.now() });
    const a = ans[path]?.shift() || { status: 200, body: path.endsWith('seen') ? { ok: true, frontier: 'ev-' + Math.max(...JSON.parse(r.request().postData()).ids.map((x) => +x.slice(3))), unseen: 0 } : { state: 'queued' } };
    if (a.wait) await a.wait; // a slow answer the scenario releases
    if (a.abort) { expectFail(path, 'net', 'connection dropped by the scenario'); return r.abort('failed'); }
    if (a.status >= 300) expectFail(path, a.status, 'answer injected by the scenario');
    r.fulfill({ status: a.status, json: a.body });
  });
  await p.goto(B + '/'); await p.waitForFunction(() => typeof setView === 'function');
  await p.evaluate(openFeatureJS(sess)); await p.waitForSelector(SEL.section('Plan and milestones'), { timeout: 15000 });
  await p.waitForSelector(data ? SEL.step(K.a) : '.steps li[data-key]', { state: 'attached' });
  return { c, p };
}
const posts = (k) => calls.filter((x) => x.path.endsWith(k));
const shot = (p, n) => p.screenshot({ path: `${E}/shots/v8fx-${n}.png` });

// Watched by the panel: its Chromium wrapper (browseragents.go) registers this browser, so the Control Panel's Browser
// lists it live (labelled with this pane's tab; its trail is kept by whichever panel holds the trails lock).
// Explicit: V8QA_WATCH=1 (else the plain binary, unseen); V8QA_WATCHED_CHROME overrides the wrapper's path. V8QA_HOLD=<minutes>:
// after the tests, the browser stays open on a "completed" page (and the scratch feature page) to look at, then closes.
const WATCHED = process.env.V8QA_WATCHED_CHROME || `${process.env.HOME}/.local/share/control-panel/browser-bin/chromium-1234/chrome-linux64/chrome`;
const watch = process.env.V8QA_WATCH === '1' && existsSync(WATCHED), KEY = `feature-page-ui-${new Date().toISOString().slice(0, 16).replace(/[-:T]/g, '')}`;
before(async () => { br = await launchChromium(watch ? { executablePath: WATCHED, env: { ...process.env, CP_BROWSER_LAUNCH: KEY } } : {});
  console.log(`# browser: ${watch ? 'watched by the panel, key ' + KEY : 'plain (not visible in the panel)'}`); });
const ran = []; afterEach((t) => { ran.push(t.name.slice(0, 120)); });
after(async () => { const hold = +process.env.V8QA_HOLD || 0;
  if (watch && hold && br) { const until = new Date(Date.now() + hold * 60e3).toISOString().slice(11, 16) + ' UTC';
    const c = await br.newContext({ viewport: { width: 1280, height: 900 } }); await c.request.post(B + '/api/login', { data: { pin: '482913' } });
    const f = await c.newPage(); await f.route((u) => u.pathname === '/api/term/feature', (r) => r.fulfill({ json: fixture() })); await f.goto(B + '/'); await f.waitForFunction(() => typeof setView === 'function').catch(() => {}); await f.evaluate(openFeatureJS(S)).catch(() => {});
    const d = await c.newPage(); await d.setContent(`<title>V8 UI proof: completed, idle</title><body style="font:16px system-ui;padding:2rem;background:#111;color:#eee"><h1>V8 UI proof session: completed (idle)</h1><p>Key <code>${KEY}</code> · tab Feature page design · scratch panel ${B} with fixture data (no production data, no real PIN).</p><p>Tests run in this browser:</p><ul>${ran.map((n) => `<li>${n.replace(/[<&]/g, '')}</li>`).join('')}</ul><p>Pass/fail: the run's suite.txt (${OUT}). The other tab is the scratch feature page, left open to look around.</p><p><b>Held open until ${until}</b>, then this browser closes; its trail stays in the Browser's replay.</p></body>`);
    console.log(`# browser held for the owner until ${until}`); await new Promise((r) => setTimeout(r, hold * 60e3)); }
  await br?.close(); });

test('F-L1 sections in the approved order; top geometry: header, then a full-width "Since you looked", then Team', async () => {
  const { c, p } = await fresh(); await sleep(400); await shot(p, '01-top');
  const got = await p.$$eval('section[aria-label]', (s) => s.map((x) => x.getAttribute('aria-label')).filter((x) => x));
  assert.deepEqual(got.filter((x) => SECTIONS.includes(x)), SECTIONS);
  const g = await p.evaluate(() => { const r = (s) => document.querySelector(s).getBoundingClientRect(); return { fv: r('.fv'), head: r('.fv-head'), since: r('section[aria-label="Since you looked"]'), team: r('section[aria-label="Team timeline"]') }; });
  assert.ok(g.head.bottom <= g.since.top + 1, 'header above Since you looked');
  assert.ok(Math.abs(g.since.width - g.fv.width) <= 2 && Math.abs(g.since.left - g.fv.left) <= 2, 'Since you looked is full width');
  assert.ok(g.team.top >= g.since.bottom - 1 && Math.abs(g.team.width - g.fv.width) <= 2, 'Team directly below, full width');
  await c.close();
});

test('F-D1 none of the prototype fixture strings; record content renders (bars, release strip, graph, links)', async () => {
  const { c, p } = await fresh(); await sleep(300);
  const txt = await p.evaluate(() => document.body.innerText);
  assert.deepEqual(FIXTURE_STRINGS.filter((s) => txt.includes(s)), []);
  assert.equal(await p.locator(SEL.laneBar).count(), 4);
  assert.equal(await p.locator('.ss.nofrom[data-sess="live:orbit:3:s-d"]').count(), 1, 'backend row id is the bar key; unknown start marked');
  assert.ok(!(await p.locator('#fv-team').innerText()).match(/1970|NaN|undefined/), 'no epoch/NaN from an unknown start');
  assert.equal(await p.locator('.ss[data-sess="s-a"].done').count(), 1, 'ended session styled done');
  assert.equal(await p.locator('.ss[data-sess="s-b"].open.st-processing').count(), 1, 'open session on a fresh sample is processing');
  assert.match(await p.locator(SEL.relCell('orbit/core', 'prod')).innerText(), /v1\.4\.0/);
  assert.match(await p.locator(SEL.relCell('orbit/core', 'br')).innerText(), /HEAD is not deployed/);
  assert.match(await p.locator(SEL.relCell('orbit/tools', 'stage')).innerText(), /unknown[\s\S]*no deploy source[\s\S]*last known v0\.2\.0/);
  assert.equal(await p.locator(SEL.branchRow).count(), 2);
  assert.equal(await p.locator('a.art[data-fvart="art-1"]').getAttribute('href'), '/api/fs/raw?path=%2Ftmp%2Forbit.png');
  await c.close();
});

test('F-D2 a stale sample shows open bars as unknown; no page at all says "not tracked"', async () => {
  const st = fixture(); st.page.observedAt = now - 3600;
  let { c, p } = await fresh({}, st);
  assert.equal(await p.locator('.ss.open.st-unknown').count(), 3, 'open bars unknown when stale');
  assert.match(await p.locator('.fv').innerText(), /May be out of date/);
  await c.close();
  ({ c, p } = await fresh({}, fixture(false), { code: { branches: null, releases: null, prx: null, at: 0 }, sessions: { sessions: null, relations: null, observedAt: 0 } }));
  const t = await p.locator('.fv').innerText();
  for (const re of [/Feature events are not tracked yet/, /Session history .* not tracked yet/, /Release versions are not tracked yet/, /Branch ancestry .* not tracked yet/]) assert.match(t, re);
  assert.deepEqual(FIXTURE_STRINGS.filter((s) => t.includes(s)), []);
  await c.close();
});

test('F-D3 ACTUAL API: no page object, the real /code and /sessions answers render honestly (none, not tracked or real rows)', async () => {
  const { c, p } = await fresh({}, fixture(false));
  await p.waitForFunction((s) => FVX[s]?.code && !FVX[s].code.refreshing && FVX[s].ev, S, { timeout: 20000 }); await sleep(400);
  const got = await p.evaluate(async (s) => Promise.all(['code', 'sessions', 'events'].map((k) => fetch(`/api/term/feature/${k}?session=${encodeURIComponent(s)}`).then((r) => r.json()))), S);
  console.log('# real /events:', JSON.stringify(got[2]).slice(0, 300));
  const since = await p.locator(SEL.section('Since you looked')).innerText();
  assert.ok(Array.isArray(got[2].events), 'events route answers {events, more}'); assert.doesNotMatch(since, /Feature events are not tracked yet/, 'real events route read, not the not-tracked fallback');
  if (!got[2].events.length) assert.match(since, /No events in this window/);
  console.log('# real /code:', JSON.stringify(got[0]).slice(0, 300)); console.log('# real /sessions:', JSON.stringify(got[1]).slice(0, 300));
  const team = await p.locator(SEL.section('Team timeline')).innerText(), code = await p.locator(SEL.section('Code delivery')).innerText();
  const ss = got[1].sessions;
  if (ss == null) assert.match(team, /not tracked yet/); else if (!ss.length) assert.match(team, /No sessions are recorded/); else assert.equal(await p.locator(SEL.laneBar).count(), ss.length);
  const B = got[0].branches; if (B == null) assert.match(code, /Branch ancestry .* not tracked yet/); else if (!B.length) assert.match(code, /No feature branches found/); else assert.equal(await p.locator(SEL.branchRow).count(), B.length);
  const Rl = got[0].releases; if (Rl == null) assert.match(code, /Release versions are not tracked yet/); else if (!Rl.length) assert.match(code, /No repository with a deploy source/); else assert.equal(await p.locator('.relr').count(), Rl.length);
  for (const t of [team, code]) assert.ok(!/1970|NaN|undefined|\[object/.test(t), 'no broken values: ' + t.match(/1970|NaN|undefined|\[object/));
  await shot(p, 'd3-actual-empty'); await c.close();
});

test('F-S1 nothing seen on open; a row on screen ≥1 s sends only its event id; 400 marks nothing, valid ids stay pending, bad id shown refused', async () => {
  calls.length = 0;
  const { c, p } = await fresh(); await p.evaluate(() => scrollTo(0, 0)); await sleep(700);
  assert.equal(posts('seen').length, 0, 'opening sent seen');
  assert.equal(await p.locator('[data-log].unseen').count(), 3, 'ev-3, ev-5, ev-6 above the frontier are unseen');
  ans['/api/term/feature/seen'] = [{ status: 400, body: { error: 'not this feature\'s events; nothing recorded', bad: [{ id: 'ev-6', reason: 'unknown' }] } }];
  await p.bringToFront(); await p.locator('#fv-log').scrollIntoViewIfNeeded(); await sleep(2600);
  const first = posts('seen')[0]; assert.ok(first, 'visible rows sent: ' + JSON.stringify(await p.evaluate(() => ({ f: document.hasFocus(), v: document.visibilityState, q: [...FV.seenQ], s: [...FV.sent], ds: [...document.querySelectorAll('[data-seen]')].map((e) => e.dataset.seen), y: scrollY, lt: document.querySelector('#fv-log')?.getBoundingClientRect().top }))));
  assert.ok(first.body.ids.every((x) => /^ev-\d+$/.test(x)), 'only event ids: ' + first.body.ids);
  assert.equal(await p.locator('[data-log].unseen').count(), 3, 'a refused batch marks nothing seen');
  assert.equal(await p.locator('[data-log="ev-6"][data-seen-refused="unknown"]').count(), 1, 'refused id shown with its reason');
  const q = await p.evaluate(() => [...FV.seenQ]); assert.ok(!q.includes('ev-6') && q.length === first.body.ids.length - 1, 'valid ids kept for retry: ' + q);
  await p.evaluate(() => fvFlushSeen()); await sleep(300);
  const second = posts('seen')[1]; assert.deepEqual(second.body.ids.sort(), q.sort(), 'retry sends the valid ids only');
  assert.equal(await p.locator('[data-log].unseen').count(), 1, 'after 200 only the refused row stays unseen');
  await c.close();
});

test('F-S2 reviewed is separate from seen: POST {id, rev}; 409 shows "changed since"', async () => {
  calls.length = 0; ans['/api/term/feature/reviewed'] = [{ status: 409, body: { changed: true } }];
  const { c, p } = await fresh(); await p.locator('[data-review="ev-3"]').click(); await sleep(300);
  assert.deepEqual(posts('reviewed')[0].body, { session: S, id: 'ev-3', rev: 'lr1' });
  assert.match(await p.locator('[data-log="ev-3"]').innerText(), /changed since/);
  await c.close();
});

test('F-T1 tickets: an option posts its server id + rev + sid; a text option needs text; free text alone is not sendable; refusals show on the ticket', async () => {
  calls.length = 0; const { c, p } = await fresh();
  await p.locator(`${SEL.needsItem}[data-q="tk-1"]`).click();
  await p.locator('[data-reply="tk-1"]').fill('Which one is the house style?'); await p.locator('[data-send="tk-1"]').click(); await sleep(300);
  assert.equal(posts('ticket/reply').length, 0, 'free text is not an answer'); const mt = posts('message')[0].body;
  assert.deepEqual({ a: mt.anchor.a, kind: mt.anchor.kind, rev: mt.anchor.rev, thread: mt.thread, text: mt.text }, { a: 'tk-1', kind: 'ticket', rev: 'r1', thread: 'th-orbit', text: 'Which one is the house style?' });
  await p.locator('[data-reply="tk-1"]').fill('');
  await p.locator('[data-opt="answer"]').click(); await sleep(300);
  assert.equal(posts('ticket/reply').length, 0, 'a text option sent without text'); assert.match(await p.locator('#needs [role=alert]').innerText(), /Write your answer first/);
  await p.locator('[data-reply="tk-1"]').fill('JSON, one per line'); await p.locator('[data-opt="answer"]').click(); await sleep(300);
  assert.deepEqual(posts('ticket/reply')[0].body, { session: S, ticket: 'tk-1', option: 'answer', text: 'JSON, one per line', rev: 'r1', sid: 's-c' });
  ans['/api/term/feature/ticket/reply'] = [{ status: 409, body: { error: 'the agent on @Builder was replaced since: look again', sid: 's-z' } }];
  await p.locator(`${SEL.needsItem}[data-q="tk-2"]`).click();
  await p.locator('[data-opt="opt-approve"]').click(); await sleep(400);
  assert.deepEqual(posts('ticket/reply')[1].body, { session: S, ticket: 'tk-2', option: 'opt-approve', rev: 'r1' });
  assert.match(await p.locator('#needs [role=alert]').innerText(), /Not done: the agent on @Builder was replaced/);
  await c.close();
});

test('F-P1 proof and objective links: objective → its job proof (three results, author unknown); milestone list → objective', async () => {
  const { c, p } = await fresh();
  await p.locator('[data-fvpt="obj"]').click(); await p.locator(SEL.objective('D1')).waitFor();
  await p.locator(`${SEL.objective('D1')} [data-proof-go="${K.a}"]`).first().click(); await p.locator('.pf').waitFor();
  const pf = await p.locator('.pf').innerText();
  for (const re of [/Check result · receipt/i, /go test passed/, /Model assessment/i, /author unknown: independence not claimed/i, /Owner attestation/i, /test log/]) assert.match(pf, re);
  await p.locator(`.ms[data-ms="M1"] [data-mso="M1"] > summary`).click(); await p.locator(`${SEL.msObjList('M1')} [data-obj-go="D1"]`).click();
  await p.locator(SEL.objective('D1')).waitFor(); assert.equal(await p.locator('[data-fvpt="obj"][aria-selected="true"]').count(), 1);
  await c.close();
});

const SHA = (c) => c.repeat(64), NOW = Math.floor(Date.now() / 1000);
const pfOpen = async (p, k) => { await p.evaluate((k) => { FV.vb = k; render(); }, k); const li = p.locator(`[data-key="${k}"]`).first(); if (!(await li.isVisible())) { await p.evaluate((k) => document.querySelector(`[data-key="${k}"]`)?.closest('.ms')?.querySelector('summary, [data-msx], button[aria-expanded="false"]')?.click(), k); }
  const pf = p.locator(`[data-key="${k}"] .pf`); await pf.waitFor({ state: 'attached' }); return pf; };
test('F-P2 FIXTURE proof dialog, R1/R2 shapes: every item renders (no blank figure); line-only = unverified claim; seal/current/provenance as integrity only; stale, missing and refused visible, refused paths never named; a Bash mention is a correlation; criterion clauses + missing exact; a stale attestation is not evidenced; no external fetch', async () => {
  const d = fixture(), ev = d.page.evidence, ext = [];
  ev[K.a] = { required: { by: 'model', text: 'run — output contains `ok`', kind: 'run', criterion: 'output contains `ok`', mechanical: ['exit 0', 'output contains `ok`'] }, criterion: { met: true, how: 'mechanical check' },
    check: { source: 'receipt', text: 'make check exited 0' }, claim: 'link https://example.com/report',
    items: [{ id: 'i1', line: 'run make check — ok' },
      { id: 'i2', kind: 'file', ref: 'work/ledger.txt', path: '/tmp/orbit-rec/work/ledger.txt', seal: { sha256: SHA('a'), size: 12, firstSeen: NOW - 3 * h, submittedBy: 'writer' }, current: { how: 'hashed', sha256: SHA('a'), checkedAt: NOW - 60 }, provenance: { tool: 'Write', toolUseId: 'tu1', bytesMatch: true, at: '2026-11-08T10:00:00Z', transcript: '/home/u/.claude/projects/x/t.jsonl' } },
      { id: 'i3', line: 'link https://example.com/report' },
      { id: 'i4', kind: 'file', ref: 'work/old.txt', path: '/tmp/orbit-rec/work/old.txt', seal: { sha256: SHA('b'), size: 5, firstSeen: NOW - 2 * h, submittedBy: 'look' }, current: { how: 'hashed', sha256: SHA('c') }, stale: true, staleWhy: 'bytes changed since sealed', provenanceNone: true, mention: { tool: 'Bash', at: '2026-11-08T11:00:00Z' } },
      { id: 'i5', kind: 'file', ref: 'work/gone.txt', refused: 'missing', seal: { sha256: SHA('d'), size: 3, firstSeen: NOW - h }, stale: true, staleWhy: 'missing since sealed' },
      { id: 'i6', kind: 'file', ref: '/home/u/.ssh/id_rsa', refused: 'outside the feature\'s folders' },
      { id: 'i7', kind: 'image', ref: 'shot.png', url: 'https://cdn.example.com/shot.png' },
      { id: 'i8', kind: 'image', ref: 'local.png', path: '/tmp/orbit-rec/local.png', current: { how: 'unchecked', why: 'budget' }, provenanceNone: true }] };
  ev[K.b] = { required: { by: 'model', text: 'run — output contains `ledger.txt: ok`', kind: 'run', criterion: 'output contains `ledger.txt: ok`', mechanical: ['exit 0', 'output contains `ledger.txt: ok`'] },
    criterion: { met: false, how: 'mechanical check', missing: ['output contains `ledger.txt: ok`'] }, claim: 'run make check',
    items: [{ id: 'j1', kind: 'file', ref: 'work/ledger.txt', path: '/tmp/orbit-rec/work/ledger.txt', current: { how: 'stat-unchanged', sha256: SHA('a') }, provenanceNone: true }, { id: 'j2', line: 'run make check' }] };
  ev[K.c] = { required: { by: 'owner', text: 'the nightly log shows a run', mechanical: null }, criterion: { met: null, how: 'needs your attestation' },
    attest: { text: 'owner verified 2026-11-08 09:00', at: NOW - h }, staleWhy: 'evidence changed since you attested: ledger.txt isn\'t the bytes you saw', claim: 'file work/ledger.txt',
    items: [{ id: 'k1', kind: 'file', ref: 'work/ledger.txt', path: '/tmp/orbit-rec/work/ledger.txt', current: { how: 'hashed', sha256: SHA('e') } }] };
  const { c, p } = await fresh({}, d); p.on('request', (r) => { if (!r.url().startsWith(B)) ext.push(r.url()); });
  // K.a: 8 items in order, none blank
  const pa = await pfOpen(p, K.a), figs = pa.locator('.pfi');
  assert.equal(await figs.count(), 8, 'every item drawn');
  for (let i = 0; i < 8; i++) { const f = figs.nth(i), cap = (await f.locator('figcaption').innerText()).trim(); assert.ok(cap.length, `item ${i} caption not blank`); assert.match(await f.innerText(), /claimed by its writer/, `item ${i} is a claim`); }
  const fig = (i) => figs.nth(i).innerText();
  assert.match(await fig(0), /run make check — ok[\s\S]*hasn't checked this line/);
  assert.match(await fig(1), /sealed sha256 aaaaaaaaaa…[\s\S]*integrity only: the same bytes since then, not that they are correct/); assert.match(await fig(1), /now sha256 aaaaaaaaaa… \(hashed/);
  assert.match(await fig(1), /written by an agent's Write at 2026-11-08T10:00:00Z \(same bytes as sealed\) — where it came from, not that it is right/); assert.doesNotMatch(await fig(1), /t\.jsonl/, 'transcript path not shown');
  assert.match(await fig(2), /link https:\/\/example\.com\/report[\s\S]*hasn't checked/); assert.equal(await figs.nth(2).locator('a').count(), 0, 'a line-only link is text, not a link');
  assert.match(await fig(3), /stale: bytes changed since sealed/); assert.match(await fig(3), /no recorded agent write of it after its criterion was set/); assert.match(await fig(3), /a Bash command named it at 2026-11-08T11:00:00Z: a correlation only, not provenance or proof/);
  assert.match(await fig(4), /work\/gone\.txt[\s\S]*missing: not found in the feature's folders[\s\S]*stale: missing since sealed/);
  assert.match(await fig(5), /not read: outside the feature's folders/); assert.doesNotMatch(await pa.innerText(), /id_rsa|\.ssh/, 'refused path never named');
  assert.equal(await figs.nth(6).locator('img').count(), 0, 'an external image is never fetched'); assert.match(await fig(6), /https:\/\/cdn\.example\.com\/shot\.png/);
  assert.equal(await figs.nth(7).locator('img').count(), 0, 'unchecked local image not loaded'); assert.match(await fig(7), /bytes now not checked: budget/);
  assert.match(await pa.locator('[data-fvcrit]').innerText(), /^Criterion met by the mechanical check, which checks only: exit 0; output contains `ok`\. That is not proof the objective holds\.$/);
  assert.doesNotMatch(await pa.innerText(), /\bverified\b/i);
  // K.b: file then run line (order kept); criterion not met, missing named; claimed only
  const pb = await pfOpen(p, K.b), fb = pb.locator('.pfi');
  assert.equal(await fb.count(), 2); assert.match(await fb.nth(0).innerText(), /ledger\.txt[\s\S]*not sealed/); assert.match(await fb.nth(1).innerText(), /run make check/);
  assert.match(await pb.locator('[data-fvcrit]').innerText(), /Criterion not met by the mechanical check, which checks only: exit 0; output contains `ledger\.txt: ok`\. That is not proof the objective holds\. Missing: output contains `ledger\.txt: ok`\./);
  assert.match(await pb.locator('.pf-h').innerText(), /claimed · not evidenced/);
  // K.c: an attestation object with a staleWhy is not evidence
  const pc = await pfOpen(p, K.c);
  assert.match(await pc.locator('.pf-h').innerText(), /claimed · not evidenced/); assert.doesNotMatch(await pc.locator('.pf-h').innerText(), /evidence-backed/);
  assert.match(await pc.innerText(), /evidence changed since you attested: ledger\.txt isn't the bytes you saw/);
  assert.match(await pc.locator('[data-fvcrit]').innerText(), /no part of it is checked mechanically; only your attestation meets it/);
  assert.equal(await p.locator(`[data-fvvb="${K.c}"].r-check`).count(), 0, 'its badge is not "proof"');
  assert.deepEqual(ext, [], 'no request left the panel');
  await c.close();
});

test('F-A1 FIXTURE artifacts: a milestone\'s own and a job\'s show in their milestone; purposes honest; missing/refused/unpreviewable inert with why and no href; https opens only on click, host shown, noreferrer; counts usable vs unavailable; ambiguous/unknown placed apart', async () => {
  const d = fixture(); d.page.arts = [
    { id: 'art-1', milestone: 'Schema', purpose: 'ui', title: 'Schema page', kind: 'html', path: '/tmp/orbit-rec/s.html', url: '/api/fs/raw?path=%2Ftmp%2Forbit-rec%2Fs.html&review=1', comments: 2, state: 'ok', source: 'PLAN.md', by: '' },
    { id: 'art-2', step: K.b, milestone: 'Schema', purpose: 'issue', title: 'Sync bug', kind: 'image', path: '/tmp/orbit-rec/b.png', url: '/api/fs/raw?path=%2Ftmp%2Forbit-rec%2Fb.png', comments: 0, state: 'ok', source: 'PLAN.md', by: '' },
    { id: 'art-3', step: K.c, milestone: 'Ship', purpose: 'diagram', title: 'Timer flow', kind: 'link', url: 'https://docs.example.com/flow', comments: 0, state: 'link', source: 'PLAN.md', by: '' },
    { id: 'art-4', step: K.c, milestone: 'Ship', purpose: '', title: '', kind: '', comments: 0, state: 'missing', why: 'the file isn\'t there', source: 'PLAN.md', by: '' },
    { id: 'art-5', milestone: 'Ship', purpose: 'ui', title: 'Secret', kind: '', comments: 0, state: 'refused', why: 'not a file this feature may show', source: 'PLAN.md', by: '' },
    { id: 'art-6', step: K.e, milestone: 'Ship', purpose: 'ui', title: 'Big binary', kind: 'binary', path: '/srv/x.bin', comments: 0, state: 'ok', source: 'PLAN.md', by: '' },
    { id: 'art-7', milestone: 'Nowhere', purpose: 'ui', title: 'Orphan', kind: 'link', url: 'https://x.example.org/', comments: 0, state: 'link', source: 'PLAN.md', by: '' }];
  const { c, p } = await fresh({}, d); const ext = []; p.on('request', (r) => { if (!r.url().startsWith(B)) ext.push(r.url()); });
  await p.evaluate(() => { FV.open = new Set(['M1', 'M2']); render(); });
  const A = (id) => p.locator(`[data-fvart="${id}"]`), inM = async (id, m) => assert.equal(await p.locator(`.ms[data-ms="${m}"] [data-fvart="${id}"]`).count(), 1, `${id} in ${m}`);
  await inM('art-1', 'M1'); await inM('art-2', 'M1'); await inM('art-3', 'M2'); await inM('art-4', 'M2'); await inM('art-5', 'M2'); await inM('art-6', 'M2');
  assert.equal(await p.locator('.arts-un [data-fvart="art-7"]').count(), 1, 'an unknown milestone is placed apart, not hidden');
  assert.match(await A('art-1').innerText(), /UI design[\s\S]*Schema page[\s\S]*html · 2 comments/i); assert.equal(await A('art-1').getAttribute('href'), '/api/fs/raw?path=%2Ftmp%2Forbit-rec%2Fs.html&review=1');
  assert.match(await A('art-2').innerText(), /Issue explanation/i); assert.doesNotMatch(await A('art-2').innerText(), /comment/, 'no invented count');
  assert.match(await A('art-3').innerText(), /diagram[\s\S]*link · opens docs\.example\.com · not fetched until you open it/i); assert.equal(await A('art-3').getAttribute('rel'), 'noopener noreferrer');
  for (const [id, re] of [['art-4', /no purpose given[\s\S]*\(untitled\)[\s\S]*unavailable: the file isn't there/i], ['art-5', /unavailable: not a file this feature may show/], ['art-6', /unavailable: the Files preview doesn't serve this file/]]) {
    assert.match(await A(id).innerText(), re); assert.equal(await A(id).evaluate((e) => e.tagName), 'DIV', id + ' is inert'); assert.equal(await A(id).getAttribute('href'), null); }
  assert.doesNotMatch(await p.locator(SEL.section('Plan and milestones')).innerText(), /\/srv\/x\.bin/, 'no path echoed');
  assert.match(await p.locator('.p-h .src').innerText(), /4 visual artifacts \(3 unavailable\)/);
  await A('art-5').click(); await A('art-4').click(); assert.equal(c.pages().length, 1, 'inert artifacts open nothing'); assert.deepEqual(ext, [], 'no external request before a click');
  await c.route('https://docs.example.com/**', (r) => r.fulfill({ body: 'stub' })); const [np] = await Promise.all([c.waitForEvent('page'), A('art-3').click()]); await np.waitForLoadState().catch(() => {});
  assert.equal(np.url(), 'https://docs.example.com/flow', 'opens only on click'); await np.close();
  await c.close();
});

test('F-L8 FIXTURE work-log authored fields: ran reported (✓/✗/?) and labelled not executed by the panel; ranUnparsed as written; next with its whitespace; objectives verified vs claimed; pr known vs claimed; proof claimed; session claimed; no injection', async () => {
  const d = fixture(); d.page.log = [{ id: 'ev-90', seq: 90, at: now - 60, who: 'Worker', kind: 'ACTION', title: 'build  <b>two</b>  spaces', rev: 'r1', session: 's-x', sessionClaimed: true,
    ran: [{ ok: true, cmd: 'go test ./...', result: 'PASS\nok  orbit' }, { ok: false, cmd: 'make lint', result: 'exit 2' }, { ok: null, cmd: 'make smoke', result: '' }], ranUnparsed: ['bogus <script>x</script>'],
    next: 'then\tdeploy\n  carefully', objectives: ['D1'], objectivesClaimed: ['Z9'], pr: 11, prKnown: true, proofStepClaimed: 'nostep' },
    { id: 'ev-91', seq: 91, at: now - 30, who: 'Worker', kind: 'ACTION', title: 'other', rev: 'r1', pr: 77, prKnown: false }];
  const { c, p } = await fresh({}, d); const row = p.locator('[data-log="ev-90"]'); await row.scrollIntoViewIfNeeded(); const t = await row.innerText();
  assert.match(t, /✓ go test \.\/\.\.\. → PASS\nok  orbit/); assert.match(t, /✗ make lint → exit 2/); assert.match(t, /\? make smoke/);
  assert.match(t, /ran: as reported by the writer, not executed or verified by the panel/); assert.match(t, /ran\?\s*bogus <script>x<\/script>/);
  assert.match(t, /next\s*then\tdeploy\n  carefully/, 'whitespace kept'); assert.match(t, /build  <b>two<\/b>  spaces/, 'title kept as text');
  assert.equal(await row.locator('b, script, img').count(), 0, 'nothing authored becomes markup');
  assert.equal(await row.locator('[data-obj-go="D1"]').count(), 1); assert.match(t, /claims Z9/); assert.equal(await row.locator('[data-obj-go="Z9"]').count(), 0);
  assert.match(t, /#11/); assert.doesNotMatch(t, /claims #11/); assert.match(t, /claims proof for nostep/); assert.match(t, /s-x \(claimed\)/);
  assert.match(await p.locator('[data-log="ev-91"]').innerText(), /claims #77/); assert.equal(await row.locator('a').count(), 0, 'no link from authored text');
  await c.close();
});

test('F-C1 PR filters (state, failing CI, repo), real links, row drills in place', async () => {
  const { c, p } = await fresh(); const nums = () => p.$$eval(SEL.prRow, (r) => r.map((x) => +x.dataset.open));
  assert.deepEqual(await nums(), [11, 10, 9]);
  await p.locator('[data-prs="merged"]').click(); assert.deepEqual(await nums(), [10]);
  await p.locator('[data-prs="all"]').click(); await p.locator(SEL.prFailing).check(); assert.deepEqual(await nums(), [11]);
  await p.locator(SEL.prFailing).uncheck(); await p.locator(SEL.prRepo).selectOption('orbit/tools'); assert.deepEqual(await nums(), [9]);
  assert.equal(await p.locator('tr.pr[data-open="9"] a').first().getAttribute('href'), 'https://github.com/orbit/tools/pull/9');
  await p.locator('tr.pr[data-open="9"] td').nth(1).click(); assert.equal(await p.locator('tr.drill').count(), 1, 'drill row opened, no pane');
  await c.close();
});

test('F-X1 right-click → manager chat at the click with anchor@rev; 409 rebind holds and confirms; 404 failed; Esc returns focus; Shift keeps native menu', async () => {
  calls.length = 0; const { c, p } = await fresh();
  const li = p.locator(SEL.step(K.b)); await li.scrollIntoViewIfNeeded(); const bb = await li.boundingBox();
  await p.mouse.click(bb.x + 40, bb.y + 8, { button: 'right' }); const dlg = p.locator(SEL.chat); await dlg.waitFor();
  const db = await dlg.boundingBox(), vp = p.viewportSize();
  assert.ok(db.x >= 8 && db.y >= 8 && db.x + db.width <= vp.width - 8 && db.y + db.height <= vp.height - 8, 'inside the viewport');
  assert.ok(Math.abs(db.x - (bb.x + 44)) < 6 || Math.abs(db.x + db.width - (bb.x + 36)) < 6, 'opens at the click');
  ans['/api/term/feature/message'] = [{ status: 409, body: { error: 'the feature\'s manager is now th-new: choose to send it there', thread: 'th-new' } }, { status: 200, body: { state: 'delivered' } }];
  await p.locator(SEL.chatText).fill('Why is the sync job slow?'); await p.locator(SEL.chatSend).click(); await p.locator(SEL.chatConfirm).waitFor();
  const m1 = posts('message')[0].body;
  assert.match(m1.clientId, /^[A-Za-z0-9_-]{1,64}$/); assert.equal(m1.thread, 'th-orbit'); assert.equal(m1.text, 'Why is the sync job slow?');
  assert.deepEqual(Object.keys(m1).sort(), ['anchor', 'clientId', 'session', 'text', 'thread']); assert.deepEqual(Object.keys(m1.anchor).sort(), ['a', 'kind', 'label', 'rev']);
  assert.equal(m1.anchor.a, 'job:' + K.b); assert.equal(m1.anchor.rev, 'r1');
  assert.match(await p.locator(SEL.chatConfirm).innerText(), /Send it to th-new instead/);
  await p.locator(SEL.chatConfirm).click(); await p.locator('.ctxchat .ss6.delivered').waitFor();
  const m2 = posts('message')[1].body; assert.notEqual(m2.clientId, m1.clientId, 'a readdressed message is a new message, not the old one rerouted'); assert.equal(m2.thread, 'th-new'); assert.equal(m2.text, m1.text, 'owner text unchanged');
  await p.keyboard.press('Escape'); assert.equal(await dlg.count(), 0);
  // keyboard: Shift+F10 on a focused step, Esc returns focus to it
  await li.focus(); await p.keyboard.press('Shift+F10'); await dlg.waitFor();
  ans['/api/term/feature/message'] = [{ status: 404, body: {} }];
  await p.locator(SEL.chatText).fill('ping'); await p.locator(SEL.chatSend).click(); await p.locator('.ctxchat .ss6.failed').waitFor();
  await p.keyboard.press('Escape'); assert.equal(await p.evaluate((k) => document.activeElement?.dataset.key, K.b), K.b, 'focus returned');
  const native = await p.evaluate((k) => { const el = document.querySelector(`[data-key="${k}"]`); const e = new MouseEvent('contextmenu', { bubbles: true, cancelable: true, shiftKey: true, clientX: 50, clientY: 50 }); el.dispatchEvent(e); return !e.defaultPrevented && !document.querySelector('.ctxchat'); }, K.b);
  assert.ok(native, 'Shift+right-click keeps the browser menu');
  await c.close();
});

test('F-X3 FIXTURE uncertainty is kept: an accepted message later held offers no new-manager send and blocks Send; an uncertain 500 is unknown and Retry resends the identical payload and id', async () => {
  calls.length = 0; const { c, p } = await fresh(); const open = async () => { const li = p.locator(SEL.step(K.b)); await li.scrollIntoViewIfNeeded(); await li.click({ button: 'right', position: { x: 30, y: 8 } }); await p.locator(SEL.chat).waitFor(); };
  ans['/api/term/feature/message'] = [{ status: 200, body: { ok: true, state: 'queued' } }, { status: 200, body: { ok: true, state: 'held', reason: 'its thread no longer manages this feature (now th-new)' } }];
  await open(); await p.locator(SEL.chatText).fill('first'); await p.locator(SEL.chatSend).click(); await p.locator('.ctxchat .ss6.held').waitFor({ timeout: 8000 });
  assert.equal(await p.locator(SEL.chatConfirm).count(), 0, 'no readdress for an accepted message'); assert.ok(await p.locator(SEL.chatSend).isDisabled(), 'Send blocked while the first may still arrive');
  assert.match(await p.locator('.ctxchat').innerText(), /may still arrive/);
  await p.keyboard.press('Escape'); calls.length = 0;
  ans['/api/term/feature/message'] = [{ status: 500, body: { error: 'queued for the lead, but its record wasn\'t saved (a retry may send it twice): disk' } }, { status: 200, body: { ok: true, state: 'queued' } }, { status: 200, body: { ok: true, state: 'delivered' } }];
  await open(); await p.locator(SEL.chatText).fill('second'); assert.ok(await p.locator(SEL.chatSend).isDisabled(), 'other words on the same item: Send off while the first may arrive');
  await p.locator('.ctxchat [data-csep]').click(); await p.locator(SEL.chatSend).click(); await p.locator('.ctxchat .ss6.unknown').waitFor();
  assert.ok(await p.locator(SEL.chatSend).isDisabled(), 'no new message while unknown'); await p.locator(SEL.chatText).fill('second, edited');
  await p.locator(SEL.chatRetry).click(); await sleep(300);
  const m = calls.filter((x) => x.path.endsWith('message') && x.body.clientId); assert.equal(m.length, 2); assert.equal(m[1].body.clientId, m[0].body.clientId); assert.deepEqual(m[1].body, m[0].body, 'Retry resends exactly what may have been accepted');
  await c.close();
});

const M8 = '/api/term/feature/message';
const msgCalls = () => calls.filter((x) => x.path === M8);
const openOn = async (p, k) => { const li = p.locator(SEL.step(k)); await li.scrollIntoViewIfNeeded(); await li.click({ button: 'right', position: { x: 30, y: 8 } }); await p.locator(SEL.chat).waitFor(); };

test('F-U3 FIXTURE closing the chat never drops a message that may be in flight: reopen reattaches it and shows its late outcome; the same words can\'t be sent again; other words only as an explicit separate message; it survives a reload', async () => {
  calls.length = 0; ans[M8] = []; const { c, p } = await fresh(); let release; const gate = new Promise((r) => (release = r));
  ans[M8].push({ status: 200, body: { ok: true, state: 'queued' }, wait: gate });
  await openOn(p, K.b); await p.locator(SEL.chatText).fill('pending words'); await p.locator(SEL.chatSend).click(); await p.locator('.ctxchat .ss6.sending').waitFor();
  const id0 = msgCalls()[0].body.clientId; await p.keyboard.press('Escape'); assert.equal(await p.locator(SEL.chat).count(), 0);
  await openOn(p, K.b); assert.equal(await p.locator('.ctxchat .ss6.sending').count(), 1, 'reopened chat shows the in-flight message'); assert.match(await p.locator('.ctxchat details').textContent(), new RegExp(id0));
  await p.locator(SEL.chatText).fill('pending words'); assert.ok(await p.locator(SEL.chatSend).isDisabled(), 'same words blocked');
  await p.locator(SEL.chatText).fill('other words'); assert.ok(await p.locator(SEL.chatSend).isDisabled(), 'other words on the same item: ordinary Send stays off');
  assert.match(await p.locator('.ctxchat .fvc-sep').innerText(), /not confirmed yet and may still arrive/);
  release(); await p.locator('.ctxchat .ss6.queued').waitFor(); // the late answer is shown, not lost
  await p.keyboard.press('Escape'); await openOn(p, K.a);
  assert.match(await p.locator('.ctxchat .fvc-pend').innerText(), /pending words[\s\S]*queued/, 'listed when the chat opens elsewhere');
  await p.keyboard.press('Escape'); await p.reload(); await p.waitForFunction(() => typeof setView === 'function'); await p.evaluate(openFeatureJS(S)); await p.waitForSelector(SEL.step(K.a), { state: 'attached' });
  await openOn(p, K.a); assert.match(await p.locator('.ctxchat .fvc-pend').innerText(), /pending words/, 'kept across a reload');
  assert.equal(msgCalls().filter((x) => x.body.clientId).length, 1, 'never sent twice'); await c.close();
});

test('F-U3b FIXTURE QA U3 exact: POST 500 (unknown, may arrive) → Esc → reopen the SAME step → type different text: ordinary Send is off and posts nothing; only the explicit "Send a separate message…", shown with the prior uncertainty, sends a NEW id; a ticket behaves the same', async () => {
  calls.length = 0; ans[M8] = [{ status: 500, body: { error: 'queued for the lead, but its receipt wasn\'t saved (a retry may send it twice): disk' } }]; const { c, p } = await fresh();
  await openOn(p, K.b); await p.locator(SEL.chatText).fill('first words'); await p.locator(SEL.chatSend).click(); await p.locator('.ctxchat .ss6.unknown').waitFor();
  const id0 = msgCalls()[0].body.clientId; await p.keyboard.press('Escape'); await openOn(p, K.b);
  assert.equal(await p.locator('.ctxchat .ss6.unknown').count(), 1, 'reopen reattaches the unresolved message'); assert.match(await p.locator('.ctxchat details').textContent(), new RegExp(id0));
  await p.locator(SEL.chatText).pressSequentially('completely different text'); assert.ok(await p.locator(SEL.chatSend).isDisabled(), 'different words do not re-enable Send');
  await p.locator(SEL.chatSend).click({ force: true }); await p.keyboard.press('Control+Enter'); await sleep(300);
  assert.equal(msgCalls().filter((x) => x.body.clientId).length, 1, 'nothing new posted by Send while the first may still arrive');
  assert.match(await p.locator('.ctxchat .fvc-sep').innerText(), /earlier message about this item is not confirmed yet and may still arrive/);
  await p.locator('.ctxchat [data-csep]').click(); assert.match(await p.locator('.ctxchat .fvc-sep').innerText(), /second, separate message[\s\S]*lead may get both/);
  assert.equal(await p.locator(SEL.chatSend).innerText(), 'Send separate message'); assert.equal(await p.locator(SEL.chatText).inputValue(), 'completely different text', 'owner text kept');
  await p.locator(SEL.chatSend).click(); await sleep(400); const m = msgCalls().filter((x) => x.body.clientId);
  assert.equal(m.length, 2); assert.notEqual(m[1].body.clientId, id0, 'a separate message has its own id'); assert.equal(m[1].body.text, 'completely different text');
  assert.ok(await p.locator(SEL.chatSend).isDisabled(), 'and Send is off again: two now unconfirmed'); await p.keyboard.press('Escape');
  // the ticket composer: same rule
  const tk = p.locator('[data-reply]').first(); const tid = await tk.getAttribute('data-reply'); ans[M8] = [{ status: 500, body: { error: 'disk' } }];
  await tk.scrollIntoViewIfNeeded(); await tk.fill('ticket words'); await p.locator(`[data-send="${tid}"]`).click(); await p.locator('.fv-tkmsg .ss6.unknown').waitFor();
  await p.locator(`[data-reply="${tid}"]`).fill('other ticket words'); assert.ok(await p.locator(`[data-send="${tid}"]`).isDisabled(), 'ticket: ordinary send off');
  const n = msgCalls().filter((x) => x.body.clientId).length; await p.locator(`[data-send="${tid}"]`).click({ force: true }); await sleep(200); assert.equal(msgCalls().filter((x) => x.body.clientId).length, n);
  await p.locator(`[data-tksep="${tid}"]`).click(); await p.locator(`[data-reply="${tid}"]`).fill('other ticket words'); assert.equal(await p.locator(`[data-send="${tid}"]`).innerText(), 'Send separate message');
  await p.locator(`[data-send="${tid}"]`).click(); await sleep(300); const t2 = msgCalls().filter((x) => x.body.clientId).at(-1); assert.equal(t2.body.text, 'other ticket words'); assert.equal(msgCalls().filter((x) => x.body.clientId).length, n + 1);
  await c.close();
});

test('F-U8 FIXTURE outbox capacity: 50 unresolved messages survive a reload uncut (resolved ones trimmed first); the 51st is refused with why and nothing is posted; a legacy store with 52 unresolved keeps all 52', async () => {
  calls.length = 0; const { c, p } = await fresh(); const f = await p.evaluate(() => FV.M.name);
  const seed = (open, done) => p.evaluate(([f, open, done]) => { const now = Date.now(), mk = (i, st) => ({ f, src: 'chat', anchorA: 'seed-' + i, id: `ow-seed-${st}-${i}`, text: `seed ${st} ${i}`, state: st === 'open' ? 'unknown' : 'delivered', maybe: st === 'open', thread: 'th-x', at: now - 1000, sent: { session: f, thread: 'th-x', text: `seed ${i}`, anchor: { a: 'seed-' + i, kind: 'step', rev: '' } } });
    sessionStorage.setItem('fv.out', JSON.stringify([...Array.from({ length: done }, (_, i) => mk(i, 'done')), ...Array.from({ length: open }, (_, i) => mk(i, 'open'))])); }, [f, open, done]);
  const reload = async () => { await p.reload(); await p.waitForFunction(() => typeof setView === 'function'); await p.evaluate(openFeatureJS(S)); await p.waitForSelector(SEL.step(K.a), { state: 'attached' }); };
  await seed(50, 10); await reload();
  assert.equal(await p.evaluate(() => FV.out.filter((h) => h.maybe).length), 50, 'all 50 unresolved kept');
  await openOn(p, K.a); await p.locator(SEL.chatText).fill('one more'); assert.ok(await p.locator(SEL.chatSend).isDisabled(), '51st refused');
  assert.match(await p.locator('.ctxchat').innerText(), /50 messages from this tab are not confirmed yet\. None is dropped/);
  const n = msgCalls().filter((x) => x.body.clientId).length; await p.locator(SEL.chatSend).click({ force: true }); await sleep(200); assert.equal(msgCalls().filter((x) => x.body.clientId).length, n, 'nothing posted');
  await p.evaluate(() => fvOutSave()); const st = await p.evaluate(() => JSON.parse(sessionStorage.getItem('fv.out')));
  assert.equal(st.filter((h) => h.maybe).length, 50, 'saved store keeps all 50 unresolved'); assert.equal(st.filter((h) => !h.maybe).length, 0, 'resolved trimmed first');
  await p.keyboard.press('Escape'); await seed(52, 3); await reload(); await p.evaluate(() => fvOutSave());
  assert.equal(await p.evaluate(() => JSON.parse(sessionStorage.getItem('fv.out')).filter((h) => h.maybe).length), 52, 'never evicts unresolved, even over capacity');
  await c.close();
});

test('F-D7 FIXTURE worktree ahead: checked:false is "not measured yet", never "in main worktree" or 0 ahead; measured 0 and 3 render as measured', async () => {
  const d = fixture(); d.trees = [{ root: '/tmp/orbit', branch: 'feature/orbit', main: false }, { root: '/tmp/orbit-b', branch: 'feature/orbit-b', checked: true, ahead: 0 }, { root: '/tmp/orbit-c', branch: 'feature/orbit-c', checked: true, ahead: 3 }];
  const { c, p } = await fresh(); await p.evaluate((trees) => { const h = document.createElement('div'); h.id = 'd7'; h.innerHTML = fvGraph({ pg: {}, d: { trees } }); document.body.append(h); }, d.trees); // the worktree list (shown while branches aren't served)
  const row = (r) => p.locator(`#d7 [data-fvtree="${r}"] .fp-st`).textContent();
  assert.equal(await row('/tmp/orbit'), 'not measured yet');
  { const d2 = fixture(); d2.page.branches = []; d2.trees = [{ root: '/tmp/orbit', branch: 'feature/orbit', main: false, checked: false, ahead: 0 }]; const { p: p2 } = await fresh({}, d2); // QA V9: branches [] must not hide the worktrees
    const code = p2.locator(SEL.section('Code delivery')); await code.scrollIntoViewIfNeeded(); const t = await code.innerText();
    assert.match(t, /No feature branches found/); const r = p2.locator('[data-fvtree="/tmp/orbit"]'); assert.ok(await r.isVisible(), 'worktree row visible with branches []');
    assert.equal(await r.locator('.fp-st').textContent(), 'not measured yet'); assert.doesNotMatch(await r.innerText(), /\b0\b|in main worktree/); } assert.equal(await row('/tmp/orbit-b'), 'in main worktree'); assert.equal(await row('/tmp/orbit-c'), '3 not in main worktree');
  { // real control-panel shape: the feature branch's parent is main, which isn't served; no 'from'; lanes merged (0 commits)
    const d3 = fixture(), now = Math.floor(Date.now() / 1000), F = 'o/r:feature/x', mk = (n, c = []) => ({ id: 'o/r:' + n, repo: 'o/r', name: n, parent: F, src: 'abcdef1234', from: null, commits: c, into: null });
    d3.page.branches = [{ id: F, repo: 'o/r', name: 'feature/x', parent: 'o/r:main', src: '05810de2d8', from: null, commits: [], into: null }, mk('feature/x--a'), mk('feature/x--qa', [{ at: now - 60, sha: '0141613', msg: 'qa2' }, { at: now - 3600, sha: '0141612', msg: 'qa' }])]; // newest first, as git log serves them
    d3.page.releases = [{ repo: 'o/r', primary: true, prod: { state: 'unknown', why: 'no GitHub deployments recorded' }, branch: { name: 'feature/x', head: '05810de2d8', deployed: null, note: 'deployed version unknown', state: 'unknown' } }];
    const { p: p3 } = await fresh({}, d3); const code = p3.locator(SEL.section('Code delivery')); await code.scrollIntoViewIfNeeded(); await sleep(300);
    assert.equal(await p3.locator(SEL.branchRow).count(), 3, 'every branch has a row though the root\'s parent (main) is not served');
    assert.equal((await p3.locator(`${SEL.branchRow}[data-row="o/r:feature/x--qa"] .bl b`).innerText()).trim(), '…--qa', 'a lane shows its own part of the name (the shared prefix cut them all to the same text)');
    const fl = await p3.locator(`${SEL.branchRow}[data-row="o/r:feature/x--qa"] .cm`).evaluateAll((xs) => xs.map((x) => +x.dataset.fvl)); assert.ok(fl.length === 2 && fl[0] < fl[1], 'commits on the time axis oldest→newest: ' + fl);
    assert.match(await p3.locator('[data-relc="rel:o/r:br"]').innerText(), /feature\/x[\s\S]*HEAD 05810de2d8[\s\S]*deployed: unknown/, 'the feature branch cell names the branch and its HEAD when only its deploy state is unknown');
    const g = await p3.evaluate(() => { const ds = [...document.querySelectorAll('#fv-gsvg path')].map((x) => x.getAttribute('d') || ''); return { n: ds.length, nan: ds.filter((d) => /NaN|undefined/.test(d)).length }; });
    assert.ok(g.n >= 4 && g.nan === 0, 'graph drawn, no NaN: ' + JSON.stringify(g)); }
  await c.close();
});

test('F-U5 FIXTURE no answer to the POST, then GET 404, is NOT proof it was unsent: stays unknown, never "safe to send again"; Retry resends the identical payload and id; its late acceptance shows', async () => {
  calls.length = 0; const { c, p } = await fresh();
  ans[M8] = [{ abort: true }, { status: 404, body: { error: 'no such message' } }, { status: 200, body: { ok: true, state: 'queued' } }, { status: 200, body: { ok: true, state: 'delivered' } }];
  await openOn(p, K.b); await p.locator(SEL.chatText).fill('maybe sent'); await p.locator(SEL.chatSend).click(); await p.locator('.ctxchat .ss6.unknown').waitFor();
  await p.waitForFunction(() => /no record of it yet/.test(document.querySelector('.ctxchat')?.innerText || ''), null, { timeout: 8000 }).catch(async (e) => { console.log('# F-U5 chat:', JSON.stringify(await p.evaluate(() => ({ t: document.querySelector('.ctxchat')?.innerText, out: FV.out.map(({ poll, ...h }) => ({ ...h, poll: !!poll })) }))), JSON.stringify(calls.map((x) => [x.path, x.body.clientId || 'GET']))); throw e; });
  const t = await p.locator('.ctxchat').innerText(); assert.doesNotMatch(t, /safe/i); assert.match(t, /may still land/);
  assert.ok(await p.locator(SEL.chatSend).isDisabled(), 'no new message with the same words');
  await p.locator(SEL.chatRetry).first().click(); await p.locator('.ctxchat .ss6.delivered').waitFor({ timeout: 8000 });
  const posts8 = msgCalls().filter((x) => x.body.clientId); assert.equal(posts8.length, 2); assert.deepEqual(posts8[1].body, posts8[0].body, 'Retry = same id, same payload');
  await c.close();
});

test('F-U4 FIXTURE long outbox backoff: the schedule never stops (2 s → 6 s → 20 s → 60 s cap); a 15-minute-old message is still checked about once a minute; checks pause while the tab is hidden and resume when shown, then show the final state', async () => {
  calls.length = 0; const { c, p } = await fresh();
  assert.deepEqual(await p.evaluate(() => [5e3, 60e3, 300e3, 15 * 60e3, 3 * 3600e3].map(fvPollWait)), [2000, 6000, 20000, 60000, 60000]);
  await openOn(p, K.b); await p.locator(SEL.chatText).fill('slow lead'); await p.locator(SEL.chatSend).click(); await p.locator('.ctxchat .ss6.queued').waitFor();
  const gets = () => msgCalls().filter((x) => !x.body.clientId).length;
  await p.evaluate(() => { const h = FV.out.at(-1); h.polledFrom = Date.now() - 15 * 60e3; fvMsgPoll(h); }); const g0 = gets();
  await sleep(65000); const g1 = gets(); assert.ok(g1 - g0 === 1, 'one check in the next minute at 15 minutes old: ' + (g1 - g0));
  await p.evaluate(() => { const h = FV.out.at(-1); h.polledFrom = Date.now() - 40e3; fvMsgPoll(h); Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true }); document.dispatchEvent(new Event('visibilitychange')); });
  const gh = gets(); await sleep(14000); assert.ok(gets() - gh <= 1, 'paused while hidden: ' + (gets() - gh));
  ans[M8] = [{ status: 200, body: { ok: true, state: 'delivered' } }]; const gv = gets();
  await p.evaluate(() => { Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true }); document.dispatchEvent(new Event('visibilitychange')); });
  await p.locator('.ctxchat .ss6.delivered').waitFor({ timeout: 10000 }); assert.ok(gets() > gv, 'resumed when shown');
});

test('F-X2 typing in the chat survives page updates: text, focus and caret stay while live data re-renders the page', async () => {
  const { c, p } = await fresh(); const li = p.locator(SEL.step(K.b)); await li.scrollIntoViewIfNeeded(); await li.click({ button: 'right', position: { x: 30, y: 8 } });
  await p.locator(SEL.chatText).click(); await p.keyboard.type('half a sentence');
  await p.evaluate(() => { render(); render(); }); await p.keyboard.type(' and the rest');
  assert.equal(await p.locator(SEL.chatText).inputValue(), 'half a sentence and the rest');
  assert.ok(await p.evaluate(() => document.activeElement?.matches('[data-ctext]')), 'focus kept');
  await c.close();
});

test('F-L3 390 px no overflow, chat clamped; reduced motion: no running animations', async () => {
  let { c, p } = await fresh({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true }); await sleep(500);
  assert.ok(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'horizontal overflow at 390');
  await p.locator(SEL.step(K.b)).scrollIntoViewIfNeeded(); await p.locator(SEL.step(K.b)).click({ button: 'right', position: { x: 20, y: 8 } });
  if (!(await p.locator(SEL.chat).count())) console.log('# F-L3 no chat; element at point:', await p.evaluate((k) => { const r = document.querySelector(`[data-key="${k}"]`).getBoundingClientRect(); const e = document.elementFromPoint(r.left + 20, r.top + 8); return e?.outerHTML.slice(0, 200); }, K.b));
  const db = await p.locator(SEL.chat).boundingBox(); assert.ok(db.x >= 8 && db.x + db.width <= 382, 'chat inside 390 px');
  await c.close();
  ({ c, p } = await fresh({ reducedMotion: 'reduce' })); await sleep(1500);
  assert.equal(await p.evaluate(() => document.getAnimations().filter((a) => a.playState === 'running' && a.effect?.target?.closest?.('.fv, .ctxchat')).length), 0);
  await c.close();
});

test('F-D4 ACTUAL API: events page past 200 (cursor after=ev-N, more=true) and all reach the page', async () => {
  const th = (await feature()).thread?.id, r0 = await agent('POST', R.aAssign, { session: S, step: '', tab: 'Worker', thread: th });
  assert.equal(r0.status, 200, 'assign: ' + JSON.stringify(r0.body));
  for (let i = 0; i < 204; i++) await agent('POST', R.aAssign, { session: S, step: '', tab: 'Worker', thread: th });
  const { c, p } = await fresh({}, fixture(false)); await sleep(2500);
  const n = await p.evaluate(async (s) => { let all = [], cur = 'ev-0', pages = 0; for (;;) { const r = await (await fetch(`/api/term/feature/events?session=${encodeURIComponent(s)}&after=${cur}&limit=200`)).json(); pages++; all = all.concat(r.events); if (!r.more) break; cur = r.events.at(-1).id; } return { all: all.length, pages, ui: FVX[s]?.ev?.length, ids: new Set(FVX[s]?.ev?.map((e) => e.id)).size }; }, S);
  assert.ok(n.all >= 205 && n.pages >= 2, 'server paged: ' + JSON.stringify(n));
  assert.equal(n.ui, n.all, 'the page read every page'); assert.equal(n.ids, n.all, 'no duplicates across pages');
  assert.ok(await p.locator('[data-fvev]').count() > 0, 'events plotted'); await c.close();
});

test('F-D5 ACTUAL API: the UI seen decoder on the real /seen: a bad id → 400 {bad:[{id,reason}]}, nothing recorded; the valid id resent → 200 {ok, frontier:"ev-N", unseen}', async () => {
  const { c, p } = await fresh({}, fixture(false), null, true);
  await p.waitForFunction((s) => FVX[s]?.ev?.length, S, { timeout: 20000 });
  const seen = []; p.on('response', async (r) => { if (r.url().endsWith('/api/term/feature/seen')) seen.push({ st: r.status(), b: await r.json().catch(() => null), req: JSON.parse(r.request().postData()) }); });
  const first = await p.evaluate((s) => FVX[s].ev[0].id, S);
  expectFail('/api/term/feature/seen', 400, 'F-D5: the real /seen refuses a batch with an unknown id (provoked)');
  await p.evaluate(([id]) => { FV.seenQ.add(id); FV.seenQ.add('ev-999999'); return fvFlushSeen(); }, [first]); await sleep(500);
  assert.equal(seen[0]?.st, 400, 'real /seen refuses the batch: ' + JSON.stringify(seen[0]));
  assert.deepEqual(seen[0].b.bad.map((x) => x.id), ['ev-999999']); assert.ok(seen[0].b.bad[0].reason, 'reason given');
  const st = await p.evaluate(() => ({ bad: [...FV.seenBad], q: [...FV.seenQ], acked: [...FV.acked] }));
  assert.deepEqual(st.q, [first], 'the valid id stays pending'); assert.deepEqual(st.acked, [], 'nothing marked seen from a refused batch');
  assert.equal(st.bad[0][0], 'ev-999999'); assert.equal(st.bad[0][1], seen[0].b.bad[0].reason);
  await p.evaluate(() => fvFlushSeen()); await sleep(500);
  assert.equal(seen[1]?.st, 200, 'valid id accepted: ' + JSON.stringify(seen[1])); assert.deepEqual(seen[1].req.ids, [first]);
  assert.match(seen[1].b.frontier, /^ev-\d+$/); assert.equal(typeof seen[1].b.unseen, 'number');
  assert.deepEqual(await p.evaluate(() => [...FV.acked]), [first]);
  assert.equal(await p.evaluate((s) => FV.fr[s], S), +seen[1].b.frontier.slice(3), 'the page keeps the frontier the panel reported');
  await c.close();
});

// ---- REAL DATA (no fixture, no routed answers unless a fault is injected on the real route): the backend's page.log rows ----
const realPage = async () => (await api('GET', R.feature(S))).body.page;
async function realRows(n, kind, tag) { const th = (await feature()).thread?.id; for (let i = 0; i < n; i++) { const r = await agent('POST', R.aLog, { session: S, line: `${kind}: ui8 ${tag} row ${i}`, thread: th }); assert.equal(r.status, 200, 'log write: ' + JSON.stringify(r.body)); } }
const seenReq = (p) => { const out = []; p.on('request', (r) => { if (new URL(r.url()).pathname === R.seen) out.push(JSON.parse(r.postData() || '{}').ids); }); return out; };
const unseenIds = (p) => p.evaluate(() => [...document.querySelectorAll('[data-seen]')].map((e) => e.dataset.seen).filter(Boolean));

test('F-R2 REAL: fresh real log rows; a fast scroll past them sends no seen', async () => {
  await realRows(6, 'ACTION', 'r2');
  const { c, p } = await fresh({}, null, null, true); const sent = seenReq(p);
  await p.evaluate(() => scrollTo(0, 0)); await sleep(1500);
  assert.ok((await unseenIds(p)).length >= 6, 'precondition: the fresh real rows are unseen');
  const before = sent.length;
  await p.evaluate(async () => { const h = document.documentElement.scrollHeight; for (let y = 0; y < h; y += 400) { scrollTo(0, y); await new Promise((r) => setTimeout(r, 40)); } scrollTo(0, 0); });
  await sleep(1500); assert.equal(sent.length, before, 'fast scroll sent seen: ' + JSON.stringify(sent.slice(before)));
  await c.close();
});

test('F-R6 REAL: a failed (503) seen batch is not recorded and the row stays unseen; after recovery it is resent and the real server records it', async () => {
  await realRows(3, 'ACTION', 'r6');
  const { c, p } = await fresh({}, null, null, true); await p.bringToFront();
  let fail = true; const tries = [];
  await p.route((u) => u.pathname === R.seen, (r) => { tries.push(JSON.parse(r.request().postData() || '{}').ids); if (fail) { expectFail(R.seen, 503, 'F-R6 injected outage'); return r.fulfill({ status: 503, json: { error: 'ui8: down' } }); } return r.continue(); });
  const id = await p.evaluate(() => { const el = [...document.querySelectorAll('[data-seen]')].filter((e) => e.dataset.seen).pop(); el?.scrollIntoView({ block: 'center' }); return el?.dataset.seen; });
  assert.ok(id, 'precondition: a real unseen row'); await sleep(2600);
  assert.ok(tries.flat().includes(id), 'seen attempted');
  const seq = +id.slice(3), p0 = (await realPage()).seen;
  assert.ok(!(p0.frontier >= seq) && !p0.ids.includes(id), 'server recorded a failed batch: ' + JSON.stringify(p0));
  assert.equal(await p.locator(`[data-log="${id}"].unseen`).count(), 1, 'row shown seen although nothing was recorded');
  fail = false; await sleep(17000);
  assert.ok(tries.flat().filter((x) => x === id).length >= 2, 'kept id not resent after recovery');
  const p1 = (await realPage()).seen;
  assert.ok(p1.frontier >= seq || p1.ids.includes(id), 'real server did not record it after recovery: ' + JSON.stringify(p1));
  assert.equal(await p.locator(`[data-log="${id}"].unseen`).count(), 0, 'row still unseen after the real 200');
  await c.close();
});

test('F-R7 REAL reviewed on a real DECISION row: 200 shows ✓ and survives a reload (page.reviewed); a row whose line carries | rev: is checked as the backend answers', async () => {
  const th = (await feature()).thread?.id;
  assert.equal((await agent('POST', R.aLog, { session: S, line: 'DECISION: ui8 review plain | considered: a; b | chose: a', thread: th })).status, 200);
  assert.equal((await agent('POST', R.aLog, { session: S, line: 'DECISION: ui8 review with rev | chose: a | rev: 0123456789abcdef', thread: th })).status, 200);
  const pg0 = await realPage(), plain = pg0.log.find((l) => l.title === 'ui8 review plain'), withRev = pg0.log.find((l) => l.title === 'ui8 review with rev');
  assert.ok(plain?.review && withRev?.review, 'DECISION rows are reviewable');
  let { c, p } = await fresh({}, null, null, true);
  await p.locator(`[data-review="${plain.id}"]`).click(); await p.locator(`[data-log="${plain.id}"] .lg-rv.done`).waitFor();
  assert.equal((await realPage()).reviewed[plain.id], plain.rev || '', 'server recorded reviewed at the row rev');
  await c.close(); ({ c, p } = await fresh({}, null, null, true));
  assert.equal(await p.locator(`[data-log="${plain.id}"] .lg-rv.done`).count(), 1, 'reviewed shown from page.reviewed after reload');
  // a writer's own "rev:" is only a claim: shown as such; reviewed sends the trusted rev and is accepted
  assert.equal(withRev.claimedRev, '0123456789abcdef'); assert.notEqual(withRev.rev, withRev.claimedRev, 'trusted rev is the plan at creation');
  assert.match(await p.locator(`[data-log="${withRev.id}"]`).innerText(), /says rev 0123456789abcdef/);
  const sentRev = []; p.on('request', (r) => { if (new URL(r.url()).pathname === R.reviewed) sentRev.push(JSON.parse(r.postData()).rev); });
  await p.locator(`[data-review="${withRev.id}"]`).click(); await p.locator(`[data-log="${withRev.id}"] .lg-rv.done`).waitFor();
  assert.deepEqual(sentRev, [withRev.rev || ''], 'reviewed sends the trusted rev, never the claim');
  await c.close();
});

test('F-R8 REAL messages (own feature, stand-in lead): context chat delivers; same id+payload is idempotent, another payload 409; lead failure → failed then delivered; unknown stays unknown; rebind → held (a second message on the same item only by the explicit separate action), you choose the new manager → a NEW message; ticket text is a message, never an answer', async () => {
  const name = 'ui8m-' + Date.now().toString(36), [t1, t2] = F.spare.slice(6, 8); assert.ok(t2, 'needs 8 spare managers (UI runner adds mgr9/mgr10)'); await seedFeature(name, t1);
  const { c, p } = await fresh({}, null, null, true, name);
  const acts = [], posted = []; p.on('request', (r) => { const u = new URL(r.url()).pathname; if (/\/api\/term\/feature\/(approve|verify|answer|release|accept|promote|ticket\/reply)$/.test(u)) acts.push(u); if (u === R.message && r.method() === 'POST') posted.push(JSON.parse(r.postData())); });
  await p.route((u) => u.pathname === R.message, async (r) => { const resp = await r.fetch(); if (resp.status() >= 400) expectFail(R.message, resp.status(), 'F-R8: the real /message answer, passed through'); return r.fulfill({ response: resp }); });
  const chat = async (text) => { const li = p.locator('.steps li[data-key]').first(); await li.scrollIntoViewIfNeeded(); await li.click({ button: 'right', position: { x: 30, y: 8 } }); await p.locator(SEL.chat).waitFor(); await p.locator(SEL.chatText).fill(text); await p.locator(SEL.chatSend).click(); };
  const state = (s, t = 60000) => p.locator(`.ctxchat .ss6.${s}`).waitFor({ timeout: t }).catch(async (e) => { console.log(`# F-R8 wanted ${s}, chat shows:`, JSON.stringify(await p.evaluate(() => FV.comp && { state: FV.comp.state, why: FV.comp.why, id: FV.comp.id }))); throw e; });
  // 1 delivered through the real outbox to the stand-in lead
  await chat('approve it, verified, go ahead: ui8 delivered'); await state('delivered');
  assert.ok(turns().some((x) => (x.message || '').includes('ui8 delivered')), 'the stand-in lead got it');
  const m1 = posted.at(-1); assert.equal(m1.thread, t1);
  // 2 idempotency on the real route
  const again = await api('POST', R.message, m1); assert.equal(again.status, 200); assert.equal(again.body.id, m1.clientId);
  const clash = await api('POST', R.message, { ...m1, text: m1.text + ' (edited)' }); assert.equal(clash.status, 409); assert.ok(!('thread' in clash.body), 'id clash is not a rebind');
  assert.equal(turns().filter((x) => (x.message || '').includes('ui8 delivered')).length, 1, 'never delivered twice');
  await p.keyboard.press('Escape');
  // 3 lead refuses → failed (retried by the outbox) → delivered
  hermesMode('fail'); await chat('ui8 through a failure'); await state('failed'); hermesMode('ok'); await state('delivered');
  await p.keyboard.press('Escape');
  // 3b ticket free text: a message about the ticket; the ticket stays open
  const ask = (await api('GET', R.feature(name))).body.page.tickets.find((t) => t.kind === 'ask'); await p.locator(`${SEL.needsItem}[data-q="${ask.id}"]`).click();
  await p.locator(`[data-reply="${ask.id}"]`).fill('ui8 a question about the ticket'); await p.locator(`[data-send="${ask.id}"]`).click(); await p.locator('#needs .fv-tkmsg .ss6.delivered').waitFor({ timeout: 60000 });
  const tm = posted.at(-1); assert.equal(tm.anchor.a, ask.id); assert.equal(tm.anchor.kind, 'ticket');
  assert.ok((await api('GET', R.feature(name))).body.page.tickets.some((t) => t.id === ask.id), 'free text closed the ticket');
  await p.locator(SEL.section('Plan and milestones')).scrollIntoViewIfNeeded();
  // 4 history unreadable → unknown, not resent
  hermesMode('unknown'); await chat('ui8 unknown history'); await state('unknown'); await sleep(4000); hermesMode('ok');
  assert.ok(turns().filter((x) => (x.message || '').includes('ui8 unknown history')).length <= 1, 'unknown was resent');
  await p.keyboard.press('Escape');
  // 5 rebind while the chat is open: held, the new manager named, a new message only when you choose it
  const li = p.locator('.steps li[data-key]').first(); await li.click({ button: 'right', position: { x: 30, y: 8 } }); await p.locator(SEL.chat).waitFor();
  assert.equal((await api('POST', '/api/ai/thread/archive', { id: t1 })).status, 200, 'archive the first manager');
  const rb = await api('POST', R.bind, { session: name, thread: t2 }); assert.equal(rb.status, 200, 'rebind: ' + JSON.stringify(rb.body));
  await p.locator(SEL.chatText).fill('ui8 after rebind'); const n0 = posted.length;
  assert.ok(await p.locator(SEL.chatSend).isDisabled(), 'step 4\'s unknown message on this item is still unresolved: ordinary Send is off'); await p.locator('.ctxchat [data-csep]').click(); // the owner explicitly chooses a separate message
  console.log('# F-R8 before send:', JSON.stringify(await p.evaluate(() => ({ comp: FV.comp && { thread: FV.comp.thread, state: FV.comp.state, text: FV.comp.text }, btn: document.querySelector('[data-csend]')?.outerHTML, chat: document.querySelector('.ctxchat')?.innerText.slice(0, 300) }))));
  await p.locator(SEL.chatSend).click(); await state('held', 20000).catch(async (e) => { console.log('# held debug', JSON.stringify(await p.evaluate(() => ({ c: FV.comp && { state: FV.comp.state, why: FV.comp.why, thread: FV.comp.thread, id: FV.comp.id }, html: document.querySelector('.ctxchat')?.innerText.slice(0, 400) }))), JSON.stringify(posted.slice(-2))); throw e; });
  assert.match(await p.locator(SEL.chatConfirm).innerText(), new RegExp(`Send it to (V8 QA spare manager \\d+|${t2}) instead`), 'names the new manager (title when the panel knows it)'); assert.equal(posted.length, n0 + 1, 'nothing re-sent on its own');
  await p.locator(SEL.chatConfirm).click(); await state('delivered');
  const [h, n] = posted.slice(-2); assert.equal(h.thread, t1); assert.equal(n.thread, t2); assert.notEqual(n.clientId, h.clientId); assert.equal(n.text, 'ui8 after rebind');
  await p.keyboard.press('Escape');
  assert.deepEqual(acts, [], 'message text reached an owner action route');
  await c.close();
});

test('F-R9 REAL tickets on a feature of its own: the real ask ticket needs text, answers through the real route and closes; a stale second tab gets the real 409 on the ticket; missed has no action', async () => {
  const name = 'ui8t-' + Date.now().toString(36); await seedFeature(name, F.spare[5]);
  const tk0 = (await api('GET', R.feature(name))).body.page?.tickets; assert.ok(Array.isArray(tk0), 'page.tickets served');
  const ask = tk0.find((t) => t.kind === 'ask'); assert.ok(ask && ask.options.every((o) => o.needsText), 'real ask ticket with text options: ' + JSON.stringify(tk0.map((t) => t.id)));
  assert.equal((await agent('POST', R.aLog, { session: name, line: 'DECISION: ui8 before the answer | chose: wait', thread: F.spare[5] })).status, 200);
  const A = await fresh({}, null, null, true, name), Bp = await fresh({}, null, null, true, name); // two tabs on the same record
  // tab B goes stale: it keeps showing the real record as it first read it (as a tab that missed the update would)
  const snap = (await api('GET', R.feature(name))).body; await Bp.p.route((u) => u.pathname === '/api/term/feature', (r) => r.fulfill({ json: snap }));
  const sent = []; A.p.on('request', (r) => { if (new URL(r.url()).pathname === R.ticketReply) sent.push(JSON.parse(r.postData())); });
  for (const x of [A.p, Bp.p]) await x.locator(`${SEL.needsItem}[data-q="${ask.id}"]`).click();
  await A.p.locator('[data-opt="answer-wait"]').click(); await sleep(300); assert.equal(sent.length, 0, 'sent without text');
  await A.p.locator(`[data-reply="${ask.id}"]`).fill('ISO 8601'); await A.p.locator('[data-opt="answer-wait"]').click();
  await A.p.waitForFunction((id) => !document.querySelector(`.qi[data-q="${CSS.escape(id)}"]`), ask.id, { timeout: 10000 });
  assert.deepEqual(Object.keys(sent[0]).sort(), ['option', 'rev', 'session', 'ticket', 'text'].concat(ask.sid ? ['sid'] : []).sort());
  const tk1 = (await api('GET', R.feature(name))).body.page.tickets; assert.ok(!tk1.some((t) => t.id === ask.id), 'real ticket closed');
  // the answer changed the plan: a log row written before it is now stale, shown as such
  const old = (await api('GET', R.feature(name))).body.page.log.find((l) => l.title === 'ui8 before the answer');
  assert.equal(old?.stale, true, 'backend marks the earlier row stale: ' + JSON.stringify(old));
  await A.p.waitForSelector(`[data-log="${old.id}"] [data-stale]`, { timeout: 15000 });
  expectFail(R.ticketReply, 409, 'F-R9: the stale tab answers a closed ticket');
  await Bp.p.locator(`[data-reply="${ask.id}"]`).fill('day-month-year'); await Bp.p.locator('[data-opt="answer"]').click(); await sleep(800);
  assert.match(await Bp.p.locator('#needs').innerText(), /Not done: .*(closed|changed)/, 'the real 409 shows on the ticket');
  const missed = tk1.find((t) => t.kind === 'missed');
  if (missed) { await A.p.locator(`${SEL.needsItem}[data-q="${missed.id}"]`).click(); assert.equal(await A.p.locator('#needs [data-opt]').count(), 0); assert.match(await A.p.locator('#needs').innerText(), /no action for this item/); }
  await A.c.close(); await Bp.c.close();
});

test('F-R13 REAL R1 evidence (own unbound feature, no model): a plan with run, file, missing-file and link evidence lines renders every item from the real response; the present file is sealed by the write path; the missing one is visible; nothing called verified', async () => {
  const name = 'ui8e-' + Date.now().toString(36); await seedFeature(name); const d0 = (await api('GET', R.feature(name))).body;
  assert.ok(d0.dossier?.dir, 'record folder'); writeFileSync(d0.dossier.dir + '/r1out.txt', 'ledger ok\n');
  const plan = '## M1 Real R1\n- [x] Write r1out @Builder\n  - evidence: run make check — ok\n  - evidence: file r1out.txt\n  - evidence: file r1gone.txt\n  - evidence: link https://example.com/r1\n';
  const w = await api('POST', R.doc, { session: name, doc: 'plan', rev: d0.dossier.revs.plan, text: plan }); assert.equal(w.status, 200, 'plan write: ' + JSON.stringify(w.body));
  const pg = (await api('GET', R.feature(name))).body; const k = steps(pg).find((x) => /Write r1out/.test(x.text))?.key; assert.ok(k, 'step');
  const its = pg.page?.evidence?.[k]?.items || []; writeFileSync(`${OUT}/f-r13-evidence.json`, JSON.stringify(pg.page?.evidence?.[k], null, 1));
  assert.equal(its.length, 4, 'four real items: ' + JSON.stringify(its).slice(0, 400));
  const { c, p } = await fresh({}, null, null, true, name); const ext = []; p.on('request', (r) => { if (!r.url().startsWith(B)) ext.push(r.url()); });
  const pf = await pfOpen(p, k), figs = pf.locator('.pfi'); assert.equal(await figs.count(), 4);
  for (let i = 0; i < 4; i++) { assert.ok((await figs.nth(i).locator('figcaption').innerText()).trim(), `real item ${i} caption`); assert.match(await figs.nth(i).innerText(), /claimed by its writer/); }
  const txt = await pf.innerText(); console.log('# F-R13 proof box:', JSON.stringify(txt.slice(0, 900)));
  assert.match(txt, /run make check — ok/); assert.match(txt, /link https:\/\/example\.com\/r1/); assert.match(txt, /r1gone\.txt[\s\S]*missing: not found/);
  const sealed = its.find((x) => x.ref === 'r1out.txt'); if (sealed?.seal) assert.match(txt, /sealed sha256 [0-9a-f]{10}…[\s\S]*integrity only/); else assert.match(txt, /not sealed/);
  assert.doesNotMatch(txt, /\bverified\b/i); assert.deepEqual(ext, []); const w0 = await pf.locator('.pf-req.warn').first().innerText().catch(() => ''); for (const part of w0.split(/(?<=[a-z]) (?=it |link )/)) assert.ok(w0.indexOf(part) === w0.lastIndexOf(part) || part.length < 12, 'a warning repeated: ' + w0);
  const tr = pg.trees || [], br = pg.page?.branches; console.log('# F-R13 real trees/branches:', JSON.stringify({ trees: tr.map(({ root, checked, ahead }) => ({ root, checked, ahead })), branches: br && br.length }));
  if (tr.length && Array.isArray(br) && !br.length) { const code = p.locator(SEL.section('Code delivery')); await code.scrollIntoViewIfNeeded();
    for (const w of tr) { const r = p.locator(`[data-fvtree="${w.root}"]`); assert.ok(await r.isVisible(), 'real worktree row visible with real branches []: ' + w.root);
      const st = (await r.locator('.fp-st').textContent()) || ''; if (!w.main && !w.checked && !w.otherRepo) { assert.equal(st, 'not measured yet'); assert.doesNotMatch(st, /\b0\b|in main worktree/); } } }
  await c.close();
});

test('F-R14 REAL artifacts (own unbound feature, no model): plan artifact lines → real page.arts; the local HTML preview answers 200 through the existing Files preview with review=1; missing and refused inert, no path echoed; https not requested until clicked', async () => {
  const name = 'ui8a-' + Date.now().toString(36); await seedFeature(name); const d0 = (await api('GET', R.feature(name))).body; const dir = d0.dossier.dir;
  writeFileSync(dir + '/art-page.html', '<!doctype html><title>art</title><h1>ui8 artifact</h1>'); writeFileSync(dir + '/art-page.html.comments.json', JSON.stringify({ comments: [{ id: 'c1', text: 'ok' }] }));
  const plan = '## M1 Arts\n- artifact: art-page.html | ui | Real page\n- [ ] Draw it @Builder\n  - artifact: art-gone.png | issue | Gone shot\n  - artifact: /etc/passwd | ui | Not allowed\n  - artifact: https://example.com/ui8 | ui | Outside page\n';
  const w = await api('POST', R.doc, { session: name, doc: 'plan', rev: d0.dossier.revs.plan, text: plan }); assert.equal(w.status, 200, 'plan write: ' + JSON.stringify(w.body));
  const pg = (await api('GET', R.feature(name))).body, arts = pg.page?.arts || []; writeFileSync(`${OUT}/f-r14-arts.json`, JSON.stringify(arts, null, 1));
  assert.equal(arts.length, 4, 'four real arts: ' + JSON.stringify(arts).slice(0, 500));
  const by = (t) => arts.find((a) => a.title === t); assert.equal(by('Real page').state, 'ok'); assert.equal(by('Gone shot').state, 'missing'); assert.equal(by('Not allowed').state, 'refused'); assert.equal(by('Outside page').state, 'link');
  const { c, p } = await fresh({}, null, null, true, name); const ext = []; p.on('request', (r) => { if (!r.url().startsWith(B)) ext.push(r.url()); });
  await p.evaluate(() => { FV.open = new Set(['M1']); render(); });
  const A = (t) => p.locator(`[data-fvart="${by(t).id}"]`);
  assert.equal(await p.locator(`.ms[data-ms="M1"] [data-fvart="${by('Real page').id}"]`).count(), 1, 'the milestone\'s own artifact shows in M1');
  const href = await A('Real page').getAttribute('href'); assert.match(href, /^\/api\/fs\/raw\?path=.*art-page\.html&review=1$/); assert.match(await A('Real page').innerText(), /UI design[\s\S]*html · 1 comment/i);
  const pr = await p.evaluate(async (u) => { const r = await fetch(u); return { s: r.status, t: await r.text() }; }, href); assert.equal(pr.s, 200, 'the existing preview serves it (as the browser opens it)'); assert.match(pr.t, /ui8 artifact/);
  for (const t of ['Gone shot', 'Not allowed']) { assert.equal(await A(t).evaluate((e) => e.tagName), 'DIV'); assert.equal(await A(t).getAttribute('href'), null); assert.match(await A(t).innerText(), /unavailable: /); }
  const txt = await p.locator(SEL.section('Plan and milestones')).innerText(); assert.doesNotMatch(txt, /\/etc\/passwd|art-gone\.png/, 'no path echoed');
  assert.match(await A('Outside page').innerText(), /opens example\.com · not fetched until you open it/); assert.deepEqual(ext, [], 'nothing external requested by viewing');
  await c.close();
});

test('F-R15 REAL work-log fields (own feature, agent route, no model): ran/next/objectives/pr/proof/session from the real parser render as the writer\'s account; whitespace kept; no markup', async () => {
  const name = 'ui8l-' + Date.now().toString(36), th = F.spare[4]; await seedFeature(name, th);
  const line = 'ACTION: ui8 build  <i>x</i> | ran: ok=1,cmd=go%20test%20./...,result=PASS%0Aok; ok=?,cmd=make; bogus | next: then%09deploy | objectives: D1, Z9 | pr: #77 | proof: nostep';
  const w = await agent('POST', R.aLog, { session: name, line, thread: th }, { 'X-Agent-Sid': 'ui8-sid-1' }); assert.equal(w.status, 200, 'log write: ' + JSON.stringify(w.body));
  const lg = (await api('GET', R.feature(name))).body.page.log.find((l) => /ui8 build/.test(l.title || '')); writeFileSync(`${OUT}/f-r15-log.json`, JSON.stringify(lg, null, 1));
  assert.ok(lg, 'real log row'); assert.equal(lg.ran?.length, 2); assert.deepEqual(lg.ranUnparsed, ['bogus']); assert.equal(lg.next, 'then\tdeploy'); assert.equal(lg.sessionClaimed, true); assert.equal(lg.prKnown, false);
  const { c, p } = await fresh({}, null, null, true, name); const row = p.locator(`[data-log="${lg.id}"]`); await row.scrollIntoViewIfNeeded(); const t = await row.innerText(); console.log('# F-R15 row:', JSON.stringify(t));
  assert.match(t, /✓ go test \.\/\.\.\. → PASS\nok/); assert.match(t, /\? make/); assert.match(t, /ran\?\s*bogus/); assert.match(t, /not executed or verified by the panel/); assert.match(t, /next\s*then\tdeploy/);
  assert.match(t, /ui8-sid-1 \(claimed\)/); assert.match(t, /claims #77/); for (const o of lg.objectivesClaimed || []) assert.match(t, new RegExp('claims ' + o)); for (const o of lg.objectives || []) assert.equal(await row.locator(`[data-obj-go="${o}"]`).count(), 1);
  if (lg.proofStepClaimed) assert.match(t, /claims proof for nostep/); assert.equal(await row.locator('i, a').count(), 0, 'no markup or link from the line');
  await c.close();
});

// Files kept outside the repo: a real PLAN.md (V8QA_REAL_PLAN) and a design mock-up page (V8QA_DESIGN_HTML, a path or file:// URL).
const REAL_PLAN = process.env.V8QA_REAL_PLAN || '';
const DESIGN_HTML = process.env.V8QA_DESIGN_HTML ? (/^file:/.test(process.env.V8QA_DESIGN_HTML) ? process.env.V8QA_DESIGN_HTML : pathToFileURL(resolve(process.env.V8QA_DESIGN_HTML)).href) : '';
const planOk = REAL_PLAN && existsSync(REAL_PLAN), designOk = DESIGN_HTML && existsSync(fileURLToPath(DESIGN_HTML));
test('F-V1 REAL style (own feature with a read-only copy of a real record\'s PLAN.md): no element inside the page has a light background (a white milestone: app.css .cur); milestone titles not doubled; screenshots beside the design HTML when V8QA_DESIGN_HTML is set', { skip: planOk ? false : 'set V8QA_REAL_PLAN to a real PLAN.md' }, async () => {
  const name = 'ui8v-' + Date.now().toString(36); await seedFeature(name); const d0 = (await api('GET', R.feature(name))).body
  const plan = readFileSync(REAL_PLAN, 'utf8'); const w = await api('POST', R.doc, { session: name, doc: 'plan', rev: d0.dossier.revs.plan, text: plan }); assert.equal(w.status, 200, 'plan copy: ' + JSON.stringify(w.body));
  const { c, p } = await fresh({ viewport: { width: 1440, height: 1000 } }, null, null, true, name); await p.evaluate(() => { FV.open = new Set(['M1', 'M2', 'M3', 'M4', 'M5', 'M6', 'M7']); render(); }); await sleep(300);
  const light = await p.evaluate(() => { const out = []; const lum = (c) => { const m = c.match(/rgba?\(([^)]+)\)/); if (!m) return null; const [r, g, b, a = 1] = m[1].split(',').map(Number); return a < 0.35 || Math.max(r, g, b) - Math.min(r, g, b) > 40 ? null : (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255; }; // white/grey surfaces only (the V8 status swatches are coloured on purpose)
    for (const el of document.querySelectorAll('.fv, .fv *')) { if (el.closest('svg, img, .pfi, iframe')) continue; const cs = getComputedStyle(el); const L = lum(cs.backgroundColor); if (L != null && L > 0.55 && el.getClientRects().length) out.push(`${el.tagName.toLowerCase()}.${[...el.classList].join('.')} ${cs.backgroundColor} "${(el.textContent || '').trim().slice(0, 40)}"`); } return out; });
  assert.deepEqual(light, [], 'white/grey backgrounds inside the feature page');
  const cur = p.locator('.ms.cur').first(); if (await cur.count()) { assert.equal(await cur.evaluate((e) => getComputedStyle(e).backgroundColor), 'rgba(0, 0, 0, 0)', 'the current milestone has no background of its own'); }
  const heads = await p.locator('.ms > summary b').allInnerTexts(); assert.ok(heads.length >= 7, 'milestones shown: ' + heads.length); for (const t of heads) assert.doesNotMatch(t, /^(M\d+) \1\b/, 'doubled id: ' + t);
  await p.locator('.ms').first().evaluate((e) => e.scrollIntoView({ block: 'start' })); await p.evaluate(() => scrollBy(0, -90)); await sleep(200); await p.screenshot({ path: `${OUT}/f-v1-plan-live-record.png` }); await p.screenshot({ path: `${OUT}/f-v1-page-live-record.png`, fullPage: true });
  if (designOk) {
    const h = await c.newPage(); await h.route((u) => !u.protocol.startsWith('file'), (r) => r.abort()); await h.goto(DESIGN_HTML); await sleep(500);
  await h.screenshot({ path: `${OUT}/f-v1-v8-approved.png`, fullPage: true }); const hm = h.locator('details.ms').first(); if (await hm.count()) { await hm.evaluate((e) => e.scrollIntoView({ block: 'start' })); await h.evaluate(() => scrollBy(0, -90)); await sleep(200); await h.screenshot({ path: `${OUT}/f-v1-v8-approved-ms.png` }); }
  }
  await c.close();
});

// ---- parity: the built page against the design HTML (V8QA_DESIGN_HTML), section by section (computed styles + screenshots)
const PAR_SECTIONS = [['Header', 'header.head', 'header.fv-head'], ...['Since you looked', 'Team timeline', 'Needs you', 'Plan and milestones', 'Code delivery', 'Work log'].map((n) => [n, `section[aria-label="${n}"]`, `section[aria-label="${n}"]`])];
const PAR_PROPS = ['background-color', 'border-top-color', 'border-top-width', 'border-top-left-radius', 'font-family', 'font-size', 'font-weight', 'line-height', 'color', 'padding-top', 'padding-left', 'gap', 'box-shadow'];
// differences kept on purpose, each with why (shown in the parity report)
const PAR_ON_PURPOSE = [
];
const parCollect = (p, sel) => p.evaluate(([sel, props]) => { const root = document.querySelector(sel); if (!root) return null; const out = {};
  const take = (el) => { const cs = getComputedStyle(el); return Object.fromEntries(props.map((k) => [k, k === 'font-family' ? cs[k].split(',')[0].replace(/"/g, '').trim() : cs.getPropertyValue(k)])); };
  out[':root'] = take(root); const h = root.querySelector('h2, h3'); if (h) out['h2,h3 (title)'] = take(h);
  for (const el of root.querySelectorAll('[class]')) { const k = '.' + [...el.classList].sort().join('.'); if (!out[k] && el.getClientRects().length) out[k] = { ...take(el), _t: (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 50) }; } // the element's full class set: like is compared with like (a .tag.needs with a .tag.needs)
  return out; }, [sel, PAR_PROPS]);
test('F-V2 FIXTURE design parity: each section\'s computed styles against the design HTML; key ones must match (a drift like the white milestone fails); writes the parity data and screenshots for the report', { skip: designOk ? false : 'set V8QA_DESIGN_HTML to a design mock-up page' }, async () => {
  const d = fixture(); d.prs[1].deploys = [{ env: 'stage', state: 'live' }]; // #10 merged and live on stage; #9 closed
  const { c, p } = await fresh({ viewport: { width: 1440, height: 1000 } }, d); await p.evaluate(() => { FV.open = new Set(['M1', 'M2']); render(); }); await sleep(300);
  await p.addStyleTag({ content: 'header.top { visibility: hidden !important; } html { font-size: 16px !important; }' }); // the panel's desktop scale (app.css: 13.6px, 'as zoom at 85%') is on purpose: compared at the V8's 16px // the panel's own top bar would sit over element captures
  const h = await c.newPage(); await h.setViewportSize({ width: 1440, height: 1000 }); await h.route((u) => !u.protocol.startsWith('file'), (r) => r.abort()); await h.goto(DESIGN_HTML); await sleep(600);
  const rows = []; mkdirSync(`${OUT}/parity`, { recursive: true });
  for (const [name, hs, ps] of PAR_SECTIONS) { const a = await parCollect(h, hs), b = await parCollect(p, ps); const slug = name.toLowerCase().replace(/\W+/g, '-');
    for (const [pg, sel, who] of [[h, hs, 'v8'], [p, ps, 'built']]) { const l = pg.locator(sel).first(); if (await l.count()) { await l.scrollIntoViewIfNeeded(); await l.screenshot({ path: `${OUT}/parity/${slug}-${who}.png` }).catch(() => {}); } }
    const diffs = []; if (a && b) for (const k of Object.keys(a)) if (b[k]) for (const pr of PAR_PROPS) if (a[k][pr] !== b[k][pr]) diffs.push({ sel: k, prop: pr, v8: a[k][pr], built: b[k][pr], v8t: a[k]._t, builtT: b[k]._t });
    rows.push({ name, slug, v8: !!a, built: !!b, common: a && b ? Object.keys(a).filter((k) => b[k]).length : 0, onlyV8: a && b ? Object.keys(a).filter((k) => !b[k]) : [], diffs }); }
  writeFileSync(`${OUT}/parity/parity.json`, JSON.stringify(rows, null, 1));
  // hard: every section's own surface and title, and nothing white inside, match the V8 HTML
  const KEY = new Set([':root', 'h2,h3 (title)']), KEYP = ['background-color', 'border-top-left-radius', 'font-family', 'font-size', 'font-weight', 'color', 'border-top-color', 'border-top-width'];
  const hard = rows.flatMap((r) => r.diffs.filter((d) => KEY.has(d.sel) && KEYP.includes(d.prop)).map((d) => `${r.name} ${d.sel} ${d.prop}: v8 ${d.v8} / built ${d.built}`));
  for (const r of rows) assert.ok(r.v8 && r.built, `section ${r.name} exists in both`);
  // and every element kind both pages draw: colours, surfaces, radius and type must match, except the differences recorded as on purpose
  const VIS = ['background-color', 'color', 'border-top-left-radius', 'font-family', 'font-size', 'font-weight'];
  const drift = rows.flatMap((r) => r.diffs.filter((d) => !KEY.has(d.sel) && VIS.includes(d.prop) && !PAR_ON_PURPOSE.some(([re]) => re.test(`${r.name} ${d.sel} ${d.prop}`))).map((d) => `${r.name} ${d.sel} ${d.prop}: v8 ${d.v8} / built ${d.built}`));
  writeFileSync(`${OUT}/parity/drift.json`, JSON.stringify(drift, null, 1));
  assert.deepEqual(drift, [], 'element styles differ from the V8 HTML');
  // the V8 elements built for 1:1 (each against the V8 HTML's own values)
  const bg = (pg, sel, pseudo) => pg.locator(sel).first().evaluate((e, ps) => getComputedStyle(e, ps).backgroundColor, pseudo || null);
  const MC = ['rgb(57, 135, 229)', 'rgb(217, 89, 38)']; // V8 M1, M2
  assert.equal(await bg(p, '.ms[data-ms="M1"] > summary .mi'), MC[0], 'milestone colour square (M1)'); assert.equal(await bg(p, '.ms[data-ms="M2"] > summary .mi'), MC[1]);
  assert.equal(await bg(h, 'details.ms[data-ms="M1"] > summary .mi'), MC[0], 'the V8 value it copies');
  const sq = async (pg, sel) => pg.locator(sel).first().evaluate((e) => { const c = getComputedStyle(e); return [c.width, c.height, c.borderTopLeftRadius].join(' '); });
  assert.equal(await sq(p, '.ms > summary .mi'), await sq(h, 'details.ms > summary .mi'), 'square size/radius as V8');
  assert.match(await p.locator('.ms[data-ms="M1"] > summary').innerText(), /▣ 1/, 'milestone artifact count');
  assert.equal(await bg(p, `.ss[data-sess="s-b"] .mk`, '::before'), MC[0], 'timeline bar marker in its milestone colour');
  assert.match(await p.locator('section[aria-label="Needs you"] .ref.ms').first().innerText(), /^M2$/); assert.equal(await bg(p, 'section[aria-label="Needs you"] .ref.ms', '::before'), MC[1], '"■ M2" ref');
  assert.match(await p.locator(`[data-key="${K.b}"] .artchip`).innerText(), /▣ UI/, 'step artifact chip'); assert.match(await p.locator(`[data-key="${K.b}"] .resp`).innerText(), /Worker\s+s-b/, 'session id chip');
  for (const k of ['background-color', 'border-top-left-radius', 'font-size', 'color']) assert.equal(await p.locator('.artchip').first().evaluate((e, k) => getComputedStyle(e).getPropertyValue(k), k), await h.locator('.artchip').first().evaluate((e, k) => getComputedStyle(e).getPropertyValue(k), k), 'artchip ' + k);
  const th = await p.locator('section[aria-label="Code delivery"] .prs thead').innerText(); assert.match(th, /PR\s+Change\s+Shipped by\s+State\s+CI\s+Deployed/i, 'PR columns as V8 (Size left out: no data)');
  const r11 = p.locator('tr.pr[data-open="11"]'); assert.match(await r11.locator('td').nth(2).innerText(), /Worker[\s\S]*claude s-b · M1 1\.2/, 'shipped by: tab, kind, session, milestone and job');
  assert.match(await p.locator('tr.pr[data-open="10"] td.hide-m').innerText(), /on stage/); assert.match(await p.locator('tr.pr[data-open="11"] td.hide-m').innerText(), /—/); assert.doesNotMatch(await p.locator('tr.pr[data-open="9"] td.hide-m').innerText(), /on stage/);
  // a blocked bar carries V8's "Needs you" tag, styled as V8's, and opens its item
  const nt = p.locator('.ss[data-sess="s-c"] .tag.needs'); assert.equal((await nt.innerText()).trim(), 'Needs you', 'Needs you tag inside the blocked bar');
  for (const k of ['background-color', 'color', 'font-size', 'font-weight', 'border-top-left-radius']) assert.equal(await nt.evaluate((e, k) => getComputedStyle(e).getPropertyValue(k), k), await h.locator('.ss .tag.needs').first().evaluate((e, k) => getComputedStyle(e).getPropertyValue(k), k), 'bar Needs-you tag ' + k);
  assert.ok(await nt.evaluate((e) => { const b = e.closest('.ss').getBoundingClientRect(), r = e.getBoundingClientRect(); return b.right - r.right < 16 && r.left - b.left > b.width / 3; }), 'pushed to the bar\'s right end, as V8 (margin-left: auto)');
  assert.equal(await p.locator('.ss[data-sess="s-b"] .tag').count(), 0, 'a bar with nothing waiting has no tag');
  await p.evaluate(() => { FV.q = null; FV.sel = null; render(); }); await nt.click(); await sleep(150); assert.equal(await p.evaluate(() => FV.q), 'tk-1', 'the tag opens its Needs-you item'); await p.evaluate(() => { FV.sel = null; render(); }); await sleep(150);
  // on timeline ↑: dims the other milestones' bars, says what is highlighted, Clear resets
  await p.locator('.ms[data-ms="M1"] > summary [data-fvmshi="M1"]').click(); await sleep(200);
  assert.match(await p.locator('section[aria-label="Team timeline"] .h3').innerText(), /Highlighting\s+M1/);
  await p.waitForFunction(() => !document.querySelector('.ss[data-sess="s-c"]').getAnimations().length, null, { timeout: 3000 }).catch(() => {}); // its .16s opacity transition
  assert.equal(await p.locator(`.ss[data-sess="s-c"]`).evaluate((e) => getComputedStyle(e).opacity), '0.25', 'other milestone dimmed'); assert.equal(await p.locator(`.ss[data-sess="s-b"]`).evaluate((e) => getComputedStyle(e).opacity), '1');
  assert.equal(await p.locator('.ms[data-ms="M1"]').getAttribute('open'), '', 'the button does not fold its milestone');
  await p.locator('section[aria-label="Team timeline"] [data-fvmshi=""]').click(); await sleep(150); assert.equal(await p.locator('.ss.dim').count(), 0, 'Clear');
  assert.deepEqual(hard, [], 'section surfaces/titles differ from the V8 HTML');
  await c.close();
});

test('F-R10 REAL objectives and evidence (shared record, read only): the Objectives tab shows the server\'s met/missing, unknown coverage, proof links; nothing without a receipt or attestation is called evidenced', async () => {
  const pg0 = (await api('GET', R.feature(S))).body.page; const O = pg0.objectives, EV = pg0.evidence || {};
  assert.ok(O && Array.isArray(O.items) && O.items.length, 'page.objectives served: ' + JSON.stringify(O)?.slice(0, 200));
  console.log('# real objectives:', JSON.stringify(O).slice(0, 400)); console.log('# real evidence:', JSON.stringify(EV).slice(0, 400));
  const { c, p } = await fresh({}, null, null, true);
  await p.locator('[data-fvpt="obj"]').click(); await p.locator('.dod > li[data-obj]').first().waitFor();
  assert.match(await p.locator(SEL.section('Plan and milestones')).innerText(), new RegExp(O.text ? O.text.slice(0, 30).replace(/[.*+?^${}()|[\]\\]/g, '\\$&') : '.'));
  for (const o of O.items) {
    const li = p.locator(`.dod > li[data-obj="${o.id}"]`); assert.equal(await li.count(), 1, 'objective ' + o.id);
    const t = await li.innerText();
    if (o.met) assert.match(t, /\bMet\b/); else { assert.doesNotMatch(t, /(^|\n|\s)Met(\s|$)/); for (const m of o.missing || []) assert.ok(t.includes(m), `missing reason shown for ${o.id}: ${m}`); }
    for (const k of o.steps) { const e = EV[k], ok = !!(e?.check || e?.attest); const row = await li.locator('.cov > div').filter({ has: p.locator(`[data-fvgo="${k}"]`) }).first().innerText();
      if (!ok) assert.doesNotMatch(row, /evidenced/, `job ${k} shown evidenced without a receipt or attestation`); }
  }
  if ((O.unknown || []).length) assert.match(await p.locator(SEL.section('Plan and milestones')).innerText(), /which no objective has/);
  const go = p.locator('.dod [data-proof-go]').first();
  if (await go.count()) { const k = await go.getAttribute('data-proof-go'); await go.click(); await p.locator(`[data-key="${k}"] .pf`).waitFor();
    const box = await p.locator(`[data-key="${k}"] .pf`).innerText(); const e = EV[k];
    if (!e?.check) assert.match(box, /CHECK RESULT[^\n]*\n(None|no receipt|—)/i, 'no receipt shown as a check');
    if (e?.assessment) assert.match(box, /author unknown|independence not claimed|assessed its own|another session/i);
    assert.doesNotMatch(box, /\bverified\b/i, 'the proof box calls something verified'); }
  await c.close();
});

test('F-R11 REAL dependencies: after: by key is a recorded blocks edge, listed (not drawn) while no session is on it; an invalid prerequisite is shown on its job and listed; assigning a blocked step is refused, no bar invented', async () => {
  const name = 'ui8d-' + Date.now().toString(36); await seedFeature(name);
  const d0 = (await api('GET', R.feature(name))).body, s0 = d0.dossier.milestones.flatMap((m) => m.steps), first = s0.find((x) => x.text.startsWith('Write ledger'));
  const plan = readFileSync(new URL('./fixture/PLAN.md', import.meta.url), 'utf8').replace('  - after: 1.1\n', `  - after: ${first.key}\n`) + '- [ ] Bad prereq job @Reviewer\n  - after: 7.7\n';
  const w = await api('POST', R.doc, { session: name, doc: 'plan', rev: d0.dossier.revs.plan, text: plan }); assert.equal(w.status, 200, 'plan write: ' + JSON.stringify(w.body));
  const d1 = (await api('GET', R.feature(name))).body, st = d1.dossier.milestones.flatMap((m) => m.steps), chk = st.find((x) => x.text.startsWith('Check ledger')), bad = st.find((x) => x.text.startsWith('Bad prereq'));
  assert.deepEqual(chk.after, [first.key]); assert.equal(bad.afterInvalid?.[0]?.ref, '7.7');
  const asg = await agent('POST', R.aAssign, { session: name, step: bad.key, tab: 'Reviewer' }); assert.equal(asg.status, 409, 'a blocked step is not assignable: ' + JSON.stringify(asg.body));
  const pg = d1.page; console.log('# real sessions:', JSON.stringify(pg.sessions)); console.log('# real relations:', JSON.stringify(pg.relations));
  const { c, p } = await fresh({}, null, null, true, name); await sleep(500);
  assert.equal(await p.locator(SEL.laneBar).count(), (pg.sessions || []).length, 'one bar per real session, none invented');
  await p.locator(`.ms[data-ms="M2"] > summary`).click().catch(() => {});
  assert.match(await p.locator(SEL.step(chk.key)).textContent(), /waits on/); assert.equal(await p.locator(`${SEL.step(bad.key)} [data-after-invalid]`).count(), 1, 'invalid prerequisite on its job');
  if (pg.sessions) { assert.equal(await p.locator(`[data-dep-issue="${bad.key}"]`).count(), 1, 'invalid prerequisite listed');
    for (const r of (pg.relations || []).filter((r) => !r.from || !r.to)) assert.equal(await p.locator(`[data-rel-open="${r.id}"]`).count(), 1, 'edge without a session end listed: ' + r.id);
    assert.equal(await p.locator('#fv-deps [data-rel]').count(), (pg.relations || []).filter((r) => r.from && r.to).length, 'only edges with both ends drawn'); }
  await c.close();
});

test('F-D6 FIXTURE: a failed record read is never shown as an empty feature: first read fails → alert; a later failure keeps the last record, marked not refreshed', async () => {
  const c = await br.newContext({ viewport: { width: 1440, height: 1000 } }); await c.request.post(B + '/api/login', { data: { pin: '482913' } });
  const p = await c.newPage(); p.on('pageerror', (e) => errs.push(String(e))); p.on('console', onConsole);
  let mode = 'down'; await p.route((u) => u.pathname === '/api/term/feature', (r) => { if (mode === 'down') { expectFail('/api/term/feature', 503, 'F-D6 injected outage'); return r.fulfill({ status: 503, json: { error: 'ui8 down' } }); } return r.fulfill({ json: fixture() }); });
  await p.goto(B + '/'); await p.waitForFunction(() => typeof setView === 'function'); await p.evaluate(openFeatureJS(S));
  await p.locator('.fv [role=alert]').waitFor(); const t0 = await p.locator('.fv').innerText();
  assert.match(t0, /Couldn't read this feature's record \(error 503: ui8 down\)/); assert.doesNotMatch(t0, /No PLAN\.md yet|Nothing needs you/);
  mode = 'up'; await p.evaluate((s) => { FP.at = 0; return fpLoad(s, true); }, S); await p.waitForSelector(SEL.step(K.a), { state: 'attached' });
  mode = 'down'; await p.evaluate((s) => { FP.at = 0; return fpLoad(s, true); }, S); await sleep(300);
  assert.match(await p.locator('.fv').innerText(), /Not refreshed\./); assert.equal(await p.locator(SEL.step(K.a)).count(), 1, 'last record kept');
  await c.close();
});

test('F-R12 REAL first load after a restart: a known record before the first team sample is the plan with "team not sampled", never "no plan"', async () => {
  const un = 'ui8u-' + Date.now().toString(36); await seedFeature(un); // unbound: no manager
  await restartPanel(); const t0 = new Date().toISOString(); const [rb, r0] = [await api('GET', R.feature(S)), await api('GET', R.feature(un))];
  if (OUT) for (const [n, x, nm] of [['bound', rb, S], ['unbound', r0, un]]) writeFileSync(`${OUT}/f-r12-${n}-first-answer.json`, JSON.stringify({ at: t0, request: 'GET ' + R.feature(nm), status: x.status, body: x.body }, null, 1));
  assert.equal(r0.status, 200); assert.equal(r0.body.thread ?? null, null, 'unbound');
  console.log('# real first answers after restart (saved f-r12-*.json): unbound teamUnsampled=' + r0.body.page?.teamUnsampled + ' steps=' + steps(r0.body).length + ' · bound teamUnsampled=' + rb.body.page?.teamUnsampled + ' steps=' + steps(rb.body).length);
  assert.ok(steps(rb.body).length > 0, 'bound record has its plan before any sample');
  assert.ok(steps(r0.body).length > 0, 'the record has its plan before any sample (W7)');
  const c = await br.newContext({ viewport: { width: 1440, height: 1000 } }); await c.request.post(B + '/api/login', { data: { pin: '482913' } });
  const p = await c.newPage(); p.on('pageerror', (e) => errs.push(String(e))); p.on('console', onConsole);
  let first = null, replayed = false;
  await p.route((u) => u.pathname === '/api/term/feature', async (r) => { const resp = await r.fetch(); const b = await resp.json();
    if (!first) { first = b; if (!b.page?.teamUnsampled && r0.body.page?.teamUnsampled) { replayed = true; return r.fulfill({ json: r0.body }); } } return r.fulfill({ status: resp.status(), json: b }); });
  await p.goto(B + '/'); await p.waitForFunction(() => typeof setView === 'function'); await p.evaluate(openFeatureJS(un));
  await p.waitForSelector('.steps li[data-key]', { state: 'attached' });
  console.log('# F-R12 UI saw ' + (replayed ? 'the REAL first answer, replayed (the sample landed before the browser loaded)' : 'the real answer live') + '; teamUnsampled=' + (replayed ? true : first?.page?.teamUnsampled));
  const t = await p.locator('.fv').innerText(); assert.doesNotMatch(t, /No PLAN\.md yet/);
  if (replayed || first?.page?.teamUnsampled) { assert.match(t, /Team not sampled yet/); assert.equal(await p.locator('.ss.open:not(.st-unknown)').count(), 0, 'no open bar claims to be processing'); }
  await c.close();
});

test('F-L7 control: an unexpected failed request is caught (and only that one is removed)', async () => {
  const { c, p } = await fresh(); const n = errs.length;
  await p.evaluate(() => fetch('/api/term/feature/not-a-route', { method: 'POST', body: '{}' }).catch(() => null)); await sleep(500);
  const got = errs.slice(n); assert.equal(got.length, 1, 'unregistered failure not caught: ' + JSON.stringify(got)); assert.match(got[0], /unexpected \d+ from \/api\/term\/feature\/not-a-route/);
  errs.splice(n, 1); await c.close();
});

test('F-L6 no page errors; viewing never calls /report; every model call is attributed (stand-in only, never from viewing)', () => {
  assert.deepEqual(errs, []);
  assert.deepEqual(expected.filter((x) => !x.seen).map((x) => `${x.status} ${x.path}: ${x.why}`), [], 'an expected failure never happened');
  assert.deepEqual(reports, []);
  // claude-calls.log is written only by the stand-in `claude` first on the panel's PATH (it refuses -p): a call there was
  // never a real model run. One the panel log doesn't tie to its background scheduler (chapters.go: +60 s, then every
  // 4 min; digests/chapters) fails this test; a scheduler one is recorded in model-calls.txt, not waived as a flake.
  const plog = OUT && existsSync(`${OUT}/panel.log`) ? readFileSync(`${OUT}/panel.log`, 'utf8') : '';
  const calls = lines('claude-calls.log').filter((l) => / -p | --print /.test(l + ' '));
  const why = (l) => { const t = l.slice(0, 19).replace('T', ' ').replace(/-/g, '/'); return [...plog.matchAll(new RegExp(`^${t.replace(/[/:]/g, '\\$&')} (digests|chapters): (.*)$`, 'gm'))].map((m) => m[1] + ': ' + m[2]); };
  const rows = calls.map((l) => ({ call: l, scheduler: why(l) }));
  if (OUT) for (const r of rows) appendFileSync(`${OUT}/model-calls.txt`, `${r.call}\n  stand-in claude (refused, no model run) · ${r.scheduler.length ? 'background scheduler: ' + r.scheduler.join('; ') : 'UNATTRIBUTED'}\n`);
  for (const r of rows) console.log('# model call (stand-in):', r.call.slice(0, 40), r.scheduler.length ? '→ scheduler ' + r.scheduler.join('; ') : '→ UNATTRIBUTED');
  assert.deepEqual(rows.filter((r) => !r.scheduler.length).map((r) => r.call), [], 'a model call not from the background scheduler');
});
