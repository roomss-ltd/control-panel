package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLastTodos(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	line := func(todos string) string {
		return `{"type":"assistant","timestamp":"2026-11-05T01:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"x","name":"TodoWrite","input":{"todos":` + todos + `}}]}}` + "\n"
	}
	os.WriteFile(p, []byte(line(`[{"content":"old","status":"pending"}]`)+line(`[{"content":"write tests","status":"completed"},{"content":"wire the endpoint","status":"in_progress"}]`)), 0o600)
	got, at := lastTodos(p)
	if len(got) != 2 || got[1].Content != "wire the endpoint" || got[1].Status != "in_progress" || at == 0 {
		t.Fatalf("want the newest list: %+v %d", got, at)
	}
	if got, _ := lastTodos(filepath.Join(t.TempDir(), "none.jsonl")); got != nil {
		t.Fatalf("no transcript, no list: %+v", got)
	}
}
