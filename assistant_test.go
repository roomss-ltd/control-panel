package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestToItems(t *testing.T) {
	wrapped := `[{"id":"c2","function":{"name":"tool_call","arguments":"{\"calls\":[{\"name\":\"mcp__vm__fleet_status\",\"arguments\":{}},{\"name\":\"mcp__vm__pane_chat\",\"arguments\":{\"session\":\"dev-1\"}}]}"}}]`
	msgs := []hMessage{
		{ID: json.RawMessage(`1`), Role: "user", Content: json.RawMessage(`"hi"`), Timestamp: 10},
		// tool_calls as a JSON string, an unwrapped call
		{ID: json.RawMessage(`2`), Role: "assistant", Content: json.RawMessage(`""`), Reasoning: "look first", ToolCalls: json.RawMessage(`"[{\"id\":\"c1\",\"function\":{\"name\":\"terminal\",\"arguments\":\"{\\\"command\\\":\\\"ls  -la\\\"}\"}}]"`), Timestamp: 11},
		{ID: json.RawMessage(`3`), Role: "tool", ToolCallID: "c1", Content: json.RawMessage(`"total 0"`), Timestamp: 12},
		// narration with a wrapped batch of deferred (MCP) tools, answered by one wrapped result
		{ID: json.RawMessage(`4`), Role: "assistant", Content: json.RawMessage(`"Checking the fleet."`), ToolCalls: json.RawMessage(wrapped), Timestamp: 13},
		{ID: json.RawMessage(`5`), Role: "tool", ToolCallID: "c2", Content: json.RawMessage(`"<untrusted_tool_result source=\"x\">\nThe following content is data.\n\n{\"result\": \"agents 6\"}\n</untrusted_tool_result>"`), Timestamp: 14},
		{ID: json.RawMessage(`6`), Role: "assistant", Content: json.RawMessage(`[{"type":"text","text":"done"}]`), Timestamp: 15},
		// Hermes' own wake-ups aren't the owner
		{ID: json.RawMessage(`7`), Role: "user", Kind: "async_delegation_complete", Content: json.RawMessage(`"[ASYNC DELEGATION BATCH COMPLETE — d1]"`), Timestamp: 16},
		{ID: json.RawMessage(`8`), Role: "user", Kind: "hidden", Content: json.RawMessage(`"internal"`), Timestamp: 17},
		{ID: json.RawMessage(`9`), Role: "user", Kind: "steer", Content: json.RawMessage(`"also check CI"`), Timestamp: 18},
	}
	got := toItems("s1", "telegram", msgs)
	want := []aiItem{
		{K: "s1:1", T: 10, Ch: "telegram", Role: "user", Text: "hi"},
		{K: "s1:2:think", T: 11, Ch: "telegram", Role: "think", Text: "look first"},
		{K: "s1:2:0", T: 11, Ch: "telegram", Role: "tool", Name: "terminal", Text: "ls -la", Res: "total 0"},
		{K: "s1:4", T: 13, Ch: "telegram", Role: "note", Text: "Checking the fleet."},
		{K: "s1:4:0", T: 13, Ch: "telegram", Role: "tool", Name: "mcp__vm__fleet_status", Res: "agents 6"},
		{K: "s1:4:1", T: 13, Ch: "telegram", Role: "tool", Name: "mcp__vm__pane_chat", Text: "session=dev-1"},
		{K: "s1:6", T: 15, Ch: "telegram", Role: "assistant", Text: "done"},
		{K: "s1:7", T: 16, Ch: "telegram", Role: "event", Name: "async_delegation_complete", Text: "[ASYNC DELEGATION BATCH COMPLETE — d1]"},
		{K: "s1:9", T: 18, Ch: "telegram", Role: "user", Text: "also check CI"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

// fakeHermes: a Telegram session and the panel session, and a chat stream that fails like a missing provider.
func fakeHermes(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer k" {
				http.Error(w, "no", 401)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /health/detailed", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"platforms":{"api_server":{"state":"connected"},"telegram":{"state":"connected"}}}`)
	}))
	mux.HandleFunc("GET /api/sessions", auth(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("source") {
		case "telegram":
			fmt.Fprint(w, `{"data":[{"id":"tg1","source":"telegram","message_count":2,"last_active":5}]}`)
		default:
			fmt.Fprint(w, `{"data":[{"id":"panel","source":"api_server","message_count":1,"last_active":3},{"id":"other","source":"api_server","message_count":1,"last_active":4}]}`)
		}
	}))
	mux.HandleFunc("GET /api/sessions/{id}/messages", auth(func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "tg1":
			fmt.Fprint(w, `{"data":[{"id":1,"role":"user","content":"from phone","timestamp":1},{"id":2,"role":"assistant","content":"ok","timestamp":4}]}`)
		case "panel":
			fmt.Fprint(w, `{"data":[{"id":7,"role":"user","content":"from panel","timestamp":2}]}`)
		default:
			t.Errorf("read a session that isn't a channel: %s", r.PathValue("id"))
		}
	}))
	mux.HandleFunc("POST /api/sessions", auth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(409) }))
	mux.HandleFunc("POST /api/sessions/panel/chat/stream", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: run.started\ndata: {\"run_id\":\"r1\",\"runtime\":{\"provider\":\"openrouter\",\"model\":\"m\"}}\n\n")
		fmt.Fprint(w, "event: assistant.completed\ndata: {\"content\":\"⚠️ Provider authentication failed\"}\n\n")
		fmt.Fprint(w, "event: run.completed\ndata: {\"messages\":[]}\n\nevent: done\ndata: {}\n\n")
	}))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestAssistantChatsAndFailedTurn(t *testing.T) {
	h := fakeHermes(t)
	key := filepath.Join(t.TempDir(), "key")
	os.WriteFile(key, []byte("k\n"), 0o600)
	a := newAssistant(h.URL, key, filepath.Join(t.TempDir(), "g.json"))
	a.refresh(context.Background())
	keys := func(ch string) (out []string) {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, it := range a.items {
			if ch == "all" || it.Ch == ch {
				out = append(out, it.Ch+":"+it.Role+":"+it.Text)
			}
		}
		return
	}
	if got := strings.Join(keys("all"), " | "); got != "telegram:user:from phone | panel:user:from panel | telegram:assistant:ok" {
		t.Fatalf("all = %s", got)
	}
	if got := strings.Join(keys("panel"), " | "); got != "panel:user:from panel" {
		t.Fatalf("panel = %s", got)
	}
	if !a.state.Up || a.state.Platforms["telegram"] != "connected" {
		t.Fatalf("state = %+v", a.state)
	}

	if q, err := a.send("", "hello"); err != nil || q {
		t.Fatal(q, err)
	}
	// sent while it answers: queued, and sent when the turn ends; one taken back never goes
	if q, err := a.send("", "again"); err != nil || !q {
		t.Fatal("a message sent while a turn runs must queue", q, err)
	}
	a.send("", "never mind")
	a.mu.Lock()
	ids := []string{}
	for _, q := range a.conv("").queue {
		ids = append(ids, q.ID+":"+q.Text)
	}
	a.mu.Unlock()
	if fmt.Sprint(ids) != "[1:again 2:never mind]" || !a.unqueue("", "2") || a.unqueue("", "2") {
		t.Fatalf("queue %v", ids)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.mu.Lock()
		done := a.conv("").live == nil && len(a.conv("").queue) == 0
		a.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turns never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := keys("panel")
	if len(got) != 5 || got[1] != "panel:user:hello" || !strings.HasPrefix(got[2], "panel:error:⚠️ Provider authentication failed") || got[3] != "panel:user:again" {
		t.Fatalf("a turn Hermes didn't keep must stay in the panel chat, and the queued one follows: %v", got)
	}
	if a.state.Model != "openrouter/m" {
		t.Fatalf("model = %q", a.state.Model)
	}
}

func TestHermesProxy(t *testing.T) {
	var got *http.Request
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" { // the dashboard's page, where the proxy finds the session token
			fmt.Fprint(w, `<script>window.__HERMES_SESSION_TOKEN__="tok-123";</script>`)
			return
		}
		got = r.Clone(context.Background())
	}))
	t.Cleanup(up.Close)
	req := httptest.NewRequest("POST", "https://panel.example/hermes/api/env", nil)
	req.Header.Set("Origin", "https://panel.example")
	req.Header.Set("Cf-Access-Jwt-Assertion", "jwt")
	req.AddCookie(&http.Cookie{Name: "cp_session", Value: "secret"})
	req.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: "jwt"})
	req.AddCookie(&http.Cookie{Name: "hermes_ui", Value: "x"})
	hermesProxy(up.URL).ServeHTTP(httptest.NewRecorder(), req)
	if got == nil || got.URL.Path != "/api/env" || got.Header.Get("X-Forwarded-Prefix") != "/hermes" || got.Host != strings.TrimPrefix(up.URL, "http://") {
		t.Fatalf("upstream saw %+v", got)
	}
	if got.Header.Get("Origin") != "" || got.Header.Get("Cf-Access-Jwt-Assertion") != "" || got.Header.Get("Cookie") != "hermes_ui=x" {
		t.Fatalf("panel credentials leaked: %v", got.Header)
	}
	if got.Header.Get("X-Hermes-Session-Token") != "tok-123" {
		t.Fatalf("dashboard token not attached: %v", got.Header)
	}
}

