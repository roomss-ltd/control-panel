# AGENTS.md

Guide for a coding agent (or a new contributor) working **in** this repository. For what the panel is and how to
run it, read [README.md](README.md); for the design, [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md); for each
feature, [docs/FEATURES.md](docs/FEATURES.md).

## The shape of the repo

- One Go module, `control-panel` (see `go.mod` for the Go version), **a single `package main` at the repository
  root**, and no third-party Go dependencies. The web UI is plain JavaScript in `web/`, embedded with `go:embed`.
- It runs as a normal user on a Linux VM and reads that user's `/proc`, zellij sessions, `~/.claude`, files and
  git checkouts. Nothing here may assume a particular user name, host name, or home directory: use
  `os.UserHomeDir()` (or the `home` the server already carries as `s.cfg.home`) in Go and `$HOME` in shell.

## Where things live (file-name map)

Files are grouped by a name prefix; find the cluster, then the file.

| Prefix / file | Responsibility |
|---|---|
| `main.go` | flags, the `set-pin` subcommand, starting the loops and both listeners |
| `server.go` | `config`, `newServer`, **all web routes** (`handler()`), middleware, SSE, asset loading, `writeJSON`, `writeFileAtomic` |
| `auth.go` | Cloudflare Access JWT verification, PIN hash/verify, lockout, sessions, audit log |
| `agent.go` | the loopback **agent API** (`/agent/*`) and its bearer token |
| `sys.go`, `actions.go`, `limits.go`, `disk.go`, `tokens.go` | `/proc` sampling and grouping, kill / restart-runner with guards, the balancer, disk usage, token use |
| `ci.go`, `ci_progress.go`, `ci_remote.go` | self-hosted runner status |
| `zellij.go`, `terminals.go`, `panes.go`, `fit.go`, `unqueue.go`, `dirs.go`, `vnc.go` | zellij CLI wrapper and prompt parsing; the Terminals model; pane HTTP handlers; tab sizing; cancelling queued messages; start-folder list; terminal-as-VNC |
| `claude.go`, `codex.go`, `transcript.go`, `agents.go`, `artifacts.go`, `skills.go`, `compact.go`, `pr.go`, `ship.go`, `git.go` | agents as the panel sees them, and what they produce |
| `browser.go`, `browserpatch.go`, `browserports.go`, `browserdevtools.go`, `browservnc.go`, `cdp.go`, `ws.go` | the owner's Chromium, its stream, localhost list and Run, DevTools, CDP client, websockets |
| `browseragents.go`, `browsertrails.go`, `browsercalls.go` | agents' browsers: registry, the `cube-browser` wrapper, recordings, captured API calls |
| `fs.go`, `fsindex.go`, `fsthumb.go`, `fsxlsx.go` | Files: routes, the name index, thumbnails, in-place xlsx patching |
| `assistant.go`, `aisession.go`, `aicard.go`, `chapters.go`, `digest.go`, `telegram.go` | the J.A.R.V.I.S view and its Hermes proxy |
| `threads.go`, `nav.go`, `scout.go`, `notes.go`, `teamdeliver.go` | threads, the sidebar's thread list, the scout, notifications, handing team news to a lead |
| `feat*.go`, `feature.go`, `featv8*.go` | **features**: see below |
| `stt.go`, `stt_worker.py` | dictation |
| `web/` | the UI (`app.js` shell and live pages, `terms.js`, `files.js`, `browser.js`, `assistant.js`, `featpage.js`, ...); `web/novnc/` is third-party and must stay as upstream ships it |

