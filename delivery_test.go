package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// leadHermes: a Hermes that stores what a thread's lead is sent, with a switchable answer to a turn:
// ok (kept), 503 (refused before anything is stored), hang (stored, then no answer until released or the
// client goes) and fail (stored, then the turn fails: kept says no though the history has it).
type leadHermes struct {
	mu      sync.Mutex
	mode    string
	hist    map[string][]string // session -> its user messages
	posts   int                 // turns asked for
	release chan struct{}
}

func (f *leadHermes) set(mode string) { f.mu.Lock(); f.mode = mode; f.mu.Unlock() }

func (f *leadHermes) count(sess, text string) (posts, stored int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.hist[sess] {
		if m == text {
			stored++
		}
	}
	return f.posts, stored
}

func newLeadHermes(t *testing.T) (*leadHermes, string) {
	f := &leadHermes{mode: "ok", hist: map[string][]string{}, release: make(chan struct{})}
	done := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/detailed", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"platforms":{}}`) })
	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var l []hSession
		if r.URL.Query().Get("source") != "telegram" {
			for id, ms := range f.hist {
				l = append(l, hSession{ID: id, Source: "api_server", Count: len(ms), LastActive: float64(len(ms))})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": l})
	})
	mux.HandleFunc("GET /api/sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		l := []map[string]any{}
		if r.URL.Query().Get("offset") == "0" {
			for i, m := range f.hist[r.PathValue("id")] {
				l = append(l, map[string]any{"id": i + 1, "role": "user", "content": m, "timestamp": i + 1})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": l})
	})
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	mux.HandleFunc("POST /api/sessions/{id}/chat/stream", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Message string }
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		mode := f.mode
		f.hist[r.PathValue("id")] = append(f.hist[r.PathValue("id")], b.Message)
		f.mu.Unlock()
		switch mode {
		case "hang":
			select {
			case <-f.release:
			case <-r.Context().Done():
				return
			case <-done:
				return
			}
		case "fail":
			fmt.Fprint(w, "event: run.failed\ndata: {\"error\":\"provider fell over\"}\n\n")
			return
		}
		fmt.Fprint(w, "event: assistant.completed\ndata: {\"content\":\"ok\"}\n\nevent: run.completed\ndata: {\"messages\":[1]}\n\n")
	})
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if r.Method == "POST" && r.URL.Path == "/api/sessions" { // each turn opens its session first
			f.posts++
		}
		mode := f.mode
		f.mu.Unlock()
		if mode == "503" {
			http.Error(w, "offline", 503)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(h.Close)
	t.Cleanup(func() { close(done) }) // runs first: a hung turn lets go before the server closes
	return f, h.URL
}

type deliveryEnv struct {
	e   *testEnv
	f   *leadHermes
	a   *assistant
	now time.Time
}

const leadID = "lead-test"

func newDeliveryEnv(t *testing.T) *deliveryEnv {
	t.Helper()
	e := newTestServer(t)
	f, base := newLeadHermes(t)
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	os.WriteFile(key, []byte("k\n"), 0o600)
	a := newAssistant(base, key, "")
	a.threadsFile = filepath.Join(dir, "threads.json")
	a.leadFile = filepath.Join(dir, "LEAD.md")
	os.WriteFile(a.leadFile, []byte("Lead of {title}."), 0o600)
	e.s.ai = a
	a.mu.Lock()
	th := a.newThread("Lead test", "")
	th.Status, th.Created = "working", 1
	a.saveThreads()
	a.mu.Unlock()
	v := &deliveryEnv{e: e, f: f, a: a, now: time.Now()}
	v.e.s.teamDeliver(v.now) // the cursor starts here
	return v
}

// news: a team agent finished; the note's id
func (v *deliveryEnv) news(title string) int64 {
	v.e.s.notes.push("done", "ok", title, "", "pane:"+leadID+":3")
	return v.e.s.notes.lastID()
}

func (v *deliveryEnv) tick(after time.Duration) {
	v.now = v.now.Add(after)
	v.e.s.teamDeliver(v.now)
}

func (v *deliveryEnv) seen() int64 {
	v.a.mu.Lock()
	defer v.a.mu.Unlock()
	return v.a.thread(leadID).TeamSeen
}

// outbox: the hand-off saved for the lead, as a restart reads it
func (v *deliveryEnv) outbox(t *testing.T) (teamHand, bool) {
	t.Helper()
	m := map[string]teamHand{}
	if b, err := os.ReadFile(v.a.threadsFile + ".outbox.json"); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
	}
	h, ok := m[leadID]
	return h, ok
}

// restart: a new assistant from the saved threads; the old one can't write them any more
func (v *deliveryEnv) restart() {
	old := v.a
	old.mu.Lock()
	file, lead := old.threadsFile, old.leadFile
	old.threadsFile = ""
	old.mu.Unlock()
	b := newAssistant(old.base, old.keyFile, "")
	b.threadsFile, b.leadFile = file, lead
	b.loadThreads()
	v.a, v.e.s.ai = b, b
}