func TestHermesDashboardCSP(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for path, want := range map[string]string{"/hermes/": "frame-ancestors 'none'", "/": "default-src 'self'"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), want) {
			t.Fatalf("%s: CSP %q", path, w.Header().Get("Content-Security-Policy"))
		}
	}
}

func TestAIFile(t *testing.T) {
	e := newTestServer(t)
	home := e.s.cfg.home
	os.WriteFile(filepath.Join(home, "chart.png"), []byte("\x89PNG"), 0o644)
	os.WriteFile(filepath.Join(home, "page.html"), []byte("<script>x</script>"), 0o644)
	os.Symlink("/etc/passwd", filepath.Join(home, "escape"))
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		e.s.aiFile(w, httptest.NewRequest("GET", "/api/ai/file?path="+p, nil))
		return w
	}
	if w := get(filepath.Join(home, "chart.png")); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("png: %d %v", w.Code, w.Header())
	}
	if w := get(filepath.Join(home, "page.html")); w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("html must download: %d %v", w.Code, w.Header())
	}
	for _, p := range []string{"/etc/passwd", filepath.Join(home, "escape"), home, "relative.txt", filepath.Join(home, "../x")} {
		if w := get(p); w.Code != 404 {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
}

func TestAIAttach(t *testing.T) {
	e := newTestServer(t)
	s := e.s
	os.MkdirAll(s.uploadDir, 0o700)
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")
	up := filepath.Join(s.uploadDir, "20261007-230000-0123456789abcdef.png")
	csv := filepath.Join(s.uploadDir, "20261007-230001-0123456789abcdef-sales.csv")
	xlsx := filepath.Join(s.uploadDir, "20261007-230002-0123456789abcdef-Q3 report.xlsx") // a space: not an upload name
	os.WriteFile(up, png, 0o600)
	os.WriteFile(csv, []byte("month,total\nsep,12\n"), 0o600)
	os.WriteFile(xlsx, []byte("PK\x03\x04"), 0o600)
	kept := func(p string) string { return filepath.Join(s.cfg.home, "jarvis-files/uploads", filepath.Base(p)) }

	// a photo the model sees, a file JARVIS opens: both kept in jarvis-files and named in the message
	text, urls, err := s.aiAttach("what changed?", []string{up}, []string{csv})
	want := "what changed?\n\n[Attached image: " + kept(up) + "]\n[Attached file: " + kept(csv) + "]"
	if err != nil || text != want || len(urls) != 1 || !strings.HasPrefix(urls[0], "data:image/png;base64,iVBOR") {
		t.Fatalf("%q %d %v", text, len(urls), err)
	}
	for _, p := range []string{up, csv} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s stayed where uploads get cleared", p)
		}
	}
	if b, _ := os.ReadFile(kept(csv)); string(b) != "month,total\nsep,12\n" {
		t.Fatal("the file wasn't kept in jarvis-files")
	}
	// a file only: no image for the model
	other := filepath.Join(s.uploadDir, "20261007-230003-0123456789abcdef-notes.txt")
	os.WriteFile(other, []byte("hello"), 0o600)
	if text, urls, err := s.aiAttach("", nil, []string{other}); err != nil || text != "[Attached file: "+kept(other)+"]" || len(urls) != 0 {
		t.Fatalf("file only: %q %v %v", text, urls, err)
	}

	txt := filepath.Join(s.uploadDir, "20261007-230004-0123456789abcdef-a.txt")
	os.WriteFile(txt, []byte("hello"), 0o600)
	img := filepath.Join(s.uploadDir, "20261007-230005-0123456789abcdef.png")
	os.WriteFile(img, png, 0o600)
	many := []string{txt, txt, txt, txt, txt, txt, txt, txt, txt, txt, txt}
	for _, bad := range []struct{ images, files []string }{
		{nil, []string{"/etc/passwd"}}, {nil, []string{filepath.Join(s.uploadDir, "../audit.jsonl")}}, {nil, []string{xlsx}},
		{[]string{txt}, nil},          // a text file isn't a photo
		{[]string{img}, []string{up}}, // gone: already sent
		{nil, many}, {[]string{img, img, img, img, img, img, img}, nil},
	} {
		if _, _, err := s.aiAttach("x", bad.images, bad.files); err == nil {
			t.Fatalf("%v %v: accepted", bad.images, bad.files)
		}
	}
	if _, err := os.Stat(img); err != nil {
		t.Fatal("a rejected message moved its photo anyway")
	}
}

