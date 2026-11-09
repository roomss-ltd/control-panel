// Feature-page acceptance, browser half: the real panel page (web/*) on the real API, in Chromium.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { SECTIONS, SEL, FIXTURE_STRINGS, openFeatureJS, R } from './names.mjs';
import { S, B, E, F, feature, page as pageData, lines, sleep, agent, causedModelCalls, unmeasuredTree, api, seedFeature, launchChromium } from './lib.mjs';

let br, ctx, pg; const errs = [], offOrigin = [], seenPosts = [];
// an HTTP failure a scenario injects is registered here and consumed once by URL path + status; any other failure is an error
const expected = []; const expectFail = (path, status, why) => expected.push({ path, status, why, seen: false });
const shot = (n, o = {}) => pg.screenshot({ path: `${E}/shots/v8qa-${n}.png`, ...o });
async function open(p = pg) {
  await p.goto(B + '/'); await p.waitForFunction(() => typeof setView === 'function');
  await p.evaluate(openFeatureJS(S));
  await p.waitForSelector(SEL.section('Plan and milestones'), { timeout: 6000 }); // page absent → fail fast
}
async function fresh(opts = {}) {
  const c = await br.newContext({ viewport: { width: 1440, height: 1000 }, ...opts });
  await c.request.post(B + '/api/login', { data: { pin: '482913' } });
  const p = await c.newPage();
  p.on('pageerror', (e) => errs.push(String(e)));
  p.on('console', (m) => { if (m.type() !== 'error') return; const f = /^Failed to load resource: the server responded with a status of (\d+)/.exec(m.text());
    if (f) { let path = '?'; try { path = new URL(m.location().url).pathname; } catch {} const e = expected.find((x) => !x.seen && x.path === path && x.status === +f[1]); if (e) { e.seen = true; return; } }
    errs.push('console: ' + m.text() + (f ? ' @ ' + (m.location().url || '?') : '')); });
  p.on('request', (r) => { if (!r.url().startsWith(B) && !r.url().startsWith('data:') && !r.url().startsWith('blob:')) offOrigin.push(r.url()); });
  await p.route('**' + R.seen + '*', (r) => { seenPosts.push({ t: Date.now(), ids: JSON.parse(r.request().postData() || '{}').ids }); r.continue(); });
  return { c, p };
}
before(async () => {
  for (let i = 0; i < 12; i++) await agent('POST', R.aLog, { session: S, line: `ACTION: qa seen row ${i} for the ledger`, thread: (await feature()).thread?.id }); // rows to see
  br = await launchChromium();
  ({ c: ctx, p: pg } = await fresh());
});
after(async () => { await br?.close(); });

test('L1 the approved layout: six sections in order, no metric cards, no floating comment button', async () => {
  await open(); await shot('01-page-1440', { fullPage: true });
  const got = await pg.$$eval('section[aria-label]', (s) => s.map((x) => x.getAttribute('aria-label')));
  assert.deepEqual(got.filter((x) => SECTIONS_SET.has(x)), [...SECTIONS]);
  assert.equal(await pg.locator(SEL.metricCards).count(), 0);
  assert.equal(await pg.locator(SEL.floatingComment).count(), 0);
});
const SECTIONS_SET = new Set(SECTIONS);

test('D1/D3 real record only: none of the prototype fixtures, and the record\'s own steps', async () => {
  await open(); await sleep(1500);
  const txt = await pg.evaluate(() => document.body.innerText);
  const leaked = FIXTURE_STRINGS.filter((s) => txt.includes(s));
  assert.deepEqual(leaked, [], 'prototype fixture text on a real page');
  const f = await feature();
  for (const m of f.dossier.milestones) for (const s of m.steps) {
    const li = pg.locator(SEL.step(s.key)); assert.equal(await li.count(), 1, `step ${s.key} rendered once`);
    assert.ok((await li.textContent()).includes(s.text), `step ${s.key} shows its record text`); // textContent: a closed milestone hides innerText
  }
});

