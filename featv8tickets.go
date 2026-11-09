package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
)

// Tickets: the feature page's list of things waiting on the owner (a question from an agent, a check to approve, a
// held tab, a requirements draft, outcomes to accept). A ticket is not stored: it is derived again from the record and
// the supervisor on every request, and a reply is carried out by calling the existing owner route.

// featTicket is something waiting on the owner. Its options are the existing owner routes that answer it, named by the
// server; a reply can only pick one of these.
type featTicket struct {
	ID      string         `json:"id"` // kind:step key (or kind:feature / kind:tab)
	Kind    string         `json:"kind"`
	Step    string         `json:"step,omitempty"`
	Tab     string         `json:"tab,omitempty"`
	Sid     string         `json:"sid,omitempty"` // the agent on that tab now; a reply naming another is refused
	Rev     string         `json:"rev"`           // what the owner must have seen
	Title   string         `json:"title"`
	Reason  string         `json:"reason,omitempty"`
	Options []featTicketOp `json:"options"`
	IfNone  string         `json:"ifNone,omitempty"`
	route   func(text string) (string, any)
}

// featTicketOp is one way to answer a ticket, with what choosing it will do. NeedsText means the owner must type a reply.
type featTicketOp struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Consequence string `json:"consequence"`
	NeedsText   bool   `json:"needsText,omitempty"`
}

// featTabSid returns the session id of the agent currently in the named tab ("" if none is known).
func (s *server) featTabSid(session, tab string) string {
	for _, se := range s.term.listing().Sessions {
		if se.Name != session {
			continue
		}
		for _, tb := range se.Tabs {
			if strings.EqualFold(tb.Name, tab) {
				for _, p := range tb.Panes {
					if p.Agent != nil && p.Agent.sid != "" {
						return p.Agent.sid
					}
				}
			}
		}
	}
	return ""
}

