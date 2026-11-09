// Feature-page acceptance: objectives and proof on the actual page and done gates (ids OB1..OB8).
// OB6 (owner proof kind at done), OB7 (coverage-edit event) and OB8 (assessment author binding) probe the edges of the contract.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { R } from './names.mjs';
import { F, api, agent, steps, seedFeature } from './lib.mjs';

const fe = async (s) => (await api('GET', R.feature(s))).body;
const writeDoc = async (s, doc, text) => { const f = await fe(s); const r = await api('POST', R.doc, { session: s, doc, text, rev: f.dossier.revs?.[doc] ?? 'e3b0c44298fc1c14' }); assert.equal(r.status, 200, `${doc}: ${JSON.stringify(r.body)}`); };
const obj = async (s) => { const p = (await fe(s)).page; assert.ok(p?.objectives?.items, 'precondition: page.objectives'); return p.objectives; };
const item = async (s, id) => (await obj(s)).items.find((x) => x.id === id);
// owner attestation verifies a claim: the worker ticks the step first (an unticked step's attestation doesn't make it done)
const tick = async (s, keys) => { const f = await fe(s); let t = f.dossier.plan; for (const st of steps(f).filter((x) => keys.includes(x.key))) t = t.replace(`- [ ] ${st.text}`, `- [x] ${st.text}`); const th = f.thread?.id; const w = await agent('POST', R.aDoc, { session: s, doc: 'plan', rev: f.dossier.revs.plan, text: t, ...(th ? { thread: th } : {}) }); assert.equal(w.status, 200, 'tick: ' + JSON.stringify(w.body)); };
const attestAll = async (s) => { await tick(s, steps(await fe(s)).map((x) => x.key)); for (const st of steps(await fe(s)).filter((x) => x.state !== 'done')) { const v = await api('POST', '/api/term/feature/verify', { session: s, key: st.key, rev: (await fe(s)).dossier.rev }); assert.equal(v.status, 200, `attest ${st.text}: ${JSON.stringify(v.body)}`); } };
const FEAT = (extra = '') => `# Quokka\n\n## Objective\nExport the ledger.\n\n## Desired outcomes\n- [ ] D1: ledger exported — done when: ledger.txt lists every row\n- [ ] D2: dates are ISO — done when: every date parses as ISO 8601\n${extra}`;
const PLAN = (c1 = 'D1', c2 = 'D2') => `## M1 Do it\n- [ ] Write ledger.txt @Worker\n  - covers: ${c1}\n- [ ] Format the dates @Worker\n  - covers: ${c2}\n`;
const fresh = async (tag, feat, plan, thread) => { const s = `${F.session}-${tag}`; await seedFeature(s, thread); await writeDoc(s, 'feature', feat); await writeDoc(s, 'plan', plan); return s; };

test('OB1 objectives are FEATURE.md D-ids with done-when; covers come only from explicit covers: lines; unknown covers are listed', async () => {
  const s = await fresh('ob1', FEAT('- [ ] an outcome with no id\n'), PLAN('D1', 'D9'));
  const o = await obj(s), k = steps(await fe(s)).map((x) => x.key);
  assert.deepEqual(o.items.map((x) => x.id), ['D1', 'D2', 'D?']);
  assert.match((await item(s, 'D1')).doneWhen, /every row/);
  assert.deepEqual((await item(s, 'D1')).steps, [k[0]]); assert.deepEqual((await item(s, 'D2')).steps, [], 'D2 covered without a covers: line');
  assert.deepEqual(o.unknown, ['D9']);
});

