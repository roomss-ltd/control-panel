// Shared helpers for the feature-page acceptance suites: HTTP clients for the scratch panel, state readers, stand-in
// agents and the browser launcher. The environment comes from run.sh (V8QA_E, V8QA_OUT, facts.json).
import { readFileSync, existsSync, writeFileSync, rmSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { R } from './names.mjs';

export const E = process.env.V8QA_E, OUT = process.env.V8QA_OUT;
if (!E) throw new Error('run through acceptance/v8/run.sh (V8QA_E unset)');
export const F = JSON.parse(readFileSync(`${E}/facts.json`, 'utf8'));
export const S = F.session, B = `http://127.0.0.1:${F.p1}`, AG = `http://127.0.0.1:${F.p2}`;
export const STATE = `${E}/home/.local/state/control-panel`;
export const REC = `${E}/home/jarvis-files/features/${S}`;

let cookie = '';
export async function login() {
  const r = await fetch(B + '/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ pin: '482913' }) });
  cookie = (r.headers.getSetCookie?.() || []).map((c) => c.split(';')[0]).join('; ');
  if (!cookie) throw new Error('login gave no cookie: ' + r.status);
  return cookie;
}
const j = async (r) => { const t = await r.text(); let body; try { body = JSON.parse(t); } catch { body = { raw: t }; } return { status: r.status, body }; };
export async function api(method, path, body) {
  if (!cookie) await login();
  return j(await fetch(B + path, { method, headers: { 'Content-Type': 'application/json', Cookie: cookie }, body: body === undefined ? undefined : JSON.stringify(body) }));
}
const token = () => readFileSync(`${E}/home/.config/control-panel/agent-token`, 'utf8').trim();
export async function agent(method, path, body, headers = {}) {
  return j(await fetch(AG + path, { method, headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token(), ...headers }, body: body === undefined ? undefined : JSON.stringify(body) }));
}

export const feature = async () => { const r = await api('GET', R.feature(S)); if (r.status !== 200) throw new Error('feature GET ' + r.status); return r.body; };
export const page = async () => (await feature()).page;

export const hash = (p) => (existsSync(p) ? createHash('sha256').update(readFileSync(p)).digest('hex') : 'absent');
export const stateHashes = () => Object.fromEntries(['featsup.json', 'notes.json', 'threads.json', 'threads.json.outbox.json'].map((f) => [f, hash(`${STATE}/${f}`)]));
export const turns = () => (existsSync(`${E}/turns.jsonl`) ? readFileSync(`${E}/turns.jsonl`, 'utf8').trim().split('\n').filter(Boolean).map((l) => JSON.parse(l)) : []);
export const lines = (f) => (existsSync(`${E}/${f}`) ? readFileSync(`${E}/${f}`, 'utf8').trim().split('\n').filter(Boolean) : []);
export const hermesMode = (m) => writeFileSync(`${E}/hermes-mode`, m + '\n');
// a pooled keep-alive socket to the stopped panel fails the first fetch after a restart: retry the login only
export const restartPanel = async (between) => { execFileSync(`${E}/stop.sh`); between?.(); execFileSync(`${E}/start.sh`); cookie = ''; for (let i = 0; ; i++) { try { return await login(); } catch (e) { if (i >= 4) throw e; await sleep(200); } } };
export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Playwright is not a dependency of the panel, so the browser suites load it on demand: V8QA_PLAYWRIGHT (a package name or
// the absolute path of playwright-core's index.mjs) or else the `playwright-core` package found from this directory upward.
// Launches V8QA_CHROME, or Playwright's own bundled Chromium when that is unset; `opts` override the defaults.
export async function launchChromium(opts = {}) {
  const { chromium } = await import(process.env.V8QA_PLAYWRIGHT || 'playwright-core');
  return chromium.launch({ headless: true, ...(process.env.V8QA_CHROME && { executablePath: process.env.V8QA_CHROME }), ...opts });
}
export async function until(fn, ms = 15000, step = 250) { const t0 = Date.now(); let v; while (Date.now() - t0 < ms) { if ((v = await fn())) return v; await sleep(step); } return v; }

// the dossier's steps, flattened
export const steps = (f) => (f.dossier?.milestones || []).flatMap((m) => m.steps.map((s) => ({ ...s, ms: m.title })));
export const stepByText = (f, t) => steps(f).find((s) => s.text.startsWith(t));

// owner write of a record doc at its current rev
export async function ownerDoc(doc, text) {
  const f = await feature();
  return api('POST', R.doc, { session: S, doc, text, rev: f.dossier.revs?.[doc] ?? f.dossier.rev });
}
export async function agentPlan(text, extra = {}, headers = {}) {
  const f = await feature();
  return agent('POST', R.aDoc, { session: S, doc: 'plan', text, rev: f.dossier.revs?.plan ?? f.dossier.rev, thread: F.thread, ...extra }, headers);
}

// Model calls the stand-in saw, classified by prompt (fixture/claude). scheduler:* = chapters.go's background loop,
// not caused by any page or route; everything else (kind=other, e.g. a report) is a call the run caused.
export const modelCalls = () => lines('claude-calls.log').filter((l) => / -p | --print /.test(l + ' '))
  .map((l) => ({ line: l, kind: (/ kind=(\S+)/.exec(l) || [])[1] || 'unclassified' }));
export const causedModelCalls = () => modelCalls().filter((c) => !c.kind.startsWith('scheduler:'));
// No real model is reachable: the panel's PATH resolves `claude` to this run's stand-in, and HOME is the run's own.
export function realModelUnreachable() {
  const pid = readFileSync(`${E}/panel.pid`, 'utf8').trim().split(' ')[0];
  const env = Object.fromEntries(readFileSync(`/proc/${pid}/environ`, 'utf8').split('\0').filter(Boolean).map((kv) => [kv.slice(0, kv.indexOf('=')), kv.slice(kv.indexOf('=') + 1)]));
  const hit = env.PATH.split(':').find((d) => existsSync(`${d}/claude`));
  return { home: env.HOME, claude: hit && `${hit}/claude`, standin: `${E}/bin/claude`, standinIntact: hash(`${E}/bin/claude`) === hash(new URL('./fixture/claude', import.meta.url).pathname) };
}


// Spare managers are F.spare[0..20]: the API suites use 0-4 and 8-20, ui-fixture.test.mjs uses 5-7.
// A fresh feature of its own (no manager bound), seeded with the fixture PLAN through the owner writer, so a suite
// doesn't depend on what earlier suites did to the shared record (api.test.mjs C6 rebinds it to an empty record).
export async function seedFeature(name, thread) { // thread: bind it to that manager first (the record then lives in its thread folder)
  const n = await api('POST', '/api/term/new', { what: 'session', name, cwd: F.repo, run: 'shell' });
  if (n.status !== 200) throw new Error('new session ' + name + ': ' + JSON.stringify(n.body));
  await sleep(1500);
  if (thread) { const b = await api('POST', R.bind, { session: name, thread }); if (b.status !== 200) throw new Error('bind ' + name + ' → ' + thread + ': ' + JSON.stringify(b.body)); }
  const plan = readFileSync(new URL('./fixture/PLAN.md', import.meta.url), 'utf8');
  const cur = (await api('GET', R.feature(name))).body; // at the rev read: a bound record may already exist
  const w = await api('POST', R.doc, { session: name, doc: 'plan', rev: cur.dossier?.revs?.plan ?? 'e3b0c44298fc1c14', text: plan });
  if (w.status !== 200) throw new Error('seed ' + name + ': ' + JSON.stringify(w.body));
  const r = await api('GET', R.feature(name)); const k = steps(r.body).map((x) => x.key);
  if (k.length < 3) throw new Error(`seeded ${name} has ${k.length} steps`);
  return k;
}

// A Terminals page's live stream: the panel samples panes in full (agents, git) only while one is open.
// Returns a function that closes the stream.
export async function openTermStream() {
  if (!cookie) await login();
  const ac = new AbortController(), r = await fetch(B + '/api/stream?view=term', { headers: { Cookie: cookie }, signal: ac.signal });
  if (r.status !== 200) throw new Error('stream ' + r.status);
  const rd = r.body.getReader(); (async () => { try { while (!(await rd.read()).done); } catch {} })();
  return () => ac.abort();
}

// A stand-in agent as the panel recognises one: a pane made through the panel (/api/term/new
// run shell), the stand-in claude typed into its shell (/api/pane/input), so it descends the pane's root shell whose
// parent is this run's zellij server and carries its session/pane env; then its ~/.claude/sessions/<pid>.json with
// pid, kernel start time, cwd, sessionId, kind interactive, status idle. Never a real Claude (the stand-in sleeps).
// Returns { pid, pane }. Only this run's own zellij socket (TMPDIR) and HOME are touched.
export async function standInAgent(s, cwd) {
  const r = await api('POST', '/api/term/new', { what: 'pane', session: s, tab: 0, name: 'qa-agent', cwd, run: 'shell' });
  if (r.status !== 200) throw new Error('new pane: ' + r.status + ' ' + JSON.stringify(r.body));
  const pane = r.body.pane; let t; // the panel lists the new pane at its next layout read (3 s with a stream open)
  for (let i = 0; i < 40 && (t = await api('POST', '/api/pane/input', { session: s, pane, text: `cd '${cwd}' && ${E}/bin/claude`, enter: true })).status === 404; i++) await sleep(500);
  if (t.status !== 200) { let ls = ''; try { ls = execFileSync('zellij', ['--session', s, 'action', 'list-panes', '--json', '--all'], { env: { ...process.env, TMPDIR: `${E}/tmp`, HOME: `${E}/home` } }).toString(); } catch (e) { ls = String(e); }
    throw new Error(`pane input to pane ${pane} (new: ${JSON.stringify(r.body)}): ${t.status} ${JSON.stringify(t.body)}; zellij panes: ${(() => { try { return JSON.stringify(JSON.parse(ls).filter((x) => !x.is_plugin).map(({ id, is_suppressed, exited, tab_id, title, pane_command, pane_cwd }) => ({ id, is_suppressed, exited, tab_id, title, pane_command, pane_cwd }))); } catch { return ls.slice(0, 800); } })()}`); }
  let pid = 0;
  for (let i = 0; i < 80 && !pid; i++) {
    await sleep(100);
    for (const d of execFileSync('ls', ['/proc']).toString().split('\n').filter((x) => /^\d+$/.test(x))) {
      try { const env = readFileSync(`/proc/${d}/environ`, 'utf8').split('\0');
        if (readFileSync(`/proc/${d}/comm`, 'utf8').trim() === 'sleep' && env.includes(`V8QA_E=${E}`) && env.includes(`ZELLIJ_PANE_ID=${pane}`)
          && env.includes(`ZELLIJ_SESSION_NAME=${s}`) && execFileSync('readlink', [`/proc/${d}/cwd`]).toString().trim() === cwd) { pid = +d; break; } } catch {}
    }
  }
  if (!pid) { const seen = [];
    for (const d of execFileSync('ls', ['/proc']).toString().split('\n').filter((x) => /^\d+$/.test(x))) {
      try { if (execFileSync('readlink', [`/proc/${d}/cwd`], { stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim() !== cwd) continue;
        const env = readFileSync(`/proc/${d}/environ`, 'utf8').split('\0').filter((x) => /^(ZELLIJ_SESSION_NAME|ZELLIJ_PANE_ID|V8QA_E)=/.test(x));
        seen.push({ pid: d, comm: readFileSync(`/proc/${d}/comm`, 'utf8').trim(), env }); } catch {}
    }
    let screen = ''; try { screen = execFileSync('zellij', ['--session', s, 'action', 'dump-screen', '--pane-id', `terminal_${pane}`], { env: { ...process.env, TMPDIR: `${E}/tmp`, HOME: `${E}/home` } }).toString().trim().slice(-400); } catch (e) { screen = String(e).slice(0, 200); }
    throw new Error(`stand-in agent did not start in pane ${pane} (${cwd}); processes there: ${JSON.stringify(seen)}; screen: ${screen}`); }
  const st = readFileSync(`/proc/${pid}/stat`, 'utf8'); const start = st.slice(st.lastIndexOf(')') + 2).split(' ')[19];
  writeFileSync(`${E}/home/.claude/sessions/${pid}.json`, JSON.stringify({ pid, sessionId: '00000000-0000-4000-8000-' + String(pid).padStart(12, '0'), cwd, name: 'qa-agent', procStart: start, status: 'idle', kind: 'interactive', statusUpdatedAt: Date.now(), startedAt: Date.now() }));
  return { pid, pane };
}

// An agent's worktree the panel has not measured yet, by an explicit barrier: every git the panel runs stalls
// (fixture/git sleeps while $E/git-stall exists; the pane's own git facts are read from files, no git), then a new
// detached worktree of the scratch repo gets a stand-in agent while a Terminals stream is open. Returns the tree
// the panel reports for it (null: the precondition is unmet) and release(), which lifts the barrier and closes the stream.
export async function unmeasuredTree(name, ms = 60000, s = S, mainAgents = 0) { // mainAgents: stand-ins in the repo first, so it is the team's main (a feature with no 'Main worktree:')
  const dir = `${E}/home/work/${name}`;
  writeFileSync(`${E}/git-stall`, '20\n');
  const close = await openTermStream();
  const release = () => { rmSync(`${E}/git-stall`, { force: true }); close(); };
  try {
    execFileSync('/usr/bin/git', ['-C', F.repo, 'worktree', 'add', '-q', '--detach', dir, 'HEAD']);
    for (let i = 0; i < mainAgents; i++) await standInAgent(s, F.repo);
    await standInAgent(s, dir);
    const tree = await until(async () => ((await api('GET', R.feature(s))).body.trees || []).find((w) => w.root === dir), ms, 2000);
    return { dir, tree: tree || null, release };
  } catch (e) { release(); throw e; }
}