test('D2 each Code delivery state says what the server knows: null = "not tracked", [] = none, never fixtures or a stuck spinner', async () => {
  await open(); await sleep(3000);
  const c = await (await fetch(`${B}/api/term/feature/code?session=${encodeURIComponent(S)}`, { headers: { Cookie: await pg.context().cookies().then((k) => k.map((x) => `${x.name}=${x.value}`).join('; ')) } })).json();
  const strip = (await pg.locator(`${SEL.section('Code delivery')} [data-k="fvrel"]`).innerText()).trim();
  if (c.releases == null) assert.match(strip, /not tracked/i, `releases null: ${strip}`);
  else if (!c.releases.length) { assert.match(strip, /no repository with a deploy source/i, `releases []: ${strip}`); assert.doesNotMatch(strip, /not tracked/i, 'a real empty answer shown as not tracked'); }
  else assert.equal(await pg.locator(`${SEL.section('Code delivery')} [data-relc]`).count() > 0, true, 'releases known but no cells');
  console.log(`# D2 /code: releases=${JSON.stringify(c.releases)} branches=${Array.isArray(c.branches) ? c.branches.length : c.branches} → "${strip.slice(0, 120)}"`);
  assert.equal(await pg.locator(`${SEL.section('Code delivery')} .spin, ${SEL.section('Team timeline')} .spin`).count(), 0);
});

test('S1 opening the page marks nothing seen; only rows really on screen ≥1 s are sent', async () => {
  seenPosts.length = 0; const t0 = Date.now(); await open();
  await sleep(800); assert.equal(seenPosts.filter((x) => x.t - t0 < 900).length, 0, 'seen sent on open');
  await sleep(2500);
  const visible = await pg.evaluate(() => [...document.querySelectorAll('[data-log], [data-seen]')].filter((e) => { const r = e.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight && r.height > 0; }).map((e) => e.dataset.log || e.dataset.seen));
  const sent = seenPosts.flatMap((x) => x.ids || []);
  assert.deepEqual(sent.filter((id) => !visible.includes(id)), [], 'seen sent for rows that were never on screen');
});

const unseenRows = () => pg.evaluate(() => [...document.querySelectorAll('[data-seen]')].filter((e) => e.dataset.seen).length);
test('S3 positive control: a row on screen ≥1 s with focus is sent once, with its own id', async () => {
  await open(); await sleep(500); seenPosts.length = 0;
  const id = await pg.evaluate(() => { const el = [...document.querySelectorAll('[data-seen]')].filter((e) => e.dataset.seen).pop(); if (!el) return null; el.scrollIntoView({ block: 'center' }); return el.dataset.seen; });
  assert.ok(id, 'precondition: an unseen row exists (log lines were written)');
  await sleep(2600);
  const sent = seenPosts.flatMap((x) => x.ids || []);
  assert.ok(sent.includes(id), 'a really-visible row was not marked seen');
  assert.equal(sent.filter((x) => x === id).length, 1, 'sent more than once');
});

test('S6 no optimistic ack: a refused/failed seen batch leaves the row unseen; it is resent after recovery', async () => {
  await open(); await sleep(300);
  let fail = true; const tries = [];
  await pg.route('**' + R.seen + '*', (r) => { tries.push(JSON.parse(r.request().postData() || '{}').ids); if (fail) { expectFail(R.seen, 503, 'S6 injected outage'); return r.fulfill({ status: 503, body: '{"error":"qa: down"}' }); } return r.continue(); });
  const id = await pg.evaluate(() => { const el = [...document.querySelectorAll('[data-seen]')].filter((e) => e.dataset.seen).pop(); el?.scrollIntoView({ block: 'center' }); return el?.dataset.seen; });
  assert.ok(id, 'precondition: an unseen row'); await sleep(2600);
  assert.ok(tries.flat().includes(id), 'seen was attempted');
  const p0 = (await pageData())?.seen;
  assert.ok(!p0 || !p0.ids?.includes(id), 'server recorded seen from a failed batch');
  assert.ok(await pg.evaluate((i) => !!document.querySelector(`[data-log="${CSS.escape(i)}"].unseen, [data-seen="${CSS.escape(i)}"]`), id), 'row shown as seen although the server never recorded it');
  fail = false; await sleep(17000);
  assert.ok(tries.flat().filter((x) => x === id).length >= 2, 'the kept id was not resent after recovery');
  await pg.unroute('**' + R.seen + '*');
});

test('S2 a fast scroll past sends nothing', async () => {
  for (let i = 0; i < 6; i++) await agent('POST', R.aLog, { session: S, line: `ACTION: qa s2 fresh row ${i}`, thread: (await feature()).thread?.id }); // its own unseen rows: S3/S6 saw the shared ones
  await open(); assert.ok(await unseenRows() > 0, 'precondition: unseen rows exist (else this test is vacuous)'); await pg.evaluate(() => scrollTo(0, 0)); await sleep(1500); seenPosts.length = 0;
  await pg.evaluate(async () => { const h = document.documentElement.scrollHeight; for (let y = 0; y < h; y += 400) { scrollTo(0, y); await new Promise((r) => setTimeout(r, 40)); } scrollTo(0, 0); });
  await sleep(300); assert.equal(seenPosts.length, 0, 'fast scroll marked rows seen');
});

