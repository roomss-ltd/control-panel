package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The feature page's "reconciledAt": when the supervisor last completed a reconciliation pass over this feature (persisted; 0: never).
// A GET reads it and writes nothing; a look skipped inside supEvery doesn't advance it; a restart keeps it.
func TestV8ReconciledAt(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	h := v.e.authed(v.e.login(t))
	page := func() int64 {
		var out struct{ Page map[string]any }
		decodeBody(t, v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", h), &out)
		n, ok := out.Page["reconciledAt"].(float64)
		if !ok {
			t.Fatalf("no page.reconciledAt: %v", out.Page["reconciledAt"])
		}
		return int64(n)
	}
	if n := page(); n != 0 {
		t.Fatalf("before any pass: %d", n)
	}
	v.now = time.Unix(1_600_000_000, 0) // a controlled clock far from the wall clock: the value is the pass's own time
	v.look(v.agent("working", v.now.Unix()))
	first := v.now.Unix()
	if n := page(); n != first {
		t.Fatalf("after a pass at %d: %d", first, n)
	}
	before, _ := os.ReadFile(v.e.s.sup.file)
	page()
	if after, _ := os.ReadFile(v.e.s.sup.file); !bytes.Equal(before, after) {
		t.Fatal("a GET wrote the supervisor's state")
	}
	tm := v.e.s.term.last
	v.e.s.observeTeams(v.now.Add(5*time.Second), tm, CI{}, Resources{}) // inside supEvery: no pass
	if n := page(); n != first {
		t.Fatalf("a skipped look advanced it: %d", n)
	}
	if after, _ := os.ReadFile(v.e.s.sup.file); !bytes.Equal(before, after) {
		t.Fatal("a skipped tick wrote the supervisor's state")
	}
	file := v.e.s.sup.file
	v.e.s.sup.file = filepath.Join(t.TempDir(), "gone", "featsup.json") // the save fails
	v.look(v.agent("working", v.now.Unix()))
	if n := page(); n != first {
		t.Fatalf("an unsaved pass showed as fresh: %d (want the last saved %d)", n, first)
	}
	v.e.s.sup.file = file
	v.e.s.sup = newFeatSup(v.e.s.sup.file) // a restart
	if n := page(); n != first {
		t.Fatalf("after a restart: %d (want %d)", n, first)
	}
	v.look(v.agent("working", v.now.Unix()))
	if n := page(); n != v.now.Unix() || n <= first {
		t.Fatalf("a later pass: %d", n)
	}
}
