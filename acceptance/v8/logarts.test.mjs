// Feature-page acceptance: supplied-record tests for the agent log and artifact grammar:
// log segments ran/next (reversible %-escaping of % | ; , =), objectives, pr, proof, session (X-Agent-Sid, claimed);
// a line over 2000 chars refused before any write; '- artifact: <path> | ui|issue | <title>' on a PLAN step.
// Only what a writer supplied is validated (absent = supported, no data). Black box: agent writer routes + owner page.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { createServer as createHttpsServer } from 'node:https';
import { writeFileSync, readFileSync, existsSync, mkdirSync, symlinkSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { R } from './names.mjs';
import { S, E, F, api, agent, sleep, seedFeature } from './lib.mjs';

const get = async (s) => (await api('GET', R.feature(s))).body;
const recDir = (th) => execFileSync('find', [`${E}/home/jarvis-files`, '-path', `*${th}*`, '-name', 'PLAN.md']).toString().trim().split('\n')[0].replace(/\/PLAN\.md$/, '');
const hashOf = (p) => (existsSync(p) ? createHash('sha256').update(readFileSync(p)).digest('hex') : 'absent');
const enc = (s) => s.replace(/%/g, '%25').replace(/\|/g, '%7C').replace(/;/g, '%3B').replace(/,/g, '%2C').replace(/=/g, '%3D');
const writePlan = async (s, th, text, rev) => { const f = await get(s); return agent('POST', R.aDoc, { session: s, doc: 'plan', text, rev: rev ?? f.dossier.revs?.plan ?? f.dossier.rev, thread: th }); };
const log = (s, th, line, h = {}) => agent('POST', R.aLog, { session: s, line, thread: th }, h);

let LS, LT, K; // one log feature, bound, with FEATURE.md (D1, D2)
async function logFeature() {
  if (LS) return;
  LS = `${S}-la`; LT = F.spare[17]; await seedFeature(LS, LT);
  const fx = readFileSync(new URL('./fixture/FEATURE.md', import.meta.url), 'utf8').replace('@REPO@', F.repo);
  const fd = await get(LS); assert.equal((await api('POST', R.doc, { session: LS, doc: 'feature', rev: fd.dossier.revs?.feature ?? 'e3b0c44298fc1c14', text: fx })).status, 200);
  K = (await get(LS)).dossier.milestones.flatMap((m) => m.steps).map((s) => s.key);
}
const entry = async (title) => (await get(LS)).page.log.find((x) => x.title === title);

test('LA1 (L1, L3, L4) every new field kept exactly; unknown D-id, step and PR labelled claimed, line kept; session only from X-Agent-Sid, claimed', async () => {
  await logFeature();
  const r = await log(LS, LT, `ACTION: la1 all fields | ran: ok=1,cmd=${enc('./check ledger.txt')},result=${enc('ledger.txt: ok')}; ok=0,cmd=${enc('./lint')},result=${enc('2 errors')}; ok=?,cmd=${enc('make')},result=${enc('')} | next: ${enc('re-run, then stop')} | objectives: D1,D77 | pr: 418 | proof: ${K[2]}`, { 'X-Agent-Sid': 'sid-qa-la1' });
  assert.equal(r.status, 200, JSON.stringify(r.body));
  const e = await entry('la1 all fields'); assert.ok(e, 'entry missing');
  assert.deepEqual(e.ran?.map(({ cmd, result, ok }) => ({ cmd, result, ok })), [{ cmd: './check ledger.txt', result: 'ledger.txt: ok', ok: true }, { cmd: './lint', result: '2 errors', ok: false }, { cmd: 'make', result: '', ok: null }], 'ran: ' + JSON.stringify(e.ran));
  assert.equal(e.next, 're-run, then stop');
  assert.deepEqual(e.objectives, ['D1']); assert.deepEqual(e.objectivesClaimed, ['D77'], 'unknown D-id: ' + JSON.stringify(e));
  assert.equal(e.pr, 418); assert.equal(e.prKnown, false, 'a PR the feature does not have is not labelled unknown');
  assert.equal(e.proofStep, K[2]); assert.equal(e.session, 'sid-qa-la1'); assert.equal(e.sessionClaimed, true);
  assert.equal((await log(LS, LT, 'ACTION: la1 claims | objectives: D99 | proof: 9.9 | pr: 12x | by: @Forger')).status, 200);
  const c = (await get(LS)).page.log.find((x) => /^la1 claims/.test(x.title || '')); assert.ok(c, 'a line with only unknown links was dropped: ' + JSON.stringify((await get(LS)).page.log.map((x) => x.title)));
  assert.ok(!c.objectives?.length && c.objectivesClaimed?.includes('D99'), 'unknown objective linked: ' + JSON.stringify(c));
  assert.ok(!c.proofStep && c.proofStepClaimed === '9.9', 'unknown step linked: ' + JSON.stringify(c));
  assert.ok(c.pr == null, 'a non-integer pr linked: ' + JSON.stringify(c));
  assert.ok(!('session' in c) && !c.sessionClaimed, 'session without the header, or from by: ' + JSON.stringify(c));
});

test('LA2 (L2) injection: a cmd with | ; , = and %7C round-trips byte-exact and makes no segment; %257C decodes once; a ran: inside the title stays title', async () => {
  await logFeature();
  const cmd = 'grep -c x a | wc -l; echo a,b=c %7C | step: 9 | done', res = 'ok | proof: 1.1';
  assert.equal((await log(LS, LT, `ACTION: la2 inject | ran: ok=1,cmd=${enc(cmd)},result=${enc(res)} | next: ${enc('%7C')}`)).status, 200);
  const e = await entry('la2 inject'); assert.ok(e, 'entry missing');
  assert.equal(e.ran?.[0]?.cmd, cmd, 'cmd not byte-exact'); assert.equal(e.ran[0].result, res);
  assert.equal(e.next, '%7C', '%257C must decode once to %7C');
  assert.ok(!e.step && !e.proofStep && !e.proofStepClaimed, 'escaped text became a segment: ' + JSON.stringify(e));
  assert.equal((await log(LS, LT, 'ACTION: la2 title ran: ok=1,cmd=rm,result=ok')).status, 200);
  const t = (await get(LS)).page.log.find((x) => /^la2 title/.test(x.title || ''));
  assert.ok(t && /ran: ok=1/.test(t.title) && !t.ran, 'a ran: inside the title became a segment: ' + JSON.stringify(t));
});

test('LA3 a line over 2000 characters is refused (400) before any write: LOG.md and events unchanged, nothing cut', async () => {
  await logFeature();
  const p = `${recDir(LT)}/LOG.md`, h0 = hashOf(p), n0 = (await get(LS)).page.log.length;
  const long = 'ACTION: la3 long | next: ' + 'x'.repeat(2001);
  const r = await log(LS, LT, long); assert.equal(r.status, 400, 'over 2000 not refused: ' + r.status + ' ' + JSON.stringify(r.body));
  await sleep(500); assert.equal(hashOf(p), h0, 'LOG.md changed by a refused line');
  const after = (await get(LS)).page.log; assert.equal(after.length, n0, 'an entry was added'); assert.ok(!after.some((x) => /la3 long/.test(x.title || '')), 'a cut entry exists');
  const at = 'ACTION: la3 edge | next: ' + 'y'.repeat(2000 - 'ACTION: la3 edge | next: '.length);
  assert.equal(at.length, 2000); assert.equal((await log(LS, LT, at)).status, 200, 'exactly 2000 refused');
  assert.equal((await entry('la3 edge'))?.next?.length, 2000 - 'ACTION: la3 edge | next: '.length, 'a 2000-char line was cut');
});

test('LA4 (L5, L6) a ran ok=1 claim matching the proof literal meets nothing; an old-grammar line parses as before (no new fields)', async () => {
  await logFeature();
  const before = await get(LS), st0 = before.dossier.milestones[0].steps[0];
  assert.equal((await log(LS, LT, `EVIDENCE: la4 claim | step: ${st0.key} | ran: ok=1,cmd=${enc('./check')},result=${enc('ledger.txt: ok')}`)).status, 200);
  const f = await get(LS), st = f.dossier.milestones[0].steps[0], ev = f.page.evidence?.[st.key];
  assert.equal(st.state, st0.state, 'a ran claim changed the step state'); assert.notEqual(st.state, 'done');
  assert.ok(!ev?.check, 'a ran claim became a check: ' + JSON.stringify(ev));
  assert.notEqual(ev?.criterion?.met, true, 'a ran claim met the proof: ' + JSON.stringify(ev?.criterion));
  assert.equal((await log(LS, LT, 'DECISION: la4 old | considered: ISO; DMY | chose: ISO')).status, 200);
  const o = await entry('la4 old'); assert.ok(o, 'old line missing');
  assert.ok(o.considered?.length === 2 && o.chose, 'old grammar fields: ' + JSON.stringify(o));
  for (const k of ['ran', 'next', 'objectives', 'objectivesClaimed', 'pr', 'proofStep', 'proofStepClaimed', 'session']) assert.ok(!(k in o) || o[k] == null, `old line carries ${k}: ` + JSON.stringify(o));
});

test('LA5 (A1–A4) artifacts: exact line gives one row per line on its job or milestone; missing/secret/outside/symlink states with no path or hash; only https links, never fetched; arts meet nothing; absent without a line; bounds and stale rev refuse atomically', async () => {
  const SA = `${S}-la5`, th = F.spare[1]; await seedFeature(SA, th);
  assert.ok(!('arts' in (await get(SA)).page), 'arts without any artifact line');
  const dir = recDir(th); mkdirSync(`${dir}/shots`, { recursive: true });
  writeFileSync(`${dir}/shots/ok.png`, 'png'); writeFileSync(`${dir}/shots/two.png`, 'png2'); writeFileSync(`${dir}/.env`, 'SECRET=1'); symlinkSync('/etc/hostname', `${dir}/shots/out.png`);
  const tls = `${E}/tmp/qa-tls`; mkdirSync(tls, { recursive: true });
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=127.0.0.1', '-keyout', `${tls}/k.pem`, '-out', `${tls}/c.pem`], { stdio: 'ignore' });
  let hits = 0; const seen = []; const h = (q, r) => { hits++; seen.push({ srv: q.socket.localPort, method: q.method, url: q.url, ua: q.headers['user-agent'], from: q.socket.remotePort, at: new Date().toISOString() }); r.end('x'); };
  const srv = createHttpsServer({ key: readFileSync(`${tls}/k.pem`), cert: readFileSync(`${tls}/c.pem`) }, h), plain = createServer(h);
  await new Promise((r) => srv.listen(0, '127.0.0.1', r)); await new Promise((r) => plain.listen(0, '127.0.0.1', r));
  const surl = `https://127.0.0.1:${srv.address().port}/shot.png`, purl = `http://127.0.0.1:${plain.address().port}/shot.png`;
  try {
    const f0 = await get(SA), plan0 = readFileSync(`${dir}/PLAN.md`, 'utf8');
    const lines = [[`${dir}/shots/ok.png`, 'ui', 'QA ok'], [`${dir}/shots/two.png`, 'issue', 'QA two'], [`${dir}/shots/missing.png`, 'ui', 'QA missing'], [`${dir}/.env`, 'ui', 'QA secret'], ['~/.config/vm-agent/api-key', 'ui', 'QA home secret'], ['/etc/passwd', 'ui', 'QA outside'], [`${dir}/shots/out.png`, 'ui', 'QA symlink'], [surl, 'ui', 'QA https'], [purl, 'ui', 'QA http'], [`https://qa:pw@example.invalid/x`, 'ui', 'QA creds']];
    const art = (rows) => rows.map(([p, u, t]) => `  - artifact: ${p} | ${u} | ${t}\n`).join('');
    const plan = f0.dossier.plan.replace('- [~] Write ledger.txt @Worker\n', '- [~] Write ledger.txt @Worker\n' + art(lines)).replace('## M2 Check it\n', '## M2 Check it\n' + art([[`${dir}/shots/two.png`, 'ui', 'QA milestone']]));
    assert.equal((await writePlan(SA, th, plan, '0000000000000000')).status, 409, 'stale rev accepted');
    const many = f0.dossier.plan.replace('- [~] Write ledger.txt @Worker\n', '- [~] Write ledger.txt @Worker\n' + art(Array.from({ length: 21 }, (_, i) => [`${dir}/shots/ok.png`, 'ui', 'QA many ' + i])));
    assert.equal((await writePlan(SA, th, many)).status, 400, '21 artifact lines on one job accepted');
    const long = f0.dossier.plan.replace('- [~] Write ledger.txt @Worker\n', '- [~] Write ledger.txt @Worker\n' + art([[`${dir}/shots/ok.png`, 'ui', 'L'.repeat(480)]]));
    assert.equal((await writePlan(SA, th, long)).status, 400, 'an artifact line over 500 characters accepted');
    assert.equal(readFileSync(`${dir}/PLAN.md`, 'utf8'), plan0, 'PLAN.md changed by a refused write');
    const w = await writePlan(SA, th, plan); assert.equal(w.status, 200, JSON.stringify(w.body));
    await sleep(2000);
    const f = await get(SA), arts = f.page.arts, k = f.dossier.milestones[0].steps[0].key;
    assert.ok(Array.isArray(arts), 'page.arts absent: ' + Object.keys(f.page).join(','));
    const by = (t) => arts.filter((a) => a.title === t);
    for (const t of ['QA ok', 'QA two']) { const [a, dup] = by(t); assert.ok(a && !dup, `${t}: exactly one row: ` + JSON.stringify(arts)); assert.equal(a.step, k, `${t} on the wrong job`); assert.equal(a.state, 'ok'); assert.ok(a.path, `${t}: ` + JSON.stringify(a)); assert.ok(!a.url || a.url.startsWith('/api/fs/raw?'), `${t}: preview is not the panel's own: ` + a.url); }
    assert.equal(by('QA ok')[0].purpose, 'ui'); assert.equal(by('QA two')[0].purpose, 'issue'); assert.notEqual(by('QA ok')[0].id, by('QA two')[0].id, 'ids not distinct');
    const [ms] = by('QA milestone'); assert.ok(ms && !ms.step && /M2/.test(ms.milestone), 'milestone-level line: ' + JSON.stringify(ms));
    const [mi] = by('QA missing'); assert.equal(mi?.state, 'missing'); assert.ok(!mi.path && !mi.sha256 && !mi.url, 'missing leaks: ' + JSON.stringify(mi));
    for (const t of ['QA secret', 'QA home secret', 'QA outside', 'QA symlink', 'QA http', 'QA creds']) { const [a] = by(t); assert.equal(a?.state, 'refused', `${t} not refused: ` + JSON.stringify(a)); assert.ok(a.why && !a.path && !a.sha256 && !a.url, `${t} leaks: ` + JSON.stringify(a)); }
    const blob = JSON.stringify(arts); for (const x of ['SECRET', '/etc/passwd', '/etc/hostname', `${dir}/.env`, 'api-key', 'qa:pw', purl]) assert.ok(!blob.includes(x), `a refused ref leaks ${x}`);
    const [u] = by('QA https'); assert.ok(u?.state === 'link' && u.url === surl, 'https as written: ' + JSON.stringify(u));
    await sleep(1500); const fetched = seen.filter((x) => x.url === '/shot.png'); // a port prober on the host machine may GET / on every new port: that is not the panel
    assert.equal(fetched.length, 0, `a link was fetched (https :${srv.address().port}, http :${plain.address().port}): ` + JSON.stringify(seen));
    assert.ok(arts.every((a) => /^QA /.test(a.title || '')), 'an unreferenced artifact was invented');
    assert.notEqual(f.dossier.milestones[0].steps[0].state, 'done', 'an artifact completed a job');
    assert.ok(!f.page.evidence?.[k]?.check && !(f.page.evidence?.[k]?.items || []).some((i) => /shots\//.test(i.ref || i.path || '')), 'an artifact became evidence');
    const t201 = 'T'.repeat(201), bad = await writePlan(SA, th, (await get(SA)).dossier.plan.replace('- [?] Pick the date format @Worker\n', `- [?] Pick the date format @Worker\n  - artifact: ${dir}/shots/ok.png | ui | ${t201}\n`));
    const fa = await get(SA), cut = (fa.page.arts || []).filter((a) => /^TTTTTTTTTT/.test(a.title || ''));
    console.log(`# LA5 plan has the 201 line: ${fa.dossier.plan.includes(t201)}; arts titles: ${JSON.stringify((fa.page.arts || []).map((a) => [a.title?.slice(0, 12), a.title?.length, a.step?.slice(0, 6), a.state]))}`);
    console.log(`# LA5 201-char title: write ${bad.status} ${JSON.stringify(bad.body).slice(0, 200)}; rows ${JSON.stringify(cut.map((a) => ({ len: a.title.length, state: a.state })))}`);
    assert.ok(bad.status >= 400 || (cut.length && cut.every((a) => a.state === 'refused')), `TITLE RULING: a 201-char title is accepted (${bad.status}) and shown cut to ${cut.map((a) => a.title.length)} chars, not refused`);
  } finally { srv.close(); plain.close(); }
});
