# Features

One section per part of the panel: what it does, where the code is, how it works, and what it needs. Routes
are `GET /api/...` and friends on the web listener (behind the login) unless they start with `/agent/`, which
is the loopback agent API. See [ARCHITECTURE.md](ARCHITECTURE.md) for the shared machinery and
[HOSTING.md](HOSTING.md) for the optional tools each feature uses.

"The owner" below means the one person who runs the panel and logs in to it.

The sidebar has six pages: **J.A.R.V.I.S** (`ai`), **Terminals** (`term`), **Browser** (`browser`), **Files**
(`files`), **Resources** (`res`) and **CI** (`ci`). Single-key shortcuts (plain keys, never while typing; `?`
lists them and turns them off): `g` then `a`, `t`, `b`, `f`, `r` or `c` goes to a page, `/` jumps to a terminal or
page by name, `j`/`k` step through terminals, `n` opens notifications.

- [Terminals and agent panes](#terminals-and-agent-panes)
- [Claude and Codex agents](#claude-and-codex-agents)
- [Browser](#browser)
- [Files](#files)
- [Resources and the balancer](#resources-and-the-balancer)
- [CI](#ci)
- [Notifications and the scout](#notifications-and-the-scout)
- [The J.A.R.V.I.S assistant view](#the-jarvis-assistant-view)
- [Threads](#threads)
- [Features: records, plans and proof](#features-records-plans-and-proof)
- [Context compaction](#context-compaction)
- [Dictation](#dictation)
- [Login, audit and security headers](#login-audit-and-security-headers)

## Terminals and agent panes

**What it does.** Mirrors zellij one to one: every live session, its tabs, and each tab's panes, in zellij's own
order (rows never jump around). Each pane wears a status orb and a plain-words state. Click a pane to peek at
it, click again to open its drawer: a live **screen**, a **chat** (for Claude and Codex panes) and a message
box. From the page you can create a session, tab or pane (choosing the start folder from a picker: `~`, `~/repos/*`,
`~/projects/*`, `~/side-quests/*`), rename, move and close them, upload an image into a chat, and put several chats
side by side in a **split view**.

**Where.** `terminals.go` (the model and the states), `zellij.go` (CLI calls, prompt parsing), `panes.go` (the
HTTP handlers: stream, input, create, close), `fit.go` (sizing a tab to the page), `vnc.go` (terminal as VNC),
`unqueue.go`, `dirs.go`; UI in `web/terms.js`, `web/split.js`, `web/orbs.js` and the pane drawer in `web/app.js`.

**How it works.**

- States: an agent is `asking` (blocked on a question or permission), `waiting` (finished, waiting for you),
  `working`, or `stalled` (busy for 15 minutes with no transcript write, no tty output and under 2% CPU); a
  shell is `idle` or `running` (with the foreground command), `stopped`, `stuck` (uninterruptible I/O) or
  `exited`. "Waiting for you" is asking plus finished agents; the sidebar badge shows it.
- Sources are listed in [ARCHITECTURE.md](ARCHITECTURE.md#agents-status-and-transcripts); zellij is read at most
  every 3 s per session while a Terminals page is open.
- The **screen** is `zellij subscribe` relayed latest-wins and drawn as HTML by a small SGR parser; **Fit**
  scales the font to the pane's columns. With Xtigervnc and xterm installed, a pane can instead be shown as a
  real terminal over VNC (`/api/pane/vnc`).
- The **chat** is built from the Claude transcript. A message box types into the pane: text is pasted, Enter
  sent. When a TUI is blocked on a numbered menu or a permission prompt, the panel parses it from the screen
  and shows one button per option, and the agent API can answer the same way (`choose`).
- **Cancelling a queued message** (`unqueue.go`): Claude Code has no way to drop a message queued behind a
  running turn. The panel presses Up (which pulls the queue back into the input box), reads from the transcript
  exactly what came out, clears the box and types back every other message.
- **Creating** a pane for Claude or Codex types the value of `CP_CLAUDE_CMD` (default `claude`) or `codex` into its shell
  ([HOSTING.md](HOSTING.md#3-the-agent-launchers)). Names must match `^[A-Za-z0-9][A-Za-z0-9._-]*$` for sessions
  and be at most 64 printable characters; directories must exist.
- Panes that run dev servers show the TCP ports they listen on and the count of error and warning lines in their
  newest output.
- The pane drawer also offers: the **skills** the agent can run (`skills.go`: project, user and plugin skills and
  commands), the **files** the session wrote or handed over (`artifacts.go`, from its transcript), a **PR card**
  (`pr.go`) and where a merged change is deployed (`ship.go`, from GitHub deployments), and a diff size
  (lines added and removed by its edit tools).

**Needs.** zellij. Optional: the status plugin, `gh` for PR cards, Xtigervnc and xterm for VNC.

## Claude and Codex agents

**What it does.** An agent in a pane gets the same view whichever tool it is: chat, state, context ring (how full
its window is), cost, model, subagents and background shells it started, the PRs it linked, the lines it changed.

**Where.** `claude.go` (Claude Code's session files and status-line numbers), `transcript.go` (transcripts to chat
items, subagents, "what is it doing", "is the turn over"), `codex.go` (Codex rollouts), `tokens.go` (token use
by hour from all transcripts, kept incrementally in `tokens.json`), `artifacts.go`, `agents.go` (the optional
status plugin's files), `skills.go`.

**How it works.** See [ARCHITECTURE.md](ARCHITECTURE.md#agents-status-and-transcripts). Codex has no transcript the
panel can read directly, so `codex.go` copies its rollout into a Claude-format file as it grows and everything
downstream treats it as a Claude chat. The model shown is what the latest real reply ran on, never a default;
a Codex model can be switched from the page (`POST /api/pane/codexmodel`).

**Needs.** The `claude` and/or `codex` CLIs installed and used in zellij panes; the panel only observes.
Optional: a status-line script that writes `~/.cache/claude-statusline/<session>.json` for exact cost and window.

## Browser

**What it does.** A real Chromium that runs on the VM, shown in a page, so `localhost` is the VM's. Two parts:

- **Your windows**: the panel's own Chromium (persistent profile, so logins stay) with tabs, address bar,
  dialogs, popups, file choosers, downloads (`~/Downloads`), find, device presets and network throttling, a
  Problems drawer (console, exceptions, failed requests), real **Chromium DevTools** docked beside the page,
  and **Annotate**: pins, boxes, arrows, pen and text drawn on a still of the page, sent with the page's
  elements and recent problems to an agent in a pane you pick.
- **Agents**: each agent's `cube-browser` session, watched live with its command timeline; **Take over** lets you
  drive it (the agent's next command waits until you hand back); every session is **recorded** and can be
  replayed, or two replays compared side by side.

Around it: a **start page** with the web servers listening on localhost (named by process, project folder, zellij
pane and `<title>`) and the projects you can **Run** (a `package.json` dev/start/serve/preview script, Django's
`manage.py`, or a folder with `index.html` that the panel serves itself).

**Where.** `browser.go` (the owner's Chromium, tabs, streaming, the page protocol), `browserpatch.go` (patches
instead of whole frames), `cdp.go` (Chrome DevTools Protocol client over a pipe or a websocket), `ws.go`,
`browserdevtools.go`, `browserports.go` (localhost list, Run, Stop, favicons), `browseragents.go` (agent
registry, attach, timeline, hold, the `cube-browser` wrapper), `browsertrails.go` and `browsercalls.go`
(recordings and captured API calls), `browservnc.go` and `vnc.go`; UI in `web/browser.js`, `web/browser.css`,
`web/vnc.js`, `web/novnc/`.

**How it works.**

- **Chromium.** The newest Playwright build under `/opt/cube/browser-binaries` or `~/.cache/ms-playwright`
  (`chromium-*/chrome-linux64/chrome`), else `chromium`, `chromium-browser` or `google-chrome` on `PATH`. It runs
  headless with `--remote-debugging-pipe`, so no other process can reach it, `--no-proxy-server`, and a fixed
  device scale factor of 2. Tabs are saved in `tabs.json` and reopened after a restart.
- **Streaming.** One websocket per open page (`/api/browser/ws`). Frames are JPEG while a page moves and one sharp
  capture follows when it rests; a viewer has at most two frames in flight, so a slow phone gets fewer frames,
  never a backlog. A change that touches only a small part of the page (a blinking caret) is sent as a patch
  rectangle. Input (mouse, wheel, touch, keys, IME text, paste) goes back the same socket.
- **VNC mode.** With Xtigervnc present the owner's Chromium runs headed on a private display and the page draws
  that display with noVNC; everything else (annotations, DevTools, input over CDP) is unchanged.
- **DevTools.** Chromium serves its own DevTools front-end files; the panel starts a short-lived helper Chromium
  with an empty profile to fetch them once per version, caches them under `browser/devtools/<version>/`, and
  frames them in the page. Its connection to a tab (`/api/browser/devtools/ws/{tab}`) is relayed by the panel
  through a debugging session of its own. On an agent's tab it is available only while you hold the tab.
- **Agents.** At start, if `/opt/cube/browser/cube-browser.mjs` and `/opt/cube/browser-binaries` exist, the panel
  installs `~/.local/bin/cube-browser` (never over a file that is not its own) and a mirror of the browsers in
  which `chrome` is a wrapper adding `--remote-debugging-port=0` and registering the process in
  `~/.local/state/control-panel/agent-browsers/<pid>.json`. The panel scans that folder every second and attaches
  as a second debugging client. Sessions are identified by cube-browser's own launch id. `CP_NO_AGENT_WATCH=1`
  turns the installation off; deleting the wrapper undoes it.
- **Recordings (trails).** For every agent session, whether or not anyone watches: events (commands, navigations,
  loads, console errors, failed requests), a picture after each command and load, a low-rate motion track, and
  XHR/fetch calls with their request and response bodies. Credentials never reach disk: auth, cookie and API-key
  headers and JSON or form fields named like password, secret, token, API key, pin or otp become `[redacted]`.
  Kept 7 days by last write and at most 1 GB in all (oldest first); a pinned recording stays. Only one panel
  per home directory records (a lock file); another copy only watches.
- **For agents.** The wrapper adds two sub-commands to `cube-browser`: `history [text]` (what other sessions have
  already seen) and `api-calls [text]` (the captured calls), so an agent can read an earlier response instead of
  calling the API again.
- **Run / Stop.** Commands run in a tab of a zellij session named `servers` (created if missing), so Terminals
  shows their output. Stop sends Ctrl-C to the pane when the server is its foreground job, else SIGINT.
- **Addresses.** A bare search term goes to DuckDuckGo. Only servers whose `/` answers a 2xx HTML document count
  as web pages in the localhost list.

**Needs.** Chromium for your windows. The agent half needs the Cube image's `cube-browser`; on other machines it
is inactive. Optional: Xtigervnc. Limit: it is one shared browser, so two screens on the same tab in
Fit mode each fit it to themselves and the last to resize wins.

## Files

**What it does.** A file manager for everything under `~` and `/tmp`: a tree on the left, tabs on the right (one
or two groups), a viewer per type, an editor for text, upload, rename, move, copy, delete, clone a git
repository, and a **search over every file name** on the box as you type (`/`, or Cmd/Ctrl-K from any page).
Files can be handed to a J.A.R.V.I.S chat or to an agent pane.

**Where.** `fs.go` (routes, confinement, raw serving), `fsindex.go` (the name index), `fsthumb.go` (thumbnails),
`fsxlsx.go` (patching edited cells into a workbook), `disk.go` (the Sizes view's disk-usage index and the delete
guard); UI in `web/files.js`, `web/files.css`, `web/office.js`, `web/sheet-worker.js`; libraries in
`web/` (`cm.min.js`, `pdf.*.mjs`, `xlsx.full.min.js`, `jszip.min.js`, `docx-preview.min.js`).

**How it works.**

| Kind | Viewer |
|---|---|
| text and code | CodeMirror 6, editable up to 5 MB (past that read-only; a log opens on its last 5 MB), find, minimap, Cmd/Ctrl-S |
| Markdown | preview, live split, or source |
| HTML | a sandboxed iframe (scripts allowed, never same-origin) plus source; design boards can be annotated with comments saved in `<page>.comments.json` and sent to an agent |
| CSV, TSV, xlsx, xls, ods | SheetJS in a worker; a virtual grid; a formula bar edits cells. An `.xlsx`/`.xlsm` save sends only the edited cells (`POST /api/fs/xlsx`) and the panel patches their XML in place, copying every other part of the zip byte for byte, so colours, fonts and charts survive |
| PDF | pdf.js, pages drawn as they scroll in |
| docx, pptx, doc, ppt | docx-preview; `office.js` draws slides from XML and reads the text of old binary formats |
| images, video, audio | native elements; big pictures come scaled from `/api/fs/thumb` (ImageMagick when present) |
| folders | grid with thumbnails, sortable list, or Sizes (disk usage) |

- Saves are atomic and refuse (HTTP 409) when the file changed on disk since it was read.
- Everything except images, media and PDF is served under a sandbox Content-Security-Policy; a file's text is
  never inserted into the page as HTML.
- Delete, move and rename refuse paths the panel itself depends on (SSH, its own folders, Claude's credentials,
  CI runners). There is no bin: delete is permanent.
- The **name index** keeps every path under `~` and `/tmp` in compact arenas outside the Go heap (anonymous `mmap`) and rewalks every 10 minutes
  (and soon after a change made through Files). Queries match the longest word across the arena in parallel and
  rank exact name, prefix, word start, then substring; junk folders (`node_modules`, `.git`, caches) sink but stay
  findable. There is deliberately no inotify watch: millions of files would exhaust its limit.
- Text search inside a folder is ripgrep (8 s cap). `GET /api/fs/repos` lists your GitHub repositories with `gh`
  and `POST /api/fs/clone` clones one over HTTPS with gh's token, streaming progress.
- Every file opened reports its kind, size and load time (never a path) to `files-timings.jsonl`, so what a real
  phone gets can be read on the VM.

**Needs.** Nothing beyond the panel. Optional: `rg`, `convert`, `gh`.

## Resources and the balancer

**What it does.** Live CPU (total and per core), load, memory (apps, page cache, kernel, shared, free; swap;
memory pressure; an hour of history), disks, network, and **process groups**: processes added up per app (a CI
runner, a zellij pane, a daemon), expandable, sortable, with the one-hour change in memory. You can kill a process
and limit a group.

**Where.** `sys.go` (the sampler: `/proc` parsers, grouping), `actions.go` (kill, restart-runner, guards),
`limits.go` (the balancer), `disk.go`, `tokens.go`; UI in `web/app.js` and `web/chart.umd.js`.

**How it works.** Everything is read from `/proc` and `statfs`; no root needed. Network bytes are summed over every
interface except loopback. The balancer exists because an unprivileged user has no cgroups, so there are no hard
CPU or RAM caps. What does work: the cores a group may run on (affinity, reversible), lower priority (nice 10 or
19, one-way), idle disk priority (reversible) and pause/resume (SIGSTOP/SIGCONT). Rules are keyed by process-group
key, saved in `~/.config/control-panel/limits.json`, and re-applied every 10 seconds so new processes in the group
get them too. A rule on the key `*` is the CI reserve: while a CI job runs, every group without cores of its own is kept
to the cores that rule names, so the rest are the runners'; when CI is idle everything gets them back. Kills ask SIGTERM first and SIGKILL after 5 s, only for the
same process (matched by start time); see the guards in [ARCHITECTURE.md](ARCHITECTURE.md#concurrency-and-safety-habits).
The sidebar's token and agent counters come from `tokens.go`.

**Needs.** Linux with `/proc`. The pressure and per-process IO figures need a kernel that exposes them.

## CI

**What it does.** Self-hosted GitHub Actions runners at a glance: a table of runners (busy with the PR, branch and
job, idle, or dead), job history grouped by workflow with success rates and average durations, recent jobs, and a
restart button.

**Where.** `ci.go`, `ci_progress.go` (public job and step metadata from `gh`; never raw logs), `ci_remote.go`
(runners on other hosts, from GitHub), `actions.go` (restart), `pr.go`.

**How it works.** It reads each runner folder under `-ci-dir` (names `r<N>` or `fe<N>`): `runner.pid`,
`runner.log` and the newest `_diag/Worker_*.log` for the job's GitHub context (repository, ref, workflow, PR
number). PR titles come from `gh api` in the background, cached. A second root, `~/ci-aws/runners/` (runner names
`aws-vm-*`, shown as the "AWS" fleet; the first root is "Original"), is scanned the same way. Runners on other hosts
(names `aws-ec2-*`) have no files here; their state comes from `gh api repos/<repo>/actions/runners` for the
repositories named in the `.runner` files under `~/ci-aws/runners/` or, failing that, `CP_AWS_REPOS`. If GitHub does not answer for 60 s those runners show as stale,
never idle. Restart kills the runner's process tree and runs `ensure-runners.sh` from `-ci-dir`.

**Needs.** A runner layout like the one above and, for titles and cloud runners, `gh`. With no runners the page is
empty.

## Notifications and the scout

**What it does.** A list (`n`) of what you would want to know: an agent finished, needs an answer, looks stuck or
exited; a CI job failed; disk or memory is filling; a thread or feature needs you. It is kept across restarts and
gathered even with no page open (the sampler runs every 10 s then). A click takes you to the pane or page.
The **scout** goes further: every hour it looks at zellij sessions that no thread owns, and one whose agents have
done nothing for a day while it still holds unshipped work (changed lines, an open PR) is handed to Main
JARVIS, who may propose taking it over.

**Where.** `notes.go`, `scout.go`, `teamdeliver.go`; routes `GET /api/notes`, `POST /api/notes/seen`,
`POST /api/ai/scout`.

**How it works.** Each tick compares the new terminal and CI snapshots with the last and writes notes with a
level (ok, warn, bad) and a deduplication key. The scout allows at most one proposal every 12 hours and remembers
your answers (take over, not now, never again) in `scout.json`. Its proposals go to Main JARVIS, so it is only
useful with the assistant running.

## The J.A.R.V.I.S assistant view

**What it does.** A chat with the owner's Hermes agent. One conversation merges every channel (this panel,
Telegram, messages agents send with `jarvis`), each message labelled with its channel. A turn shows its work as a
trace (thinking, narration, each tool call and result) above the answer. Answers can carry styled tables,
`mermaid` diagrams, `chart` charts (Chart.js, including Venn), buttons that open a terminal or page, files on the VM,
and message **cards**. A composer with queue, stop, attachments, dictation and a model picker. Open a new
conversation ("new session") when a long history keeps the model refusing. **Connections** and a **control room**
(overview, memory, skills, tools, MCP, jobs, logs) are Hermes' own dashboard inside the panel, plus a form for the
Telegram bot token and your user id. An agent's HTML page opened from a chat shows beside it with annotate tools
(comment pins, highlight, draw); marks are saved next to the page and sent to the chat.

**Where.** `assistant.go` (the conversation, streaming, Hermes proxy), `aisession.go` (sessions and models),
`aicard.go` (cards), `telegram.go`, `chapters.go` and `digest.go` (the history rail's one-line summaries and
readable agent messages), `agent.go`; UI in `web/assistant.js`, `web/ai-control.js`, `web/ai-rich.js`,
`web/artside.js`, `web/review.js`, `web/aurora.js`, `web/eyetracker.js`.

**How it works.** See [ARCHITECTURE.md](ARCHITECTURE.md#the-assistant-and-the-hermes-proxy). Chat turns go to Hermes'
API server; the panel session is named `panel` and a thread's is `thread-<id>`. The Telegram form only writes the
bot token and allowed user id into Hermes' `.env` (mode 600, only those lines touched); the token is write-only,
reading says whether one is set. Cards (`aicard.go`) are rendered by the vm-agent repository's TypeScript toolkit
(`attachments/` there, run with its `node_modules/.bin/tsx`); without it a card is just not drawn.

**Needs.** The `vm-agent` repository running Hermes. Without it the page says the assistant is not set up; the
rest of the panel works. Details of Main and threads on the vm-agent side: `vm-agent/docs/THREADS.md`.

## Threads

**What it does.** Main JARVIS is the executive assistant; a **thread** is a fresh JARVIS (its own Hermes session
`thread-<id>`, same persona and memory) that leads **one feature**. It grows from a message in Main's chat ("Reply
in thread") or Main opens one with a brief. Its lead plans the feature, decides the team it needs, and runs that
team (Claude or Codex agents) in the zellij session named after the thread, on one git branch. The sidebar lists
open threads with a status dot, the lead's one-line note and how recently it moved; archived ones fold away.

**Where.** `threads.go` (store, turns, first-turn context, archive), `agent.go` (`thread/open`, `thread/send`,
`thread/update`), `teamdeliver.go` (team news to the lead), `nav.go` (`GET /api/nav`, the sidebar's threads);
UI in `web/assistant.js`.

**How it works.**

- A thread record (`threads.json`): `id` (a slug of the title), `title`, `parent` (the Main chat item it grew
  from), `status` (`planning`, `working`, `needs_you`, `done`, `archived`), a one-line `note`, `team` (the zellij
  session), `repo`, `branch`, timestamps, a short event log and the agreed plan.
- Every thread turn sends Hermes per-turn `instructions`: the lead playbook (`~/.hermes/LEAD.md`, supplied by
  vm-agent) filled in with the thread's id, title, team and branch. Every Main turn carries a one-line summary of
  each thread.
- Messages between them use the agent API: `thread/open` (Main opens a thread; its first turn starts at once),
  `thread/send` (a message into a thread, queued behind a running turn, `from` being `main`, `owner` or an agent),
  `thread/update` (the lead keeps its own record; `needs_you` notifies the owner).
- When a team pane finishes, asks or stalls, the panel hands that news to the lead through a durable cursor
  (`TeamSeen`), so a restart or a failed hand-off delivers it later instead of losing it.
- Archiving (owner only) tells the lead to stop its team and close the session once its work is saved.

**Needs.** vm-agent. The git branch and worktree conventions (`bin/feature`) are vm-agent's.

## Features: records, plans and proof

**What it does.** Any zellij session can be a **feature**, and a thread's team session always is. A feature has a
page (open it from Terminals; "Open as feature" tiles all its agents as chats) that shows, from real data, what is
going on: a "since you looked" digest, the team and what each agent last reported, what needs you, the plan as
milestones and as objectives, **code delivery** (each worktree's branch, commits, the PRs agents linked, what
production and stage actually run) and the work log. You edit the record on that page, answer questions, approve
checks and say when something is verified.

**Where.** The `feat*` and `v8*` files (the names are historical; they are the feature page and its data layer):
`featdoc.go` (the record files), `featsup.go` (the supervisor), `featverify.go` (proof), `featv8obj.go`
(objectives), `featv8tickets.go` (what waits on the owner), `featv8page.go` (the page's data), `featv8arts.go`
(artifacts), `featv8msg.go` (owner comments to the lead), `featevents.go`, `featseen.go` (seen receipts),
`featr1.go` (where an evidence file came from), `featsessions.go` (who ran which step), `featcode.go` and
`ship.go` (code delivery), `featreport.go` (the written report), `feature.go`; UI in `web/featpage.js` and
`web/featpage.css`. New feature-page routes register through `v8Register` in `featv8.go`, not in `server.go`.

**How it works.**

- **The record** lives outside git, in `~/jarvis-files/features/<session>/`, so it survives a deleted branch
  and every agent reads it at one absolute path:
  - `FEATURE.md`: the owner's: what must be true (objective, requirements, definition of done, which
    worktree is the main one). Desired outcomes are lines like `- [ ] D1: <outcome> — done when: <condition>`,
    with an optional indented `- proof: ...` the owner requires.
  - `PLAN.md`: milestones as `## headings`, steps as `- [ ] ... @Tab` (the tab that owns it), ticked by the
    agents. Lines indented under a step carry `evidence:`, `result:`, `kind: read|build|test|mutation`,
    `due:`, `ask:`/`answer:`, `receipt:`, `covers: D1`.
  - `TESTS.md`: test cases (`[x]` pass, `[!]` fail, `[-]` skipped, `[ ]` to run).
  - `LOG.md`: the team's dated updates and push-back.
  - `FEATURE.draft.md`: requirements an agent proposes, until the owner adopts or discards them.
  Edits are made against the revision that was read; a write against another revision is refused.
- **A ticked box is a claim.** A step counts as done only through (a) a **receipt**: the owner explicitly approved
  the step's check (its exact command and folder, for its exact wording), an agent of the feature ran exactly
  that command with Claude Code's Bash tool, and Claude Code's own transcript records the run, its output and an
  exit status of 0; or (b) the owner's **attestation** ("Mark verified"). File evidence is never more than a
  claim. An objective is met only when steps cover it and every covering step is verified. Approval, attestation,
  answers and releasing a hold are page routes behind the login, never the agent API. The panel executes nothing
  itself. Limit: transcripts are files the same Unix user can write, so a deliberately forged transcript is not
  detected.
- **The supervisor** looks at each thread's team against its `PLAN.md` on every tick. It raises, once each: an
  agent waiting on an approval dialog, an agent that exited mid-step (and resumes it with `$CP_CLAUDE_CMD --resume`
  when the step is safe to repeat: read, build, test, check), a team gone quiet with nothing running (45 minutes
  by default), a `due:` checkpoint that passed, a question waiting on the owner, a milestone or the whole feature
  verified. What it raises becomes a note its lead receives through the durable cursor. It never stops or kills
  anything.
- **Done is gated**: a thread cannot be `done` unless the record passes the gate, and a feature marked done whose
  record later stops passing is reopened.
- **The report** (`featreport.go`) gathers facts the panel can read itself and has a one-off `claude -p` run
  write the parts a project manager would; it is rewritten only when the owner asks, since it costs a model run.
- **Code delivery** runs `git` read-only (`--no-optional-locks`) over the feature's worktrees and `gh` for PRs and
  deployments; it assumes the branch flow *feature, stage, main* that vm-agent's `bin/feature` creates.
- **Brief the team** (and every compaction the panel performs) points agents at the record folder, so a fresh
  context starts from the files rather than from memory.

**Agent API for features** (`/agent/feature*`): `GET feature` (the record and revisions), `POST feature/doc`
(write a record file against the revision read), `POST feature/log` (append a dated line), `GET feature/team`,
`POST feature/assign` (record who works on which step), `GET feature/events` (read-only; nothing is marked seen).

**Needs.** zellij and the agents; for the lead and the MCP tools, vm-agent. A feature on a session without a
thread works with the page and the record, without a lead.

## Context compaction

**What it does.** Keeps agents' context windows from filling without cutting one off mid-thought. Past the auto
limit (60% by default) a Claude whose turn is over is sent `/compact`; one still working is asked to stop at its
next natural break, compacted then, and told to continue. A reply that asks you something waits for your answer.
The page offers Compact from the suggest limit (40%). Claude Code's own auto-compact near a full window remains the
backstop.

**Where.** `compact.go`; `POST /api/compact` (limits, on/off) and `POST /api/pane/compact` (one agent).
`-compact-sessions '<regexp>'` limits it to sessions with matching names (empty means all).

**How it works.** A small per-session state machine (`hold`, `pausing`, `compacting`, `done`, `failed`) driven from
the sampler tick; messages it types start with `[Control panel]` so the chat can say who wrote them. A failed
compaction is retried after a rest period, and a session compacted recently is left alone so a limit lower than a
fresh start cannot loop.

## Dictation

**What it does.** A microphone in every chat box: tap and talk, tap again, and the words land at the cursor; or
hold it (or Alt+Space in the box) and let go.

**Where.** `stt.go`, `stt_worker.py` (embedded), `web/voice.js`; `POST /api/stt`.

**How it works.** The clip goes to the panel, which hands it to a long-lived Python worker (faster-whisper,
`small.en`, int8 on 4 CPU threads) in the venv at `~/.local/opt/stt`; ffmpeg decodes it. The worker keeps the
model loaded and exits after 20 idle minutes; clips are at most about 25 MB. Nothing leaves the machine.

**Needs.** The venv, the cached model and ffmpeg ([HOSTING.md](HOSTING.md#voice-dictation)); otherwise the route
answers 503.

## Login, audit and security headers

**What it does.** The PIN screen, **Lock panel** in the menu, the audit trail and the security headers described in
[ARCHITECTURE.md](ARCHITECTURE.md#request-path-and-authentication). Uncaught JavaScript errors from any browser are
sent to `client-errors.jsonl` with the user agent, so a failure that only one phone has can be read on the VM.

**Where.** `auth.go`, `server.go`, `web/report.js`.