func (v *deliveryEnv) waitPosts(t *testing.T, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if p, _ := v.f.count("", ""); p >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("never %d turns", n)
		}
	}
}

// (a) Hermes refusing the turn (503) isn't delivery: the cursor stays, the hand-off is saved; it goes again after
// its backoff and, once kept, the cursor moves and it's never sent again
func TestDeliver503ThenSuccess(t *testing.T) {
	v := newDeliveryEnv(t)
	v.f.set("503")
	id := v.news("builder finished")
	v.tick(0)
	waitIdle(t, v.a, leadID)
	if v.seen() >= id {
		t.Fatalf("cursor %d moved past #%d though Hermes refused it", v.seen(), id)
	}
	h, ok := v.outbox(t)
	if !ok || h.UpTo < id || h.Attempts != 1 || !strings.Contains(h.Text, fmt.Sprintf("- #%d done: builder finished [%s:3]", id, leadID)) ||
		!strings.HasPrefix(h.Text, threadFrom("team")) {
		t.Fatalf("outbox %+v %v", h, ok)
	}
	v.tick(10 * time.Second) // within its backoff
	if p, _ := v.f.count("", ""); p != 1 {
		t.Fatalf("retried within the backoff: %d turns", p)
	}
	v.f.set("ok")
	v.tick(30 * time.Second)
	waitIdle(t, v.a, leadID)
	if v.seen() != id {
		t.Fatalf("cursor %d after delivery, want %d", v.seen(), id)
	}
	if _, ok := v.outbox(t); ok {
		t.Fatal("outbox kept a delivered hand-off")
	}
	for range 3 {
		v.tick(time.Hour)
	}
	if p, stored := v.f.count(threadPrefix+leadID, h.Text); p != 2 || stored != 1 {
		t.Fatalf("turns %d, stored %d: want 2 asked, 1 kept", p, stored)
	}
	v.a.mu.Lock()
	lg := v.a.thread(leadID).Log
	v.a.mu.Unlock()
	if len(lg) == 0 || lg[len(lg)-1].Kind != "team" || lg[len(lg)-1].Text != "builder finished" {
		t.Fatalf("timeline %+v", lg)
	}
}

// (b) Hermes kept the message but its answer never came (a crash, a lost response): after a restart the lead's
// history shows it, so it's acknowledged without going again. A turn that's stored and then fails is the same.
func TestDeliverLostResponseAckedFromHistory(t *testing.T) {
	v := newDeliveryEnv(t)
	v.f.set("hang")
	id := v.news("builder finished")
	v.tick(0)
	h, ok := v.outbox(t)
	if !ok || h.Attempts != 1 || v.seen() >= id {
		t.Fatalf("before the restart: outbox %+v %v, cursor %d", h, ok, v.seen())
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) { // Hermes has stored it
		if _, stored := v.f.count(threadPrefix+leadID, h.Text); stored == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never stored")
		}
	}
	old := v.a
	v.restart()
	v.f.set("ok")
	v.tick(0) // within the saved backoff
	if v.seen() >= id {
		t.Fatal("acknowledged without a look at the history")
	}
	v.tick(31 * time.Second)
	if p, stored := v.f.count(threadPrefix+leadID, h.Text); p != 1 || stored != 1 || v.seen() != id {
		t.Fatalf("turns %d stored %d cursor %d: want it acknowledged from the history, not sent again", p, stored, v.seen())
	}
	if _, ok := v.outbox(t); ok {
		t.Fatal("outbox kept it")
	}
	b := newAssistant(v.a.base, v.a.keyFile, "")
	b.threadsFile = v.a.threadsFile
	b.loadThreads()
	if b.thread(leadID).TeamSeen != id {
		t.Fatalf("cursor on disk %d", b.thread(leadID).TeamSeen)
	}
	old.stop(leadID)
	waitIdle(t, old, leadID)

	// stored, then the turn failed: kept is false, so not acknowledged then; its retry finds it and doesn't resend
	v.f.set("fail")
	id2 := v.news("tester finished")
	v.tick(0)
	waitIdle(t, v.a, leadID)
	if v.seen() >= id2 {
		t.Fatal("a failed turn acknowledged")
	}
	v.f.set("ok")
	v.tick(31 * time.Second)
	if p, _ := v.f.count("", ""); p != 2 || v.seen() != id2 {
		t.Fatalf("turns %d cursor %d", p, v.seen())
	}
}

