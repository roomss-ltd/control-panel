// Feature-page acceptance: seen and reviewed at the route level (ids W1..W8). State is read through the routes' own
// answers and the run's private featsup.json.
import { test, before } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, chmodSync } from 'node:fs';
import { R } from './names.mjs';
import { F, STATE, api, agent, hash, restartPanel, steps, seedFeature } from './lib.mjs';

const S = `${F.session}-sa`, S2 = `${F.session}-sb`; // two fresh features of this suite's own: ids in each start at ev-1
const sup = () => JSON.parse(readFileSync(`${STATE}/featsup.json`, 'utf8'));
const seenOf = (s) => sup().seen?.[s] ?? { frontier: 0 };
const reviewedOf = (s) => sup().reviewed?.[s] ?? {};
const evs = async (s) => { const out = []; let a = 'ev-0'; for (;;) { const r = await api('GET', R.events(s, a, 200)); assert.equal(r.status, 200); out.push(...r.body.events); if (!r.body.more) return out; a = out.at(-1).id; } };
// after a restart the record has read empty for a moment in full runs (W7): the wait only measures it; reading empty fails
const keysOf = async (s) => { const t0 = Date.now(); let k = [];
  while (Date.now() - t0 < 5000) { k = steps((await api('GET', R.feature(s))).body).map((x) => x.key); if (k.length >= 3) break; await new Promise((r) => setTimeout(r, 200)); }
  if (Date.now() - t0 > 300) console.log(`# NOTE ${s}: the record read ${k.length >= 3 ? 'empty for ' + (Date.now() - t0) + ' ms' : 'EMPTY for 5 s'} after a restart`);
  assert.ok(k.length >= 3, `${s} has ${k.length} steps`);
  assert.ok(Date.now() - t0 <= 300, `product loading-state bug (lead 2026-11-08): the record read empty for ${Date.now() - t0} ms after a restart (the wait only diagnoses it)`); return k; };
const assign = async (s, n, tab = 'Worker') => { const [k] = await keysOf(s); const ids = []; for (let i = 0; i < n; i++) { const r = await agent('POST', R.aAssign, { session: s, step: k, tab }); assert.equal(r.status, 200, JSON.stringify(r.body)); ids.push(r.body.id); } return ids; };
const seen = (s, ids) => api('POST', R.seen, { session: s, ids });

before(async () => { await seedFeature(S); await seedFeature(S2); });

test('W1 a batch with any bad id → 400 {error, bad:[{id, reason}]}, nothing recorded; >200 ids refused', async () => {
  const [good] = await assign(S, 1); const h = hash(`${STATE}/featsup.json`), s0 = seenOf(S);
  const r = await seen(S, [good, 'ev-99999999', 'not-an-id']);
  assert.equal(r.status, 400); assert.ok(r.body.error);
  assert.deepEqual(r.body.bad, [{ id: 'ev-99999999', reason: 'unknown' }, { id: 'not-an-id', reason: 'malformed' }]);
  assert.equal(hash(`${STATE}/featsup.json`), h, 'a refused batch wrote state'); assert.deepEqual(seenOf(S), s0);
  assert.equal((await seen(S, Array.from({ length: 201 }, (_, i) => `ev-${i + 1}`))).status, 400);
});

test('W2 the frontier moves only over contiguous ids; sparse receipts are kept, counted and idempotent', async () => {
  const all = await evs(S); assert.equal((await seen(S, all.map((e) => e.id).slice(0, 200))).status, 200);
  const rest = all.slice(200).map((e) => e.id); for (let i = 0; i < rest.length; i += 200) assert.equal((await seen(S, rest.slice(i, i + 200))).status, 200);
  const base = all.at(-1).seq; assert.equal(seenOf(S).frontier, base, 'everything seen = frontier at the last id');
  const ids = await assign(S, 5); // base+1 .. base+5
  const top = await seen(S, [ids[4]]); assert.equal(top.status, 200);
  assert.equal(top.body.frontier, `ev-${base}`, 'a receipt past a gap moved the frontier'); assert.equal(top.body.unseen, 4);
  const mid = await seen(S, [ids[2]]); assert.equal(mid.body.frontier, `ev-${base}`); assert.equal(mid.body.unseen, 3);
  const fill = await seen(S, [ids[0], ids[1]]); assert.equal(fill.body.frontier, `ev-${base + 3}`, 'frontier stops at the next gap');
  const close = await seen(S, [ids[3]]); assert.equal(close.body.frontier, `ev-${base + 5}`); assert.equal(close.body.unseen, 0);
  const again = await seen(S, [ids[3], ids[4]]); assert.equal(again.status, 200); assert.equal(again.body.frontier, `ev-${base + 5}`);
  assert.deepEqual(reviewedOf(S), {}, 'seen created reviews');
});

