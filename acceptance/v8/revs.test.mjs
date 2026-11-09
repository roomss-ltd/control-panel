// Feature-page acceptance: event revisions are the panel's, not the writer's. Route tests only; ids RV1..RV5.
import { test, before } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { R } from './names.mjs';
import { F, B, STATE, api, agent, seedFeature, hash } from './lib.mjs';

const S = `${F.session}-rv`;
const fe = async () => (await api('GET', R.feature(S))).body;
const sup = () => JSON.parse(readFileSync(`${STATE}/featsup.json`, 'utf8'));
before(async () => { await seedFeature(S); });

test('RV1 a log line\'s rev is the plan the panel read; the writer\'s "rev:" is only claimedRev', async () => {
  const d = (await fe()).dossier;
  const r = await agent('POST', R.aLog, { session: S, line: 'DECISION: qa rev claim | chose: a | rev: deadbeefdeadbeef' }); assert.equal(r.status, 200, JSON.stringify(r.body));
  const f = await fe(), l = f.page.log.find((x) => x.id === r.body.id), e = f.page.events.find((x) => x.id === r.body.id);
  assert.ok(l && e, 'the line is a log entry and an event');
  assert.equal(l.rev, d.rev, 'page.log rev = the actual plan rev'); assert.equal(l.claimedRev, 'deadbeefdeadbeef'); assert.equal(e.rev, d.rev, 'event rev = the actual plan rev');
  assert.ok(!l.stale, 'not stale while the plan is unchanged');
});

test('RV2 an injected claimed rev cannot be reviewed: 409 {changed, rev: actual}, nothing recorded; the actual rev can', async () => {
  const f = await fe(), l = f.page.log.find((x) => x.claimedRev === 'deadbeefdeadbeef'), h = hash(`${STATE}/featsup.json`);
  const bad = await api('POST', R.reviewed, { session: S, id: l.id, rev: l.claimedRev });
  assert.equal(bad.status, 409, JSON.stringify(bad.body)); assert.equal(bad.body.changed, l.id); assert.equal(bad.body.rev, l.rev);
  assert.equal(hash(`${STATE}/featsup.json`), h, 'a refused review wrote state'); assert.ok(!(l.id in (sup().reviewed?.[S] || {})));
  const ok = await api('POST', R.reviewed, { session: S, id: l.id, rev: l.rev }); assert.equal(ok.status, 200, JSON.stringify(ok.body));
  assert.equal(sup().reviewed[S][l.id], l.rev, 'reviewed at the actual rev');
});

test('RV3 after a plan change the entry is stale (its rev is kept, not moved)', async () => {
  const f0 = await fe(), l0 = f0.page.log.find((x) => x.claimedRev === 'deadbeefdeadbeef');
  const w = await api('POST', R.doc, { session: S, doc: 'plan', text: f0.dossier.plan.replace('@Reviewer', '@Reviewer\n  - kind: review'), rev: f0.dossier.revs.plan });
  assert.equal(w.status, 200, JSON.stringify(w.body));
  const f = await fe(), l = f.page.log.find((x) => x.id === l0.id); assert.notEqual(f.dossier.rev, f0.dossier.rev, 'precondition: plan rev moved');
  assert.equal(l.stale, true); assert.equal(l.rev, l0.rev, 'its rev must stay the one it was written at');
});

test('RV4 the agent reads reviewed (read only) and cannot review: bearer has no reviewed/seen route', async () => {
  const a = await agent('GET', R.aFeature(S)); assert.equal(a.status, 200); assert.equal(typeof a.body.reviewed, 'object', 'reviewed map on the agent record');
  const l = (await fe()).page.log[0];
  for (const p of ['/agent/feature/reviewed', '/agent/feature/seen']) assert.ok([404, 405].includes((await agent('POST', p, { session: S, id: l.id, ids: [l.id], rev: l.rev })).status), p);
  const tok = readFileSync(`${process.env.V8QA_E}/home/.config/control-panel/agent-token`, 'utf8').trim();
  const own = await fetch(B + R.reviewed, { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + tok }, body: JSON.stringify({ session: S, id: l.id, rev: l.rev }) });
  assert.ok([401, 403].includes(own.status), `bearer on the owner reviewed route: ${own.status}`);
});

test('RV5 an assign event carries the actual plan rev, whatever the caller sends', async () => {
  const f = await fe(), k = f.dossier.milestones[0].steps[0].key;
  const a = await agent('POST', R.aAssign, { session: S, step: k, tab: 'Worker', rev: 'deadbeefdeadbeef' }); assert.equal(a.status, 200, JSON.stringify(a.body));
  const e = (await fe()).page.events.find((x) => x.id === a.body.id); assert.equal(e.rev, f.dossier.rev);
});
