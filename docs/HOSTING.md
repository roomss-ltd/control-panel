# Hosting the control panel

This guide takes a fresh Linux VM to a panel reachable at `https://panel.example.com`, behind a Cloudflare
Tunnel and Cloudflare Access, and starting on its own at login. Replace `panel.example.com` with your hostname
throughout.

- [What you are building](#what-you-are-building)
- [Before you start](#before-you-start)
- [1. Install the tools](#1-install-the-tools)
- [2. Get the code, build, set the PIN](#2-get-the-code-build-set-the-pin)
- [3. The agent launchers](#3-the-agent-launchers)
- [4. Cloudflare Tunnel](#4-cloudflare-tunnel)
- [5. Cloudflare Access](#5-cloudflare-access)
- [6. Configure and start](#6-configure-and-start)
- [7. Start on login](#7-start-on-login)
- [8. Check it](#8-check-it)
- [Updating](#updating)
- [Running without Cloudflare](#running-without-cloudflare)
- [Optional features and what they need](#optional-features-and-what-they-need)
- [Operating it](#operating-it)
- [Troubleshooting](#troubleshooting)

## What you are building

```
 browser ──HTTPS──▶ Cloudflare edge ──▶ Access (who are you?)
                                           │ adds a signed Cf-Access-Jwt-Assertion header
                                           ▼
                                  Cloudflare Tunnel  ◀── cloudflared on the VM dials OUT, no inbound port
                                           │ http://127.0.0.1:7777
                                           ▼
                                   control-panel (checks the JWT again, then asks for the PIN)
```

Three layers protect the panel, and each one fails closed:

1. **Cloudflare Access** decides who may reach the hostname at all (your email, one-time code).
2. **The panel verifies the Access token itself** (signature, audience, issuer, expiry). A request that reaches
   `127.0.0.1:7777` without a valid token, for example from another process on the VM, gets a 403.
3. **A PIN** (6 to 32 digits) unlocks a session. Five wrong PINs lock logins for 15 minutes.

The panel can type into your terminals, start processes and read and write your files as the user it runs as.
Treat access to it like an SSH login: a strong Access policy, a PIN you do not reuse, and a VM you can afford to
lose.

## Before you start

You need:

- a Linux VM with a normal user account (not root) that will own everything: the panel sees and controls that
  user's processes, files and terminals. Debian or Ubuntu are what the notes below assume; other distributions
  work if you map the package names. The panel reads `/proc`, so it is Linux only.
- outbound HTTPS from the VM (cloudflared dials out; the panel fetches Cloudflare's signing keys from
  `https://<team>.cloudflareaccess.com/cdn-cgi/access/certs`). The scripts unset `HTTPS_PROXY`, so that traffic
  must be able to go direct.
- a **Cloudflare account** with a domain whose DNS is on Cloudflare, and the free **Zero Trust** plan enabled
  (it asks you to choose a *team name*). Only the Cloudflare steps need this; see
  [Running without Cloudflare](#running-without-cloudflare) to try the panel first.

Tools, by how much the panel needs them:

| Tool | Needed for | Without it |
|---|---|---|
| Go (the version in `go.mod`, currently 1.27.1) | building the panel | cannot build |
| `zellij` 0.45 (the version this was built and tested with) or newer | Terminals, agent panes, features, everything about sessions | the Terminals page is empty |
| `cloudflared` | the tunnel (remote access) | local-only use |
| `git`, `pgrep` (procps), `script` (util-linux) | the scripts, worktree and PR views, headless zellij attach | parts of those views |
| `claude` ([Claude Code](https://docs.claude.com/en/docs/claude-code)) | Claude panes, chat transcripts, token use | no Claude panes; background summaries are skipped |
| `codex` | Codex panes | no Codex panes |
| Chromium (Playwright's, or `chromium`, `chromium-browser`, `google-chrome` on `PATH`) | the Browser page | the Browser page says there is no Chromium |
| `gh` (logged in) | PR cards, the CI page's PR titles, the Files "clone a repo" list | those parts are empty |
| `rg` (ripgrep) | text search inside a folder in Files | that search is unavailable |
| `convert` (ImageMagick) | fast photo thumbnails, HEIC | falls back to Go's image library; HEIC does not open |
| `ffmpeg` and a faster-whisper venv | voice dictation in chat boxes | the mic answers "not set up" |
| Xtigervnc, `xterm`, `xrandr` | terminal and browser as VNC | falls back to the screen/frame streams |
| the sibling `vm-agent` repo | the J.A.R.V.I.S assistant and feature threads | that page says the assistant is not set up |

The `claude-tab-status` zellij plugin (it writes `/tmp/claude-tab-status/<session>.json`) is optional: it is a
fallback status signal, mainly for Codex.

## 1. Install the tools

On Debian or Ubuntu, with sudo:

```sh
sudo apt-get update
sudo apt-get install -y git curl procps util-linux ripgrep imagemagick ffmpeg python3-venv
```

Then, as your normal user:

- **Go**: install the version named in `go.mod` from <https://go.dev/dl/>, or any Go new enough to fetch it
  (`GOTOOLCHAIN` downloads the pinned toolchain automatically). Check with `go version`.
- **zellij** 0.45 (what the panel was built against; the code relies on `list-panes --json`, `subscribe` and `attach --create-background`): download a release binary from <https://github.com/zellij-org/zellij/releases> and put
  it on your `PATH` (the panel also looks in `~/.local/bin/zellij`).
- **cloudflared**: from Cloudflare's downloads page (<https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/>).
- **GitHub CLI** (`gh`), then `gh auth login`.
- **Claude Code** and/or **Codex**, signed in, if you will run agents.
- **Chromium for the Browser page**: either `npx playwright install chromium` (the panel finds the newest
  `~/.cache/ms-playwright/chromium-*/chrome-linux64/chrome`) or a distribution `chromium` on `PATH`.

The panel finds zellij's sessions from their sockets, which it looks for in `$TMPDIR` (else `/tmp`) under
`zellij-<uid>/contract_version_*`. Start zellij and the panel with the same `TMPDIR` (the usual case is that
neither sets it). If Terminals stays empty although `zellij list-sessions` shows sessions, check where those
sockets are.

## 2. Get the code, build, set the PIN

```sh
mkdir -p ~/projects && cd ~/projects
git clone https://github.com/roomss-ltd/control-panel control-panel
git clone https://github.com/roomss-ltd/vm-agent vm-agent        # optional: the assistant, see below
cd control-panel
go build -o control-panel .
./control-panel set-pin
```

`set-pin` asks for the PIN twice without echo and stores only its hash in `~/.config/control-panel/pin`
(mode 0600). It takes effect immediately; run it again any time to change the PIN. Use `-pin-file` to put the
file elsewhere (then pass the same flag when you start the panel).

`~/projects/control-panel` and `~/projects/vm-agent` side by side is the layout the two projects assume, but the
panel's own scripts work from wherever the repository is, because they `cd` into their own directory.

## 3. The agent launchers

The panel's **New session / tab / pane** form can start an agent by typing a command into the new pane's shell.
They must resolve in that shell (`panes.go`):

- **Claude**: the value of `CP_CLAUDE_CMD`, default `claude`. It is also what the panel types to resume a
  Claude conversation (`$CP_CLAUDE_CMD --resume <session id>`). To run agents without permission prompts (the
  panel already surfaces on-screen questions), add this to `panel.env`:

  ```sh
  export CP_CLAUDE_CMD="claude --dangerously-skip-permissions"
  ```

  Without it, Claude asks for permissions as normal. Make sure `claude` is on `PATH` for interactive shells
  inside zellij.
- **Codex**: `codex`.

If you start your agents by hand in a "Shell" pane instead, the panel still recognises them (it identifies
agents by their process and Claude Code's own session files), but "Resume" and the supervisor's automatic
resume type `$CP_CLAUDE_CMD`.

## 4. Cloudflare Tunnel

The scripts expect a tunnel named `control-panel` whose token is in `~/.cloudflared/control-panel.token`, and
they run it as:

```sh
cloudflared tunnel --no-autoupdate --loglevel warn run \
  --token-file ~/.cloudflared/control-panel.token --url http://127.0.0.1:7777
```

Create it from the VM's shell:

```sh
cloudflared tunnel login                       # prints a URL; open it, pick your domain, authorize
cloudflared tunnel create control-panel        # creates the tunnel and its credentials file
cloudflared tunnel route dns control-panel panel.example.com
mkdir -p ~/.cloudflared
cloudflared tunnel token control-panel > ~/.cloudflared/control-panel.token
chmod 600 ~/.cloudflared/control-panel.token
```

`route dns` creates the `panel.example.com` CNAME to the tunnel in your Cloudflare zone. The token file is all
the scripts need; `~/.cloudflared/cert.pem` (from `login`) is only for managing tunnels.

If you prefer the Zero Trust dashboard (Networks, Tunnels, create a Cloudflared tunnel), create it there, add a
public hostname `panel.example.com` with service `HTTP`, URL `127.0.0.1:7777`, and save the token it shows in
the same file. That route is not exercised by these scripts' authors; the CLI route above is.

## 5. Cloudflare Access

The tunnel alone would expose the login page to the internet. Access puts an identity check in front of it.

1. In the Zero Trust dashboard note your **team name**. It is the `<team>` in `<team>.cloudflareaccess.com`
   (Settings, General). This is `CF_TEAM`.
2. Access, Applications, **Add an application**, **Self-hosted**.
   - Application domain: `panel.example.com` (the same hostname as the tunnel's).
   - Policy: **Allow**, include **Emails** and list the addresses that may use the panel. Keep it to you.
   - Login methods: the default **One-time PIN** (a code mailed to the address) is enough.
3. Save, open the application, and copy the **Application Audience (AUD) Tag** from its Overview tab. This is
   `CF_AUD`.

(Cloudflare moves menu items around now and then; the names above are the concepts, not guaranteed labels.)

Nothing in the panel trusts the tunnel: if the Access application is missing or misconfigured, the panel does
not open up, it answers 403 to every request that lacks a valid token whose audience is `CF_AUD`.

## 6. Configure and start

```sh
cd ~/projects/control-panel
cp panel.env.example panel.env
$EDITOR panel.env          # set CF_TEAM and CF_AUD; panel.env is git-ignored
```

`panel.env` is sourced by the shell scripts, so keep to plain `NAME=value` lines. `CF_TEAM` and `CF_AUD` are
passed to the binary as `-cf-team` and `-cf-aud`. Anything else you want the panel process to see must be
exported, for example:

```sh
export CP_NO_BACKGROUND_MODELS=1     # no background `claude -p` runs (chat summaries); see below
export CP_AWS_REPOS=example-org/example-repo,example-org/another-repo   # CI page: cloud-side runners
export CP_CLAUDE_CMD="claude --dangerously-skip-permissions"   # command typed to start Claude in a new pane (default: claude); see section 3
```

**Decide about `CP_NO_BACKGROUND_MODELS` now.** With J.A.R.V.I.S chats and threads in use, the panel runs short
`claude -p` jobs in the background (a haiku summary line per chat stretch, readable versions of agent
messages) that spend tokens on the account the `claude` CLI is signed in to. `CP_NO_BACKGROUND_MODELS=1` turns
those off. The feature report (sonnet) only runs when you press the button for it.

Start both pieces in the foreground first to watch them:

```sh
./run-panel.sh        # builds, then runs the panel; rebuilds and restarts itself when it exits
./run-tunnel.sh       # in a second terminal: runs cloudflared; restarts itself when it exits
```

Both scripts `cd` into their own directory, so you can call them from anywhere. They are meant to live in a
zellij tab each (Ctrl-C stops one for good). `run-panel.sh` stops a copy that `ensure-panel.sh` started in the
background before taking over, and `./ensure-panel.sh restart` from anywhere rebuilds and restarts the panel,
which the `run-panel.sh` loop then brings back.

Equivalent by hand:

```sh
. ./panel.env
./control-panel -cf-team "$CF_TEAM" -cf-aud "$CF_AUD"
```

## 7. Start on login

There is no systemd unit or cron entry; `ensure-panel.sh` is meant to be called from your shell profile on every
login and is safe to run at any time (it starts only what is not already running):

```sh
echo '"$HOME/projects/control-panel/ensure-panel.sh"' >> ~/.zshrc     # or ~/.bashrc
```

What it does:

- needs `panel.env` and `~/.cloudflared/control-panel.token`; if either is missing it prints what it needs and
  exits quietly;
- builds `./control-panel` if absent;
- starts the panel and the tunnel with `nohup` in the background, logging to `run/panel.log` and
  `run/tunnel.log` and writing `run/panel.pid` and `run/tunnel.pid`;
- recognises a copy already running anywhere (for example in a zellij tab started by `run-panel.sh`) and does
  not start a second one;
- `./ensure-panel.sh restart` rebuilds and restarts the panel; the tunnel stays up.

If you would rather use systemd, run the same two commands as user services (the panel with `-cf-team` and
`-cf-aud`, the tunnel with the `cloudflared` line from section 4). The panel needs a normal user session so
that zellij and the agents run as the same user.

## 8. Check it

```sh
curl -i http://127.0.0.1:7777/          # direct, no Access token: expect 403 forbidden
tail run/panel.log                      # "control panel on http://127.0.0.1:7777" and "agent API on ..."
```

Then open `https://panel.example.com`: Cloudflare Access asks for your email and a code, and then the panel asks
for the PIN. After that you should see the sidebar (J.A.R.V.I.S, Terminals, Browser, Files, Resources, CI).
A `403 forbidden` in the browser means the Access token did not verify; check `CF_TEAM` and `CF_AUD` against the
dashboard, and look for `access-reject` lines (with the reason) in `~/.local/state/control-panel/audit.jsonl`.

## Updating

```sh
cd ~/projects/control-panel && git pull
./ensure-panel.sh restart      # rebuilds and restarts the panel; the tunnel keeps running
```

The web UI is embedded in the binary, so any change under `web/` needs the rebuild. Logged-in sessions survive a
restart (they are kept hashed in `sessions.json`), so nobody retypes the PIN.

## Running without Cloudflare

The panel has exactly one switch for this: `-insecure-no-access`. It skips the Cloudflare Access check and nothing
else: the PIN login, the session cookie and the security headers stay. The listener must still be a loopback
address (`validateConfig` refuses anything else), so "without Cloudflare" means *on the VM itself or through an
SSH port forward*, not a public port.

```sh
go build -o control-panel .
./control-panel set-pin                          # once
./control-panel -insecure-no-access              # http://127.0.0.1:7777, agent API on 127.0.0.1:7778
```

From your own machine: `ssh -L 7777:127.0.0.1:7777 you@the-vm`, then open `http://127.0.0.1:7777`. This was
checked in Chromium: the PIN screen appears and logging in works over plain `http://127.0.0.1` even though the
session cookie is marked `Secure` (browsers treat loopback as a secure origin; a browser that does not will not
keep the cookie).

Things to know in this mode:

- the identity shown in the audit log and bound to sessions is the fixed string `local`;
- any other user on the VM can reach the loopback port and try PINs (the global lockout still applies), so use it
  on a single-user machine;
- `-addr 127.0.0.1:PORT` and `-agent-addr 127.0.0.1:PORT2` let you run a second instance for testing; give it its
  own `-pin-file`, `-audit-file` and `-agent-token-file` (or a different `HOME`) so state does not mix;
- `ensure-panel.sh` and `run-panel.sh` always start the Cloudflare-protected form, so they do not apply here.

## Optional features and what they need

None of these block the panel; each page degrades on its own.

### The Browser page

Needs Chromium (section 1). The panel starts its own headless Chromium with a persistent profile in
`~/.local/state/control-panel/browser/profile`, so logins survive restarts. Downloads land in `~/Downloads`.

Watching **agents'** browsers and recording them ("trails") needs the Cube dev-VM image's `cube-browser` tool:
at start the panel looks for `/opt/cube/browser/cube-browser.mjs` and `/opt/cube/browser-binaries` and, only if
both exist, installs a wrapper at `~/.local/bin/cube-browser` (it never replaces a file that is not its own).
On a stock VM that part is simply inactive. Set `CP_NO_AGENT_WATCH=1` to keep it off where the tool exists.

### VNC for terminals and the Browser

If `Xtigervnc` and `xterm` are found, a terminal can be shown as VNC (only changed rectangles are sent, which is
kinder to a phone), and the owner's Chromium runs headed on a private display. The panel looks first in
`~/.local/opt/vnc/usr/bin` (an unpacked `tigervnc-standalone-server` package, for machines without root) and
then on `PATH`. With sudo:

```sh
sudo apt-get install -y tigervnc-standalone-server xterm x11-xserver-utils fonts-dejavu-core
```

Without them the page uses its older stream. Sockets are private unix sockets under `/tmp/cpvnc*`.

### Voice dictation

The mic button in chat boxes records a clip, posts it to the panel, and `faster-whisper` transcribes it on the VM
(nothing leaves the machine). It needs a Python environment at `~/.local/opt/stt` with `faster-whisper`, the
`small.en` model already downloaded (the worker runs with `HF_HUB_OFFLINE=1`), and `ffmpeg` on `PATH` (or at
`~/.pixi/bin/ffmpeg`):

```sh
python3 -m venv ~/.local/opt/stt
~/.local/opt/stt/bin/pip install faster-whisper
~/.local/opt/stt/bin/python -c "from faster_whisper import WhisperModel; WhisperModel('small.en')"   # downloads the model
```

Without it `POST /api/stt` answers 503 "Dictation isn't set up on this machine".

### Agent cost and context numbers

If a Claude Code status-line script saves its input to `~/.cache/claude-statusline/<session id>.json`
(`$XDG_CACHE_HOME/claude-statusline/` when that is set), the panel shows Claude Code's own cost and context
window size for each agent. Without it those two numbers are inferred or missing.

### The CI page

It lists self-hosted GitHub Actions runners kept in `-ci-dir` (default `~/projects/ci-cd-jobs`): one folder per
runner named `r<N>` or `fe<N>`, each with the runner's `runner.pid`, a `runner.log` that has one line per job event
(`<time>Z: Running job: <name>` and `<time>Z: Job <name> completed with result: <result>`), and `_diag/Worker_*.log`.
Restarting a runner kills its process tree and runs `./ensure-runners.sh` from `-ci-dir`, which you supply and
which starts any runner whose `runner.pid` is not alive. The panel also scans `~/ci-aws/runners/` the same way (runners named `aws-vm-*`, a second fleet). If you do not
self-host runners the page is empty and you can ignore it. `CP_AWS_REPOS` lists repositories whose cloud-side runners the page asks GitHub about (via
`gh`).

### The assistant (J.A.R.V.I.S)

The assistant is the sibling `vm-agent` repository, a Hermes-based agent. Install it as its README describes
(`setup.sh`, then `run-agent.sh`). The panel needs three things from it, all with defaults you only change if
you moved them:

- Hermes' API server at `-ai-url` (default `http://127.0.0.1:8642`) with the key in `-ai-key-file` (default
  `~/.config/vm-agent/api-key`, written by `setup.sh`);
- Hermes' dashboard at `127.0.0.1:9119` (fixed), proxied at `/hermes/`;
- the agent API on `-agent-addr` (default `127.0.0.1:7778`) with the token the panel creates in
  `~/.config/control-panel/agent-token`; vm-agent reads that file.

`-hermes-env` (default `~/.hermes/.env`) is the file the Connections form writes Telegram settings into.
Threads and features are described in [FEATURES.md](FEATURES.md) and, from the vm-agent side, in
`vm-agent/docs/THREADS.md`.

### Context compaction

On by default: a Claude at 60% of its context window gets `/compact` typed into its pane when its turn is
over. Start the panel with `-compact-sessions '<regexp>'` to limit that to sessions whose name matches, or turn
it off from the page.

## Operating it

| Where | What |
|---|---|
| `run/panel.log`, `run/tunnel.log` | output when started by `ensure-panel.sh` |
| `~/.local/state/control-panel/audit.jsonl` | sign-ins, PIN failures, lockouts, Access rejects, file and terminal actions |
| `~/.local/state/control-panel/client-errors.jsonl` | uncaught JavaScript errors reported by browsers (with the user agent) |
| `~/.config/control-panel/` | PIN hash, login sessions, agent token, balancer rules |
| `~/.local/state/control-panel/` | all other state: threads, notes, token use, browser profile, recordings |

Back up `~/.config/control-panel/` and `~/.local/state/control-panel/` if you care about threads and history.
To lock everyone out, delete `~/.config/control-panel/sessions.json` and restart, or press **Lock panel** in the
menu. To reset a forgotten PIN, run `./control-panel set-pin` on the VM.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Browser shows `403 forbidden` | The Access JWT did not verify. Compare `CF_TEAM` and `CF_AUD` with the dashboard; read the reason in the `access-reject` audit line. The VM must reach `<team>.cloudflareaccess.com` directly. |
| `control-panel: needs panel.env ... and ...token` | `ensure-panel.sh` is missing one of the two files (sections 4 and 6). |
| Panel will not start: "set -cf-team ... and -cf-aud" | The two flags are empty: `panel.env` has no values, or you ran the binary without them (use `-insecure-no-access` to test locally). |
| Panel will not start: "is not loopback" | `-addr` or `-agent-addr` is not a loopback IP. The panel only listens on loopback by design. |
| PIN screen says "no PIN set" | Run `./control-panel set-pin` as the user that runs the panel (or pass the same `-pin-file`). |
| Locked out for 15 minutes | Five wrong PINs. Wait, or restart the panel and use the right PIN; the lock is in memory. |
| Terminals is empty but zellij has sessions | The panel looks for zellij's sockets in `$TMPDIR` or `/tmp` under `zellij-<uid>/`; see section 1. Also check it runs as the same user as zellij. |
| "New session, Claude" opens a shell that says `claude: command not found` (or the first word of your `CP_CLAUDE_CMD`) | Section 3: put it on `PATH` for shells inside zellij, or change `CP_CLAUDE_CMD`. |
| Browser page: "no Chromium on this box" | Section 1: install Playwright's Chromium or a `chromium` package. |
| J.A.R.V.I.S page says the assistant is not set up | No `-ai-key-file`: the vm-agent setup has not run. |
| `/hermes/` answers 502 | Hermes' dashboard is not running (vm-agent's `run-agent.sh` starts it). |
| Tunnel is up but the page never loads | Is the public hostname's service `http://127.0.0.1:7777`? Does `curl -i http://127.0.0.1:7777/` answer 403 on the VM? |