**`feat*` and `v8*` are one thing**: the feature page and its data layer (record files, supervisor, proof,
tickets, session ledger, code delivery, the page's JSON). The `v8` in the names is historical and tests named
`v8_*_test.go` and `acceptance/v8/` belong to it too. A new route for that page registers itself through
`v8Register` (`featv8.go`; see `featv8routes_b.go` for the pattern: an `init()` that appends a function taking
`(s, mux, owner)`) instead of being added to `server.go`. Owner routes must wrap handlers with the `owner`
argument (it is `withSession`), serve what is cached, and fetch nothing on GET.

## Conventions

- **Format and vet**: `gofmt` and `go vet` must be clean. Check with
  `go build ./... && go vet ./... && gofmt -l . | grep -v novnc` (nothing should print after the build).
- **Comments**: every exported identifier and every non-trivial unexported function has a godoc-style comment that
  starts with its name; each file opens with (or has near the top) a short comment saying what the file is for.
  Comments say what and why, in plain English for a newcomer, not the history of how the code got there.
- **Tests next to the code**: `foo.go` is tested by `foo_test.go` in the same package. Use `t.TempDir()` for state
  and the fakes the tests already have (`newTestServer` in `server_test.go` with a fake zellij and a fake Access,
  `newFsEnv`, `newSupEnv`, `newDeliveryEnv`, ...). A bug fix comes with a test that fails without it.
  Do not weaken an assertion to make a test pass.
- **No new dependencies.** Go: standard library only (the websocket, the JWT check and the CDP client are written
  in-tree on purpose). JavaScript: no framework, no bundler in the run path. The libraries in `web/` are vendored
  files; `scripts/vendor/build.sh` regenerates the ones it owns, and `THIRD_PARTY.md` lists them. Add to both if
  you vendor another.
- **`web/` is embedded.** Changing anything in it needs `go build` before the running panel serves it
  (`./ensure-panel.sh restart` does that). `index.html` names each asset by exact quoted name and `loadAssets`
  rewrites those names to `name?v=<hash>`; a script that loads another file lazily does it through a
  `<meta name="lib-...">` tag in `index.html`.
- **Run external programs as argv, never through a shell**, with `cleanEnv(os.Environ())` so a panel started from
  an agent's pane does not leak `ZELLIJ*`, `CLAUDE*` or `AI_AGENT` into children. Validate every name, path and
  number that reaches a command (`sessionNameRe`, `cleanName`, `resolveDir`, `paneParams`).
- **Every write is audited**: `s.audit.add(event, detail, clientIP(r))`. State files are written with
  `writeFileAtomic` (temp file, rename) and mode 0600 when private.
- **Both listeners stay on loopback.** `validateConfig` and `validAgentAddr` enforce it; do not add a flag that
  weakens it.
- **Each data source fails alone.** A reader that errors reports it for its own card and the rest of the page
  keeps working; do not let one failing source take down a tick.
- **Gentle on the machine.** The sampler runs every 2 s with a page open and 10 s without; anything expensive is
  cached or incremental (see how `transcript.go`, `tokens.go`, `fsindex.go`, `disk.go` do it). Calling the zellij
  CLI makes the zellij server re-broadcast to its plugins, so keep those calls rare.
- **JavaScript**: `'use strict'`, a short header comment per file saying what it is for, comments on non-obvious
  functions, text inserted with `textContent` or `esc()` (never raw HTML from a file, a transcript or a model).
  Pure helpers shared with tests live in `web/lib.js`.
- **No personal data, ever.** No real names, company or project names, hostnames, e-mail addresses, IDs,
  tokens or machine paths in code, comments, tests, fixtures or docs. Use `owner`, `example-org/example-repo`,
  `panel.example.com`, and neutral fixtures.
- **Behaviour-preserving cleanups.** Do not rename exported or shared identifiers or change signatures just for
  taste; the vm-agent contract (below) and the saved JSON files depend on current names.

## Build, run, test

```sh
go build -o control-panel .                    # the binary; web/ is inside it
./control-panel set-pin                        # once: the PIN (~/.config/control-panel/pin)
./control-panel -insecure-no-access            # local run on 127.0.0.1:7777 / :7778, no Cloudflare check
```

To try a change **without touching your real panel's state**, give the copy its own home and ports:

```sh
H=$(mktemp -d); mkdir -p "$H/tmp"
HOME=$H TMPDIR=$H/tmp ./control-panel set-pin
HOME=$H TMPDIR=$H/tmp ./control-panel -addr 127.0.0.1:17777 -agent-addr 127.0.0.1:17778 -insecure-no-access -ai-url ''
```

(`TMPDIR` also gives that copy a private zellij socket directory, so it cannot see or touch your sessions.)

| Check | Command |
|---|---|
| format, vet, build | `go build ./... && go vet ./... && gofmt -l . \| grep -v novnc` |
| a targeted Go test | `go test -count=1 -run 'TestName\|TestOther' .` |
| the whole Go suite | `go test .` (a few minutes) |
| the same without the shared-browser tests | `acceptance/v8/go-test-safe.sh ALL` (or `'<regex>'`); it excludes `TestBrowserLive`, `TestBrowserPatches`, `TestBrowserDevTools`, `TestAgentWatch`, which drive one real Chromium and should not run twice at once |
| JavaScript helpers | `node --test lib_test.js sheet_test.js` (name the files; a bare `node --test` would also pick up `acceptance/`) |
| black-box feature-page suites | `acceptance/v8/run.sh [source dir] [output dir]` (private home, ports, zellij and stand-in Hermes; see the script's header) |
| vendored viewer libraries | `cd scripts/vendor && npm install && ./build.sh` |

Do not run tests that start zellij or Chromium against your live session, and do not point a test at the real
`$HOME`.

## The agent API contract with vm-agent

`agent.go` is the surface the sibling `vm-agent` repository is built on: its MCP server `mcp/vm_mcp.py`, the
`bin/jarvis` and `bin/feature-doc` helpers and `hermes/scripts/fleet_pings.py` all call `/agent/*` with a bearer
token. The contract tests live there and run against **this** repository's real handlers:

```sh
sh ../vm-agent/mcp/contract/run.sh "$PWD"      # needs go, git and uv; RACE=1 for the race detector, RUN=<regex> to select
```

It copies this checkout (uncommitted changes included) into a temp directory, adds vm-agent's `*_test.go` files,
and runs them with a fake zellij and a fake Hermes. Rules that follow:

- A change to a route's path, method, request field, response field or status code in `agent.go`, or in a feature
  handler it shares (`featdoc.go`, `featverify.go`, `threads.go`, ...) must keep that suite green; if the change is
  intended, change vm-agent in the same breath and say so.
- Decisions that belong to the owner (approving a check, attesting a step, answering a question, releasing a hold,
  accepting outcomes) are **page routes** (`/api/term/feature/*`), never `/agent/*`. Do not expose them to agents.
- Agent-side writes are revision-checked (`rev`); keep that, so two writers cannot silently overwrite each other.

## Do

- Read the file and its test before changing it; follow the pattern of its neighbours.
- Keep a change small and local; extend a cluster of files rather than inventing a new layer.
- Write the failing test first for a bug, then fix.
- Update [docs/FEATURES.md](docs/FEATURES.md) or [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) when behaviour,
  routes, flags or on-disk state change, and the flag table in [README.md](README.md) when you add a flag.
- When you hit something that needs a product decision (a change in what a route returns, a rename, a new
  dependency), stop and ask rather than guess.

## Don't

- Don't add Go modules, npm runtime dependencies, a bundler, or a framework.
- Don't bind a listener to anything but loopback, and don't add a way around the Access check other than the
  existing `-insecure-no-access`.
- Don't build shell strings from user input, or put a secret in a flag, an environment variable, a log line or the
  repository.
- Don't edit `web/novnc/` or hand-edit the minified libraries in `web/`; rebuild or replace them as a whole.
- Don't commit `panel.env`, the built `control-panel` binary, `run/`, or anything under a real home directory.
- Don't make the test suite depend on network access, a real Hermes, a real GitHub account, or paid model calls.
  The code that would run `claude -p` is switched off in scratch instances with `CP_NO_BACKGROUND_MODELS=1`.
- Don't "fix" a Cube-specific path (`/opt/cube/...`, `cube-browser`) by deleting it: it is how the agent-browser
  watcher detects the image it needs; it is inactive elsewhere by design.

## Known environment assumptions

These are in the code today. Know them before you are surprised by them:

- The value of `CP_CLAUDE_CMD` (default `claude`; `claudeCmd` in `panes.go`) and `codex` are typed into new panes;
  `$CP_CLAUDE_CMD --resume <id>` is typed to resume (`panes.go`, `featsup.go`).
- The agent-browser wrapper activates only where `/opt/cube/browser/cube-browser.mjs` and
  `/opt/cube/browser-binaries` exist (`browseragents.go`); `killGuard` (`actions.go`) also protects Cube's own
  processes.
- zellij sessions are found from sockets under `$TMPDIR` (else `/tmp`) `/zellij-<uid>/contract_version_*`
  (`sys.go`).
- The Hermes dashboard is proxied from the fixed address `127.0.0.1:9119` (`server.go`).
- Project folders are looked up under `~/repos`, `~/projects` and `~/side-quests`; a feature's record is under
  `~/jarvis-files/features/<session>/`.
