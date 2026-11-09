package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// threadHermes: a Hermes with Main's panel session and one thread's, recording every turn's body by session.
func threadHermes(t *testing.T) (*httptest.Server, func(sess string) []map[string]any) {
	var mu sync.Mutex
	turns := map[string][]map[string]any{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/detailed", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"platforms":{}}`) })
	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("source") == "telegram" {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"panel","source":"api_server","message_count":2,"last_active":3},{"id":"thread-kpi-v2","source":"api_server","message_count":2,"last_active":9}]}`)
	})
	mux.HandleFunc("GET /api/sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "panel":
			fmt.Fprint(w, `{"data":[{"id":1,"role":"user","content":"port KPI v2 to Atlas","timestamp":1},{"id":2,"role":"assistant","content":"That is a big one.","timestamp":2}]}`)
		case "thread-kpi-v2":
			fmt.Fprint(w, `{"data":[{"id":1,"role":"user","content":"plan it","timestamp":8},{"id":2,"role":"assistant","content":"Plan: 3 agents.","timestamp":9}]}`)
		}
	})
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	mux.HandleFunc("POST /api/sessions/{id}/chat/stream", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		turns[r.PathValue("id")] = append(turns[r.PathValue("id")], b)
		mu.Unlock()
		fmt.Fprint(w, "event: assistant.completed\ndata: {\"content\":\"ok\"}\n\nevent: run.completed\ndata: {\"messages\":[1]}\n\n")
	})
	h := httptest.NewServer(mux)
	t.Cleanup(h.Close)
	return h, func(sess string) []map[string]any {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			mu.Lock()
			n := len(turns[sess])
			mu.Unlock()
			if n > 0 {
				break
			}
		}
		mu.Lock()
		defer mu.Unlock()
		return turns[sess]
	}
}

func threadAssistant(t *testing.T) (*assistant, func(string) []map[string]any) {
	h, turns := threadHermes(t)
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	os.WriteFile(key, []byte("k\n"), 0o600)
	a := newAssistant(h.URL, key, "")
	a.threadsFile = filepath.Join(dir, "threads.json")
	a.leadFile = filepath.Join(dir, "LEAD.md")
	os.WriteFile(a.leadFile, []byte("Lead of {title} ({id}); team {team}; branch {branch}; repo {repo}."), 0o600)
	return a, turns
}

func waitIdle(t *testing.T, a *assistant, c string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		a.mu.Lock()
		cv := a.conv(c)
		idle := cv.live == nil && len(cv.queue) == 0
		a.mu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the turn never finished")
		}
	}
}

func TestThreadSlug(t *testing.T) {
	for in, want := range map[string]string{
		"KPI v2: port Atlas & Orbit (dev → stage)": "kpi-v2-port-atlas-orbit",
		"   ":                            "thread",
		"Ünïcode only ✨":                 "n-code-only",
		strings.Repeat("abcdefghij ", 3): "abcdefghij-abcdefghij-abcdefghij",
	} {
		if got := threadSlug(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	a, _ := threadAssistant(t)
	a.mu.Lock()
	x, y := a.newThread("Fix login", ""), a.newThread("fix  LOGIN", "")
	a.mu.Unlock()
	if x.ID != "fix-login" || y.ID != "fix-login-2" || y.Team != "fix-login-2" || y.Status != "planning" {
		t.Fatalf("ids %+v %+v", x, y)
	}
}

func TestThreadItemsAndTurns(t *testing.T) {
	a, turns := threadAssistant(t)
	a.mu.Lock()
	a.newThread("KPI v2", "panel:2")
	a.saveThreads()
	a.mu.Unlock()
	a.refresh(context.Background())
	a.mu.Lock()
	main, th := a.items, a.thItems["kpi-v2"]
	v := a.threadViews()
	a.mu.Unlock()
	if len(main) != 2 || main[0].Text != "port KPI v2 to Atlas" {
		t.Fatalf("Main must not show a thread's messages: %+v", main)
	}
	if len(th) != 2 || th[1].Ch != "thread" || th[1].Text != "Plan: 3 agents." {
		t.Fatalf("thread items %+v", th)
	}
	if len(v) != 1 || v[0].Replies != 2 || v[0].Last != 9 {
		t.Fatalf("views %+v", v)
	}

	// a thread turn goes to its own session with the playbook; Main's carries the open threads
	if _, err := a.send("kpi-v2", "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.send("nope", "go"); err != errNoThread {
		t.Fatalf("an unknown thread: %v", err)
	}
	got := turns("thread-kpi-v2")
	if len(got) != 1 || got[0]["message"] != "go" || got[0]["instructions"] != "Lead of KPI v2 (kpi-v2); team kpi-v2; branch feature/kpi-v2; repo (not chosen yet)." {
		t.Fatalf("thread turn %v", got)
	}
	a.send("", "status?")
	got = turns("panel")
	if len(got) != 1 || !strings.Contains(got[0]["instructions"].(string), `- kpi-v2 "KPI v2" planning`) {
		t.Fatalf("main turn %v", got)
	}
	waitIdle(t, a, "kpi-v2")
	waitIdle(t, a, "")

	// the threads survive a restart
	b := newAssistant(a.base, a.keyFile, "")
	b.threadsFile = a.threadsFile
	b.loadThreads()
	if len(b.threads) != 1 || b.threads[0].ID != "kpi-v2" {
		t.Fatalf("reloaded %+v", b.threads)
	}
}

func TestThreadContext(t *testing.T) {
	a, _ := threadAssistant(t)
	a.refresh(context.Background())
	a.mu.Lock()
	ctx := a.threadContext("panel:2")
	none := a.threadContext("gone")
	a.mu.Unlock()
	if none != "" || !strings.HasPrefix(ctx, "[Thread context]") || !strings.Contains(ctx, "(Main JARVIS, ") ||
		!strings.Contains(ctx, "That is a big one.") || !strings.Contains(ctx, "The owner: port KPI v2 to Atlas") || !strings.HasSuffix(ctx, "[/Thread context]\n\n") {
		t.Fatalf("context:\n%s", ctx)
	}
}

func TestAgentThreadEndpoints(t *testing.T) {
	e := newTestServer(t)
	a, turns := threadAssistant(t)
	e.s.ai = a
	call := func(h http.HandlerFunc, body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if c, _ := call(e.s.agentThreadOpen, `{"title":"x"}`); c != 400 {
		t.Fatalf("no brief: %d", c)
	}
	c, out := call(e.s.agentThreadOpen, `{"title":"Checkout v2 Flow","brief":"Port it.","repo":"~/repos/acme-app"}`)
	if c != 200 || out["thread"].(map[string]any)["id"] != "checkout-v2-flow" {
		t.Fatalf("open %d %v", c, out)
	}
	if got := turns("thread-checkout-v2-flow"); len(got) != 1 || !strings.HasPrefix(got[0]["message"].(string), "[Brief from Main JARVIS") {
		t.Fatalf("first turn %v", got)
	}
	waitIdle(t, a, "checkout-v2-flow")
	if c, out := call(e.s.agentThreadUpdate, `{"id":"checkout-v2-flow","status":"sleeping"}`); c != 400 {
		t.Fatalf("bad status %d %v", c, out)
	}
	before := e.s.notes.lastID()
	if c, out := call(e.s.agentThreadUpdate, `{"id":"checkout-v2-flow","status":"needs_you","note":"Squash into stage?","branch":"feature/checkout-v2-flow"}`); c != 200 {
		t.Fatalf("update %d %v", c, out)
	}
	news := e.s.notes.since(before)
	if len(news) != 1 || news[0].Go != "thread:checkout-v2-flow" || news[0].Body != "Squash into stage?" {
		t.Fatalf("needs_you must notify: %+v", news)
	}
	a.mu.Lock()
	th := *a.thread("checkout-v2-flow")
	a.mu.Unlock()
	if th.Repo != "~/repos/acme-app" || th.Branch != "feature/checkout-v2-flow" || th.Status != "needs_you" {
		t.Fatalf("record %+v", th)
	}
	if c, _ := call(e.s.agentThreadSend, `{"id":"nope","text":"hi"}`); c != 404 {
		t.Fatalf("send to unknown: %d", c)
	}
	if c, _ := call(e.s.agentThreadSend, `{"id":"checkout-v2-flow","text":"yes, ship it","from":"owner"}`); c != 200 {
		t.Fatalf("send %d", c)
	}
	waitIdle(t, a, "checkout-v2-flow")
	got := turns("thread-checkout-v2-flow")
	if last := got[len(got)-1]["message"].(string); !strings.HasPrefix(last, "[From the owner, passed on by Main JARVIS") || !strings.HasSuffix(last, "yes, ship it") {
		t.Fatalf("relayed %q", last)
	}

	// a team pane's news goes to its lead; another session's doesn't
	e.s.teamDeliver(time.Now()) // its cursor starts here
	e.s.notes.push("done", "ok", "tester finished", "", "pane:checkout-v2-flow:3")
	e.s.notes.push("done", "ok", "elsewhere", "", "pane:box-dev:1")
	e.s.teamDeliver(time.Now())
	waitIdle(t, a, "checkout-v2-flow")
	got = turns("thread-checkout-v2-flow")
	if last := got[len(got)-1]["message"].(string); !strings.Contains(last, "tester finished [checkout-v2-flow:3]") || strings.Contains(last, "elsewhere") {
		t.Fatalf("team news %q", last)
	}
	if got := turns("panel"); len(got) != 0 {
		t.Fatalf("Main got team news: %v", got)
	}
}

// A thread waiting on the owner stops waiting once the owner writes in (panel or Telegram); Main writing in doesn't clear it.
func TestOwnerAnswerClearsNeedsYou(t *testing.T) {
	a, _ := threadAssistant(t)
	th, err := a.openThread("Answer me", "", "", "go")
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a, th.ID)
	status := func() string { a.mu.Lock(); defer a.mu.Unlock(); return a.thread(th.ID).Status }
	st, q := "needs_you", "which bucket?"
	a.updateThread(th.ID, map[string]*string{"status": &st, "note": &q})
	if _, err := a.tellThread(th.ID, "main", "fyi"); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "needs_you" {
		t.Fatalf("Main's message cleared it: %s", got)
	}
	waitIdle(t, a, th.ID)
	if _, err := a.tellThread(th.ID, "owner", "the stage one"); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "working" {
		t.Fatalf("after the owner's answer: %s, want working", got)
	}
}

// a thread's state follows what runs: "working" as its lead left it, with nothing running, is waiting on the owner
func TestThreadStateFollowsWhatRuns(t *testing.T) {
	a, _ := threadAssistant(t)
	th, err := a.openThread("State", "", "", "go")
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a, th.ID)
	busy := false
	a.teamBusy = func(ss []string) bool { return busy && slices.Contains(ss, th.Team) }
	state := func() string {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, v := range a.threadViews() {
			if v.ID == th.ID {
				return v.State
			}
		}
		return ""
	}
	st := "working"
	a.updateThread(th.ID, map[string]*string{"status": &st})
	if got := state(); got != "waiting" {
		t.Fatalf("nothing runs: %s, want waiting", got)
	}
	busy = true
	if got := state(); got != "working" {
		t.Fatalf("its team's agent works: %s, want working", got)
	}
	st = "needs_you"
	a.updateThread(th.ID, map[string]*string{"status": &st})
	if got := state(); got != "waiting" {
		t.Fatalf("its lead asks the owner while its team works: %s, want waiting", got)
	}
	busy, st = false, "done"
	a.updateThread(th.ID, map[string]*string{"status": &st})
	if got := state(); got != "done" {
		t.Fatalf("done: %s", got)
	}
}