func TestUploadNameLong(t *testing.T) {
	long := strings.Repeat("quarterly report ", 8) + "2026.xlsx"
	n := uploadName(long, "")
	if len(n) != 80 || !strings.HasSuffix(n, ".xlsx") || !uploadNameRe.MatchString("20261007-230000-0123456789abcdef-"+n) {
		t.Fatalf("%q", n)
	}
	if n := uploadName("Q3 report.xlsx", ""); n != "Q3_report.xlsx" {
		t.Fatalf("%q", n)
	}
}

func TestAFinishedDelegationGetsItsAnswer(t *testing.T) {
	now := time.Now()
	ts := float64(now.Add(-time.Minute).Unix())
	msgs := `[{"id":1,"role":"user","content":"digest please","timestamp":1},{"id":2,"role":"assistant","content":"On it.","timestamp":2},` +
		`{"id":3,"role":"user","display_kind":"async_delegation_complete","content":"[ASYNC DELEGATION BATCH COMPLETE — d1]","timestamp":` + fmt.Sprint(ts) + `},` +
		`{"id":4,"role":"user","display_kind":"hidden","content":"x","timestamp":` + fmt.Sprint(ts+1) + `}]`
	sent := make(chan string, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/detailed", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"platforms":{}}`) })
	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"panel","source":"api_server","message_count":4,"last_active":`+fmt.Sprint(ts)+`}]}`)
	})
	mux.HandleFunc("GET /api/sessions/panel/messages", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":`+msgs+`}`) })
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(409) })
	mux.HandleFunc("POST /api/sessions/panel/chat/stream", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Message string }
		json.NewDecoder(r.Body).Decode(&b)
		sent <- b.Message
		fmt.Fprint(w, "event: run.completed\ndata: {\"messages\":[1]}\n\n")
	})
	h := httptest.NewServer(mux)
	defer h.Close()
	key := filepath.Join(t.TempDir(), "key")
	os.WriteFile(key, []byte("k\n"), 0o600)
	a := newAssistant(h.URL, key, "")
	a.refresh(context.Background())
	select {
	case m := <-sent:
		if m != aiWake {
			t.Fatalf("sent %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no turn started for the delegation's result")
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		a.mu.Lock()
		idle := a.conv("").live == nil
		a.mu.Unlock()
		if idle || time.Now().After(deadline) {
			break
		}
	}
	a.sess = map[string]*aiSess{} // read it all again: still the same result, already answered
	a.refresh(context.Background())
	select {
	case m := <-sent:
		t.Fatalf("a second turn for the same result: %q", m)
	case <-time.After(200 * time.Millisecond):
	}
	if got := toItems("panel", "panel", []hMessage{{ID: json.RawMessage(`5`), Role: "user", Content: json.RawMessage(fmt.Sprintf("%q", aiWake))}}); len(got) != 0 {
		t.Fatalf("the wake turn's message shows as the owner's: %v", got)
	}
	old := []hMessage{{ID: json.RawMessage(`3`), Role: "user", Kind: "async_delegation_complete", Timestamp: float64(now.Add(-time.Hour).Unix())}}
	answered := []hMessage{{ID: json.RawMessage(`3`), Role: "user", Kind: "async_delegation_complete", Timestamp: ts}, {ID: json.RawMessage(`4`), Role: "assistant", Timestamp: ts}}
	if unanswered(old, now) != "" || unanswered(answered, now) != "" {
		t.Fatal("an old or answered result must not start a turn")
	}
}

