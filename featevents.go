package main

import (
	"cmp"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A feature's event log: an append-only record, kept in the supervisor's state file (featsup.json), of what happened on
// the feature that the owner may want to see or review: assignments, owner comments, plan transitions, reopens,
// wakes, deliveries, coverage changes and agents' typed log lines. The feature page reads it; the owner marks
// events seen or reviewed (featseen.go).

// featEv is one entry of a feature's event log. Ids are "ev-<seq>", per feature, ascending and never reused; a
// reader pages by id (after=), never by offset or time, so nothing arriving mid-read is lost.
type featEv struct {
	ID     string    `json:"id"`
	Seq    int64     `json:"seq"`
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // assign, message, transition, reopen, wake, delivery, coverage, …
	Step   string    `json:"step,omitempty"`
	Tab    string    `json:"tab,omitempty"`
	Pane   int       `json:"pane,omitempty"`
	Sid    string    `json:"sid,omitempty"` // claimed, attribution only
	Thread string    `json:"thread,omitempty"`
	Rel    *featRel  `json:"relation,omitempty"`
	Rev    string    `json:"rev,omitempty"`
	Text   string    `json:"text,omitempty"`
	Msg    *featMsg  `json:"msg,omitempty"` // an owner comment (featv8msg.go)
}

// maxFeatEvents is how many events a feature's log keeps; older ones are compacted away, but never one that is still
// waiting for the owner's review.
var maxFeatEvents = 5000

// featNeedsReview reports whether the owner is asked to review e: decisions, tickets, reopens, coverage changes, and
// log lines typed DECISION.
func featNeedsReview(e featEv) bool {
	return map[string]bool{"decision": true, "ticket": true, "reopen": true, "coverage": true}[e.Kind] ||
		e.Kind == "log" && strings.HasPrefix(e.Text, "DECISION")
}

// featEvent appends e to the session's log, assigns its id and time, and saves; an event whose save failed doesn't
// exist. Past maxFeatEvents, the oldest events not awaiting review go (wherever they are), and the seen receipts of
// what went with them; events awaiting review stay, and with twice the cap of those, new events are refused (with an
// explanation) until the owner reviews some.
func (s *server) featEvent(session string, e featEv) (featEv, error) {
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	c := s.sup
	old, oldSeq, oldSeen := c.Events[session], c.EvSeq[session], c.Seen[session]
	waiting := 0
	for _, x := range old {
		if _, ok := c.Reviewed[session][x.ID]; featNeedsReview(x) && !ok {
			waiting++
		}
	}
	if waiting >= 2*maxFeatEvents {
		return featEv{}, fmt.Errorf("the event log is full: %d events await the owner's review", waiting)
	}
	e.Seq = oldSeq + 1
	e.ID = fmt.Sprintf("ev-%d", e.Seq)
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	evs := append(old[:len(old):len(old)], e)
	if drop := len(evs) - maxFeatEvents; drop > 0 {
		kept := make([]featEv, 0, len(evs))
		for _, x := range evs {
			if _, ok := c.Reviewed[session][x.ID]; drop > 0 && x.Seq != e.Seq && !(featNeedsReview(x) && !ok) {
				drop--
				continue
			}
			kept = append(kept, x)
		}
		evs = kept
		sn := featSeen{Frontier: oldSeen.Frontier}
		for _, n := range oldSeen.Above {
			if _, found := slices.BinarySearchFunc(evs, n, func(x featEv, n int64) int { return cmp.Compare(x.Seq, n) }); found {
				sn.Above = append(sn.Above, n)
			}
		}
		c.Seen[session] = sn
	}
	c.Events[session], c.EvSeq[session] = evs, e.Seq
	if err := c.save(); err != nil {
		c.Events[session], c.EvSeq[session], c.Seen[session] = old, oldSeq, oldSeen
		return featEv{}, err
	}
	return e, nil
}

// evIDRe matches an event id, "ev-<seq>".
var evIDRe = regexp.MustCompile(`^ev-([0-9]{1,18})$`)

// evSeq parses an "ev-<n>" event id into its sequence number.
func evSeq(id string) (int64, bool) {
	m := evIDRe.FindStringSubmatch(id)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	return n, err == nil
}

// featEventsGet handles GET /api/term/feature/events?session=&after=ev-<n>&limit=: events after that id, ascending,
// at most 200. It only reads (the agent API serves it too, as GET /agent/feature/events).
func (s *server) featEventsGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	session := q.Get("session")
	after, ok := evSeq(cmpOr(q.Get("after"), "ev-0"))
	limit, err := strconv.Atoi(cmpOr(q.Get("limit"), "200"))
	if !sessionNameRe.MatchString(session) || !ok || err != nil || limit < 1 {
		writeJSON(w, 400, map[string]any{"error": "session, after=ev-<n> and limit 1..200"})
		return
	}
	limit = min(limit, 200)
	s.sup.mu.Lock()
	out := []featEv{}
	all := s.sup.Events[session]
	for _, e := range all {
		if e.Seq > after && len(out) < limit {
			out = append(out, e)
		}
	}
	more := len(out) > 0 && all[len(all)-1].Seq > out[len(out)-1].Seq
	s.sup.mu.Unlock()
	writeJSON(w, 200, map[string]any{"events": out, "more": more})
}

