// Feature-page acceptance, adversarial route regressions. Each test runs on fresh features of its own and fails its
// preconditions explicitly: rebind identity (AD1), retained seen state under the receipt cap and compaction (AD2), a
// pending review surviving compaction (AD3).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, chmodSync } from 'node:fs';
import { R } from './names.mjs';
import { F, STATE, api, agent, restartPanel, steps, seedFeature, turns } from './lib.mjs';

const sup = () => JSON.parse(readFileSync(`${STATE}/featsup.json`, 'utf8'));
const fe = async (s) => (await api('GET', R.feature(s))).body;
const evs = async (s) => { const out = []; let a = 'ev-0'; for (;;) { const r = await api('GET', R.events(s, a, 200)); assert.equal(r.status, 200, JSON.stringify(r.body)); out.push(...r.body.events); if (!r.body.more) return out; a = out.at(-1).id; } };
const assignN = async (s, n, step) => { for (let i = 0; i < n; i++) { const r = await agent('POST', R.aAssign, { session: s, step, tab: 'W' + (i % 3) }); assert.equal(r.status, 200, JSON.stringify(r.body)); } };
const seenBatch = async (s, ids) => { for (let i = 0; i < ids.length; i += 200) { const r = await api('POST', R.seen, { session: s, ids: ids.slice(i, i + 200) }); assert.equal(r.status, 200, JSON.stringify(r.body)); } };
const unseenNow = async (s) => { const r = await api('POST', R.seen, { session: s, ids: [] }); assert.equal(r.status, 200); return r.body.unseen; };
const isSeen = (s, seq) => { const x = sup().seen?.[s] ?? { frontier: 0, above: [] }; return seq <= x.frontier || (x.above || []).includes(seq); };
const snapshot = async (s) => { const f = await fe(s); return { plan: f.dossier?.plan, feature: f.dossier?.feature, keys: steps(f).map((x) => x.key), events: await evs(s), seen: sup().seen?.[s], reviewed: sup().reviewed?.[s] }; };

test('AD1 rebinding a feature to another manager keeps the exact record, step keys, events, seen and reviewed', async () => {
  const s = `${F.session}-ad1`, [A, B] = F.spare.slice(0, 2);
  const keys = await seedFeature(s, A);
  await assignN(s, 4, keys[0]);
  const ids = (await evs(s)).map((e) => e.id);
  assert.equal((await api('POST', R.seen, { session: s, ids: [ids[0], ids[2]] })).status, 200);
  const e3 = (await evs(s))[3]; assert.equal((await api('POST', R.reviewed, { session: s, id: e3.id, rev: e3.rev ?? '' })).status, 200);
  const before = await snapshot(s); assert.ok(before.keys.length >= 3 && before.plan, 'precondition: the bound record has its plan');
  assert.equal((await api('POST', '/api/ai/thread/archive', { id: A })).status, 200, 'archive A');
  const b = await api('POST', R.bind, { session: s, thread: B }); assert.equal(b.status, 200, JSON.stringify(b.body));
  const after = await snapshot(s);
  assert.equal(after.plan, before.plan, 'the record the page reads changed on rebind (record/event split)');
  assert.deepEqual(after.keys, before.keys, 'step keys changed on rebind');
  assert.deepEqual(after.events, before.events, 'events changed on rebind');
  assert.deepEqual([after.seen, after.reviewed], [before.seen, before.reviewed], 'seen/reviewed changed on rebind');
  const r = await agent('POST', R.aAssign, { session: s, step: keys[0], tab: 'W', thread: B }); assert.equal(r.status, 200, 'the new manager cannot assign on the kept steps: ' + JSON.stringify(r.body));
  assert.equal(r.body.id, `ev-${before.events.at(-1).seq + 1}`, 'ids continue across the rebind');
});

test('AD2 >1000 sparse receipts, compaction past 5000 and restarts: a retained seen event never turns unseen, an unseen one never turns seen', async () => {
  const s = `${F.session}-ad2`, [k] = await seedFeature(s);
  const t0 = Date.now(); await assignN(s, 2601, k);
  const marked = new Set(); for (let q = 2; q <= 2600; q += 2) marked.add(q); // ev-1 never seen: the frontier stays at 0, 1300 sparse receipts
  await seenBatch(s, [...marked].map((q) => `ev-${q}`));
  const issues = []; // every stage runs; all violations are reported together at the end
  const check = async (when) => {
    const all = await evs(s); let wrongSeen = [], wrongUnseen = [];
    for (const e of all) { const v = isSeen(s, e.seq); if (marked.has(e.seq) && !v) wrongUnseen.push(e.seq); if (!marked.has(e.seq) && v) wrongSeen.push(e.seq); }
    if (wrongUnseen.length) issues.push(`${when}: ${wrongUnseen.length} events the owner saw are unseen again (ev-${wrongUnseen[0]}..ev-${wrongUnseen.at(-1)})`);
    if (wrongSeen.length) issues.push(`${when}: ${wrongSeen.length} events the owner never saw count as seen (ev-${wrongSeen[0]}..)`);
    const u = await unseenNow(s), want = all.filter((e) => !marked.has(e.seq)).length; if (u !== want) issues.push(`${when}: unseen count ${u}, expected ${want}`);
    return all;
  };
  await check('after 1300 sparse receipts');
  await restartPanel(); await check('after restart');
  await assignN(s, 2500, k); // 5101 > 5000: the oldest compact away
  const all = await check('after compaction');
  assert.ok(all[0].seq > 1, `precondition: compaction ran (oldest retained ev-${all[0].seq}, ${all.length} retained)`);
  const probe = await api('POST', R.seen, { session: s, ids: [] }); assert.equal(probe.body.retainedFrom, all[0].id, 'retainedFrom names the oldest retained event');
  const gone = await api('POST', R.seen, { session: s, ids: ['ev-1'] });
  assert.equal(gone.status, 400); assert.deepEqual(gone.body.bad, [{ id: 'ev-1', reason: 'compacted' }]);
  await restartPanel(); await check('after compaction + restart');
  const later = (await evs(s)).filter((e) => !marked.has(e.seq)).map((e) => e.id); // now see everything retained
  await seenBatch(s, later); later.forEach((id) => marked.add(+id.slice(3)));
  const left = await unseenNow(s); if (left) issues.push(`after seeing everything retained: ${left} still count unseen (frontier stuck below the compacted prefix?)`);
  assert.deepEqual(issues, [], issues.join('\n'));
  console.log(`# AD2 load: ${5101} events in ${Math.round((Date.now() - t0) / 1000)} s, featsup.json ${readFileSync(`${STATE}/featsup.json`).length} bytes`);
});

