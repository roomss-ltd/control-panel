package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAICardRendersVMAgentCards(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects/vm-agent/attachments")
	os.MkdirAll(filepath.Join(dir, "cards"), 0o755)
	os.MkdirAll(filepath.Join(dir, "node_modules/.bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "cards/metric-card.tsx"), []byte("x"), 0o644)
	runs := filepath.Join(home, "runs")
	os.WriteFile(filepath.Join(dir, "node_modules/.bin/tsx"), []byte("#!/bin/sh\necho run >> "+runs+"\nprintf '<div>%s %s %s</div>' \"$2\" \"$3\" \"$5\"\n"), 0o755)
	s := &server{cfg: config{home: home}}
	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		s.aiCard(w, httptest.NewRequest("POST", "/api/ai/card", strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	if code, b := post(`{"card":"metric-card","props":{"value":"84%"}}`); code != 200 || !strings.Contains(b, `metric-card --html {\"value\":\"84%\"}`) {
		t.Fatalf("%d %s", code, b)
	}
	post(`{"card":"metric-card","props":{"value":"84%"}}`)
	if b, _ := os.ReadFile(runs); strings.Count(string(b), "run") != 1 {
		t.Fatalf("the same card twice must render once: %q", b)
	}
	for _, bad := range []string{`{"card":"../x"}`, `{"card":"metric-card","props":[1]}`} {
		if code, _ := post(bad); code != 400 {
			t.Fatalf("%s: %d", bad, code)
		}
	}
	if code, _ := post(`{"card":"nope"}`); code != 404 {
		t.Fatalf("missing card: %d", code)
	}
}
