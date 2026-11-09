// Feature-page acceptance, browser: the owner's chat never sends a second message while the first may still arrive.
// Pending messages are kept outside the composer, their status refresh adapts, and an unanswered POST followed by a 404
// never resends under a new id. The message route is injected per scenario (outage shapes); everything else is the real page.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { SEL, openFeatureJS } from './names.mjs';
import { R } from './names.mjs';
import { S, B, F, api, feature, sleep, seedFeature, launchChromium } from './lib.mjs';

let br; before(async () => { br = await launchChromium(); }); after(async () => br?.close());

async function page(clock, scale, name = S) {
  const c = await br.newContext({ viewport: { width: 1440, height: 1000 } }); await c.request.post(B + '/api/login', { data: { pin: '482913' } });
  const p = await c.newPage(); if (clock) await p.clock.install();
  if (scale) await p.addInitScript((k) => { const st = window.setTimeout; window.setTimeout = (f, ms, ...a) => st(f, Math.max(0, (ms || 0) / k), ...a); }, scale); // injected scheduler, short timers
  const posts = [], gets = [];
  await p.goto(B + '/'); await p.waitForFunction(() => typeof setView === 'function'); await p.evaluate(openFeatureJS(name));
  await p.waitForSelector(SEL.section('Plan and milestones'), { timeout: 6000 });
  return { c, p, posts, gets };
}
async function openChat(p, text, name = S) {
  const k = steps0(await (await api('GET', R.feature(name))).body), li = p.locator(SEL.step(k));
  await li.evaluate((el) => el.closest('details')?.setAttribute('open', '')); await li.scrollIntoViewIfNeeded();
  const b = await li.boundingBox(); await p.mouse.click(b.x + 40, b.y + 8, { button: 'right' });
  await p.locator(SEL.chat).waitFor({ timeout: 3000 }); if (text != null) await p.locator(SEL.chatText).fill(text);
}
const steps0 = (f) => f.dossier.milestones[0].steps[0].key;
const show = async (p, name) => { await p.evaluate(openFeatureJS(name)); await p.waitForSelector(SEL.section('Plan and milestones'), { timeout: 6000 }); await sleep(500); };
const ids = (posts) => [...new Set(posts.map((x) => x.clientId))];
async function trySendAgain(p, text) { // what an owner would do: reopen and send, or press whatever send/retry is enabled
  if (!(await p.locator(SEL.chat).count())) await openChat(p, text);
  for (const sel of [SEL.chatSend, SEL.chatRetry]) { const b = p.locator(sel); if ((await b.count()) && (await b.isEnabled())) { await b.click(); await sleep(800); return sel; } }
  return null;
}

test('U3 closing the chat while a message may still arrive does not let a new message be sent', async () => {
  const { c, p, posts } = await page();
  await p.route('**/api/term/feature/message*', (r) => { if (r.request().method() === 'POST') { posts.push(JSON.parse(r.request().postData())); return r.fulfill({ status: 500, json: { error: 'queued for the lead, but its record wasn\'t saved (a retry may send it twice)' } }); } return r.fulfill({ json: { ok: true, state: 'unknown', reason: 'its lead\'s history can\'t be read' } }); });
  await openChat(p, 'qa u3 first'); await p.locator(SEL.chatSend).click(); await sleep(1500);
  assert.equal(posts.length, 1, 'precondition: one POST');
  await p.keyboard.press('Escape'); await sleep(300);
  await openChat(p, 'qa u3 second'); await trySendAgain(p, 'qa u3 second');
  assert.deepEqual(ids(posts), [posts[0].clientId], `a second message (new id) was sent while the first may still arrive: ${JSON.stringify(posts.map((x) => [x.clientId, x.text]))}`);
  await c.close();
});

test('U5 an unanswered POST followed by GET 404 never authorizes a resend under a new id', async () => {
  const { c, p, posts } = await page();
  await p.route('**/api/term/feature/message*', (r) => { if (r.request().method() === 'POST') { posts.push(JSON.parse(r.request().postData())); return r.abort('connectionreset'); } return r.fulfill({ status: 404, json: { error: 'no such message' } }); });
  await openChat(p, 'qa u5'); await p.locator(SEL.chatSend).click(); await sleep(4500); // one poll answers 404
  await trySendAgain(p, 'qa u5');
  assert.deepEqual(ids(posts), [posts[0].clientId], `resent under a new id after an unanswered POST: ${JSON.stringify(posts.map((x) => x.clientId))}`);
  await c.close();
});

