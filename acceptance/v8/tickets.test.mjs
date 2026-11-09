// Feature-page acceptance: tickets on the real page and the owner reply route (ids TK1..TK9). Fresh features bound to
// spare managers (answers need a lead). T3 (free text without an option) is covered in api.test.mjs.
import { test, before } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { R } from './names.mjs';
import { F, B, api, agent, turns, steps, seedFeature, sleep } from './lib.mjs';

const S = `${F.session}-tk`, LEAD = F.spare[4];
const fe = async (s = S) => (await api('GET', R.feature(s))).body;
const tickets = async (s = S) => { const p = (await fe(s)).page; assert.ok(p && Array.isArray(p.tickets), 'precondition: page.tickets'); return p.tickets; };
const reply = (b) => api('POST', R.ticketReply, { session: S, ...b });
const PLAN = readFileSync(new URL('./fixture/PLAN.md', import.meta.url), 'utf8').replace('  - after: 1.1\n', '')
  + `\n## M3 Second question\n- [?] Pick a currency @Worker\n  - ask: EUR or USD?\n- [ ] Run the smoke check @Worker\n  - check: true\n  - cwd: ${F.repo}\n- [ ] Run the folderless check @Worker\n  - check: true\n`;
const ownerPlan = async (text, s = S) => { const f = await fe(s); const r = await api('POST', R.doc, { session: s, doc: 'plan', text, rev: f.dossier.revs.plan }); assert.equal(r.status, 200, JSON.stringify(r.body)); };
const stepOf = async (t, s = S) => steps(await fe(s)).find((x) => x.text.startsWith(t));

before(async () => { await seedFeature(S, LEAD); await ownerPlan(PLAN); });

test('TK1 page.tickets: the ask and the check approval, each with exactly the server-named options, rev and consequences', async () => {
  const d = (await fe()).dossier, ts = await tickets();
  const date = await stepOf('Pick the date format'), smoke = await stepOf('Run the smoke check');
  const ask = ts.find((t) => t.id === `ask:${date.key}`); assert.ok(ask, 'ask ticket: ' + JSON.stringify(ts.map((t) => t.id)));
  assert.equal(ask.kind, 'ask'); assert.equal(ask.step, date.key); assert.equal(ask.rev, d.rev); assert.match(ask.title, /ISO 8601/);
  assert.deepEqual(ask.options.map((o) => [o.id, !!o.needsText]), [['answer', true], ['answer-wait', true]]);
  for (const o of ask.options) assert.ok(o.label && o.consequence, 'each option says what it does');
  assert.ok(ask.ifNone, 'what happens if the owner does nothing');
  const ap = ts.find((t) => t.id === `approve:${smoke.key}`); assert.ok(ap, 'approval ticket for the step with a check');
  assert.deepEqual(ap.options.map((o) => o.id), ['approve']);
  assert.ok(!ts.some((t) => ['draft', 'outcomes'].includes(t.kind)), 'no draft/outcomes ticket without a draft or with steps open');
  const ag = await agent('GET', R.aFeature(S)); assert.equal(ag.status, 200);
  assert.deepEqual(JSON.stringify(ag.body).includes(`ask:${date.key}`), true, 'the bearer record shows tickets (read only)');
});

test('TK2 refusals: not offered / missing option / missing text → 400; unknown or closed ticket → 409; moved rev → 409 {rev}; nothing changes', async () => {
  const date = await stepOf('Pick the date format'), d = (await fe()).dossier, id = `ask:${date.key}`, plan0 = d.plan;
  const cases = [
    [{ ticket: id, option: 'approve', text: 'x', rev: d.rev }, 400, 'an option this ticket does not offer'],
    [{ ticket: id, text: 'ISO', rev: d.rev }, 400, 'no option (free text alone is pending messages)'],
    [{ ticket: id, option: 'answer', text: '  ', rev: d.rev }, 400, 'answer without text'],
    [{ ticket: 'ask:000000000000', option: 'answer', text: 'x', rev: d.rev }, 409, 'no such ticket'],
    [{ ticket: id, option: 'answer', text: 'x', rev: '0000000000000000' }, 409, 'stale rev'],
    [{ option: 'answer', text: 'x', rev: d.rev }, 400, 'no ticket'],
  ];
  for (const [b, code, why] of cases) { const r = await reply(b); assert.equal(r.status, code, `${why}: ${JSON.stringify(r.body)}`); if (why === 'stale rev') assert.equal(r.body.rev, d.rev, '409 names the current rev'); }
  assert.equal((await fe()).dossier.plan, plan0, 'a refused reply changed the record');
});

test('TK3 the agent bearer cannot act on a ticket (owner route only)', async () => {
  const date = await stepOf('Pick the date format'), d = (await fe()).dossier;
  const body = JSON.stringify({ session: S, ticket: `ask:${date.key}`, option: 'answer', text: 'agent says ISO', rev: d.rev });
  const tok = readFileSync(`${process.env.V8QA_E}/home/.config/control-panel/agent-token`, 'utf8').trim();
  const own = await fetch(B + R.ticketReply, { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + tok }, body });
  assert.ok([401, 403].includes(own.status), `bearer on the owner route: ${own.status}`);
  const ag = await agent('POST', '/agent/feature/ticket/reply', JSON.parse(body)); assert.ok([404, 405].includes(ag.status), `agent route exists: ${ag.status}`);
  assert.equal((await stepOf('Pick the date format')).state, 'asking', 'the agent answered the owner\'s question');
});