// featTickets derives the open tickets for a feature: steps asking the owner a question, checks waiting for approval
// (or unable to be approved yet), missed checkpoints, held tabs, a requirements draft, and finished work whose
// outcomes the owner has yet to accept.
func (s *server) featTickets(session string) []featTicket {
	d := s.featDossier(session)
	out := []featTicket{}
	all := true
	for _, m := range d.Milestones {
		for _, st := range m.Steps {
			all = all && st.State == "done"
			st := st
			if st.State == "asking" && st.Ask != "" {
				out = append(out, featTicket{ID: "ask:" + st.Key, Kind: "ask", Step: st.Key, Tab: st.Owner, Sid: s.featTabSid(session, st.Owner), Rev: d.Rev,
					Title: st.Ask, Reason: "@" + st.Owner + " waits for your answer on " + trunc(st.Text, 80),
					Options: []featTicketOp{{"answer", "Answer and let it go on", "the answer goes under this step; the step goes back to doing", true},
						{"answer-wait", "Answer, keep it waiting", "the answer goes under this step; the step goes back to todo", true}},
					IfNone: "the step stays waiting; nothing else is blocked",
					route: func(text string) (string, any) {
						return "answer", map[string]any{"session": session, "key": st.Key, "qid": st.Qid, "rev": d.Rev, "text": text}
					}})
			}
			if st.Check != "" && !st.Approval.Check && !filepath.IsAbs(st.Cwd) {
				out = append(out, featTicket{ID: "approve:" + st.Key, Kind: "approve", Step: st.Key, Tab: st.Owner, Rev: d.Rev,
					Title: "A check for " + trunc(st.Text, 80) + " can't be approved yet", Options: []featTicketOp{},
					Reason: "`" + st.Check + "` has no absolute folder (cwd: " + cmpOr(st.Cwd, "none") + "): the agent sets an absolute cwd, then you can approve it",
					IfNone: "the step can only be verified by your attestation"})
			}
			if st.Check != "" && !st.Approval.Check && filepath.IsAbs(st.Cwd) {
				out = append(out, featTicket{ID: "approve:" + st.Key, Kind: "approve", Step: st.Key, Tab: st.Owner, Sid: s.featTabSid(session, st.Owner), Rev: d.Rev,
					Title: "Approve the check for " + trunc(st.Text, 80), Reason: "`" + st.Check + "` in " + st.Cwd,
					Options: []featTicketOp{{"approve", "Approve this check", "its run in the agent's transcript will verify the step; nothing else is approved", false}},
					IfNone:  "the step can only be verified by your attestation",
					route: func(string) (string, any) {
						return "approve", map[string]any{"session": session, "items": []map[string]string{{"key": st.Key, "check": st.Check, "cwd": st.Cwd}}}
					}})
			}
			if st.Missed && st.State != "done" {
				out = append(out, featTicket{ID: "missed:" + st.Key, Kind: "missed", Step: st.Key, Tab: st.Owner, Rev: d.Rev,
					Title: "Missed its checkpoint: " + trunc(st.Text, 80), Reason: "due " + st.Due, Options: []featTicketOp{}, IfNone: "the lead is told on its next wake"})
			}
		}
	}
	s.sup.mu.Lock()
	var holds []string
	for k := range s.sup.Holds {
		if strings.HasPrefix(k, session+"/") {
			holds = append(holds, k)
		}
	}
	slices.Sort(holds)
	hs := map[string]featHold{}
	for _, k := range holds {
		hs[k] = s.sup.Holds[k]
	}
	s.sup.mu.Unlock()
	for _, k := range holds {
		tab, h := strings.TrimPrefix(k, session+"/"), hs[k]
		out = append(out, featTicket{ID: "hold:" + tab, Kind: "hold", Tab: tab, Sid: s.featTabSid(session, tab), Rev: d.Rev, Title: "@" + tab + " is held",
			Reason:  h.Why,
			Options: []featTicketOp{{"release", "Release the hold", "lifts the hold only; it authorizes nothing new, the lead re-plans", false}},
			IfNone:  "the held steps stay held, and done is refused",
			route: func(string) (string, any) {
				return "release", map[string]any{"session": session, "tab": tab, "rev": d.Rev}
			}})
	}
	if d.Draft != "" {
		out = append(out, featTicket{ID: "draft:feature", Kind: "draft", Rev: d.Revs["draft"], Title: "A requirements draft waits", Reason: "FEATURE.draft.md differs from FEATURE.md",
			Options: []featTicketOp{{"promote", "Adopt the draft", "FEATURE.md becomes the draft; the outcomes need accepting again", false},
				{"discard", "Discard the draft", "the draft is deleted; FEATURE.md stays", false}},
			IfNone: "FEATURE.md stays as it is",
			route: func(string) (string, any) {
				return "", map[string]any{"session": session, "draftRev": d.Revs["draft"], "featureRev": d.Revs["feature"]}
			}})
	}
	if all && len(d.Outcomes) > 0 && !d.Accepted && len(d.Milestones) > 0 {
		out = append(out, featTicket{ID: "outcomes:feature", Kind: "outcomes", Rev: d.Revs["feature"], Title: "Accept the outcomes", Reason: strings.Join(d.Outcomes, "; "),
			Options: []featTicketOp{{"accept", "Accept the outcomes as met", "done is allowed once accepted; a change to them needs accepting again", false}},
			IfNone:  "the feature can't be marked done",
			route: func(string) (string, any) {
				return "accept", map[string]any{"session": session, "rev": d.Revs["feature"]}
			}})
	}
	return out
}