// (c) a restart with a hand-off pending that the lead's history doesn't have: it goes again, once
func TestDeliverRestartResendsWhenNotInHistory(t *testing.T) {
	v := newDeliveryEnv(t)
	v.f.set("503")
	id := v.news("builder finished")
	v.tick(0)
	waitIdle(t, v.a, leadID)
	h, _ := v.outbox(t)
	v.restart()
	v.f.set("ok")
	v.tick(0)
	if p, _ := v.f.count("", ""); p != 1 {
		t.Fatalf("the saved backoff ignored: %d turns", p)
	}
	v.tick(31 * time.Second)
	waitIdle(t, v.a, leadID)
	for range 3 {
		v.tick(time.Hour)
	}
	if p, stored := v.f.count(threadPrefix+leadID, h.Text); p != 2 || stored != 1 || v.seen() != id {
		t.Fatalf("turns %d stored %d cursor %d", p, stored, v.seen())
	}
}

// (d) the owner taking the hand-off out of the lead's queue isn't delivery: it goes again
func TestDeliverUnqueueIsNotAck(t *testing.T) {
	v := newDeliveryEnv(t)
	v.a.mu.Lock()
	cv := v.a.conv(leadID)
	cv.live = &aiLive{} // the lead is busy
	v.a.mu.Unlock()
	id := v.news("builder finished")
	v.tick(0)
	v.a.mu.Lock()
	if len(cv.queue) != 1 {
		v.a.mu.Unlock()
		t.Fatalf("queue %+v", cv.queue)
	}
	qid := cv.queue[0].ID
	v.a.mu.Unlock()
	if !v.a.unqueue(leadID, qid) {
		t.Fatal("unqueue")
	}
	v.tick(0)
	if v.seen() >= id {
		t.Fatal("unqueued, yet acknowledged")
	}
	if _, ok := v.outbox(t); !ok {
		t.Fatal("unqueued, and the outbox forgot it")
	}
	v.a.mu.Lock()
	cv.live, cv.queue = nil, nil // the lead frees up
	v.a.mu.Unlock()
	v.tick(time.Second)
	waitIdle(t, v.a, leadID)
	v.tick(time.Hour)
	if p, _ := v.f.count("", ""); p != 1 || v.seen() != id {
		t.Fatalf("turns %d cursor %d", p, v.seen())
	}
}

// (e) the notes a lead hasn't acknowledged outlive the 200 cap; past the hard cap all stay and the store says it's full
func TestNotesKeepUndelivered(t *testing.T) {
	v := newDeliveryEnv(t)
	v.f.set("503")
	floor := max(v.seen(), 0)
	id := v.news("builder finished")
	v.tick(0)
	waitIdle(t, v.a, leadID)
	for i := range 300 {
		v.e.s.notes.push("ci", "bad", fmt.Sprint("CI failed ", i), "", "ci")
	}
	v.tick(0) // still undelivered (backoff)
	ns := v.e.s.notes.since(floor)
	if top := v.e.s.notes.lastID(); int64(len(ns)) != top-floor || ns[0].ID != id {
		t.Fatalf("notes above the cursor %d: %d of %d, first %d", floor, len(ns), top-floor, ns[0].ID)
	}
	v.f.set("ok")
	v.tick(time.Minute)
	waitIdle(t, v.a, leadID)
	v.tick(0) // acknowledged: the floor moves up and the store compacts as before
	v.e.s.notes.push("ci", "bad", "one more", "", "ci")
	if n := len(v.e.s.notes.since(0)); n != maxNotes {
		t.Fatalf("after delivery %d notes", n)
	}

	n := newNotifier("")
	n.setFloor(5)
	for range fullNotes + 50 {
		n.add(time.Now(), "ci", "bad", "x", "", "ci")
	}
	if len(n.Notes) != fullNotes+45 || !n.stuffed || n.Notes[0].ID != 6 { // 1-5 acknowledged: compacted
		t.Fatalf("pending past the hard cap: %d notes, full %v", len(n.Notes), n.stuffed)
	}
	n.setFloor(math.MaxInt64)
	n.add(time.Now(), "ci", "bad", "x", "", "ci")
	if len(n.Notes) != maxNotes || n.stuffed {
		t.Fatalf("after: %d notes, full %v", len(n.Notes), n.stuffed)
	}
}

// (f) a hand-off in its lead's conversation isn't sent again however many ticks pass, nor once it's delivered
func TestDeliverNoDuplicate(t *testing.T) {
	v := newDeliveryEnv(t)
	v.f.set("hang")
	id := v.news("builder finished")
	for range 5 {
		v.tick(time.Hour)
	}
	v.waitPosts(t, 1)
	close(v.f.release)
	waitIdle(t, v.a, leadID)
	for range 5 {
		v.tick(time.Hour)
	}
	if p, _ := v.f.count("", ""); p != 1 || v.seen() != id {
		t.Fatalf("turns %d cursor %d", p, v.seen())
	}
	v.f.mu.Lock()
	n := len(v.f.hist[threadPrefix+leadID])
	v.f.mu.Unlock()
	if n != 1 {
		t.Fatalf("stored %d", n)
	}
}
