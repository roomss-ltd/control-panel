#!/bin/bash
# Runs the panel's Go tests without the four tests that drive the machine's shared browser (TestBrowserLive,
# TestBrowserPatches, TestBrowserDevTools, TestAgentWatch), so a run here never disturbs a live browser. Extra flags
# may not carry -run/-skip/-list (a later flag would override the guard), and the selection is listed and checked first.
# Usage (from the repo root): acceptance/v8/go-test-safe.sh '<run regex>|ALL' [-race|-count=N|-timeout=D|-v]...
set -u
SEL=${1:?run regex, or ALL for every test except the shared-browser ones}; shift
FORBID='^(TestBrowserLive|TestBrowserPatches|TestBrowserDevTools|TestAgentWatch)$'
for a in "$@"; do case "$a" in -run*|--run*|-skip*|--skip*|-list*|--list*|-test.*) echo "REFUSED: flag $a could override the guard"; exit 4;; -race|-v|-count=*|-timeout=*|-short|-failfast) ;; *) echo "REFUSED: flag $a not on the allowlist"; exit 4;; esac; done
if [ "$SEL" = ALL ]; then echo "selected: all tests except $(go test -list . . 2>/dev/null | grep -cE "$FORBID") shared-browser ones (-skip)"; exec go test -run . -skip "$FORBID" "$@" .; fi
list=$(go test -list "$SEL" . 2>&1) || { echo "go test -list failed:"; echo "$list"; exit 2; }
bad=$(echo "$list" | grep -E "$FORBID")
[ -n "$bad" ] && { echo "REFUSED: the selection includes shared-browser tests: $bad"; exit 3; }
echo "selected: $(echo "$list" | grep -c '^Test') tests (shared-browser tests excluded)"
exec go test -run "$SEL" -skip "$FORBID" "$@" .