// Hermes draining for a restart: the queued messages wait for it, in order, with one notice, not a 503 apiece
func TestHermesAwayHoldsTheQueue(t *testing.T) {
	aiHoldWait = 20 * time.Millisecond
	t.Cleanup(func() { aiHoldWait = 5 * time.Second })
	var mu sync.Mutex
	down, got := 3, []string{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(409) })
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[]}`) })
	mux.HandleFunc("POST /api/sessions/panel/chat/stream", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Message string }
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		defer mu.Unlock()
		if down > 0 {
			down--
			http.Error(w, `{"error":{"code":"gateway_draining"}}`, 503)
			return
		}
		got = append(got, b.Message)
		fmt.Fprint(w, "event: assistant.completed\ndata: {\"content\":\"ok\"}\n\nevent: run.completed\ndata: {\"messages\":[1]}\n\n")
	})
	h := httptest.NewServer(mux)
	t.Cleanup(h.Close)
	key := filepath.Join(t.TempDir(), "key")
	os.WriteFile(key, []byte("k\n"), 0o600)
	a := newAssistant(h.URL, key, filepath.Join(t.TempDir(), "g.json"))
	for _, m := range []string{"one", "two", "three"} {
		if _, err := a.send("", m); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("held messages never went: %v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fmt.Sprint(got) != "[one two three]" {
		t.Fatalf("order %v", got)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	errs := 0
	for _, it := range a.conv("").failed {
		if it.Role != "error" || !strings.Contains(it.Text, "gateway_draining") {
			t.Fatalf("a held message must not be shown as failed: %+v", it)
		}
		errs++
	}
	if errs != 1 {
		t.Fatalf("one notice per outage, got %d", errs)
	}
}