test('U4 the status keeps refreshing past the outbox\'s longest backoff (10 min), on a scaled scheduler (×1/100)', async () => {
  const { c, p, gets } = await page(false, 100); // page timers run 100× faster; Date is not frozen
  await p.route('**/api/term/feature/message*', (r) => { if (r.request().method() === 'POST') return r.fulfill({ json: { ok: true, id: 'x', state: 'queued' } }); gets.push(Date.now()); return r.fulfill({ json: { ok: true, state: 'queued' } }); });
  await openChat(p, 'qa u4'); await p.locator(SEL.chatSend).click();
  await sleep(4000); const n4 = gets.length; // ≈ 6.7 scaled minutes
  await sleep(8000); const n12 = gets.length; // ≈ 20 scaled minutes
  assert.ok(n4 > 0, 'precondition: polled at all');
  assert.ok(n12 > n4, `status refresh stopped: ${n4} GETs by ~7 scaled min, still ${n12} at ~20 scaled min`);
  await c.close();
});

// The page keeps pending messages outside the composer (sessionStorage) and polls them; these check that what
// survives a reload, a feature switch or a rebind is only ever followed by its OWN id, and never auto-sent anywhere.
const outage = (posts, gets) => async (r) => { const q = r.request(); if (q.method() === 'POST') { posts.push(JSON.parse(q.postData())); return r.fulfill({ status: 500, json: { error: 'queued for the lead, but its record wasn\'t saved (a retry may send it twice)' } }); }
  gets.push(new URL(q.url()).searchParams.get('id')); return r.fulfill({ json: { ok: true, state: 'unknown', reason: 'its lead\'s history can\'t be read' } }); };

test('U6 two pending messages (two features) survive a reload: each is followed by its own id, neither is resent under a new id', async () => {
  const S2 = `${S}-u6`; await seedFeature(S2, F.spare[18]);
  const { c, p, posts, gets } = await page(); await p.route('**/api/term/feature/message*', outage(posts, gets));
  await openChat(p, 'qa u6 one'); await p.locator(SEL.chatSend).click(); await sleep(1200); await p.keyboard.press('Escape');
  await show(p, S2); await openChat(p, 'qa u6 two', S2); await p.locator(SEL.chatSend).click(); await sleep(1200);
  assert.equal(new Set(posts.map((x) => x.clientId)).size, 2, 'precondition: two messages, two ids');
  assert.deepEqual(posts.map((x) => x.session), [S, S2], 'each message went to its own feature');
  const sent = posts.map((x) => x.clientId); gets.length = 0;
  await p.reload(); await p.waitForFunction(() => typeof setView === 'function'); await show(p, S); await sleep(8000);
  assert.deepEqual(posts.map((x) => x.clientId).filter((id) => !sent.includes(id)), [], 'a pending message was resent under a new id after the reload');
  assert.ok(sent.some((id) => gets.includes(id)), `no pending message is followed after the reload (GETs: ${JSON.stringify([...new Set(gets)])})`);
  assert.ok(gets.every((id) => sent.includes(id)), 'polls for ids that were never sent');
  await c.close();
});

test('U7 a pending message is never auto-sent to the new manager after a rebind (reload, sessionStorage)', async () => {
  const S3 = `${S}-u7`, [A, Bm] = [F.spare[19], F.spare[20]]; await seedFeature(S3, A);
  const { c, p, posts, gets } = await page(false, 0, S3); await p.route('**/api/term/feature/message*', outage(posts, gets));
  await openChat(p, 'qa u7 pending', S3); await p.locator(SEL.chatSend).click(); await sleep(1200);
  assert.equal(posts.length, 1); assert.equal(posts[0].thread, A, 'precondition: addressed to A');
  assert.equal((await api('POST', '/api/ai/thread/archive', { id: A })).status, 200); assert.equal((await api('POST', R.bind, { session: S3, thread: Bm })).status, 200);
  await p.reload(); await p.waitForFunction(() => typeof setView === 'function'); await show(p, S3); await sleep(8000);
  assert.equal(posts.length, 1, `after the rebind something was POSTed on its own: ${JSON.stringify(posts.map((x) => [x.clientId, x.thread]))}`);
  assert.ok(!posts.some((x) => x.thread === Bm), 'the pending message was readdressed to the new manager');
  await c.close();
});


