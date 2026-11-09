# Control Panel

A web panel for a development VM, built to be driven from a phone as well as a desktop. It is **one Go
binary** with the whole web UI embedded in it, no dependencies beyond the standard library, and it listens on
loopback only. Put a Cloudflare Tunnel and Cloudflare Access in front of it and you have a private control
room for the box: your terminals, your coding agents, a browser that runs on the VM, your files, and an AI
assistant that watches over all of it.

## What you get

- **Terminals** — every [zellij](https://zellij.dev) session, tab and pane, each with a truthful state
  (working, asking you something, finished, stalled, running a server). Open a pane to see its live screen, type
  into it, answer a permission prompt with one tap, or put several agents' chats side by side.
- **Claude and Codex agent panes** — for each agent: the chat built from its transcript, subagents and
  background shells, context-window fill, cost, the files it wrote, the pull requests it opened and where they
  are deployed, and automatic context compaction.
- **Browser** — a Chromium that runs on the VM and streams to your page, so `localhost` is the VM's. Real
  DevTools docked beside it, annotate a page and send it to an agent, a list of the web servers running on the
  box with one-click **Run** for your projects, and (on a Cube image) agents' own browsers watched live and
  recorded as replayable **trails**.
- **Files** — a file manager for `~` and `/tmp` with viewers and editors for code, Markdown, HTML, CSV and Excel
  (edits patch only the changed cells), PDF, Word and PowerPoint, thumbnails, and a search over every file name
  on the machine as you type.
- **Resources and CI** — CPU, memory, disks, network, process groups with a *balancer* (cores, priority, pause),
  and self-hosted GitHub Actions runners at a glance.
- **Notifications** — an agent finished or is stuck, a CI job failed, the disk is filling.
- **Features and threads** — treat a zellij session as a *feature*: an owner-written record, a plan of
  milestones whose steps count as done only with proof, a supervisor that nudges a quiet team, code delivery from
  worktree to production.
- **J.A.R.V.I.S** — a chat view for an AI assistant (one conversation across the panel and Telegram, tables,
  diagrams, charts, cards) that can run the fleet for you. The assistant itself is the companion repository
  `vm-agent` (a [Hermes](https://github.com/NousResearch/hermes-agent) agent); this panel is its
  front end and gives it a loopback API to see and drive the terminals.

See [docs/FEATURES.md](docs/FEATURES.md) for each feature in turn.

## How it fits together

```
phone / laptop ──HTTPS──▶ Cloudflare Access ──▶ cloudflared (dials out; no inbound port)
                                                       │
                                                       ▼  http://127.0.0.1:7777
┌ control-panel (this repo, one binary) ─────────────────────────────────────────────────────┐
│ web UI (embedded)  /api/*  SSE + websockets    checks the Access JWT, then the PIN         │
│ 127.0.0.1:7777  web listener                                                               │
│ 127.0.0.1:7778  agent API (bearer token)    <-- called by vm-agent on the same machine     │
│                                                                                            │
│ reads:  /proc, zellij CLI, ~/.claude, ~/.codex, gh, git, runner logs                       │
│ drives: zellij panes, Chromium (its own, over a pipe), Xtigervnc (optional)                │
│ asks:   Hermes API 127.0.0.1:8642;  proxies its dashboard 127.0.0.1:9119 at /hermes/       │
└────────────────────────────────────────────────────────────────────────────────────────────┘
                                                       ▲
        vm-agent: Hermes + MCP server (vm_mcp.py), bin/jarvis ...  (separate repo, optional)
```

| Port | What | Reachable from |
|---|---|---|
| 7777 | the web UI and `/api/*` (`-addr`) | loopback; the tunnel forwards to it |
| 7778 | the agent API, `/agent/*` (`-agent-addr`) | loopback; bearer token in `~/.config/control-panel/agent-token` |
| 8642 | Hermes' API server (`-ai-url`) | loopback; belongs to vm-agent |
| 9119 | Hermes' dashboard, shown at `/hermes/` | loopback; belongs to vm-agent |

Details: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Quick start

You need a Linux machine (the panel reads `/proc`), a normal user account, and:

- **Go** — the version in [`go.mod`](go.mod) (currently 1.27.1);
- **zellij** 0.45 (the version it was built and tested with) on `PATH` (or in `~/.local/bin`);
- for hosting: **cloudflared** and a Cloudflare account;
- for agents: **Claude Code** (`claude`) and/or **Codex** (`codex`);
- for the Browser page: a **Chromium** (Playwright's, or `chromium` on `PATH`);
- optional: `gh`, `rg`, ImageMagick, ffmpeg + faster-whisper (dictation), TigerVNC. The
  [tool matrix in HOSTING.md](docs/HOSTING.md#before-you-start) says what each one enables.

Try it on the machine itself first, with no Cloudflare:

```sh
git clone https://github.com/roomss-ltd/control-panel ~/projects/control-panel
cd ~/projects/control-panel
go build -o control-panel .
./control-panel set-pin                  # choose a 6 to 32 digit PIN (stored hashed)
./control-panel -insecure-no-access      # serves http://127.0.0.1:7777 and the agent API on 127.0.0.1:7778
```

Open `http://127.0.0.1:7777` (or forward it: `ssh -L 7777:127.0.0.1:7777 you@the-vm`) and enter the PIN.
`-insecure-no-access` skips only the Cloudflare check; it is for local testing and still refuses to listen on a
non-loopback address.

Before you leave it running, read these four things:

1. **Background model runs cost tokens.** With J.A.R.V.I.S chats and threads in use, the panel runs short
   `claude -p` jobs in the background (chat summaries). Set `CP_NO_BACKGROUND_MODELS=1` in the panel's
   environment to turn them off.
2. **Context compaction is on by default.** A Claude past 60% of its context window gets `/compact` typed into
   its pane once its turn ends. Start with `-compact-sessions '<regexp>'` to restrict it, or switch it off in
   the page.
3. **"New session → Claude" types the value of `CP_CLAUDE_CMD`** (default `claude`) into the new shell, and
   "Codex" types `codex`. To run agents without permission prompts (the panel already surfaces on-screen
   questions), put `export CP_CLAUDE_CMD="claude --dangerously-skip-permissions"` in `panel.env`; without it,
   Claude asks for permissions as normal. See [HOSTING.md](docs/HOSTING.md#3-the-agent-launchers).
4. **The agent half of the Browser is Cube-specific.** Watching and recording agents' browsers needs the
   `cube-browser` tool that the Cube VM image (the platform this panel was developed on) ships. On any other machine your own Chromium, DevTools and the localhost list
   still work; the agent timeline and trails stay empty.

## Host it behind Cloudflare

The full walkthrough, with the reasoning, is [docs/HOSTING.md](docs/HOSTING.md). In short:

1. **Tunnel**: `cloudflared tunnel login`, `cloudflared tunnel create control-panel`,
   `cloudflared tunnel route dns control-panel panel.example.com`, then save the tunnel token in
   `~/.cloudflared/control-panel.token` (mode 600) with `cloudflared tunnel token control-panel`.
2. **Access**: in the Zero Trust dashboard add a self-hosted application for `panel.example.com` with an Allow
   policy for your email; note your **team name** and the application's **AUD tag**.
3. **Configure**: `cp panel.env.example panel.env` and set `CF_TEAM` and `CF_AUD`.
4. **Run**: `./run-panel.sh` and `./run-tunnel.sh` (one terminal or zellij tab each; each rebuilds or restarts
   its program when it exits), or let `ensure-panel.sh` start both in the background.
5. **Start on login**: add `"$HOME/projects/control-panel/ensure-panel.sh"` to `~/.zshrc` (or `~/.bashrc`). It is
   idempotent: it starts only what is not already running. `./ensure-panel.sh restart` rebuilds and restarts
   the panel after an update.

The panel verifies Cloudflare's signed token on every request itself (RS256, audience, issuer, expiry), so a
misconfigured tunnel fails closed with a 403, and the PIN is a second factor behind it.

## Configuration

`control-panel [flags]` and `control-panel set-pin [flags]`. Defaults are for a user whose home is `~`.

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `127.0.0.1:7777` | Web listener. Must be a loopback address. |
| `-cf-team` | none | Cloudflare Zero Trust team name (`<team>.cloudflareaccess.com`). Required unless `-insecure-no-access`. |
| `-cf-aud` | none | AUD tag of the Access application. Required unless `-insecure-no-access`. |
| `-insecure-no-access` | off | Skip the Cloudflare Access check (local testing only). |
| `-pin-file` | `~/.config/control-panel/pin` | Hash written by `set-pin`. Sessions are kept in `sessions.json` beside it. |
| `-audit-file` | `~/.local/state/control-panel/audit.jsonl` | Audit log; the panel's other state files live in the same directory. |
| `-agent-addr` | `127.0.0.1:7778` | Agent API listener (loopback only); empty turns it off. |
| `-agent-token-file` | `~/.config/control-panel/agent-token` | Bearer token for the agent API; created (mode 0600) if missing. |
| `-ai-url` | `http://127.0.0.1:8642` | Hermes API server of the assistant. |
| `-ai-key-file` | `~/.config/vm-agent/api-key` | Its API key, written by vm-agent's `setup.sh`. |
| `-hermes-env` | `~/.hermes/.env` | Hermes' env file, where the Connections form saves Telegram settings. |
| `-ci-dir` | `~/projects/ci-cd-jobs` | Folder of self-hosted GitHub Actions runner directories. |
| `-status-dir` | `/tmp/claude-tab-status` | Where the optional `claude-tab-status` zellij plugin writes status files. |
| `-compact-sessions` | empty (all) | Regexp of zellij sessions whose agents the context compactor may type into. |

Environment variables the panel reads: `CP_NO_BACKGROUND_MODELS=1` (no background `claude -p` runs),
`CP_NO_AGENT_WATCH=1` (do not install the agent-browser wrapper), `CP_AWS_REPOS` (comma-separated `owner/repo`
the CI page asks GitHub about for cloud-side runners), `CP_CLAUDE_CMD` (the command typed into a new pane to
start Claude Code, and with `--resume <session id>` to resume it; default `claude`), and for debugging `BR_DEBUG`, `BR_NOCAP`,
`CP_FEATURE_QUIET`. `panel.env` is sourced by the run scripts, so `export` them there. Where state lives:
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#state-on-disk).

## Testing

```sh
go vet ./... && gofmt -l . | grep -v novnc    # nothing should print
go test .                                      # the Go suite, a few minutes
go test -run 'TestPIN|TestLockout' .          # a targeted run
node --test lib_test.js sheet_test.js          # the JavaScript helpers
```

The Go tests inject fakes for zellij, Hermes and `gh` where they need them. Four of them drive a real shared Chromium
(`TestBrowserLive`, `TestBrowserPatches`, `TestBrowserDevTools`, `TestAgentWatch`); the browser ones skip
themselves when no Chromium is installed, and `TestAgentWatch` also needs the Cube image's `cube-browser`.
[`acceptance/v8/`](acceptance) holds black-box tests of the feature pages and agent API: `acceptance/v8/run.sh
[source dir] [output dir]` builds the checkout, runs it on free ports in a scratch home with a private zellij and
stand-ins for Hermes, `claude` and `gh`, and runs the `node --test` suites against it (it needs bash, go, git,
curl, python3, zellij and Node 20+; the `ui*` suites also need Playwright, see the header of the script). The
agent-API contract with vm-agent is tested from that repository: `mcp/contract/run.sh <this checkout>`.

## Repository layout

| Path | What |
|---|---|
| `main.go`, `server.go`, `auth.go` | entry point and flags, routes and middleware, Access JWT, PIN, sessions, audit |
| `sys.go`, `actions.go`, `limits.go`, `disk.go`, `tokens.go` | `/proc` sampling, kill and runner restart, the balancer, disk usage, token use |
| `ci.go`, `ci_progress.go`, `ci_remote.go` | CI runners |
| `zellij.go`, `terminals.go`, `panes.go`, `fit.go`, `unqueue.go`, `dirs.go`, `vnc.go` | zellij and the Terminals page |
| `claude.go`, `codex.go`, `transcript.go`, `agents.go`, `artifacts.go`, `skills.go`, `compact.go`, `pr.go`, `ship.go` | agents, their transcripts and what they produce |
| `browser*.go`, `cdp.go`, `ws.go` | the Browser, DevTools, agent watching, recordings, websockets |
| `fs*.go`, `git.go` | Files |
| `agent.go`, `assistant.go`, `aisession.go`, `aicard.go`, `chapters.go`, `digest.go`, `telegram.go`, `threads.go`, `nav.go`, `scout.go`, `notes.go`, `teamdeliver.go` | the assistant, threads, notifications, the agent API |
| `feat*.go`, `feature.go` | features: records, supervisor, proof, page data |
| `stt.go`, `stt_worker.py` | dictation |
| `web/` | the UI, embedded with `go:embed`; `web/novnc/` is third-party |
| `scripts/vendor/` | rebuilds the vendored viewer libraries in `web/`; `scripts/icons.mjs` builds `web/icons.js` |
| `run-panel.sh`, `run-tunnel.sh`, `ensure-panel.sh`, `panel.env.example` | running and hosting |
| `acceptance/`, `testdata/`, `*_test.go`, `*_test.js` | tests |
| `docs/` | [ARCHITECTURE](docs/ARCHITECTURE.md), [HOSTING](docs/HOSTING.md), [FEATURES](docs/FEATURES.md) |

Contributing, or pointing a coding agent at the repo: start with [AGENTS.md](AGENTS.md).

## The assistant: vm-agent

`vm-agent` (the sibling repository, usually cloned next to this one) is the other half. It sets up Hermes with a persona, skills and an MCP server that calls
this panel's agent API as tools (see the table in [ARCHITECTURE.md](docs/ARCHITECTURE.md#the-agent-api-port-7778)),
adds the `jarvis` command agents use to report in, and the `feature` helper that gives each thread one branch and a
worktree per agent. How the main assistant and its per-feature threads fit together is in its
`docs/THREADS.md` in that repository; its own components, ports and files are in its
`docs/ARCHITECTURE.md`. The panel works without it; the J.A.R.V.I.S page then says the
assistant is not set up.

## License

MIT, see [LICENSE](LICENSE). The bundled third-party software (noVNC under MPL-2.0, and the vendored `*.min.js`
libraries and fonts under their own licences) is listed in [THIRD_PARTY.md](THIRD_PARTY.md).
