// Feature-page acceptance: code delivery from GitHub deployments, through the stand-in gh (fixture/gh, canned GraphQL for
// qa8/ledger, origin never fetched). Ids DP1..DP4. GET must never wait on gh; refreshes run in the background.
import { test, before } from 'node:test';
import assert from 'node:assert/strict';
import { writeFileSync } from 'node:fs';
import { R } from './names.mjs';
import { F, E, api, lines, until, sleep, seedFeature } from './lib.mjs';

// its own feature, whose FEATURE.md names the scratch repo as its main worktree (the owner's "Main worktree:" line)
const S = `${F.session}-dp`;
before(async () => { await seedFeature(S); const f = (await api('GET', R.feature(S))).body;
  const w = await api('POST', R.doc, { session: S, doc: 'feature', rev: f.dossier.revs?.feature ?? 'e3b0c44298fc1c14', text: `# Quokka\n\nMain worktree: ${F.repo}\n\n## Desired outcomes\n- [ ] D1: exported — done when: ledger.txt exists\n` });
  assert.equal(w.status, 200, JSON.stringify(w.body)); });

const code = async () => { const t0 = Date.now(); const r = await api('GET', `/api/term/feature/code?session=${encodeURIComponent(S)}`); assert.equal(r.status, 200); r.body.ms = Date.now() - t0; return r.body; };
const rel = (c) => (c.releases || []).find((r) => r.repo === 'qa8/ledger');
const iso = (m) => new Date(Date.now() - m * 60000).toISOString();
const st = (state, m) => ({ state, createdAt: iso(m), logUrl: 'https://example.invalid/log' });
const dep = (env, sha, m, statuses) => ({ environment: env, commitOid: sha, createdAt: iso(m), newest: { nodes: [...statuses].reverse().slice(0, 2) }, oldest: { nodes: statuses } });
const C2 = 'c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2';
const ship = { data: { repository: {
  deployments: { nodes: [
    dep('production', F.c1, 50, [st('SUCCESS', 49)]),          // live
    dep('production', C2, 20, [st('FAILURE', 19)]),            // a newer failed attempt: never "deployed"
    dep('stage', F.featureHead, 30, [st('SUCCESS', 29)]),      // live on stage
    dep('stage', C2, 5, []),                                   // pending (no status yet)
  ] },
  pullRequests: { nodes: [{ number: 7, title: 'Release stage to main', url: 'https://github.com/qa8/ledger/pull/7', state: 'OPEN', baseRefName: 'main', mergedAt: '0001-01-01T00:00:00Z', mergeCommit: null, author: { login: 'someone' } }] },
} } };

test('DP1 gh down: deploys are unknown with why ("gh failed, never fetched"), never guessed; GET does not wait on gh', async () => {
  const t0 = Date.now(), c = await until(async () => { const x = await code(); return rel(x) && x; }, 60000, 1000); if (c) console.log(`# DP1 first release row after ${Date.now() - t0} ms (background refresh)`); assert.ok(c, 'precondition: the scratch repo (origin github.com/qa8/ledger) is a release row; /code: ' + JSON.stringify(await code()).slice(0, 900));
  const r = rel(c);
  for (const t of ['prod', 'stage']) { assert.equal(r[t].state, 'unknown', `${t}: ${JSON.stringify(r[t])}`); assert.match(r[t].why || '', /gh failed|not fetched/); assert.ok(!r[t].sha, `${t} has a sha with no source`); }
  assert.ok(c.ms < 1500, `GET /code took ${c.ms} ms`);
});

test('DP2 gh back: prod = the live SUCCESS (c1), the newer FAILURE is an attempt, never the deployed version; stage live + pending attempt', async () => {
  writeFileSync(`${E}/gh-ship.json`, JSON.stringify(ship)); writeFileSync(`${E}/gh-mode`, 'ok\n');
  const c = await until(async () => { const x = await code(); return rel(x)?.prod?.sha && x; }, 100000, 2000); // the failed fetch retries after shipRetry (1 min)
  assert.ok(c, 'deployments never fetched after gh recovered; gh calls:\n' + lines('gh-calls.log').slice(-5).join('\n'));
  const r = rel(c);
  assert.equal(r.prod.sha, F.c1, 'prod runs the last SUCCESS'); assert.equal(r.prod.attempt?.state, 'fail'); assert.equal(r.prod.attempt?.sha, C2);
  assert.equal(r.stage.sha, F.featureHead); assert.equal(r.stage.attempt?.state, 'run', 'a pending stage deploy is an attempt in progress');
  for (const t of ['prod', 'stage']) { assert.equal(r[t].state, 'fresh'); assert.ok(r[t].fetchedAt > 1.7e9, 'fetchedAt'); }
  assert.ok(c.ms < 1500, `GET /code took ${c.ms} ms`);
});

test('DP3 an open stage→main release PR the feature did not link is not "preparing" for this feature', async () => {
  const r = rel(await code());
  assert.equal(r.preparing?.state, 'unknown', JSON.stringify(r.preparing)); assert.ok(!r.preparing?.pr, 'an unlinked release PR shown as preparing this feature');
});

test('DP4 GETs read the cache only: repeated GETs within the TTL spawn no gh', async () => {
  const n0 = lines('gh-calls.log').length; for (let i = 0; i < 5; i++) await code(); await sleep(1000);
  assert.equal(lines('gh-calls.log').length, n0, 'GETs spawned gh:\n' + lines('gh-calls.log').slice(n0).join('\n'));
});
