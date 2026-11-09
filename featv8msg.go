package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Owner comments on the feature page. A comment is addressed to the thread that manages the feature, is stored as a
// note for that thread, and is delivered to the thread's lead through the delivery outbox (teamdeliver.go), which
// persists it, checks the lead's history before sending and holds anything it cannot be sure about.

// featMsg is an owner comment from the page, addressed to the feature's managing thread. The outbox carries it
// (persisted, history-checked, unknown held); a rebind never readdresses it.
type featMsg struct {
	ClientID string     `json:"clientId"`
	Sum      string     `json:"sum"` // its text, anchor and thread: a retry must match
	Thread   string     `json:"thread"`
	Note     int64      `json:"note"`
	Anchor   featAnchor `json:"anchor"`
}

// featMsgRec is a comment's receipt (featSup.Msgs), used to make retries idempotent. It is never compacted away
// with the events.
type featMsgRec struct {
	Sum    string `json:"sum"`
	Event  string `json:"event"`
	Thread string `json:"thread"`
	Note   int64  `json:"note"`
	State  string `json:"state,omitempty"` // "", delivered, cancelled (dropped: its thread stopped leading the feature)
}

// featAnchor says what part of the page a comment is about (a step, a milestone, ...) and, for page text written by
// agents, quotes what the owner was looking at.
type featAnchor struct {
	A     string `json:"a"`
	Kind  string `json:"kind"`
	Rev   string `json:"rev,omitempty"`
	Label string `json:"label,omitempty"` // page text, written by agents: quoted, never the owner's words
	Quote string `json:"quote,omitempty"`
}

// Validation for the fields a client sends with a comment.
var (
	clientIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	anchorARe  = regexp.MustCompile(`^[A-Za-z0-9:_#./@-]{0,100}$`)
	anchorRev  = regexp.MustCompile(`^[A-Za-z0-9]{0,40}$`)
)

// featQuote renders agent-written page text as a quote block: every line prefixed, so none reads as the owner's.
func featQuote(s string) string {
	s = strings.TrimSpace(trunc(s, 1500))
	if s == "" {
		return ""
	}
	return "> " + strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", "\n> ")
}

// featMessagePost handles POST /api/term/feature/message {session, clientId, thread, text, anchor{a, kind, rev,
// label?, quote?}}. It saves the comment as a note for the managing thread and queues it for the lead; a retry with
// the same clientId returns the first send's state instead of sending again.
func (s *server) featMessagePost(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Session, ClientID, Thread, Text string
		Anchor                          featAnchor
	}
	if err := readJSONN(r, &b, 1<<15); err != nil || !sessionNameRe.MatchString(b.Session) || !clientIDRe.MatchString(b.ClientID) ||
		strings.TrimSpace(b.Text) == "" || len(b.Text) > 4000 || !anchorARe.MatchString(b.Anchor.A) || !anchorRev.MatchString(b.Anchor.Rev) || len(b.Anchor.Kind) > 20 {
		writeJSON(w, 400, map[string]any{"error": "session, clientId, thread, text (≤4000) and a well-formed anchor"})
		return
	}
	featPlanMu.Lock() // one at a time: a retry racing its first send is still one message
	defer featPlanMu.Unlock()
	owner := s.featOwner(b.Session)
	if owner == nil || owner.ID != b.Thread {
		id := ""
		if owner != nil {
			id = owner.ID
		}
		writeJSON(w, 409, map[string]any{"error": "the feature's manager is now " + cmpOr(id, "nobody") + ": choose to send it there", "thread": id})
		return
	}
	sum := featRev([]byte(b.Thread + "\x00" + b.Text + "\x00" + toJSONString(b.Anchor)))
	if rec, ok := s.featMsgByClient(b.Session, b.ClientID); ok {
		if rec.Sum != sum {
			writeJSON(w, 409, map[string]any{"error": "this message id was used for another message"})
			return
		}
		writeJSON(w, 200, s.featMsgState(b.Session, b.ClientID))
		return
	}
	d := s.featDossier(b.Session)
	about := "[Owner comment on " + cmpOr(b.Anchor.A, "the feature") + map[bool]string{true: " @" + b.Anchor.Rev}[b.Anchor.Rev != ""] + "]"
	if (b.Anchor.Kind == "step" || b.Anchor.Kind == "milestone") && b.Anchor.Rev != "" && b.Anchor.Rev != d.Rev {
		about += fmt.Sprintf(" (about plan rev %s; now %s)", b.Anchor.Rev, d.Rev)
	}
	body := about + " " + strings.TrimSpace(b.Text) + "\nThis is not an approval: approvals, verification, releases and acceptance are the owner's own actions on the page."
	if q := featQuote(strings.TrimSpace(b.Anchor.Label + "\n" + b.Anchor.Quote)); q != "" {
		body += "\nWhat the owner was looking at (page text written by agents, not the owner's words):\n" + q
	}
	nt := s.notes
	nt.mu.Lock()
	notes, next, stuffed := nt.Notes, nt.Next, nt.stuffed // addSaved, but the owner's words whole (a note's body is cut at 300)
	n := nt.add(time.Now(), "feature", "ok", "🧵 "+owner.Title+": Owner comment", "", "thread:"+owner.ID)
	n.Ref, n.Body = "msg/"+b.Session+"/"+b.ClientID, body
	noteID, err := n.ID, nt.saveLocked()
	if err != nil {
		nt.Notes, nt.Next, nt.stuffed = notes, next, stuffed
	}
	nt.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": "not saved: " + err.Error()})
		return
	}
	e, err := s.featEvent(b.Session, featEv{Kind: "message", Step: map[bool]string{true: b.Anchor.A}[b.Anchor.Kind == "step"], Thread: owner.ID, Rev: d.Rev,
		Text: trunc(b.Text, 500), Msg: &featMsg{ClientID: b.ClientID, Sum: sum, Thread: owner.ID, Note: noteID, Anchor: b.Anchor}})
	k := b.Session + "/" + b.ClientID
	s.sup.mu.Lock()
	if err == nil {
		s.sup.Msgs[k] = featMsgRec{Sum: sum, Event: e.ID, Thread: owner.ID, Note: noteID}
		if err = s.sup.save(); err != nil {
			delete(s.sup.Msgs, k)
		}
	}
	s.sup.mu.Unlock()
	if err != nil { // the note stands and will be delivered; its receipt (dedupe) didn't save: say so
		writeJSON(w, 500, map[string]any{"error": "queued for the lead, but its receipt wasn't saved (a retry may send it twice): " + err.Error()})
		return
	}
	s.wakeUp()
	writeJSON(w, 200, s.featMsgState(b.Session, b.ClientID))
}

