package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// State as an earlier version of the panel wrote it (made up here, no real state copied), plus fields from some
// later version and a field of the wrong type: the current panel starts on it without losing or rewriting what it means.
func TestLegacyStateFromDeployedPanel(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	// threads.json in the earlier shape (no teamSeen): a done thread, a working one with only a threads.json plan, an
	// archived one; one carries a field this version doesn't know
	writeFile(t, filepath.Join(dir, "threads.json"), fmt.Sprintf(`[
 {"id":"legacy-done","title":"Shipped before the gate","status":"done","note":"merged","team":"legacy-done","created":%d,"updated":%d,
  "log":[{"t":%d,"kind":"status","text":"done"}],"plan":[{"text":"Ship it","state":"done"}]},
 {"id":"legacy-working","title":"Still going","status":"working","team":"legacy-w","created":%d,"updated":%d,
  "plan":[{"text":"Step A","state":"doing"},{"text":"Step B","state":"todo"}],"futureField":{"x":1}},
 {"id":"legacy-old","title":"Old","status":"archived","team":"","created":%d,"updated":%d}
]`, now-900, now-800, now-800, now-700, now-600, now-5000, now-4000))
	// notes.json in the earlier notifier shape (notes, seen, next; no floor, no ref/exit on notes): more than the 200 it keeps
	var ns []string
	for i := int64(1); i <= 230; i++ {
		ns = append(ns, fmt.Sprintf(`{"id":%d,"t":%d,"kind":"done","level":"ok","title":"note %d","go":"pane:legacy-w:3"}`, i, now-1000+i, i))
	}
	writeFile(t, filepath.Join(dir, "notes.json"), `{"notes":[`+strings.Join(ns, ",")+`],"seen":200,"next":231,"later":"x"}`)
	// a supervisor file from some later version, with a field of the wrong type: ignored, never a crash
	writeFile(t, filepath.Join(dir, "featsup.json"), `{"holds":5,"future":{"a":1}}`)
	// the done feature's record: prose FEATURE.md (no outcomes) and a ticked plan, as before the gate
	writeFile(t, filepath.Join(dir, "jarvis-files", "features", "legacy-done", "FEATURE.md"), "# Shipped\nIt works.\n")
	writeFile(t, filepath.Join(dir, "jarvis-files", "features", "legacy-done", "PLAN.md"), "- [x] Ship it\n")

	s := newServer(config{addr: "127.0.0.1:0", auditFile: filepath.Join(dir, "audit.jsonl"), ciDir: filepath.Join(dir, "ci"),
		statusDir: filepath.Join(dir, "status"), home: dir, pinFile: filepath.Join(dir, "pin")})
	s.sys.sockDir = filepath.Join(dir, "zellij-sockets")
	os.MkdirAll(s.sys.sockDir, 0o755)

	status := func(id string) (string, *thread) {
		s.ai.mu.Lock()
		defer s.ai.mu.Unlock()
		th := s.ai.thread(id)
		if th == nil {
			return "", nil
		}
		c := *th
		return c.Status, &c
	}
	for id, want := range map[string]string{"legacy-done": "done", "legacy-working": "working", "legacy-old": "archived"} {
		if got, _ := status(id); got != want {
			t.Fatalf("%s loaded as %q", id, got)
		}
	}
	// a done from before the done gate existed is not flipped (on looks and reads), only shown as no longer holding
	s.reopenStale()
	s.featReopen("legacy-done")
	if st, _ := status("legacy-done"); st != "done" {
		t.Fatalf("a pre-gate done was reopened on deploy: %s", st)
	}
	if d := s.featDossier("legacy-done"); d.DoneHistorical == "" || d.DoneStale != "" {
		t.Fatalf("the pre-gate done isn't shown as historical/unverified: historical=%q stale=%q", d.DoneHistorical, d.DoneStale)
	}
	// the working thread's plan (threads.json only) is what the record shows until a PLAN.md exists
	d := s.featDossier("legacy-w")
	if len(d.Milestones) != 1 || len(d.Milestones[0].Steps) != 2 || d.Milestones[0].Steps[0].State != "doing" {
		t.Fatalf("the legacy plan: %+v", d.Milestones)
	}
	// every legacy note survives the first notes after the upgrade (no retention floor known yet)
	if err := s.notes.push("thread", "ok", "first after deploy", "", "thread:legacy-working"); err != nil {
		t.Fatal(err)
	}
	if n := len(s.notes.since(0)); n != 231 {
		t.Fatalf("notes after the first new one: %d, want 231", n)
	}
	// the supervisor with a malformed file: empty maps, usable, and saving it works
	s.sup.mu.Lock()
	ok := s.sup.Holds != nil && s.sup.Completed != nil && s.sup.save() == nil
	s.sup.mu.Unlock()
	if !ok {
		t.Fatal("the supervisor didn't recover from its malformed file")
	}
	// the first write keeps every known field; the unknown one is dropped, nothing else changes
	if _, _, err := s.ai.updateThread("legacy-working", map[string]*string{"note": sptr("after deploy")}); err != nil {
		t.Fatal(err)
	}
	var saved []map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "threads.json"))
	if err := json.Unmarshal(b, &saved); err != nil || len(saved) != 3 {
		t.Fatalf("threads.json after the first write: %v %s", err, b)
	}
	for _, th := range saved {
		if th["id"] == "legacy-done" && (th["status"] != "done" || th["note"] != "merged") {
			t.Fatalf("an untouched legacy thread changed: %v", th)
		}
		if th["id"] == "legacy-working" && len(th["plan"].([]any)) != 2 {
			t.Fatalf("the legacy plan was lost on save: %v", th)
		}
	}
	// a gated done from now on does reopen when its record changes (the contrast)
	s.featCompleted("legacy-done", true)
	s.featReopen("legacy-done")
	if st, _ := status("legacy-done"); st != "working" {
		t.Fatalf("a gated done that no longer holds stayed %s", st)
	}
}

func sptr(s string) *string { return &s }