test('OB2 activity and ticks are not progress: a ticked step with no evidence, or with only a model assessment, leaves its objective unmet', async () => {
  const s = await fresh('ob2', FEAT(), PLAN());
  const f = await fe(s);
  const w = await agent('POST', R.aDoc, { session: s, doc: 'plan', rev: f.dossier.revs.plan, text: f.dossier.plan.replace('- [ ] Write ledger.txt @Worker', '- [x] Write ledger.txt @Worker\n  - assessment: pass — looks complete').replace('- [ ] Format the dates @Worker', '- [x] Format the dates @Worker') });
  assert.equal(w.status, 200, JSON.stringify(w.body));
  for (const id of ['D1', 'D2']) { const it = await item(s, id); assert.equal(it.met, false, `${id} met by a tick/assessment`); assert.ok(it.missing?.length, `${id} says why not`); }
  const st = steps(await fe(s)); assert.ok(st.every((x) => x.state !== 'done'), 'a claim became done: ' + st.map((x) => x.state));
  const ev = (await fe(s)).page.evidence; const e1 = ev?.[st[0].key]; assert.ok(e1, 'evidence entry for the ticked step');
  assert.ok(!e1.check, 'an assessment is shown as a check'); assert.ok(e1.assessment, 'the assessment is kept as an assessment'); assert.equal(e1.assessment.author ?? null, null, 'author unknown is null');
});

test('OB3 met only when every covering step is verified (owner attestation counts); one unverified covering step keeps it unmet', async () => {
  const s = await fresh('ob3', FEAT(), PLAN('D1', 'D1, D2'));
  const [a, b] = steps(await fe(s)).map((x) => x.key); await tick(s, [a, b]);
  const v = await api('POST', '/api/term/feature/verify', { session: s, key: a, rev: (await fe(s)).dossier.rev }); assert.equal(v.status, 200, JSON.stringify(v.body));
  assert.equal((await item(s, 'D1')).met, false, 'D1 met with one of two covering steps verified');
  const v2 = await api('POST', '/api/term/feature/verify', { session: s, key: b, rev: (await fe(s)).dossier.rev }); assert.equal(v2.status, 200);
  assert.equal((await item(s, 'D1')).met, true); assert.equal((await item(s, 'D2')).met, true);
});

test('OB4 an objective nothing covers blocks accept and done, even with every step verified', async () => {
  const s = await fresh('ob4', FEAT(), PLAN('D1', 'D1'), F.spare[14]);
  await attestAll(s); assert.equal((await item(s, 'D2')).met, false);
  const acc = await api('POST', '/api/term/feature/accept', { session: s, rev: (await fe(s)).dossier.revs.feature });
  const done = await agent('POST', R.aThreadUpdate, { id: F.spare[14], status: 'done' });
  assert.ok(acc.status >= 400 || done.status >= 400, `D2 has no covering step, yet accept=${acc.status} and done=${done.status}: ${JSON.stringify(done.body)}`);
  assert.ok(done.status >= 400 || /not done/.test(JSON.stringify(done.body)), 'done accepted with an uncovered objective: ' + JSON.stringify(done.body));
});

test('OB5 a newly authored malformed objective id (D?) is not a bypass: it blocks done like any uncovered objective', async () => {
  const s = await fresh('ob5', FEAT('- [ ] D3a: exports are signed — done when: a signature verifies\n- [ ] d4: lower-case id\n'), PLAN('D1', 'D2'), F.spare[15]);
  const o = await obj(s); assert.deepEqual(o.items.filter((x) => x.id === 'D?').length, 2, 'malformed ids are flagged D?: ' + JSON.stringify(o.items.map((x) => x.id)));
  for (const x of o.items.filter((y) => y.id === 'D?')) assert.equal(x.met, false, `"${x.text}" counts as met`);
  await attestAll(s);
  const acc = await api('POST', '/api/term/feature/accept', { session: s, rev: (await fe(s)).dossier.revs.feature });
  const done = await agent('POST', R.aThreadUpdate, { id: F.spare[15], status: 'done' });
  assert.ok(acc.status >= 400 && (done.status >= 400 || /not done/.test(JSON.stringify(done.body))), `malformed objectives bypassed the gate: accept=${acc.status} ${JSON.stringify(acc.body)}, done=${done.status} ${JSON.stringify(done.body)}`);
});

