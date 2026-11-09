package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// This file is the agent API: the panel's own handlers served on a second, loopback-only listener
// (127.0.0.1:7778 by default, see -agent-addr) for the owner's assistant. The assistant is the
// vm-agent project's Hermes agent, whose MCP server (mcp/vm_mcp.py) calls these routes as tools.
// A bearer token kept in a 0600 file (agentToken) stands in for Cloudflare Access and login, and every
// write is audited like the web page's, with the client shown as "agent".
//
// Route groups, all under /agent/:
//
//	snapshot                      GET   one sampler tick of a view (res, ci, term or sec)
//	pane/screen                   GET   what a terminal pane shows now, and the question a blocked TUI asks
//	pane/chat                     GET   the last items of the Claude transcript in a pane
//	pane/files                    GET   the files that pane's Claude session wrote or handed over
//	pane/input                    POST  type into a pane
//	pane/codexmodel               POST  switch a Codex pane's model
//	term/new, term/close          POST  open or close a terminal session
//	notes                         GET   the panel's notes (its notifications), newest first
//	threads                       GET   every thread (a product lead per feature) and its state
//	thread/open                   POST  open a thread with a brief; its lead starts on it at once
//	thread/send                   POST  message a thread's lead
//	thread/update                 POST  the lead keeps its record (a status of needs_you notifies the owner)
//	scout                         POST  run the scout now: Main proposes stalled terminal tabs to take over
//	feature                       GET   a feature's record as the panel sees it, with the whole LOG.md
//	feature/doc                   POST  update a record file against the revision the agent read
//	feature/log                   POST  append a dated line to LOG.md
//	feature/team                  GET   a feature's agent team and what each member is doing
//	feature/assign                POST  record who works on which step
//	feature/events                GET   a feature's events after a given id (read-only: nothing is marked seen)

// agentHandler returns the agent API's mux behind a bearer-token check; token is what agentToken returned.
func (s *server) agentHandler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agent/snapshot", s.agentSnapshot)
	mux.HandleFunc("GET /agent/pane/screen", s.agentScreen)
	mux.HandleFunc("GET /agent/pane/chat", s.agentChat)
	mux.HandleFunc("GET /agent/pane/files", s.paneFiles) // the files its Claude session wrote or handed over: its artifacts
	mux.HandleFunc("POST /agent/pane/input", s.paneInput)
	mux.HandleFunc("POST /agent/pane/codexmodel", s.codexModel)
	mux.HandleFunc("POST /agent/term/new", s.termNew)
	mux.HandleFunc("POST /agent/term/close", s.termClose)
	mux.HandleFunc("GET /agent/notes", s.notesList)
	mux.HandleFunc("GET /agent/threads", s.agentThreads)
	mux.HandleFunc("POST /agent/thread/open", s.agentThreadOpen)
	mux.HandleFunc("POST /agent/scout", s.agentScout)
	mux.HandleFunc("POST /agent/thread/send", s.agentThreadSend)
	mux.HandleFunc("POST /agent/thread/update", s.agentThreadUpdate)
	mux.HandleFunc("GET /agent/feature", s.agentFeature)
	mux.HandleFunc("POST /agent/feature/doc", s.agentFeatureDoc)
	mux.HandleFunc("POST /agent/feature/log", s.agentFeatureLog)
	mux.HandleFunc("GET /agent/feature/team", s.agentFeatureTeam)
	mux.HandleFunc("POST /agent/feature/assign", s.agentFeatureAssign)
	mux.HandleFunc("GET /agent/feature/events", s.featEventsGet) // reads only: no seen or reviewed
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			writeJSON(w, 401, map[string]any{"error": "bad token"})
			return
		}
		r.Header.Set("Cf-Connecting-Ip", "agent") // what clientIP puts in audit lines
		mux.ServeHTTP(w, r)
	})
}

// validAgentAddr accepts only a host:port whose host is a loopback IP, so the API can't be exposed by a typo.
func validAgentAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip, err := netip.ParseAddr(host); err != nil || !ip.IsLoopback() {
		return errors.New("-agent-addr must be loopback")
	}
	return nil
}

// agentToken reads the token file, creating it (32 random bytes, hex, mode 0600) on first use.
func agentToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 32)
	rand.Read(buf)
	tok := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return tok, os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

// look has the sampler run one tick for view as if a page showed it ("term" also refreshes the full
// pane snapshot that s.term.pane reads) and returns that view's data.
func (s *server) look(view string) (json.RawMessage, error) {
	c, done := s.subscribe(view)
	defer done()
	select {
	case msg := <-c.ch:
		var m struct {
			Data json.RawMessage `json:"data"`
		}
		err := json.Unmarshal(msg, &m)
		return m.Data, err
	case <-time.After(15 * time.Second):
		return nil, errors.New("the panel's sampler did not answer in 15 s")
	}
}

// agentSnapshot: GET /agent/snapshot?view=res|ci|term|sec, one sampler tick of that view as JSON.
func (s *server) agentSnapshot(w http.ResponseWriter, r *http.Request) {
	view := r.URL.Query().Get("view")
	if !views[view] || view == "ai" {
		writeJSON(w, 400, map[string]any{"error": "view must be res, ci, term or sec"})
		return
	}
	data, err := s.look(view)
	if err != nil {
		writeJSON(w, 503, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// agentScreen: what the pane shows now, and the question on it if a TUI is blocked on one.
func (s *server) agentScreen(w http.ResponseWriter, r *http.Request) {
	session, pane, err := paneParams(r)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if !s.sessionLive(session) {
		writeJSON(w, 404, map[string]any{"error": "no live session " + session})
		return
	}
	screen, err := s.z.dump(session, pane)
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"screen": screen, "prompt": parsePrompt(screen)})
}

// agentChat: the last n items (default 30, at most 300) of the Claude transcript in a pane.
func (s *server) agentChat(w http.ResponseWriter, r *http.Request) {
	session, pane, err := paneParams(r)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	n := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil {
		n = min(max(v, 1), 300)
	}
	if _, err := s.look("term"); err != nil {
		writeJSON(w, 503, map[string]any{"error": err.Error()})
		return
	}
	p, ok := s.term.pane(session, pane)
	if !ok || p.Agent == nil || p.Agent.path == "" {
		writeJSON(w, 404, map[string]any{"error": "no Claude transcript for this pane"})
		return
	}
	fi, err := os.Stat(p.Agent.path)
	if err != nil {
		writeJSON(w, 404, map[string]any{"error": err.Error()})
		return
	}
	ls, _ := readBefore(p.Agent.path, fi.Size(), chatPage)
	items, title := parseChat(ls)
	writeJSON(w, 200, map[string]any{"title": title, "state": p.State, "items": lastItems(items, n)})
}
