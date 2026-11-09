#!/bin/bash
# Feature-page (v8) acceptance suite: a black-box run of the panel against a scratch environment.
#
# Builds the panel from SRC and starts it on free ports with its own HOME and TMPDIR (so zellij gets a private socket
# directory), stand-ins for Hermes, claude and gh (fixture/), a scratch git repo, and unique session and thread names.
# It then runs the node:test suites in this directory against that panel. It touches no live panel, agent, Hermes,
# ~/.claude, GitHub network or AWS, and spends no model tokens (the stand-in claude refuses -p).
#
# Cleanup touches ONLY processes this run owns (ownproc.sh): each PID is recorded with its kernel start time and
# command line when started (panel, Hermes), plus every process whose environment has exactly this run's private
# TMPDIR (the daemonized zellij server, its panes, the stand-ins). A PID whose start time no longer matches is not
# touched. Never a name, port or substring match; the ledger and every signal go to $OUT/cleanup.log.
#
# Usage: acceptance/v8/run.sh [SRC] [OUT] [test files...]
#   SRC          the control-panel checkout to build and test (default: the repo this script is in)
#   OUT          where logs, junit.xml and screenshots go (default: $V8QA_RESULTS/run-<revision>-<UTC time>)
#   test files   run just these suites, named relative to this directory (default: all of them, in dependency order)
#                Pass '' for SRC or OUT to keep its default: run.sh '' '' ui-fixture.test.mjs
#
# Needs Linux (it reads /proc), bash, go, git, curl, python3, zellij and node 20+ (node --test). The ui*.test.mjs suites also need Playwright:
# `npm install playwright-core && npx playwright-core install chromium` in the repo root.
#
# Environment (all optional):
#   V8QA_BASE         scratch directory for per-run state (default: ${XDG_CACHE_HOME:-$HOME/.cache}/v8qa); keep it short,
#                     zellij's socket paths inside it must stay under 107 bytes
#   V8QA_RESULTS      parent of the default OUT (default: $V8QA_BASE/results)
#   V8QA_PLAYWRIGHT   what the browser suites import: a package name, or an absolute path to playwright-core's
#                     index.mjs (default: playwright-core, found by walking up from this directory)
#   V8QA_CHROME       Chromium binary for the browser suites (default: Playwright's own bundled Chromium)
#   V8QA_DESIGN_HTML  a design mock-up page (path or file:// URL) for the style-parity test F-V2 in ui-fixture.test.mjs
#   V8QA_REAL_PLAN    a real PLAN.md with milestones M1..M7 for the style test F-V1 in ui-fixture.test.mjs
#                     (F-V1 and F-V2 are skipped without their file)
#   V8QA_WATCH=1      ui-fixture.test.mjs: launch V8QA_WATCHED_CHROME (the panel's own Chromium wrapper, default
#                     $HOME/.local/share/control-panel/browser-bin/chromium-1234/chrome-linux64/chrome) so the
#                     panel's Browser page lists the run live; V8QA_HOLD=<minutes> then keeps that browser open
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
SRC=${1:-$(cd "$HERE/../.." && pwd)}
if REV=$(git -C "$SRC" rev-parse --short HEAD 2>/dev/null); then git -C "$SRC" diff --quiet HEAD || REV=$REV-dirty; else REV=nogit; fi
BASE=${V8QA_BASE:-${XDG_CACHE_HOME:-$HOME/.cache}/v8qa}; mkdir -p "$BASE"
OUT=${2:-${V8QA_RESULTS:-$BASE/results}/run-$REV-$(date -u +%Y%m%dT%H%M%SZ)}
shift $(( $# < 2 ? $# : 2 )); FILES=${*:-"$HERE/api.test.mjs $HERE/events.test.mjs $HERE/seen.test.mjs $HERE/adversarial.test.mjs $HERE/tickets.test.mjs $HERE/revs.test.mjs $HERE/objectives.test.mjs $HERE/deploy.test.mjs $HERE/ui.test.mjs $HERE/ui-msg.test.mjs $HERE/logarts.test.mjs $HERE/ui-logarts.test.mjs"}
E=$(mktemp -d "$BASE/qa8-XXXXXX") || exit 2; ID=$(echo "${E##*-}" | tr A-Z a-z); mkdir -p "$E"/{bin,tmp,home/.claude/sessions,home/.config/vm-agent,home/.hermes,home/.local/state/control-panel,pin,work,shots} "$OUT"
export V8QA_E=$E
read P1 P2 P3 < <(python3 -c 'import socket;s=[socket.socket() for _ in range(3)];[x.bind(("127.0.0.1",0)) for x in s];print(*[x.getsockname()[1] for x in s])')
S=qa8-$ID T=qa8-$ID-mgr TB=qa8-$ID-mgr2
cp "$HERE"/fixture/{claude,git,gh} "$E/bin/"; cp "$HERE/fixture/hermes.py" "$E/"; echo ok > "$E/hermes-mode"; echo fail > "$E/gh-mode"
echo key > "$E/home/.config/vm-agent/api-key"; echo "Lead of {title} ({id}); team {team}." > "$E/home/.hermes/LEAD.md"
NOW=$(date +%s)
echo "[{\"id\":\"$T\",\"title\":\"V8 QA manager\",\"status\":\"working\",\"team\":\"$S\",\"created\":$NOW,\"updated\":$NOW},{\"id\":\"$TB\",\"title\":\"V8 QA second manager\",\"status\":\"working\",\"created\":$NOW,\"updated\":$NOW}$(for i in $(seq 3 23); do printf ',{"id":"%s","title":"V8 QA spare manager %s","status":"working","created":%s,"updated":%s}' "$S-mgr$i" $i $NOW $NOW; done)]" > "$E/home/.local/state/control-panel/threads.json"
log() { echo "$(date -u +%T) $*" | tee -a "$OUT/steps.log"; }
PENV="HOME=$E/home TMPDIR=$E/tmp PATH=$E/bin:$PATH V8QA_E=$E CP_NO_BACKGROUND_MODELS=1" # CP_NO_BACKGROUND_MODELS: no background chapters/digests model loop

# scratch repo: main has commits c1-c2, feature/v8 forks at c1 with 2 commits, so the merge-base is known
R=$E/home/work/repo; # inside the run's HOME and symlink-free: the panel only reads a worktree within HOME and outside secret roots
 mkdir -p "$R"; G() { /usr/bin/git -C "$R" -c user.email=qa@local -c user.name=qa "$@"; }
G init -q -b main; echo a > "$R/a"; G add a; G commit -qm c1; C1=$(G rev-parse HEAD)
echo b >> "$R/a"; G commit -qam c2
G remote add origin https://github.com/qa8/ledger.git # never fetched: only its slug is read
G checkout -qb feature/v8 "$C1"; echo f > "$R/f"; G add f; G commit -qm f1; echo g >> "$R/f"; G commit -qam f2; HEADF=$(G rev-parse HEAD)

log "build $SRC @ $REV"; echo "$REV $(git -C "$SRC" log --oneline -1 2>/dev/null)" > "$OUT/revision.txt"
(cd "$SRC" && go build -o "$E/cp-v8qa" .) || { log "BUILD FAILED"; exit 2; }
printf '482913\n482913\n' | env $PENV "$E/cp-v8qa" set-pin -pin-file "$E/pin/pin" >/dev/null
python3 "$E/hermes.py" $P3 "$E/turns.jsonl" "$E/hermes-mode" > "$OUT/hermes.log" 2>&1 & "$HERE/ownproc.sh" rec "$E/owned" $!
# start.sh and stop.sh are scripts so tests can restart the panel mid-run
cat > "$E/start.sh" <<EOF
#!/bin/sh
cd $E && env $PENV ./cp-v8qa -addr 127.0.0.1:$P1 -insecure-no-access -pin-file $E/pin/pin -audit-file $E/home/.local/state/control-panel/audit.jsonl -agent-addr 127.0.0.1:$P2 -ai-url http://127.0.0.1:$P3 -compact-sessions '^cpc-never\$' -ci-dir $E/ci -status-dir $E/status >> $OUT/panel.log 2>&1 &
P=\$!; echo "\$P \$(sed 's/^.*) //' /proc/\$P/stat | awk '{print \$20}')" > $E/panel.pid; $HERE/ownproc.sh rec $E/owned \$P
for i in \$(seq 1 50); do curl -s -o /dev/null http://127.0.0.1:$P1/ && break; sleep 0.2; done
EOF
cat > "$E/stop.sh" <<EOF
#!/bin/sh
# only the PID this run recorded, only while its kernel start time still matches and it is this run's binary
read P ST < $E/panel.pid 2>/dev/null || exit 0
[ "\$(sed 's/^.*) //' /proc/\$P/stat 2>/dev/null | awk '{print \$20}')" = "\$ST" ] && [ "\$(readlink /proc/\$P/exe)" = "$E/cp-v8qa" ] || exit 0
kill \$P; for i in \$(seq 1 50); do [ -e /proc/\$P ] || exit 0; sleep 0.1; done
EOF
chmod +x "$E"/{start,stop}.sh
cleanup() { "$E/stop.sh"
  for x in $S $S-solo; do env TMPDIR="$E/tmp" zellij kill-session $x >/dev/null 2>&1; env TMPDIR="$E/tmp" zellij delete-session $x >/dev/null 2>&1; done
  sleep 0.5; "$HERE/ownproc.sh" env "$E/owned" "$E/tmp"; { echo "# ledger (pid start cmdline)"; cat "$E/owned"; echo "# kill"; } >> "$OUT/cleanup.log"
  "$HERE/ownproc.sh" kill "$E/owned" >> "$OUT/cleanup.log" 2>&1
  cp "$E"/shots/*.png "$OUT/" 2>/dev/null; cp -r "$E"/{turns.jsonl,claude-calls.log,claude-prompts,gh-calls.log} "$OUT/" 2>/dev/null; mkdir -p "$OUT/state"; cp -r "$E/home/.local/state/control-panel/." "$OUT/state/" 2>/dev/null; cp -r "$E/home/jarvis-files" "$OUT/state/" 2>/dev/null; rm -rf "$E"; }
trap cleanup EXIT; trap 'exit 130' INT TERM HUP # a stopped run still cleans up its own processes
"$E/start.sh"
CJ=$E/cj; curl -s -c "$CJ" -X POST "http://127.0.0.1:$P1/api/login" -H 'Content-Type: application/json' -d '{"pin":"482913"}' >/dev/null
api() { curl -s -b "$CJ" -X "$1" "http://127.0.0.1:$P1$2" -H 'Content-Type: application/json' ${3:+-d "$3"}; }
log "session $S (owner-created, then manager $T attached in the seed)"
api POST /api/term/new "{\"what\":\"session\",\"name\":\"$S\",\"cwd\":\"$R\",\"run\":\"shell\"}" >/dev/null; sleep 1.5
env TMPDIR="$E/tmp" zellij --session $S action rename-tab --tab-id 0 Worker 2>/dev/null
J() { python3 -c 'import json,sys;print(json.dumps({"session":sys.argv[1],"doc":sys.argv[2],"rev":"e3b0c44298fc1c14","text":open(sys.argv[3]).read().replace("@REPO@",sys.argv[4])}))' "$S" "$1" "$2" "$R"; }
api POST /api/term/feature/doc "$(J feature "$HERE/fixture/FEATURE.md")" > "$OUT/seed-feature.json"
api POST /api/term/feature/doc "$(J plan "$HERE/fixture/PLAN.md")" > "$OUT/seed-plan.json"
printf '{"session":"%s","thread":"%s","threadB":"%s","repo":"%s","c1":"%s","featureHead":"%s","p1":%s,"p2":%s,"spare":%s}\n' "$S" "$T" "$TB" "$R" "$C1" "$HEADF" $P1 $P2 "$(seq 3 23 | sed "s/.*/\"$S-mgr&\"/" | paste -sd, | sed "s/^/[/;s/$/]/")" > "$E/facts.json"
cp "$E/facts.json" "$OUT/"

log "suites: $FILES"
(cd "$HERE" && V8QA_OUT="$OUT" node --test --test-concurrency=1 --test-timeout=900000 \
  --test-reporter=spec --test-reporter-destination=stdout --test-reporter=junit --test-reporter-destination="$OUT/junit.xml" $FILES) 2>&1 | tee "$OUT/suite.txt"
RC=${PIPESTATUS[0]}
log "done rc=$RC: $OUT"; exit $RC
