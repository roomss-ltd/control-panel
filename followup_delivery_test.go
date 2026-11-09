package main

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// historyDown makes Hermes, as v.a sees it from now on, accept POSTs but answer every read of its sessions with a 503.
func historyDown(t *testing.T, v *deliveryEnv) (restore func()) {
	target, _ := url.Parse(v.a.base)
	proxy := httputil.NewSingleHostReverseProxy(target)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/sessions") {
			http.Error(w, "history unavailable", 503)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	old := v.a.base
	v.a.base = hs.URL
	return func() { v.a.base = old }
}

// A hand-off that may be in Hermes (its turn was stored, then failed) isn't sent again while the lead's history
// can't be read: it stays pending, says its delivery is unknown, and is acknowledged once the history shows it.
func TestFollowupUnknownHistoryDuplicates(t *testing.T) {
	v := newDeliveryEnv(t)
	v.f.set("fail")
	id := v.news("critical delivery")
	v.tick(0)
	waitIdle(t, v.a, leadID)
	h, ok := v.outbox(t)
	if !ok {
		t.Fatal("outbox missing")
	}
	restore := historyDown(t, v)
	v.f.set("ok")
	v.tick(time.Minute)
	waitIdle(t, v.a, leadID)
	if _, stored := v.f.count(threadPrefix+leadID, h.Text); stored != 1 {
		t.Fatalf("sent again with its history unreadable: stored %d", stored)
	}
	if h, ok := v.outbox(t); !ok || h.Unknown == 0 || v.seen() >= id {
		t.Fatalf("outbox %+v %v, cursor %d: want it pending and marked unknown", h, ok, v.seen())
	}
	if u := v.e.s.deliveryUnknown(); len(u) != 1 || u[0] != leadID {
		t.Fatalf("not surfaced: %v", u)
	}
	v.tick(time.Second) // within the backoff of the failed look: not even looked at again
	restore()
	v.tick(time.Hour)
	if p, stored := v.f.count(threadPrefix+leadID, h.Text); p != 1 || stored != 1 || v.seen() != id {
		t.Fatalf("turns %d stored %d cursor %d: want it acknowledged from the history", p, stored, v.seen())
	}
	if u := v.e.s.deliveryUnknown(); len(u) != 0 {
		t.Fatalf("still unknown: %v", u)
	}
}

// A first send Hermes refused before storing anything (503) still goes again though the history can't be read.
func TestFollowupRefusedRetriesWithoutHistory(t *testing.T) {
	v := newDeliveryEnv(t)
	v.f.set("503")
	id := v.news("builder finished")
	v.tick(0)
	waitIdle(t, v.a, leadID)
	historyDown(t, v)
	v.f.set("ok")
	v.tick(time.Minute)
	waitIdle(t, v.a, leadID)
	if p, _ := v.f.count("", ""); p != 2 || v.seen() != id {
		t.Fatalf("turns %d cursor %d", p, v.seen())
	}
}

// A restart keeps the notes a lead hasn't acknowledged, before anything tells it who's behind.
func TestFollowupRestartRetention(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notes.json")
	n := newNotifier(file)
	n.setFloor(0)
	for range 201 {
		n.push("feature", "warn", "pending", "", "thread:lead")
	}
	n = newNotifier(file)
	n.push("feature", "warn", "new after restart", "", "thread:lead")
	if len(n.Notes) != 202 || n.Notes[0].ID != 1 {
		t.Fatalf("retention after restart: %d notes, first #%d", len(n.Notes), n.Notes[0].ID)
	}
	n.setFloor(150) // acknowledged up to #150: saved, so after a restart those compact and the rest stay
	n = newNotifier(file)
	n.push("feature", "warn", "another", "", "thread:lead")
	if n.Floor != 150 || len(n.Notes) != maxNotes || n.Notes[0].ID != 4 {
		t.Fatalf("floor %d after restart: %d notes, first #%d", n.Floor, len(n.Notes), n.Notes[0].ID)
	}
	n = newNotifier(filepath.Join(t.TempDir(), "notes.json")) // nothing known yet: nothing compacted
	for range 201 {
		n.push("feature", "warn", "x", "", "thread:lead")
	}
	if len(n.Notes) != 201 {
		t.Fatalf("a fresh store compacted before its floor was known: %d", len(n.Notes))
	}
}

// A raised event whose note couldn't be saved isn't marked sent: raised again (after a restart too), it's noted.
func TestFollowupRaiseLostOnSaveFailure(t *testing.T) {
	v := newSupEnv(t, "")
	parent := filepath.Join(t.TempDir(), "not-directory")
	writeFile(t, parent, "x") // a file as the notes' folder: no save can succeed
	v.e.s.notes.file = filepath.Join(parent, "notes.json")
	before := len(v.e.s.notes.Notes)
	th := v.a.thread(supTeam)
	v.e.s.raise(v.now, th, true, "lost-critical", "bad", "critical blocker", "must persist")
	full := supTeam + "/lost-critical"
	if v.e.s.sup.sentAt(full) != 0 {
		t.Fatal("marked sent though its note wasn't saved")
	}
	if len(v.e.s.notes.Notes) != before {
		t.Fatal("an unsaved note kept in memory")
	}
	v.e.s.sup = newFeatSup(v.e.s.sup.file)
	v.e.s.notes = newNotifier(filepath.Join(t.TempDir(), "fresh-notes.json"))
	v.e.s.raise(v.now, th, true, "lost-critical", "bad", "critical blocker", "must persist")
	if len(v.e.s.notes.Notes) != 1 || v.e.s.sup.sentAt(full) == 0 {
		t.Fatalf("after the restart: %d notes, sent %d", len(v.e.s.notes.Notes), v.e.s.sup.sentAt(full))
	}
}