test('AD3 a DECISION waiting for review survives compaction (reviewed, it may go); a message\'s clientId still dedupes after its event compacts', async () => {
  const s = `${F.session}-ad3`, LEAD = F.spare[13], keys = await seedFeature(s, LEAD);
  const msg = { session: s, clientId: 'qa-ad3-msg', thread: LEAD, text: 'qa ad3 once only', anchor: { a: 'job:' + keys[0], kind: 'Job', rev: (await fe(s)).dossier.rev } };
  assert.equal((await api('POST', R.message, msg)).status, 200, 'precondition: message sent');
  const delivered = async () => (await api('GET', R.messageGet(s, msg.clientId))).body.state === 'delivered';
  for (let i = 0; i < 60 && !(await delivered()); i++) await new Promise((r) => setTimeout(r, 250));
  assert.ok(await delivered(), 'precondition: message delivered');
  const d = await agent('POST', R.aLog, { session: s, line: 'DECISION: qa keep me | considered: a; b | chose: a' });
  assert.equal(d.status, 200, JSON.stringify(d.body)); assert.match(d.body.id || '', /^ev-\d+$/, 'precondition: the DECISION is an event: ' + JSON.stringify(d.body));
  const pg = (await fe(s)).page; assert.ok(pg, 'precondition: featFacts.page');
  const entry = (pg.log || []).find((l) => l.id === d.body.id); assert.ok(entry, 'DECISION on page.log'); assert.equal(entry.review, true, 'DECISION asks for review');
  await assignN(s, 5100, keys[0]);
  const all = await evs(s); assert.ok(all[0].seq > 1 || all.some((e, i) => i && e.seq !== all[i - 1].seq + 1), 'precondition: compaction ran');
  const still = all.find((e) => e.id === d.body.id); assert.ok(still, `${d.body.id} (DECISION) was compacted before review`);
  assert.equal(all.length, 5000, 'retained = cap');
  const r = await api('POST', R.reviewed, { session: s, id: still.id, rev: still.rev ?? '' }); assert.equal(r.status, 200, JSON.stringify(r.body));
  await assignN(s, 1, keys[0]); assert.ok(!(await evs(s)).some((e) => e.id === d.body.id), 'once reviewed, the oldest event compacts normally');
  // the message's event was among the oldest: a retry of the same clientId must still be the same message, not a second one
  const again = await api('POST', R.message, msg); assert.equal(again.status, 200, JSON.stringify(again.body));
  const g = await api('GET', R.messageGet(s, msg.clientId)); assert.equal(g.status, 200, 'the message is unknown after its event compacted: ' + JSON.stringify(g.body));
  await new Promise((r) => setTimeout(r, 4000));
  assert.equal(turns().filter((t) => (t.message || '').includes('qa ad3 once only')).length, 1, 'a retry after compaction delivered the message twice');
});

test('AD4 an agent log line whose event cannot be saved: the line stands, 200 with a warning and no id, and nothing fakes an event for it', async () => {
  const s = `${F.session}-ad4`; await seedFeature(s);
  chmodSync(STATE, 0o555); let r; try { r = await agent('POST', R.aLog, { session: s, line: 'DECISION: qa unsaved event' }); } finally { chmodSync(STATE, 0o755); }
  assert.equal(r.status, 200, JSON.stringify(r.body)); assert.match(r.body.warning || '', /event wasn't saved/); assert.ok(!('id' in r.body), 'an id for an unsaved event');
  const f = await fe(s); assert.match(f.dossier?.log || '', /qa unsaved event/, 'the LOG.md line stands');
  assert.ok(!(await evs(s)).some((e) => /qa unsaved event/.test(e.text || '')), 'an event exists for it');
  const l = (f.page?.log || []).filter((x) => /qa unsaved event/.test(JSON.stringify(x)));
  assert.ok(l.every((x) => !/^ev-\d+$/.test(x.id || '') && !x.review), 'page.log presents the unsaved line as a reviewable event: ' + JSON.stringify(l));
});