test('OB6 the owner\'s required proof binds: an agent\'s claim with its own evidence does not meet D1; only the owner\'s attestation does', async () => {
  const s = await fresh('ob6', FEAT().replace('every row\n', 'every row\n  - proof: image: a screenshot of the exported ledger\n'), PLAN('D1', 'D2'));
  assert.match((await item(s, 'D1')).proof || '', /screenshot/, 'owner proof kept');
  const k = steps(await fe(s))[0].key; assert.equal((await fe(s)).page.evidence?.[k]?.required?.by, 'owner', 'required.by = owner (FEATURE.md binds)');
  const f = await fe(s); const w = await agent('POST', R.aDoc, { session: s, doc: 'plan', rev: f.dossier.revs.plan, text: f.dossier.plan.replace('- [ ] Write ledger.txt @Worker', `- [x] Write ledger.txt @Worker\n  - evidence: ${F.repo}/a\n  - result: screenshot attached`) });
  assert.equal(w.status, 200, JSON.stringify(w.body));
  const d1 = await item(s, 'D1'); assert.equal(d1.met, false, 'D1 met by the agent\'s own claim of the owner\'s proof'); assert.ok(d1.missing?.length);
  const v = await api('POST', '/api/term/feature/verify', { session: s, key: k, rev: (await fe(s)).dossier.rev }); assert.equal(v.status, 200, JSON.stringify(v.body));
  assert.equal((await item(s, 'D1')).met, true, 'owner attestation does not meet it');
});

test('OB7 (declared gap) a worker covers: edit that rewires coverage is a visible coverage event and voids acceptance', async () => {
  const s = await fresh('ob7', FEAT(), PLAN('D1', 'D2'));
  await attestAll(s);
  assert.equal((await api('POST', '/api/term/feature/accept', { session: s, rev: (await fe(s)).dossier.revs.feature })).status, 200, 'precondition: accepted');
  const f = await fe(s); const w = await agent('POST', R.aDoc, { session: s, doc: 'plan', rev: f.dossier.revs.plan, text: f.dossier.plan.replace('covers: D2', 'covers: D1') }); assert.equal(w.status, 200);
  const g = await fe(s);
  assert.equal((await item(s, 'D2')).met, false, 'D2 still met after its only covering step was rewired away');
  assert.notEqual(g.dossier.accepted, true, 'acceptance survived the rewire');
  assert.ok((g.page.events || []).some((e) => e.kind === 'coverage'), 'no coverage event for the rewire');
});

test('OB8a an assessment line\'s own "by <sid>" is kept as claimedSid and decides nothing (author null)', async () => {
  const s = await fresh('ob8a', FEAT(), PLAN()); const f = await fe(s);
  const w = await agent('POST', R.aDoc, { session: s, doc: 'plan', rev: f.dossier.revs.plan, text: f.dossier.plan.replace('- [ ] Write ledger.txt @Worker', '- [x] Write ledger.txt @Worker\n  - assessment: pass — independent review — by sid-claimed-qa') });
  assert.equal(w.status, 200);
  const a = (await fe(s)).page.evidence?.[steps(await fe(s))[0].key]?.assessment; assert.ok(a, 'assessment shown: ' + JSON.stringify((await fe(s)).page.evidence));
  assert.equal(a.author ?? null, null, 'a claimed sid made the author known'); assert.equal(a.claimedSid, 'sid-claimed-qa');
});

test('OB8b (contract QA GAP 2) the writer\'s X-Agent-Sid header is recorded as its claim on the assessment, never as authority', async () => {
  const s = await fresh('ob8b', FEAT(), PLAN()); const f = await fe(s);
  const w = await agent('POST', R.aDoc, { session: s, doc: 'plan', rev: f.dossier.revs.plan, text: f.dossier.plan.replace('- [ ] Write ledger.txt @Worker', '- [x] Write ledger.txt @Worker\n  - assessment: pass — independent review') }, { 'X-Agent-Sid': 'unobserved-sid' });
  assert.equal(w.status, 200);
  const a = (await fe(s)).page.evidence?.[steps(await fe(s))[0].key]?.assessment; assert.ok(a, 'assessment shown');
  assert.equal(a.author ?? null, null, 'an unobserved sid made the author known'); assert.equal(a.claimedSid, 'unobserved-sid', 'the header claim is not recorded (only an in-line "by <sid>" is)');
});