// tabNameRe matches an acceptable tab name: 1 to 64 characters, no control characters.
var tabNameRe = regexp.MustCompile(`^[^\x00-\x1f]{1,64}$`)

// agentFeatureAssign handles POST /agent/feature/assign {session, step, tab, pane?, sid?, thread?, relation?{kind,
// with, why}}: the lead records who works on which step, and an explicit relation to another step (handoff, blocks,
// pair, resume); nothing is typed into any pane. A step that is not ready (see featDepBlocks) is refused.
func (s *server) agentFeatureAssign(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Session, Step, Tab, Sid string
		Pane                    int
		Thread                  *string
		Relation                *featRel
	}
	if err := readJSONN(r, &b, 1<<13); err != nil || !sessionNameRe.MatchString(b.Session) || !tabNameRe.MatchString(b.Tab) || len(b.Sid) > 128 || b.Pane < 0 {
		writeJSON(w, 400, map[string]any{"error": "session and tab are required"})
		return
	}
	featPlanMu.Lock()
	defer featPlanMu.Unlock()
	if !s.featExpect(w, b.Session, b.Thread) {
		return
	}
	dz := s.featDossier(b.Session)
	keys, ms := map[string]bool{}, dz.Milestones
	for _, m := range ms {
		for _, st := range m.Steps {
			keys[st.Key] = true
		}
	}
	if b.Step != "" && !keys[b.Step] {
		writeJSON(w, 400, map[string]any{"error": "no step " + strconv.Quote(b.Step) + " in PLAN.md"})
		return
	}
	if why := featDepBlocks(ms)[b.Step]; why != "" {
		writeJSON(w, 409, map[string]any{"error": "not ready: " + why, "blocked": why})
		return
	}
	if rl := b.Relation; rl != nil {
		if !map[string]bool{"handoff": true, "blocks": true, "pair": true, "resume": true}[rl.Kind] || !keys[rl.With] || rl.With == b.Step {
			writeJSON(w, 400, map[string]any{"error": "relation: kind handoff|blocks|pair|resume, with = another step's key"})
			return
		}
		rl.Why = trunc(strings.TrimSpace(rl.Why), 300)
	}
	e, err := s.featEvent(b.Session, featEv{Kind: "assign", Step: b.Step, Tab: b.Tab, Pane: b.Pane, Sid: b.Sid, Thread: s.featOwnerID(b.Session), Rel: b.Relation, Rev: dz.Rev})
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": "not saved: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": e.ID})
}