test('W3 ids collide across features (ev-1 each): receipts and reviews stay in their own feature', async () => {
  const b = await assign(S2, 3, 'B'); assert.deepEqual(b, ['ev-1', 'ev-2', 'ev-3'], 'second feature numbers from ev-1');
  const s2 = seenOf(S2), rv2 = reviewedOf(S2);
  assert.equal((await seen(S, ['ev-1', 'ev-2', 'ev-3'])).status, 200);
  assert.deepEqual(seenOf(S2), s2, 'seeing S ev-1..3 marked S2 events seen');
  const e1 = (await evs(S)).find((e) => e.id === 'ev-1');
  const rv = await api('POST', R.reviewed, { session: S, id: 'ev-1', rev: e1.rev ?? '' });
  assert.ok([200, 409].includes(rv.status), JSON.stringify(rv.body));
  assert.deepEqual(reviewedOf(S2), rv2, 'a review on S landed on S2');
  const beyond = `ev-${(await evs(S)).at(-1).seq}`; // exists in S, not in S2
  const r = await seen(S2, [beyond]); assert.equal(r.status, 400); assert.deepEqual(r.body.bad, [{ id: beyond, reason: 'unknown' }]);
  const ok = await seen(S2, ['ev-1']); assert.equal(ok.body.frontier, 'ev-1'); assert.equal(ok.body.unseen, 2, 'S2 counts its own events only');
});

test('W4 reviewed: 409 {changed, rev} on another rev or a newer same-kind event for the step; separate from seen', async () => {
  const [a, b] = await assign(S2, 2, 'R'); // same step, same kind: a is superseded by b
  const ev = Object.fromEntries((await evs(S2)).map((e) => [e.id, e]));
  const wrong = await api('POST', R.reviewed, { session: S2, id: b, rev: 'not-the-rev' });
  assert.equal(wrong.status, 409); assert.equal(wrong.body.changed, b); assert.equal(wrong.body.rev, ev[b].rev ?? '');
  const stale = await api('POST', R.reviewed, { session: S2, id: a, rev: ev[a].rev ?? '' });
  assert.equal(stale.status, 409); assert.equal(stale.body.changed, b, 'names the newer event');
  const s0 = seenOf(S2);
  const ok = await api('POST', R.reviewed, { session: S2, id: b, rev: ev[b].rev ?? '' }); assert.equal(ok.status, 200, JSON.stringify(ok.body));
  assert.ok(b in reviewedOf(S2)); assert.ok(!(a in reviewedOf(S2)), 'the refused review was recorded');
  assert.deepEqual(seenOf(S2), s0, 'reviewing changed seen');
  for (const id of ['ev-99999999', 'nope']) assert.equal((await api('POST', R.reviewed, { session: S2, id, rev: '' })).status, 400);
});

test('W5 opening the page reads nothing as seen or reviewed (no auto-read)', async () => {
  await assign(S, 2); const h = hash(`${STATE}/featsup.json`), s0 = seenOf(S), r0 = reviewedOf(S);
  for (let i = 0; i < 3; i++) { await api('GET', R.feature(S)); await api('GET', R.events(S, 'ev-0', 200)); await api('GET', R.sessions(S)); }
  assert.equal(hash(`${STATE}/featsup.json`), h); assert.deepEqual(seenOf(S), s0); assert.deepEqual(reviewedOf(S), r0);
});

test('W6 seen and reviewed survive a restart unchanged', async () => {
  const s = seenOf(S), r = reviewedOf(S2); await restartPanel();
  assert.deepEqual(seenOf(S), s); assert.deepEqual(reviewedOf(S2), r);
  const probe = await seen(S, []); assert.equal(probe.status, 200); assert.equal(probe.body.frontier, `ev-${s.frontier}`);
});

test('W7 a seen or reviewed whose save fails is not recorded (500)', async () => {
  const [x] = await assign(S, 1); const s0 = seenOf(S), h = hash(`${STATE}/featsup.json`);
  const e = (await evs(S)).find((v) => v.id === x);
  chmodSync(STATE, 0o555); let a, b;
  try { a = await seen(S, [x]); b = await api('POST', R.reviewed, { session: S, id: x, rev: e.rev ?? '' }); } finally { chmodSync(STATE, 0o755); }
  assert.equal(a.status, 500, JSON.stringify(a.body)); assert.equal(b.status, 500, JSON.stringify(b.body));
  assert.deepEqual(seenOf(S), s0, 'a failed receipt was written'); // the whole file may change meanwhile: the supervisor writes it in the background
  const now = await seen(S, []); assert.equal(now.body.frontier, `ev-${s0.frontier}`, 'the failed receipt is counted in memory');
  assert.ok(!(x in reviewedOf(S)));
});
