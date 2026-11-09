// Feature-page acceptance: the event log and lead assignments (ids E1..E9). Black box over HTTP and the run's own state dir.
// Runs after api.test.mjs on the same record (serial).
import { test, before } from 'node:test';
import assert from 'node:assert/strict';
import { chmodSync } from 'node:fs';
import { R } from './names.mjs';
import { F as F0, B, STATE, api, agent, turns, hash, restartPanel, steps, seedFeature } from './lib.mjs';

// this suite's own feature (no manager bound): the shared record is rebound by api.test.mjs C6
const S = `${F0.session}-ev`, F = { ...F0, session: S };
const feature = async () => (await api('GET', R.feature(S))).body;
before(async () => { await seedFeature(S); });

// the manager holding the feature now (api.test.mjs C6 rebinds it), and one that doesn't
const mgr = async () => (await feature()).thread?.id ?? ''; // '' = no manager: the feature is the owner's
const other = async () => ((await mgr()) === F.thread ? F.threadB : F.thread);
const keys = async () => { const k = steps(await feature()).map((s) => s.key); assert.ok(k.length >= 3, `the record has ${k.length} steps`); return k; };
const assign = (b) => agent('POST', R.aAssign, { session: S, ...b });
const seqOf = (id) => { const m = /^ev-(\d+)$/.exec(id); assert.ok(m, `id ${id} is not ev-<n>`); return +m[1]; };
// every event of S, paged by the last id seen (works whether or not the server sends a cursor)
async function all(route = (a, l) => api('GET', R.events(S, a, l)), limit = 200) {
  const got = []; let after = 'ev-0';
  for (let i = 0; i < 1000; i++) {
    const r = await route(after, limit); assert.equal(r.status, 200, JSON.stringify(r.body));
    if (!r.body.events.length) break;
    got.push(...r.body.events); after = r.body.events.at(-1).id;
  }
  return got;
}
const lastSeq = async () => { const a = await all(); return a.length ? a.at(-1).seq : 0; };
const contiguous = (evs) => { const q = evs.map((e) => e.seq); for (let i = 1; i < q.length; i++) assert.equal(q[i], q[i - 1] + 1, `gap/dup after seq ${q[i - 1]}`); };

test('E1 invalid assignments are refused and record nothing (no id consumed)', async () => {
  const [k1, k2] = await keys(); const before = await lastSeq(); const m = await mgr(), wrong = await other();
  const bad = [
    [{ step: 'deadbeef0000', tab: 'Worker' }, 400, 'unknown step'],
    [{ step: k1 }, 400, 'no tab'],
    [{ step: k1, tab: 'Worker', relation: { kind: 'friend', with: k2 } }, 400, 'relation kind'],
    [{ step: k1, tab: 'Worker', relation: { kind: 'pair', with: k1 } }, 400, 'relation with itself'],
    [{ step: k1, tab: 'Worker', relation: { kind: 'pair', with: 'deadbeef0000' } }, 400, 'relation with unknown step'],
    [{ step: k1, tab: 'Worker', thread: wrong }, 409, 'caller is not the bound manager'],
  ];
  for (const [b, code, why] of bad) {
    const r = await assign(b); assert.equal(r.status, code, `${why}: ${JSON.stringify(r.body)}`);
    if (code === 409) assert.equal(r.body.thread, m, '409 names the manager that holds the feature');
  }
  assert.equal(await lastSeq(), before, 'a refused assignment left an event');
  const ok = await assign({ step: k1, tab: 'Worker', ...(m ? { thread: m } : {}) }); assert.equal(ok.status, 200, JSON.stringify(ok.body));
  assert.equal(seqOf(ok.body.id), before + 1, 'refusals consumed ids');
});

test('E2 an assignment is recorded as stated, attributed to the bound manager, and types nothing anywhere', async () => {
  const [k1] = await keys(); const t0 = turns().length; const m = await mgr();
  const r = await assign({ step: k1, tab: 'Worker', pane: 1, sid: 'claimed-sid-qa', ...(m ? { thread: m } : {}) });
  assert.equal(r.status, 200, JSON.stringify(r.body)); assert.equal(r.body.ok, true);
  const e = (await all()).find((x) => x.id === r.body.id); assert.ok(e, 'the returned id is readable');
  assert.equal(e.kind, 'assign'); assert.equal(e.step, k1); assert.equal(e.tab, 'Worker'); assert.equal(e.pane, 1);
  assert.equal(e.sid, 'claimed-sid-qa', 'sid kept as a claim');
  assert.equal(e.thread ?? '', m, 'attributed to the record\'s manager, not to the caller');
  assert.equal(turns().length, t0, 'an assignment sent a message to a manager');
});

test('E3 a relation exists only when an assign states it (the sessions route)', async () => {
  const [k1, k2] = await keys();
  const before = await api('GET', R.sessions(S)); assert.equal(before.status, 200, JSON.stringify(before.body));
  assert.ok(!(before.body.relations || []).some((x) => x.kind === 'pair'), 'pair relation before any stated one');
  const r = await assign({ step: k2, tab: 'Worker', relation: { kind: 'pair', with: k1, why: 'qa-pair-why' } });
  assert.equal(r.status, 200, JSON.stringify(r.body));
  const rel = (await api('GET', R.sessions(S))).body.relations || [];
  const p = rel.filter((x) => x.kind === 'pair'); assert.equal(p.length, 1, JSON.stringify(rel));
  assert.ok(JSON.stringify(p[0]).includes('qa-pair-why'), 'why carried'); assert.ok(p[0].src, 'names its source');
});