// toJSONString marshals v for hashing; errors are ignored because the inputs are plain structs.
func toJSONString(v any) string { b, _ := json.Marshal(v); return string(b) }

// featMsgByClient looks up a comment's receipt by session and client id, after folding in the outbox's outcomes.
func (s *server) featMsgByClient(session, id string) (featMsgRec, bool) {
	s.featMsgFlush()
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	r, ok := s.sup.Msgs[session+"/"+id]
	return r, ok
}

// featMsgFlush records the outbox's outcomes (delivered, cancelled) into the receipts and saves them.
func (s *server) featMsgFlush() {
	teamMsgOutcomes.Lock()
	got := teamMsgOutcomes.m
	teamMsgOutcomes.m = map[string]string{}
	teamMsgOutcomes.Unlock()
	if len(got) == 0 {
		return
	}
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	for k, st := range got {
		if r, ok := s.sup.Msgs[k]; ok && r.State != "delivered" {
			r.State = st
			s.sup.Msgs[k] = r
		}
	}
	if s.sup.save() != nil { // not saved: told again next time
		teamMsgOutcomes.Lock()
		for k, st := range got {
			teamMsgOutcomes.m[k] = st
		}
		teamMsgOutcomes.Unlock()
	}
}

// featMsgState reports where a comment stands: queued, sending, delivered, unknown (its delivery can't be told: not sent again), failed (Hermes didn't
// keep it; it goes again), or held (not delivered and never will be on its own: its thread no longer leads the
// feature; the owner sends it again to the new lead).
func (s *server) featMsgState(session, id string) map[string]any {
	r, _ := s.featMsgByClient(session, id)
	out := map[string]any{"ok": true, "id": id, "event": r.Event, "thread": r.Thread}
	owner := s.featOwnerID(session)
	a := s.ai
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.thread(r.Thread)
	teamPending.Lock()
	defer teamPending.Unlock()
	h, pending := teamPending.m[r.Thread]
	carried := pending && teamPending.a == a && slices.Contains(h.Msgs, session+"/"+id)
	switch {
	case r.State == "delivered":
		out["state"] = "delivered"
	case r.State == "cancelled":
		out["state"], out["reason"] = "held", "not delivered: its thread stopped leading this feature before it went; send it again to "+cmpOr(owner, "the new lead")
	case carried && h.Unknown > 0:
		out["state"], out["reason"] = "unknown", "its lead's history can't be read: not sent again until it can"
	case t == nil || t.Status == "archived" || owner != r.Thread || t.Team != session:
		if carried && h.Maybe {
			out["state"], out["reason"] = "unknown", "it may have reached "+r.Thread+" before it stopped leading this feature; it won't be sent again"
		} else {
			out["state"], out["reason"] = "held", "not delivered: its thread no longer leads this feature (now "+cmpOr(owner, "nobody")+"); send it again there if you want"
		}
	case carried && h.Attempts > 0 && !h.Maybe:
		out["state"], out["reason"] = "failed", "the lead's turn didn't keep it: it goes again"
	case carried:
		out["state"] = "sending"
	default: // never "delivered" from the thread's cursor: only the outbox's acknowledgement of this comment says that
		out["state"] = "queued"
	}
	return out
}

// featMessageGet handles GET /api/term/feature/message?session=&id=<clientId>: the state of one comment.
func (s *server) featMessageGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !sessionNameRe.MatchString(q.Get("session")) || !clientIDRe.MatchString(q.Get("id")) {
		writeJSON(w, 400, map[string]any{"error": "session and id"})
		return
	}
	if _, ok := s.featMsgByClient(q.Get("session"), q.Get("id")); !ok {
		writeJSON(w, 404, map[string]any{"error": "no such message"})
		return
	}
	writeJSON(w, 200, s.featMsgState(q.Get("session"), q.Get("id")))
}