// featTicketReply handles POST /api/term/feature/ticket/reply {session, ticket, option, text?, rev, sid?}. The ticket
// is derived again now (it must still exist, unchanged since the owner saw it, with the same agent on its tab); the
// option must be one it offers; the reply then goes to that owner route, unchanged, with no more authority than
// calling the route directly.
func (s *server) featTicketReply(w http.ResponseWriter, r *http.Request) {
	var b struct{ Session, Ticket, Option, Text, Rev, Sid string }
	if err := readJSONN(r, &b, 1<<14); err != nil || !sessionNameRe.MatchString(b.Session) || b.Ticket == "" {
		writeJSON(w, 400, map[string]any{"error": "session, ticket and option"})
		return
	}
	var tk *featTicket
	for _, x := range s.featTickets(b.Session) {
		if x.ID == b.Ticket {
			tk = &x
			break
		}
	}
	switch {
	case tk == nil:
		writeJSON(w, 409, map[string]any{"error": "this ticket is closed or changed: look again"})
		return
	case b.Rev != tk.Rev:
		writeJSON(w, 409, map[string]any{"error": "it changed since you saw it: look again", "rev": tk.Rev})
		return
	case tk.Sid != "" && b.Sid != tk.Sid:
		writeJSON(w, 409, map[string]any{"error": "the agent on @" + tk.Tab + " was replaced since: look again", "sid": tk.Sid})
		return
	}
	i := slices.IndexFunc(tk.Options, func(o featTicketOp) bool { return o.ID == b.Option })
	if i < 0 || tk.route == nil {
		writeJSON(w, 400, map[string]any{"error": "not an option of this ticket"})
		return
	}
	op := tk.Options[i]
	if op.NeedsText && strings.TrimSpace(b.Text) == "" {
		writeJSON(w, 400, map[string]any{"error": "this option needs your text"})
		return
	}
	name, body := tk.route(b.Text)
	if name == "" {
		name = op.ID // promote, discard
	}
	if name == "answer" {
		body.(map[string]any)["resume"] = op.ID == "answer"
	}
	h := map[string]http.HandlerFunc{"answer": s.termFeatureAnswer, "approve": s.termFeatureApprove, "release": s.termFeatureRelease,
		"accept": s.termFeatureAccept, "promote": s.termFeaturePromote, "discard": s.termFeatureDiscard}[name]
	js, _ := json.Marshal(body)
	ctx := r.Context()
	if tk.Sid != "" {
		ctx = context.WithValue(ctx, ticketExpectKey{}, ticketExpect{b.Session, tk.Tab, tk.Sid})
	}
	if featTicketBeforeEffect != nil {
		featTicketBeforeEffect()
	}
	req := r.Clone(ctx)
	req.Body, req.ContentLength = io.NopCloser(bytes.NewReader(js)), int64(len(js))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code == 200 { // what was done, for whom it was checked: the ticket, the option, the tab and agent seen at the effect
		s.featEvent(b.Session, featEv{Kind: "ticket", Step: tk.Step, Tab: tk.Tab, Sid: tk.Sid, Rev: tk.Rev, Text: tk.ID + " → " + op.ID})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rec.Code)
	w.Write(rec.Body.Bytes())
}

// ticketExpectKey and ticketExpect carry, in the request context, which agent a ticket reply was checked against, so
// the owner route can check again at the moment of its effect (featTicketGuard).
type ticketExpectKey struct{}

// ticketExpect is the agent a ticket reply was checked against.
type ticketExpect struct{ session, tab, sid string }

// featTicketBeforeEffect is a test hook that runs between a reply's checks and the owner route's effect (to simulate
// an agent being replaced just then).
var featTicketBeforeEffect func()

// featTicketGuard is called by an owner route at its effect when it was run from a ticket reply. It returns why the
// effect must not happen (the ticket's tab has another agent than the owner saw), or a release to call once the
// effect is written. Until then no new listing is published, so an observed replacement can't land between this
// check and the write. (A replacement the panel hasn't observed yet can't be seen by anything.)
func (s *server) featTicketGuard(r *http.Request) (release func(), why string) {
	x, ok := r.Context().Value(ticketExpectKey{}).(ticketExpect)
	if !ok {
		return func() {}, ""
	}
	s.term.hold.RLock()
	if now := s.featTabSid(x.session, x.tab); now != x.sid {
		s.term.hold.RUnlock()
		return nil, "the agent on @" + x.tab + " was replaced since you looked: nothing done, look again"
	}
	if featTicketAtEffect != nil {
		featTicketAtEffect()
	}
	return s.term.hold.RUnlock, ""
}

// featTicketAtEffect is a test hook that runs inside the guarded effect, after the check (to simulate a new terminal
// listing being published then).
var featTicketAtEffect func()