test('E4 paging 285+ events by id: ascending, contiguous, no duplicates, new arrivals mid-paging not lost', async () => {
  const [k1] = await keys(); const start = await lastSeq();
  for (let i = 0; i < 285; i++) assert.equal((await assign({ step: k1, tab: 'Worker', pane: i % 4 })).status, 200);
  const got = []; let after = 'ev-0', pages = 0, added = 0;
  for (;;) {
    const r = await api('GET', R.events(S, after, 50)); assert.equal(r.status, 200, JSON.stringify(r.body));
    assert.ok(r.body.events.length <= 50, 'limit honoured');
    got.push(...r.body.events); pages++;
    if (pages === 2) for (; added < 7; added++) assert.equal((await assign({ step: k1, tab: 'Late' })).status, 200);
    if (!r.body.events.length) break;
    after = r.body.events.at(-1).id;
  }
  contiguous(got);
  assert.equal(got[0].seq, 1, 'paging from the start begins at ev-1');
  assert.equal(got.at(-1).seq, start + 285 + 7, 'last event = everything written, including arrivals mid-paging');
  assert.equal(got.filter((e) => e.tab === 'Late').length, 7, 'arrivals during paging are all read once');
  for (const e of got) assert.equal(e.id, `ev-${e.seq}`);
  assert.ok(pages >= 7, `only ${pages} pages`);
});

test('E5 events route: limits, bad cursors, auth', async () => {
  const big = await api('GET', R.events(S, 'ev-0', 500)); assert.equal(big.status, 200); assert.ok(big.body.events.length <= 200, 'limit capped at 200');
  const def = await api('GET', R.events(S, 'ev-0', '')); assert.equal(def.status, 200); assert.equal(def.body.events.length, 200, 'default page = 200');
  for (const [a, l] of [['ev-0', 0], ['ev-0', -1], ['ev-x', 10], ['7', 10], ['ev-0', 'abc']]) assert.equal((await api('GET', R.events(S, a, l))).status, 400, `after=${a} limit=${l}`);
  const tail = await api('GET', R.events(S, `ev-${await lastSeq()}`, 10)); assert.deepEqual(tail.body.events, [], 'nothing after the last id');
  const anon = await fetch(B + R.events(S, 'ev-0', 1)); assert.ok([401, 403].includes(anon.status), `owner route without login: ${anon.status}`);
  const noBearer = await fetch(`http://127.0.0.1:${F.p2}` + R.aEvents(S, 'ev-0', 1)); assert.equal(noBearer.status, 401);
  assert.deepEqual(await all((a, l) => agent('GET', R.aEvents(S, a, l))), await all(), 'agent and owner read the same log');
});

test('E6 events are per feature: another session reads none of them', async () => {
  for (const o of [F.threadB, `${S}-none`]) {
    const r = await api('GET', R.events(o, 'ev-0', 200)); assert.equal(r.status, 200, JSON.stringify(r.body));
    assert.deepEqual(r.body.events, [], `${o} sees ${S}'s events`);
  }
});

test('E7 restart: the log persists unchanged and ids continue, never reused', async () => {
  const before = await all(); await restartPanel();
  assert.deepEqual(await all(), before, 'log changed across restart');
  const [k1] = await keys(); const r = await assign({ step: k1, tab: 'Worker' });
  assert.equal(seqOf(r.body.id), before.at(-1).seq + 1);
});

test('E8 save failure: 500, the event does not exist, no id is burnt, state file untouched', async () => {
  const [k1] = await keys(); const before = await all(); const h = hash(`${STATE}/featsup.json`);
  chmodSync(STATE, 0o555); // the run's own private state dir: the atomic write cannot create its temp file
  let r; try { r = await assign({ step: k1, tab: 'Worker' }); } finally { chmodSync(STATE, 0o755); }
  assert.equal(r.status, 500, JSON.stringify(r.body)); assert.match(r.body.error || '', /not saved/);
  assert.deepEqual(await all(), before, 'an unsaved event is visible');
  assert.equal(hash(`${STATE}/featsup.json`), h, 'state file changed');
  const ok = await assign({ step: k1, tab: 'Worker' }); assert.equal(seqOf(ok.body.id), before.at(-1).seq + 1, 'the failed save consumed an id');
  await restartPanel(); assert.equal((await all()).at(-1).id, ok.body.id, 'after restart the log is what the server acknowledged');
});

test('E9 wire shape (lead, 2026-11-08): {events, more}; events {id, seq, at RFC3339, kind, step, tab, …}; more is exact', async () => {
  const n = await lastSeq();
  const r = await api('GET', R.events(S, 'ev-0', 2)); assert.equal(r.status, 200);
  assert.deepEqual(Object.keys(r.body).sort(), ['events', 'more']);
  assert.equal(r.body.more, n > 2, 'more on a partial page');
  const e = r.body.events[0];
  assert.equal(typeof e.at, 'string'); assert.match(e.at, /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)$/, 'at is RFC3339');
  const t = Date.parse(e.at) / 1000; assert.ok(t > 1.7e9 && t < Date.now() / 1000 + 5, `at ${e.at} out of range`);
  assert.equal(e.id, `ev-${e.seq}`); assert.equal(typeof e.kind, 'string');
  const end = await api('GET', R.events(S, `ev-${n - 1}`, 5)); assert.equal(end.body.events.length, 1); assert.equal(end.body.more, false, 'more on the last page');
});
