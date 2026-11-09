#!/bin/bash
# Runs the Cloudflare tunnel (your panel hostname -> the panel on 127.0.0.1:7777) in the foreground, meant
# for a terminal tab beside run-panel.sh. It starts the tunnel again if it exits. Ctrl-C stops it for good.
#
# Needs:
#   cloudflared                          on PATH
#   ~/.cloudflared/control-panel.token   the tunnel's token (mode 600), made with
#                                        `cloudflared tunnel token control-panel`; the tunnel's public hostname
#                                        must point at http://127.0.0.1:7777
#
# Run:  ./run-tunnel.sh        (from anywhere; it changes into its own directory)
cd "$(dirname "$0")" || exit 1
unset HTTPS_PROXY https_proxy HTTP_PROXY http_proxy # the tunnel must go direct

# take over from a background copy that ensure-panel.sh started (a login can start one whenever the tunnel
# is down for a moment); a copy started here never writes the pidfile, so only that one is stopped
takeover() {
  p=$(cat run/tunnel.pid 2>/dev/null)
  [ -n "$p" ] && [ "$(cat /proc/$p/comm 2>/dev/null)" = cloudflared ] && tr '\0' ' ' < /proc/$p/cmdline | grep -q -- "control-panel.token" && kill "$p" && sleep 0.5
}
while true; do
  takeover
  cloudflared tunnel --no-autoupdate --loglevel warn run --token-file "$HOME/.cloudflared/control-panel.token" --url http://127.0.0.1:7777
  echo "tunnel exited ($?): back in 2 s (Ctrl-C to stop)"
  sleep 2
done