test('U3b ordinary Send stays off for the item while its message may still arrive, whatever the new words or manager; only the explicit separate action sends one, warning both may arrive', async () => {
  const S3 = `${S}-u3b`, [A, Bm] = [F.spare[9], F.spare[10]]; await seedFeature(S3, A);
  const { c, p, posts, gets } = await page(false, 0, S3); await p.route('**/api/term/feature/message*', outage(posts, gets));
  await openChat(p, 'qa u3b first', S3); await p.locator(SEL.chatSend).click(); await sleep(1200);
  assert.equal(posts.length, 1, 'precondition: one POST'); assert.equal(posts[0].thread, A);
  await p.keyboard.press('Escape'); await sleep(300);
  await openChat(p, 'completely different words', S3); // changed words
  assert.ok(await p.locator(SEL.chatSend).isDisabled(), 'ordinary Send is on with new words while the first may still arrive');
  await p.locator(SEL.chatSend).click({ force: true }); await p.keyboard.press('Control+Enter'); await sleep(800);
  assert.equal(posts.length, 1, 'new words sent a second message');
  await p.keyboard.press('Escape');
  assert.equal((await api('POST', '/api/ai/thread/archive', { id: A })).status, 200); assert.equal((await api('POST', R.bind, { session: S3, thread: Bm })).status, 200);
  await p.reload(); await p.waitForFunction(() => typeof setView === 'function'); await show(p, S3); // changed manager
  await openChat(p, 'to the new manager', S3);
  assert.ok(await p.locator(SEL.chatSend).isDisabled(), 'ordinary Send is on after the manager changed');
  await p.locator(SEL.chatSend).click({ force: true }); await sleep(800); assert.equal(posts.length, 1, 'a manager change let a second message through');
  await p.locator('[data-csep]').click(); await sleep(300);
  const warn = await p.locator(SEL.chat).innerText(); await p.screenshot({ path: `${process.env.V8QA_E}/shots/u3b-separate.png` });
  assert.match(warn, /may get both/, 'the explicit separate action does not warn that both may arrive: ' + warn);
  await p.locator(SEL.chatSend).click(); await sleep(1200);
  assert.equal(posts.length, 2, 'the explicit separate message was not sent'); assert.notEqual(posts[1].clientId, posts[0].clientId); assert.equal(posts[1].thread, Bm);
  await c.close();
});

test('U8 at 50 unresolved messages (sent through the page) a new one is refused with why and none is evicted, also over a reload; a legacy store over 50 keeps every unresolved one', async () => {
  const S3 = `${S}-u8`; await seedFeature(S3, F.spare[11]);
  const { c, p, posts, gets } = await page(false, 0, S3); await p.route('**/api/term/feature/message*', outage(posts, gets));
  await openChat(p, null, S3);
  for (let i = 0; i < 50; i++) { // the same item: each after the first only via the explicit separate action
    if (i) { await p.locator('[data-csep]').click(); await sleep(100); }
    await p.locator(SEL.chatText).fill('qa u8 ' + i); await p.locator(SEL.chatSend).click(); await sleep(250);
  }
  assert.equal(ids(posts).length, 50, 'precondition: 50 distinct messages posted, all unresolved');
  const sep = p.locator('[data-csep]'); if ((await sep.count()) && /Send a separate/.test(await sep.innerText())) await sep.click();
  await p.locator(SEL.chatText).fill('qa u8 51st');
  assert.ok(await p.locator(SEL.chatSend).isDisabled(), 'the 51st is not refused');
  assert.match(await p.locator(SEL.chat).innerText(), /50 messages .*not confirmed yet\. None is dropped/, 'no reason given');
  await p.locator(SEL.chatSend).click({ force: true }); await sleep(500); assert.equal(posts.length, 50, 'the 51st was posted');
  const kept = () => p.evaluate(() => JSON.parse(sessionStorage.getItem('fv.out') || '[]').filter((h) => h.maybe).map((h) => h.id));
  const before = await kept(); assert.equal(before.length, 50, 'an unresolved message was evicted');
  await p.reload(); await p.waitForFunction(() => typeof setView === 'function'); await show(p, S3); await sleep(4000);
  assert.deepEqual((await kept()).sort(), before.sort(), 'unresolved messages lost over a reload');
  const g0 = gets.length; await sleep(6000); assert.ok(new Set(gets.slice(g0)).size > 1, 'unresolved messages are no longer re-checked after the reload');
  const legacy = before.slice(0, 3).map((id, i) => ({ f: S3, src: 'chat', anchorA: 'legacy-' + i, id: 'ow-legacy-' + i, text: 'legacy ' + i, state: 'unknown', maybe: true, thread: F.spare[11], at: Date.now() - 1000 }));
  await p.evaluate((l) => { const o = JSON.parse(sessionStorage.getItem('fv.out')); sessionStorage.setItem('fv.out', JSON.stringify([...o, ...l])); }, legacy);
  await p.reload(); await p.waitForFunction(() => typeof setView === 'function'); await show(p, S3); await sleep(1500); await p.evaluate(() => fvOutSave());
  assert.equal((await kept()).length, 53, 'a legacy store with 53 unresolved lost some');
  assert.equal(posts.length, 50, 'something was posted on its own');
  await c.close();
});