test('TK4 answer: the answer goes under the step, the step resumes, the ticket closes; a second reply is 409', async () => {
  const date = await stepOf('Pick the date format'), d = (await fe()).dossier, others = steps(await fe()).filter((x) => x.key !== date.key).map((x) => [x.key, x.state]);
  const r = await reply({ ticket: `ask:${date.key}`, option: 'answer', text: 'ISO 8601 (qa answer)', rev: d.rev });
  assert.equal(r.status, 200, JSON.stringify(r.body));
  const f = await fe(), now = steps(f).find((x) => x.key === date.key);
  assert.equal(now.state, 'doing', 'answer resumes the step'); assert.match(f.dossier.plan, /ISO 8601 \(qa answer\)/);
  assert.deepEqual(steps(f).filter((x) => x.key !== date.key).map((x) => [x.key, x.state]), others, 'other steps changed');
  assert.ok(!(await tickets()).some((t) => t.id === `ask:${date.key}`), 'ticket still open');
  const again = await reply({ ticket: `ask:${date.key}`, option: 'answer', text: 'again', rev: f.dossier.rev }); assert.equal(again.status, 409, 'closed ticket');
});

test('TK5 answer-wait: the answer is recorded and the step goes back to todo (not resumed)', async () => {
  const cur = await stepOf('Pick a currency'), d = (await fe()).dossier;
  const r = await reply({ ticket: `ask:${cur.key}`, option: 'answer-wait', text: 'EUR (qa wait)', rev: d.rev });
  assert.equal(r.status, 200, JSON.stringify(r.body));
  const f = await fe(); assert.equal(steps(f).find((x) => x.key === cur.key).state, 'todo'); assert.match(f.dossier.plan, /EUR \(qa wait\)/);
});

test('TK6 a reply has exactly the authority of the route it names: approve via ticket = approve via its route, nothing more', async () => {
  const smoke = await stepOf('Run the smoke check'), d = (await fe()).dossier;
  const r = await reply({ ticket: `approve:${smoke.key}`, option: 'approve', rev: d.rev }); assert.equal(r.status, 200, JSON.stringify(r.body));
  const f = await fe(), st = steps(f).find((x) => x.key === smoke.key);
  assert.equal(st.state, smoke.state, 'approving a check changed the step state'); assert.ok(!f.dossier.accepted, 'approving accepted the outcomes');
  assert.ok(!(await tickets()).some((t) => t.id === `approve:${smoke.key}`), 'approval ticket still open');
});

test('TK7 draft gate: an agent draft makes a draft ticket (adopt/discard); discard via the ticket keeps FEATURE.md; a stale draft rev is 409', async () => {
  const f0 = await fe(); const feat0 = f0.dossier.feature;
  const w = await agent('POST', R.aDoc, { session: S, doc: 'draft', text: '# Quokka v2 (qa draft)\n', rev: f0.dossier.revs?.draft ?? 'e3b0c44298fc1c14', thread: LEAD });
  assert.equal(w.status, 200, 'precondition: agent writes a draft: ' + JSON.stringify(w.body));
  const t = (await tickets()).find((x) => x.kind === 'draft'); assert.ok(t, 'draft ticket'); assert.deepEqual(t.options.map((o) => o.id), ['promote', 'discard']);
  const stale = await reply({ ticket: t.id, option: 'discard', rev: '0000000000000000' }); assert.equal(stale.status, 409);
  const r = await reply({ ticket: t.id, option: 'discard', rev: t.rev }); assert.equal(r.status, 200, JSON.stringify(r.body));
  const f = await fe(); assert.equal(f.dossier.feature, feat0, 'FEATURE.md changed'); assert.ok(!f.dossier.draft, 'draft still there');
  assert.ok(!(await tickets()).some((x) => x.kind === 'draft'), 'draft ticket still open');
});

test('TK8 every offered option can succeed: no approval ticket for a check its route will always refuse (no absolute folder)', async () => {
  const st = await stepOf('Run the folderless check'), d = (await fe()).dossier;
  const t = (await tickets()).find((x) => x.id === `approve:${st.key}`);
  if (!t || !t.options.some((o) => o.id === 'approve')) return; // not offered: fine
  const r = await reply({ ticket: t.id, option: 'approve', rev: d.rev });
  assert.equal(r.status, 200, `the server offered "approve" for a check with no folder, and its route refuses it: ${JSON.stringify(r.body)}`);
});


test('TK9 an owner message to a manager that waits on the owner (needs_you) leaves every open ticket, held lane and step as it was (main a3608e7: the thread may go back to working)', async () => {
  assert.equal((await agent('POST', R.aThreadUpdate, { id: LEAD, status: 'needs_you', note: 'qa: waits on the owner' })).status, 200);
  const f0 = await fe(), t0 = (await tickets()).map((t) => t.id).sort(), s0 = steps(f0).map((x) => [x.key, x.state]), date = await stepOf('Pick the date format');
  assert.ok(t0.includes(`ask:${date.key}`), 'precondition: the ask is open');
  const r = await api('POST', R.message, { session: S, clientId: 'qa-tk9-' + Date.now(), thread: LEAD, text: 'qa tk9: still thinking about the currency', anchor: { a: date.key, kind: 'step', rev: f0.dossier.rev } });
  assert.ok(r.status === 200, 'message: ' + r.status + ' ' + JSON.stringify(r.body));
  await sleep(3000);
  const f1 = await fe();
  assert.deepEqual((await tickets()).map((t) => t.id).sort(), t0, 'an owner chat message closed or changed open tickets');
  assert.deepEqual(steps(f1).map((x) => [x.key, x.state]), s0, 'an owner chat message changed a step');
  assert.equal(f1.dossier.plan, f0.dossier.plan, 'an owner chat message changed PLAN.md');
  console.log('# TK9 thread status after the owner message: ' + JSON.stringify(f1.thread?.status ?? f1.page?.manager));
});
