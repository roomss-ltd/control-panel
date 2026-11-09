// Feature-page acceptance, browser: the owner's page shows supplied log metadata and artifacts truthfully and safely.
// Real panel, real records written through the agent routes; nothing injected.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:https';
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { SEL, openFeatureJS, R } from './names.mjs';
import { S, B, E, F, api, agent, sleep, seedFeature, launchChromium } from './lib.mjs';

let br; before(async () => { br = await launchChromium(); }); after(async () => br?.close());
const get = async (s) => (await api('GET', R.feature(s))).body;
const enc = (s) => s.replace(/%/g, '%25').replace(/\|/g, '%7C').replace(/;/g, '%3B').replace(/,/g, '%2C').replace(/=/g, '%3D');

test('UL1 log metadata and artifacts on the real page: authored text escaped, ran labelled as the writer\'s report, claims shown as claims; artifacts placed on their milestone, unavailable ones inert with no path, links not fetched, nothing off-origin', async () => {
  const SU = `${S}-ul`, th = F.spare[2]; await seedFeature(SU, th);
  const fx = readFileSync(new URL('./fixture/FEATURE.md', import.meta.url), 'utf8').replace('@REPO@', F.repo);
  assert.equal((await api('POST', R.doc, { session: SU, doc: 'feature', rev: (await get(SU)).dossier.revs?.feature ?? 'e3b0c44298fc1c14', text: fx })).status, 200);
  const dir = execFileSync('find', [`${E}/home/jarvis-files`, '-path', `*${th}*`, '-name', 'PLAN.md']).toString().trim().split('\n')[0].replace(/\/PLAN\.md$/, '');
  mkdirSync(`${dir}/shots`, { recursive: true }); writeFileSync(`${dir}/shots/ok.png`, Buffer.from('89504e470d0a1a0a', 'hex')); writeFileSync(`${dir}/.env`, 'SECRET=1');
  const tls = `${E}/tmp/qa-tls-ul`; mkdirSync(tls, { recursive: true });
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=127.0.0.1', '-keyout', `${tls}/k.pem`, '-out', `${tls}/c.pem`], { stdio: 'ignore' });
  const seen = []; const srv = createServer({ key: readFileSync(`${tls}/k.pem`), cert: readFileSync(`${tls}/c.pem`) }, (q, r) => { seen.push(q.url); r.end('x'); });
  await new Promise((r) => srv.listen(0, '127.0.0.1', r)); const url = `https://127.0.0.1:${srv.address().port}/shot.png`;
  const xss = '<img src=x onerror="window.__qaXss=1">';
  try {
    const f0 = await get(SU), plan = f0.dossier.plan.replace('- [~] Write ledger.txt @Worker\n', `- [~] Write ledger.txt @Worker\n  - artifact: ${dir}/shots/ok.png | ui | QA shot ${xss}\n  - artifact: ${dir}/.env | issue | QA secret\n  - artifact: ${dir}/shots/missing.png | ui | QA missing\n  - artifact: ${url} | ui | QA link\n`);
    assert.equal((await agent('POST', R.aDoc, { session: SU, doc: 'plan', text: plan, rev: f0.dossier.revs?.plan ?? f0.dossier.rev, thread: th })).status, 200);
    const line = `ACTION: QA ui log ${xss} | ran: ok=1,cmd=${enc(`echo ${xss} | wc -l`)},result=${enc('1')}; ok=0,cmd=${enc('./lint')},result=${enc('2 errors')} | next: ${enc(`then ${xss}`)} | objectives: D1,D77 | pr: 418`;
    assert.equal((await agent('POST', R.aLog, { session: SU, line, thread: th }, { 'X-Agent-Sid': 'sid-qa-ul' })).status, 200);
    const c = await br.newContext({ viewport: { width: 1440, height: 1000 } }); await c.request.post(B + '/api/login', { data: { pin: '482913' } });
    const p = await c.newPage(), errs = [], off = [];
    p.on('pageerror', (e) => errs.push(String(e))); p.on('request', (q) => { if (!q.url().startsWith(B) && !q.url().startsWith('data:')) off.push(q.url()); });
    await p.goto(B + '/'); await p.waitForFunction(() => typeof setView === 'function'); await p.evaluate(openFeatureJS(SU));
    await p.waitForSelector(SEL.section('Plan and milestones'), { timeout: 6000 }); await sleep(1500);
    await p.evaluate(() => document.querySelectorAll('details').forEach((d) => d.setAttribute('open', ''))); await sleep(500);
    await p.screenshot({ path: `${E}/shots/ul1-log-arts.png`, fullPage: true });
    assert.equal(await p.evaluate(() => window.__qaXss), undefined, 'authored text ran as HTML');
    assert.equal(await p.locator('img[src="x"]').count(), 0, 'authored <img> became an element');
    const logTxt = await p.locator(SEL.section('Work log')).innerText();
    assert.ok(logTxt.includes(`echo ${xss} | wc -l`) && logTxt.includes(`then ${xss}`), 'ran/next not shown as written: ' + logTxt.slice(0, 600));
    assert.match(logTxt, /reported by the writer, not executed or verified by the panel/, 'ran not labelled as the writer\'s report');
    assert.match(logTxt, /claims D77/); assert.match(logTxt, /claims #418/, 'an unknown PR not shown as a claim');
    assert.ok(!/sid-qa-ul/.test(logTxt) || /claim/i.test(await p.locator('[data-log]').filter({ hasText: 'QA ui log' }).innerHTML()), 'session not marked as claimed');
    const arts = p.locator('[data-fvart]'); assert.equal(await arts.count(), 4, 'artifact rows');
    const plan1 = p.locator(SEL.section('Plan and milestones'));
    assert.ok(await plan1.locator('details').first().locator('[data-fvart]').count() === 4, 'artifacts not placed in their job\'s milestone (M1)');
    for (const t of ['QA secret', 'QA missing']) { const a = arts.filter({ hasText: t }); assert.equal(await a.evaluate((e) => e.tagName), 'DIV', `${t} is clickable`); assert.equal(await a.getAttribute('aria-disabled'), 'true'); assert.ok(!(await a.innerText()).includes(dir), `${t} shows a path`); }
    const ok = arts.filter({ hasText: 'QA shot' }); assert.match(await ok.getAttribute('href'), /^\/api\/fs\/raw\?/, 'preview is not the panel\'s own'); assert.ok((await ok.innerText()).includes(xss), 'title not shown as text');
    const ln = arts.filter({ hasText: 'QA link' }); assert.equal(await ln.getAttribute('href'), url); assert.equal(await ln.getAttribute('target'), '_blank'); assert.match(await ln.getAttribute('rel'), /noopener/); assert.match(await ln.getAttribute('rel'), /noreferrer/);
    await sleep(1000); assert.deepEqual(seen.filter((u) => u === '/shot.png'), [], 'the link was fetched by the page or panel');
    assert.deepEqual(off, [], 'requests left the panel origin'); assert.deepEqual(errs, [], 'page errors');
    assert.ok(!(await p.content()).includes('SECRET=1'), 'secret bytes on the page');
    await c.close();
  } finally { srv.close(); }
});
