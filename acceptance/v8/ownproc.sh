#!/bin/bash
# Owned-process ledger for scratch runs (used by run.sh): one ownership protocol for everything a run starts, so cleanup
# never touches a process that is not the run's own: PID + kernel start time + cmdline, or this run's exact private TMPDIR.
# A process is ours only if its PID AND its kernel start time
# (/proc/PID/stat field 22) match what we recorded when we started it (a reused PID has a different start time).
#   ownproc.sh rec LEDGER PID           record PID + start time + cmdline
#   ownproc.sh env LEDGER TMPDIR        record every process of ours whose environment has exactly TMPDIR=<dir>
#                                       (the zellij server daemonizes away from the panel; its private TMPDIR is its identity)
#   ownproc.sh kill LEDGER              TERM, then KILL, each recorded process that still matches, plus its descendants
#                                       (each descendant's identity is read at kill time; nothing is matched by name or port)
set -u
st() { [ -r /proc/$1/stat ] && sed 's/^.*) //' /proc/$1/stat | awk '{print $20}'; } # field 22 = 20th after "comm) "
cmd() { tr '\0' ' ' < /proc/$1/cmdline 2>/dev/null | cut -c1-120; }
case $1 in
rec) s=$(st "$3") && [ -n "$s" ] && echo "$3 $s $(cmd "$3")" >> "$2" ;;
env) for p in /proc/[0-9]*; do p=${p#/proc/}; [ -O /proc/$p ] || continue
       2>/dev/null tr '\0' '\n' < /proc/$p/environ | grep -qx "TMPDIR=$3" || continue
       grep -q "^$p $(st $p) " "$2" 2>/dev/null || echo "$p $(st $p) $(cmd $p)" >> "$2"; done ;;
kill)
  [ -f "$2" ] || exit 0
  kids() { for c in $(cat /proc/$1/task/*/children 2>/dev/null); do echo "$c $(st $c)"; kids "$c"; done; }
  todo=()
  while read -r pid start rest; do
    [ "$(st "$pid")" = "$start" ] || { echo "ownproc: $pid gone or reused (start $start), not touched" >&2; continue; }
    todo+=("$pid $start"); while read -r k ks; do [ -n "$k" ] && todo+=("$k $ks"); done < <(kids "$pid")
  done < "$2"
  for sig in TERM KILL; do
    for e in "${todo[@]}"; do set -- $e; [ "$(st "$1")" = "$2" ] && kill -$sig "$1" 2>/dev/null; done
    [ $sig = TERM ] && sleep 1
  done
  for e in "${todo[@]}"; do set -- $e; [ "$(st "$1")" = "$2" ] && echo "ownproc: $1 still alive" >&2; done; true ;;
esac
