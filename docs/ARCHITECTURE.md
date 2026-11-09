# Architecture

This is how the control panel is put together: one Go process, two loopback listeners, an embedded web UI,
and a lot of small readers of things that already live on the machine (`/proc`, zellij, Claude Code's
transcripts, git, `gh`). Every claim below points at the file that does it.

- [Process and listeners](#process-and-listeners)
- [Request path and authentication](#request-path-and-authentication)
- [Live data: the sampler and server-sent events](#live-data-the-sampler-and-server-sent-events)
- [Websockets](#websockets)
- [zellij integration](#zellij-integration)
- [Agents' status and transcripts](#agents-status-and-transcripts)
- [The agent API (port 7778)](#the-agent-api-port-7778)
- [The assistant and the Hermes proxy](#the-assistant-and-the-hermes-proxy)
- [Embedding the web UI](#embedding-the-web-ui)
- [State on disk](#state-on-disk)
- [Background work and its cost](#background-work-and-its-cost)
- [Concurrency and safety habits](#concurrency-and-safety-habits)

## Process and listeners

`main.go` parses flags, then starts everything in one process:

```
your browser ──HTTPS──▶ Cloudflare Access ──▶ Cloudflare edge ◀── cloudflared on the VM (dials out)
                                                                        │ http://127.0.0.1:7777
                                                                        ▼
┌ control-panel (one Go binary) ─────────────────────────────────────────────────────────────────┐
│ 127.0.0.1:7777  web listener     s.handler()       Access JWT + PIN session                    │
│ 127.0.0.1:7778  agent listener   s.agentHandler()  bearer token  (-agent-addr)                 │
│                                                                                                │
│ sampler loops: /proc, zellij CLI, ~/.claude, runner logs, gh  ──▶  SSE to pages                │
│ browser.go: pipe ──▶ the owner's Chromium    browseragents.go: websocket ──▶ agents' Chromiums │
│ vnc.go: unix sockets ──▶ Xtigervnc (optional)                                                  │
│ assistant.go: HTTP ──▶ 127.0.0.1:8642 (Hermes API)    /hermes/ ──▶ 127.0.0.1:9119 (dashboard)  │
└────────────────────────────────────────────────────────────────────────────────────────────────┘
        ▲  127.0.0.1:7778, Authorization: Bearer <token>
        │
   vm-agent: MCP server mcp/vm_mcp.py, bin/jarvis, bin/feature-doc, hermes/scripts/fleet_pings.py
```

| Port | Who listens | Flag | Notes |
|---|---|---|---|
| 7777 | the panel's web UI and `/api/*` | `-addr` | `validateConfig` (server.go) refuses anything that is not a loopback IP. Reached from outside only through the tunnel. |
| 7778 | the agent API (`/agent/*`) | `-agent-addr` | Loopback only (`validAgentAddr`, agent.go). An empty value turns it off. |
| 8642 | Hermes' API server (not ours) | `-ai-url` | Where the J.A.R.V.I.S view sends chat turns. Started by the vm-agent repo. |
| 9119 | Hermes' dashboard (not ours) | none | Hard-coded in `server.go` (`hermesProxy("http://127.0.0.1:9119")`). Shown inside the panel at `/hermes/`. |
| random | Chromium's debugging port (agents' browsers, DevTools helper), the static-site server for "Run", Xvnc unix sockets under `/tmp/cpvnc*` | none | All on `127.0.0.1` or unix sockets. |

Background goroutines started in `main` (all in the same process):
`historyLoop` (one hour of cheap totals), `sampleLoop` (the live feed, see below), `tokensLoop` (token
use from transcripts), `chaptersLoop` (chat summaries, background model), `scoutLoop` (stalled-session
scout), `limits.run` (the balancer), and the browser's agent watcher (`installAgentWatch` then `agentsScan`).

The only subcommand is `control-panel set-pin`: it asks for a 6 to 32 digit PIN twice without echo and
writes its PBKDF2 hash to the PIN file. It takes effect at once; no restart.

## Request path and authentication

`server.handler()` builds one `http.ServeMux` and wraps it:

```
securityHeaders ─▶ http.NewCrossOriginProtection ─▶ requireAccess ─▶ mux ─▶ withSession(handler)
```

1. **`securityHeaders`** sets a strict Content-Security-Policy (`default-src 'self'`, no framing),
   `nosniff`, `no-referrer`. Three paths relax it on purpose: Chromium's DevTools files
   (`/api/browser/devtools/f/`, framed by the Browser view), the split-view embed (`/?embed=`) and the proxied
   Hermes dashboard under `/hermes/` (still unframeable).
2. **Cross-origin protection** is the standard library's `CrossOriginProtection`: unsafe-method requests
   from another origin are rejected.
3. **`requireAccess`** (server.go) is the Cloudflare Access gate. Unless the panel was started with
   `-insecure-no-access`, every request must carry a valid `Cf-Access-Jwt-Assertion` header. Failures get
   a bare 403 and an `access-reject` audit line (at most one a minute, so a local spammer cannot flood
   the log). With `-insecure-no-access` the identity is the fixed string `local`.
4. **`withSession`** is the second factor: the request needs a valid `cp_session` cookie, which is issued
   by `POST /api/login` after a correct PIN. Only `/`, static assets and `/api/login` skip it.

### Cloudflare Access JWT (auth.go, `access.verify`)

The panel verifies the token itself; it does not trust the tunnel. The checks, in order:

- three dot-separated parts, header `alg` is exactly `RS256`;
- RSA PKCS#1 v1.5 / SHA-256 signature, against the key whose `kid` the header names. Keys come from
  `https://<team>.cloudflareaccess.com/cdn-cgi/access/certs` (`-cf-team`), cached for one hour. An
  unknown `kid` or a failed fetch triggers at most one fetch a minute, and a failing fetch keeps serving
  the cached keys;
- `aud` (string or list) contains `-cf-aud`;
- `iss` equals `https://<team>.cloudflareaccess.com`;
- `exp` is set and not past (60 s of clock skew allowed); `nbf`, if set, is not in the future;
- the token has an `email`, which becomes the request's identity.

Because the panel needs the certificate fetch to work, the process must reach `<team>.cloudflareaccess.com`
directly; the run scripts unset `HTTPS_PROXY` and friends for that reason. `-cf-team` must match
`^[a-z0-9][a-z0-9-]{0,62}$` and `-cf-aud` must be non-empty, or the panel refuses to start
(`validateConfig`).

### PIN, lockout, sessions (auth.go)

- The PIN is 6 to 32 digits, stored as `pbkdf2-sha256$600000$<salt>$<key>` (mode 0600). Verification is
  constant time.
- Login and any other PIN check share one global counter: 5 wrong PINs lock logins for 15 minutes
  (`maxFails`, `lockoutFor`). It is global on purpose: per-IP counters would let an attacker rotate
  addresses. Failures and lockouts are in the audit log, and the 24-hour failure history survives restarts.
- A session is 32 random bytes in an `HttpOnly; Secure; SameSite=Strict` cookie, valid for 12 hours of
  idleness and **bound to the Access identity (email) that logged in**. `sessions.json` beside the PIN file
  stores only SHA-256 hashes of tokens, so a panel restart does not ask for the PIN again and the file
  cannot be used to log in.
- Live connections (the SSE streams, the Browser and VNC websockets) re-check the session on their keep-alive
  tick and close when it is gone (Lock, expiry). `peekSession` checks without extending the idle time: an
  open stream is not user activity.
- Audit log: append-only JSON lines (`-audit-file`): logins, PIN failures, lockouts, Access rejects,
  kills, runner restarts, file operations, terminal typing (once a minute per pane), creates and so on.
  The client address is `Cf-Connecting-Ip` when present, else the socket address; on the agent listener it
  is the literal `agent`.

### Local use without Cloudflare

`-insecure-no-access` skips only the Access gate. The PIN login, sessions, CSP and cross-origin checks stay.
`validateConfig` still insists on a loopback `-addr`, and `main` logs a warning at start. See
[HOSTING.md](HOSTING.md#running-without-cloudflare).

## Live data: the sampler and server-sent events

Pages do not poll. A page opens `GET /api/stream?view=<res|ci|term|ai|files|browser>` (server-sent events,
gzip when accepted) and gets a frame per tick.

- `sampleLoop` (server.go) ticks every 2 s while any stream is open and every 10 s with none, so
  notifications still catch things that happen while nobody is looking. A new subscriber wakes the loop
  so its first frame is immediate.
- One `tick` reads `/proc` (`sys.go`: CPU, memory, pressure, disks, network, process groups), the
  zellij sessions and panes (`terminals.go`), the CI runners (`ci.go`, every 5 s) and the sockets (every
  10 s, only while a page is open), then runs the bookkeeping (notifications, the feature supervisor,
  handing news to thread leads) and sends each client the data for **its** view plus a small `badges`
  object for the sidebar.
- Only the `res`, `ci` and `term` views carry data in the frame. `ai`, `files` and `browser` get badges
  only and fetch their own data from their own routes.
- A client that cannot keep up skips frames (`client.send` drops when its buffer is full) and gets the
  next state instead of a backlog.
- Per-pane streams are separate SSE routes: `GET /api/pane/stream?session=&pane=&mode=screen|chat` and
  `GET /api/ai/stream` (the assistant chat).

The agent API can ask the sampler for one tick of a view on demand (`look` in agent.go), which is what
`GET /agent/snapshot` does.

## Websockets

The standard library has no websocket, and the project takes no dependencies, so `ws.go` implements RFC 6455
(version 13 only, no extensions, no subprotocols): the server side for the panel's pages and a client for the
agents' Chromium debugging sockets. `wsUpgrade` accepts a handshake only from the same origin, because a
socket is not covered by the cross-origin check and the cookie alone is not enough.

| Route | File | What goes over it |
|---|---|---|
| `GET /api/browser/ws` | browser.go | JSON both ways; JPEG/WebP frames and patches down as binary; mouse, keys, touch up |
| `GET /api/browser/devtools/ws/{tab}` | browserdevtools.go | the DevTools frontend's protocol, relayed to the tab |
| `GET /api/browser/vnc` | browservnc.go, vnc.go | RFB (VNC) bytes between noVNC in the page and the Xvnc display of the owner's Chromium |
| `GET /api/pane/vnc` | vnc.go | RFB for an xterm that runs a zellij client on a pane |

Flow control is by acknowledgement: the browser view has at most two frames in flight, and Chromium's
screencast is acknowledged only when some viewer has room. Anything that waits on Chromium runs on one
ordered worker per viewer, off the read loop, so input is never stuck behind a slow page.

## zellij integration

zellij is driven only through its CLI (`zellij.go`), always as argv, never through a shell.

- **Binary:** `zellij` from `PATH`, else `~/.local/bin/zellij`.
- **Environment:** every child gets `cleanEnv`, which drops `ZELLIJ*`, `CLAUDE*` and `AI_AGENT`. The panel
  may itself have been started from inside an agent's pane, and a nested `claude` or zellij must not
  think it is a child of that one.
- **Discovering sessions:** from `/proc`, not from `zellij list-sessions`. A session is live when a
  process named `zellij` with a parent of 1 owns a socket under
  `$TMPDIR` (else `/tmp`)`/zellij-<uid>/contract_version_*` (`defaultSockDir`, `zellijSessions` in sys.go).
  If zellij keeps its sockets somewhere else (a different `TMPDIR`, say), the panel sees no sessions.
- **Structure:** `zellij --session S action list-panes --json --all` gives tabs and panes (ids, titles,
  sizes, geometry, exit status). It is called at most about every 3 s per session, and only while a
  Terminals page is open, because every CLI call makes the zellij server re-broadcast its state to its
  plugins.
- **Reading a pane:** `dump-screen --pane-id terminal_N` (optionally `--ansi`) for the plain screen, and
  `zellij --session S subscribe --pane-id terminal_N` for the live view in the drawer. That subscribe
  process exists only while a drawer is open.
- **Typing into a pane:** `paste`, `write-chars`, `write` (raw bytes) and `send-keys`. Everything the page
  or an agent types is audited.
- **Creating and closing:** `attach --create-background`, `new-tab`, `new-pane`, `close-pane`,
  `close-tab-by-id` and `delete-session --force`. A new Claude or Codex pane gets the command typed into its shell: the value of `CP_CLAUDE_CMD`
  (default `claude`; `claudeCmd` in panes.go) for "Claude" and `codex` for "Codex" (`termNew` in panes.go;
  a resume types `$CP_CLAUDE_CMD --resume <session id>`). Those commands must resolve in the pane's shell.
- **Headless attach:** zellij 0.45 only creates tabs while some client is attached, so `attachHeadless`
  runs `zellij attach` inside a pty made with util-linux `script`, at the session's own size, for the
  few moments that is needed. `fit.go` uses the same trick to size a tab to the room the page has.
- **Terminal as VNC:** `GET /api/pane/vnc` runs an `xterm` with a zellij client on a private Xvnc display
  and bridges it (vnc.go). Optional; without Xtigervnc the page falls back to the screen stream.
- **Status plugin:** `-status-dir` (default `/tmp/claude-tab-status`) is read for `<session>.json` files
  written by the `claude-tab-status` zellij plugin. It is a fallback signal (Codex status, and a Claude with
  no session file yet). The panel works without it; see below.

## Agents' status and transcripts

The status of a Claude pane is read from Claude Code's own files, not from screen scraping:

- `~/.claude/sessions/<pid>.json` (`claude.go`): `status` busy/idle/waiting, `waitingFor`, session id. It is
  trusted only if the pid is alive and its start time matches, so a stale file cannot mislead. The pid to pane
  mapping comes from the sampler (the process whose parent is the zellij server owns the pane; its descendants
  belong to it).
- `~/.claude/projects/<slug>/<sessionId>.jsonl` (`transcript.go`): the conversation. The pane drawer's Chat
  tab is built server-side from it (user, text, tool calls with results, notes), the last ~200 items first and
  "Load earlier" by byte offset. Subagent transcripts are read from `<sessionId>/subagents/agent-*.jsonl`.
- `~/.cache/claude-statusline/<session>.json` (optional): if a Claude Code status-line script saves its
  input there, the panel shows Claude Code's own cost and context-window numbers (`ccStatus`, claude.go);
  otherwise it infers the window from the model.
- Codex (`codex.go`): the rollout file `~/.codex/sessions/.../rollout-*-<id>.jsonl` is copied, entry by entry
  as it grows, into a file in Claude Code's transcript format, so the chat, the peek and the agent API read it
  like any other. Codex status comes from the status plugin's word and from the rollout.
- Pane states (`terminals.go`): `asking`, `waiting`, `working`, `stalled` (busy for 15 minutes with no
  transcript write, no tty output and under 2% CPU), `idle`, `running`, `stopped`, `stuck`, `exited`.
- A TUI that blocks on a question (permission prompts, option lists) is parsed from the screen
  (`parsePrompt`, zellij.go) so the page and the agent API can answer by choosing an option.

## The agent API (port 7778)

`agent.go` serves a second `http.ServeMux` on `-agent-addr`. It reuses the web handlers where it can, so a
write made through it is audited exactly like the page's, with the client shown as `agent`.

- **Auth:** `Authorization: Bearer <token>`, compared in constant time. The token is read from
  `-agent-token-file` (default `~/.config/control-panel/agent-token`); if the file is missing or shorter than
  32 characters the panel creates it (32 random bytes, hex, mode 0600) at start. No Cloudflare Access, no
  cookie: the guard is that only local processes can reach the loopback port and only readers of the token file
  have the token.
- **Routes** (all under `/agent/`):

| Route | Method | Purpose |
|---|---|---|
| `snapshot?view=` | GET | one sampler tick of a view (`res`, `ci`, `term`) |
| `pane/screen?session=&pane=` | GET | the pane's screen and, if a TUI is blocked on a question, the parsed prompt |
| `pane/chat?session=&pane=&n=` | GET | the last items of the Claude transcript in a pane (default 30, at most 300) |
| `pane/files` | GET | files that pane's session wrote or handed over |
| `pane/input` | POST | type text, press keys, or choose a prompt option |
| `pane/codexmodel` | POST | switch a Codex pane's model |
| `term/new`, `term/close` | POST | open or close a session, tab or pane |
| `notes` | GET | the panel's notifications, newest first |
| `threads` | GET | every thread and its state |
| `thread/open`, `thread/send`, `thread/update` | POST | open a thread, message its lead, let the lead keep its record |
| `scout` | POST | run the scout now |
| `feature`, `feature/team`, `feature/events` | GET | a feature's record, its team, its events |
| `feature/doc`, `feature/log`, `feature/assign` | POST | write a record file against the revision read, append to `LOG.md`, record who works on which step |

- **Contract with vm-agent.** The consumers are in the sibling `vm-agent` repository: `mcp/vm_mcp.py`
  (the MCP server Hermes loads), `bin/jarvis`, `bin/feature-doc` and `hermes/scripts/fleet_pings.py`. The
  contract tests are `vm-agent/mcp/contract/*_test.go`, run by `vm-agent/mcp/contract/run.sh <control-panel
  checkout>`: the script copies this repository to a temp directory, drops the test files in, and runs them
  against the real handlers with a fake zellij and a fake Hermes. Any change to a route, field or status in
  `agent.go` (or in the feature handlers it shares) has to keep that suite green.
- **Owner-only decisions stay off this API.** Approving a check, attesting a step, answering an owner
  question, releasing a hold and accepting a feature's outcomes are page routes behind the login
  (`/api/term/feature/*`), never `/agent/*`.

## The assistant and the Hermes proxy

`assistant.go` is the server side of the J.A.R.V.I.S view. It does not run a model; it talks to a separate
Hermes agent (the vm-agent repository sets one up):

- chat turns go to Hermes' API server at `-ai-url`, authenticated with the key in `-ai-key-file`
  (written by vm-agent's `setup.sh`);
- every channel's conversation (Telegram DMs, the panel's own `panel` session, agent messages) is merged
  into one chat, each message labelled with its channel; what the page sends goes to the panel session;
- a thread is a separate Hermes session `thread-<id>` (`threads.go`); the panel sends the lead playbook
  (`~/.hermes/LEAD.md`) with every thread turn;
- the page's "Connections" and "control room" panels use Hermes' own dashboard through a reverse proxy
  (`hermesProxy` in assistant.go): `/hermes/` plus the dashboard's absolute asset paths (`/assets/`,
  `/fonts/`, ...) go to `127.0.0.1:9119`, behind the same login. The proxy strips the panel's own
  credentials (`Cf-Access-Jwt-Assertion`, the `cp_session` and `CF_Authorization` cookies, `Origin`,
  `Referer`) before forwarding and adds the dashboard's session token itself.
- with no Hermes running, the view says the assistant is not set up (no key file) or answers 502 for the
  dashboard; the rest of the panel is unaffected.

## Embedding the web UI

The whole UI is the `web/` directory, compiled into the binary:

```go
//go:embed web
var webFS embed.FS
```

At start `loadAssets` (server.go) walks it once and, for every file, keeps the bytes, a MIME type, a
short content hash and a pre-gzipped copy. It rewrites `index.html` so that each asset name it mentions
becomes `name?v=<hash>`, and sets the page version to a hash of that rewritten file. Assets requested with
the right `?v=` get `Cache-Control: private, max-age=31536000, immutable`; `index.html` is `no-cache` with
an `ETag`, so a launch revalidates it with a 304. Consequences:

- **Editing anything under `web/` needs a rebuild** (`go build`) before the running panel serves it. The run
  scripts rebuild on every start.
- New files in `web/` are picked up automatically (the walk covers the tree), but they only get a cache-busted
  URL if `index.html` mentions their exact quoted name, or a script loads them through a
  `<meta name="lib-...">` tag.
- `web/novnc/` is served by the same handler under `/novnc/`.
- `stt_worker.py` is embedded separately (`//go:embed stt_worker.py`) and written to the state directory when
  dictation first runs.
- The UI is plain JavaScript with no build step and no framework. `app.js` owns the sidebar, the live view,
  the pane drawer and the Resources and CI pages; `terms.js`, `files.js`, `browser.js`, `assistant.js` and
  `featpage.js` are the other pages. Large libraries load the first time a page needs them
  (`<meta name="lib-...">` in `index.html`).

## State on disk

Everything the panel keeps is under the user's home directory. Nothing is stored in the repository.

| Path | What |
|---|---|
| `~/.config/control-panel/pin` | PBKDF2 hash of the PIN (`-pin-file`) |
| `~/.config/control-panel/sessions.json` | hashes of live login sessions |
| `~/.config/control-panel/agent-token` | bearer token for the agent API (`-agent-token-file`) |
| `~/.config/control-panel/limits.json` | the balancer's rules |
| `~/.local/state/control-panel/` | the directory of `-audit-file`: `audit.jsonl`, `notes.json`, `tokens.json`, `du.json`, `assistant.json`, `threads.json`, `chapters.json`, `digests.json`, `scout.json`, `featsup.json`, `compact.json`, `client-errors.jsonl`, `files-timings.jsonl`, `uploads/` |
| `~/.local/state/control-panel/browser/` | the owner's Chromium profile, tabs, DevTools files, logs |
| `~/.local/state/control-panel/agent-browsers/` | agents' browser registry, timelines and recordings (trails) |
| `~/.local/share/control-panel/browser-bin` | the mirror of Chromium builds used by the agent-browser wrapper |
| `~/.cache/control-panel/thumbs` | Files thumbnails (30 days unused) |
| `~/jarvis-files/features/<session>/` | a feature's record: `FEATURE.md`, `PLAN.md`, `TESTS.md`, `LOG.md`, `report.json`. A thread's manager may keep the same files in `~/jarvis-files/threads/<id>/`; the feature's own folder wins, and if both hold documents, writes and completion are refused until one is removed |
| `~/jarvis-files/uploads` | files attached to a J.A.R.V.I.S chat message |
| `run/` in the repository (git-ignored) | pid files and logs written by `ensure-panel.sh` |

All state files are written atomically (`writeFileAtomic`: temp file, rename), mode 0600 where they hold
anything private.

## Background work and its cost

Most loops only read the machine. Three things start model runs through the `claude` CLI (`claude -p`),
so they spend tokens on the account that CLI is logged in to:

- `chapters.go` writes one line per stretch of each J.A.R.V.I.S chat (haiku), `digest.go` rewrites agents' messages
  readably (haiku). Both run from `chaptersLoop`, a minute after start and then every 4 minutes, and each pass
  handles the stretches and messages that have no summary yet.
- `featreport.go` writes a feature's report (sonnet), and only when the owner asks for it.

Setting `CP_NO_BACKGROUND_MODELS=1` in the panel's environment turns the first two off (`chaptersLoop` logs
that and returns). Other switches: `CP_NO_AGENT_WATCH` (do not install the agent-browser wrapper),
`CP_AWS_REPOS` (repositories the CI page asks GitHub about), `CP_FEATURE_QUIET` (a Go duration; shortens the
supervisor's "team has gone quiet" time, for scratch instances), `BR_DEBUG` (browser protocol logging) and
`BR_NOCAP` (a debugging switch that turns off the sharp captures `browserpatch.go` takes).

The context compactor (`compact.go`) is on by default: when a Claude's context passes 60% it types `/compact`
into the pane once the turn is over. `-compact-sessions` limits it to sessions whose name matches a regexp, and
the page can switch it off or change the limits (`POST /api/compact`).

## Concurrency and safety habits

- Each source fails independently: a data source that errors shows `unavailable` in its card; the others keep
  working.
- Every action takes argv, never a shell string (`actions.go`): `kill` (SIGTERM, then SIGKILL after 5 s if
  it is still the same process) refuses PID 1, other users' processes, the panel, `cloudflared`, `zellij`
  and Cube's own processes; `restart-runner` accepts only names that match `^(r|fe)[0-9]+$` and that exist.
- Files operations are confined to `~` and `/tmp`, and refuse paths the panel itself needs (SSH keys, its own
  folders, Claude's credentials, CI runners). Delete has no bin.
- The balancer (`limits.go`) can only do what an unprivileged user can: CPU affinity, nice, idle IO priority,
  SIGSTOP/SIGCONT. It never touches the panel, `cloudflared` or another user's processes, and it will not
  pause the processes `killGuard` protects (zellij, Cube's own).
- Expensive reads are cached with a reason: transcripts are tailed from the last size, `du` runs niced and
  rarely, the file-name index is rebuilt every 10 minutes, GitHub lookups (`gh`) are cached for 30 s to 10 min.
