// Tests for headlessCmd: telling a one-shot agent run (claude -p, codex exec) from an interactive one.

package main

import (
	"strings"
	"testing"
)

func TestHeadlessAgent(t *testing.T) {
	for cmd, want := range map[string]bool{
		"claude":                             false,
		"claude --resume abc":                false,
		"claude -p --model haiku --tools ":   true,
		"/usr/bin/codex exec do the thing":   true,
		"codex":                              false,
		"node /usr/lib/claude --print hello": true,
		"/home/x/.local/bin/claude --dangerously-skip-permissions --append-system-prompt " + strings.Repeat("You are running inside Cube. ", 40) + "-p --model haiku": true,
	} {
		if got := headlessCmd(cmd); got != want {
			t.Errorf("headlessAgent(%q) = %v, want %v", cmd, got, want)
		}
	}
}
