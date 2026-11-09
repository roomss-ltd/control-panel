package main

import (
	"net/http"
	"time"
)

// Shared glue for the feature page (the per-feature dashboard the Terminals page links to; its data is assembled by
// featPage in featv8page.go). The "v8" in these identifiers is just the internal version label of that page.
//
// Files that add feature-page routes register them here instead of editing server.go:
//
//	func init() { v8Register = append(v8Register, (*server).registerV8B) }
//
// A registered function must wrap its handlers with owner (= withSession, the owner login), serve cached data
// and fetch nothing on GET.

// v8Register collects the route registrars that registerV8 runs when the server builds its mux.
var v8Register []func(s *server, mux *http.ServeMux, owner func(http.HandlerFunc) http.HandlerFunc)

// registerV8 adds every route registered through v8Register, each behind the owner login.
func (s *server) registerV8(mux *http.ServeMux) {
	for _, f := range v8Register {
		f(s, mux, s.withSession)
	}
}

// featRel is an explicit relation between plan steps, recorded by the lead together with an assignment. It is never
// inferred from adjacency or timing.
type featRel struct {
	Kind string `json:"kind"` // handoff|blocks|pair|resume
	With string `json:"with"` // the other step's key (12 hex), never a sid
	Why  string `json:"why,omitempty"`
}

// featAssign is one assignment event (POST /agent/feature/assign): the lead put an agent on a step (or on the whole
// feature). featsessions.go reads these to build the session ledger.
type featAssign struct {
	ID     string    `json:"id"` // "ev-<seq>"
	Seq    int64     `json:"seq"`
	At     time.Time `json:"at"`
	Step   string    `json:"step"` // step key; "" = the whole feature
	Tab    string    `json:"tab"`
	Pane   int       `json:"pane,omitempty"` // 0 = unknown
	Sid    string    `json:"sid,omitempty"`  // claimed by the lead; attribution only
	Thread string    `json:"thread,omitempty"`
	Rel    *featRel  `json:"relation,omitempty"`
}

// featAssignments returns a copy of the feature's assignment events in ascending Seq order. It reads memory only (no
// IO) and is safe with or without featPlanMu held, but it takes s.sup.mu, so don't call it while holding that.
func (s *server) featAssignments(session string) []featAssign {
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	var out []featAssign
	for _, e := range s.sup.Events[session] {
		if e.Kind == "assign" {
			a := featAssign{ID: e.ID, Seq: e.Seq, At: e.At, Step: e.Step, Tab: e.Tab, Pane: e.Pane, Sid: e.Sid, Thread: e.Thread}
			if e.Rel != nil {
				r := *e.Rel
				a.Rel = &r
			}
			out = append(out, a)
		}
	}
	return out
}