test('S4a another tab in front (real focus loss) sends nothing', async () => {
  await open(); const cdp = await ctx.newCDPSession(pg); await cdp.send('Emulation.setFocusEmulationEnabled', { enabled: false });
  const other = await ctx.newPage(); await other.goto(B + '/'); await other.bringToFront();
  const bg = await pg.evaluate(() => ({ vis: document.visibilityState, focus: document.hasFocus() }));
  assert.equal(bg.focus, false, 'precondition: the feature page lost focus: ' + JSON.stringify(bg));
  seenPosts.length = 0; await pg.evaluate(() => { const el = [...document.querySelectorAll('[data-seen]')].find((e) => e.dataset.seen) || document.querySelector('[data-log]'); el?.scrollIntoView({ block: 'center' }); }); await sleep(2000);
  assert.equal(seenPosts.length, 0, 'seen sent while another tab was in front');
  await other.close(); await pg.bringToFront(); await cdp.send('Emulation.setFocusEmulationEnabled', { enabled: true }); await cdp.detach();
});

test('S4b hidden tab (SIMULATED visibilitychange: headless Chromium cannot hide a page) sends nothing', async () => {
  await open(); await pg.evaluate(() => scrollTo(0, document.documentElement.scrollHeight)); seenPosts.length = 0;
  await pg.evaluate(() => { Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' }); document.dispatchEvent(new Event('visibilitychange')); });
  await pg.evaluate(() => document.querySelector('[data-log]')?.scrollIntoView({ block: 'center' })); await sleep(2000);
  assert.equal(seenPosts.length, 0, 'seen sent while hidden');
  await pg.evaluate(() => { delete document.visibilityState; document.dispatchEvent(new Event('visibilitychange')); });
});

test('C1-C3 right-click a step opens the manager chat at the click, with the record anchor@rev', async () => {
  await open();
  const f = await feature(), k = f.dossier.milestones[0].steps[0].key;
  const li = pg.locator(SEL.step(k)); await li.evaluate((el) => el.closest('details')?.setAttribute('open', '')); await li.scrollIntoViewIfNeeded(); const b = await li.boundingBox();
  await pg.mouse.click(b.x + 40, b.y + 8, { button: 'right' });
  const chat = pg.locator(SEL.chat); await chat.waitFor({ timeout: 3000 });
  const r = await chat.boundingBox(), vw = 1440, vh = 1000;
  assert.ok(r.x >= 8 && r.y >= 8 && r.x + r.width <= vw - 8 && r.y + r.height <= vh - 8, 'chat inside the viewport');
  assert.ok(Math.hypot(r.x - (b.x + 40), r.y - (b.y + 8)) < 400, 'chat opens near the click');
  const t = await chat.innerText();
  assert.ok(t.includes(f.thread?.title || F.thread), 'destination = the bound manager');
  assert.ok(t.includes(k) && t.includes(f.dossier.revs.plan), `anchor@rev from the record (key ${k}, plan rev ${f.dossier.revs.plan}); chat says: ${JSON.stringify(t.slice(0, 400))}`);
  assert.ok(!/simulated/i.test(t), 'production chat must not say simulated');
  await shot('02-chat');
  await pg.keyboard.press('Escape'); assert.equal(await chat.count(), 0);
});

test('C1 Shift+right-click keeps the browser menu; Shift+F10 opens the chat from the keyboard', async () => {
  await open();
  const k = (await feature()).dossier.milestones[0].steps[0].key, li = pg.locator(SEL.step(k)); await li.evaluate((el) => el.closest('details')?.setAttribute('open', ''));
  const prevented = await li.evaluate((el) => { const e = new MouseEvent('contextmenu', { bubbles: true, cancelable: true, shiftKey: true, clientX: 50, clientY: 50 }); el.dispatchEvent(e); return e.defaultPrevented; });
  assert.equal(prevented, false); assert.equal(await pg.locator(SEL.chat).count(), 0);
  await li.focus(); await pg.keyboard.press('Shift+F10');
  await pg.locator(SEL.chat).waitFor({ timeout: 3000 });
  await pg.keyboard.press('Escape');
  assert.equal(await pg.evaluate(() => document.activeElement?.closest('[data-key]')?.dataset.key), k, 'Esc returns focus to the opener');
});

test('L3 390 px: no horizontal overflow; chat stays in view', async () => {
  const { c, p } = await fresh({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
  await open(p); await sleep(800);
  assert.ok(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'horizontal overflow at 390');
  await p.screenshot({ path: `${E}/shots/v8qa-03-390.png`, fullPage: true });
  await c.close();
});

test('L4 reduced motion: no running animations', async () => {
  const { c, p } = await fresh({ reducedMotion: 'reduce' });
  await open(p); await sleep(1200);
  const n = await p.evaluate(() => document.getAnimations().filter((a) => a.playState === 'running').length);
  assert.equal(n, 0, 'animations run under reduced motion');
  await c.close();
});

test('L5 accessibility basics: named controls, chat is a named dialog', async () => {
  await open();
  const n = await pg.evaluate(() => { let i = 0; for (const e of document.querySelectorAll('main button, main [role=button], main input, main textarea, main select, main a[href]')) if (e.offsetParent) e.dataset.qaA11y = String(i++); return i; });
  const unnamed = [];
  for (let i = 0; i < n; i++) {
    const el = pg.locator(`[data-qa-a11y="${i}"]`);
    if (!(await el.count())) continue; // re-rendered away meanwhile
    const role = await el.evaluate((e) => e.getAttribute('role') || ({ BUTTON: 'button', A: 'link', SELECT: 'combobox', TEXTAREA: 'textbox' })[e.tagName] || (e.type === 'checkbox' ? 'checkbox' : e.type === 'radio' ? 'radio' : 'textbox'), undefined, { timeout: 2000 }).catch(() => null);
    if (!role) continue;
    if (!(await pg.getByRole(role, { name: /\S/, includeHidden: true }).and(el).count())) { const h = await el.evaluate((e) => e.outerHTML.slice(0, 100), undefined, { timeout: 2000 }).catch(() => null); if (h) unnamed.push(h); }
  }
  assert.deepEqual(unnamed, [], 'controls with no accessible name (computed by the browser)');
  await pg.evaluate(() => document.querySelectorAll('[data-qa-a11y]').forEach((e) => delete e.dataset.qaA11y));
});

test('V9 UI: an unmeasured worktree shows no "in main worktree" or a count (precondition: such a tree exists)', async () => {
  // the worktree list is the surface only while a feature's branches aren't served (no 'Main worktree:'): its own feature
  const SV = `${S}-v9ui`; await seedFeature(SV, F.spare[12]);
  const u = await unmeasuredTree('child-ui', 60000, SV, 2);
  try {
    assert.ok(u.tree, `UNMET PRECONDITION: the panel returned no tree for the agent working in ${u.dir}: ` + JSON.stringify((await feature()).trees));
    assert.equal(u.tree.checked ?? false, false, 'precondition: unmeasured: ' + JSON.stringify(u.tree));
    await open(); await pg.evaluate(openFeatureJS(SV)); await pg.waitForSelector(SEL.section('Plan and milestones'), { timeout: 6000 }); await sleep(2500);
    const row = pg.locator(`[data-fvtree="${u.dir}"]`);
    if (!(await row.count())) { await pg.screenshot({ path: `${E}/shots/v9-missing.png`, fullPage: true }); const st = await pg.evaluate(() => ({ branches: FV.M?.pg?.branches ?? null, codeReading: FV.M?.pg?.codeReading, trees: FV.M?.d?.trees, name: FV.M?.name, fvtrees: [...document.querySelectorAll('[data-fvtree]')].map((e) => e.dataset.fvtree) })).catch((e) => String(e)); assert.fail('VISIBLE ROW UNMET: the unmeasured worktree is not listed : ' + JSON.stringify(st)); }
    console.log('# V9 UI page.branches: ' + JSON.stringify((await api('GET', R.feature(SV))).body.page?.branches ?? null));
    const t = await row.innerText(); await pg.screenshot({ path: `${E}/shots/v9-unmeasured.png`, fullPage: true });
    assert.doesNotMatch(t, /in main worktree|\d+ not in main|merged|up to date/i, `unmeasured ${u.dir} reads as measured: ${t}`);
    assert.match(t, /not measured yet/, 'the unmeasured worktree does not say so: ' + t);
  } finally { u.release(); }
});

test('L6/X3 no page errors, nothing leaves the panel origin, no model call from viewing', () => {
  assert.deepEqual(errs, []);
  assert.deepEqual(expected.filter((x) => !x.seen).map((x) => `${x.status} ${x.path}: ${x.why}`), [], 'an injected failure never surfaced');
  assert.deepEqual(offOrigin, []);
  assert.deepEqual(causedModelCalls(), [], 'a model call not from the background scheduler (chapters.go chaptersLoop)'); // scheduler calls are listed by api X3, refused by the stand-in
});
