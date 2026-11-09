#!/bin/bash
# Keeps the control panel and its Cloudflare tunnel alive without systemd or cron: call it from your shell
# profile (for example ~/.zshrc) on every login. It is idempotent, so it is safe to run at any time, and it
# starts only what is not already running.
#
# Needs:
#   panel.env                            next to this script, with CF_TEAM and CF_AUD (copy panel.env.example)
#   ~/.cloudflared/control-panel.token   the tunnel's token (mode 600), made with
#                                        `cloudflared tunnel token control-panel`
#   go, cloudflared                      on PATH
# If either file is missing it prints what it needs and exits quietly (status 0).
#
# Run:
#   ./ensure-panel.sh           start whatever isn't running, in the background (logs and pidfiles in run/)
#   ./ensure-panel.sh restart   rebuild + restart the panel (the tunnel stays up)
#
# The panel and tunnel may instead run in terminal tabs (run-panel.sh, run-tunnel.sh): a copy running
# anywhere counts, so this never starts a second one, and restart only kills the panel: its tab brings it
# back.
cd "$(dirname "$0")" || exit 1
# agent sessions may export a local HTTPS proxy; the tunnel and the Access cert fetch must go direct
unset HTTPS_PROXY https_proxy HTTP_PROXY http_proxy
TOKEN="$HOME/.cloudflared/control-panel.token"   # `cloudflared tunnel token control-panel` (mode 600)
[ -f panel.env ] && [ -f "$TOKEN" ] || { echo "control-panel: needs panel.env (CF_TEAM, CF_AUD) and $TOKEN"; exit 0; }
. ./panel.env
mkdir -p run

# running COMM PATTERN: pids of processes named COMM whose command line has PATTERN, wherever they run
running() {
  for p in $(pgrep -x "$1"); do
    tr '\0' ' ' 2>/dev/null < "/proc/$p/cmdline" | grep -q -- "$2" && echo "$p"
  done
}

if [ "$1" = restart ]; then
  go build -o control-panel . || exit 1
  old=" $(running control-panel "-cf-team" | tr '\n' ' ')"
  if [ "$old" != " " ]; then
    kill $old
    [ "$old" = " $(cat run/panel.pid 2>/dev/null) " ] && old="" # ours, from the background: nothing brings it back
    [ -n "$old" ] && for _ in 1 2 3 4 5 6 7 8 9 10; do # a zellij tab's loop brings it back on its own
      sleep 0.5
      for p in $(running control-panel "-cf-team"); do
        case "$old " in *" $p "*) ;; *) echo "panel restarted by its tab (pid $p)"; exit 0 ;; esac
      done
    done
  fi
fi
[ -x control-panel ] || go build -o control-panel . || exit 1

start() {
  name=$1; comm=$2; pattern=$3; shift 3
  [ -n "$(running "$comm" "$pattern")" ] && return
  # all three fds redirected BEFORE the fork, so an ssh login that runs this is never held open
  nohup "$@" >> "run/$name.log" 2>&1 < /dev/null &
  echo $! > "run/$name.pid"
  echo "started $name (pid $!)"
}
start panel control-panel "-cf-team" ./control-panel -cf-team "$CF_TEAM" -cf-aud "$CF_AUD"
# the tunnel "control-panel" (your public hostname) → the panel on loopback
start tunnel cloudflared "control-panel.token" cloudflared tunnel --no-autoupdate --loglevel warn run --token-file "$TOKEN" --url http://127.0.0.1:7777
