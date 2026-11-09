package main

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
)

// The owner's "seen" and "reviewed" receipts for a feature's event log (featevents.go). The feature page posts the
// ids of events that actually became visible, so it can show how many are still unseen; separately, the owner can
// mark an event as reviewed at the revision they saw. Seen is bookkeeping only: it is never a review or approval.

// featSeen is the owner's seen receipts for one feature. Frontier: every event up to it was seen; Above: seen ids past
// a gap, kept until the gap closes. Seen is never a review or an acknowledgement.
type featSeen struct {
	Frontier int64   `json:"frontier"`
	Above    []int64 `json:"above,omitempty"`
}

// featEvLookup returns the event with that id in this feature's log, or why there is none: "malformed" (not an
// ev-<n> id), "unknown" (never issued) or "compacted" (issued, then dropped from the log). c.mu must be held.
func (c *featSup) featEvLookup(session, id string) (featEv, string) {
	n, ok := evSeq(id)
	switch {
	case !ok:
		return featEv{}, "malformed"
	case n < 1 || n > c.EvSeq[session]:
		return featEv{}, "unknown"
	}
	evs := c.Events[session]
	if i, found := slices.BinarySearchFunc(evs, n, func(e featEv, n int64) int { return cmp.Compare(e.Seq, n) }); found {
		return evs[i], ""
	}
	return featEv{}, "compacted"
}

// featSeenPost handles POST /api/term/feature/seen {session, ids[]}: events the owner actually saw (the page sends
// them once visible). Any bad id refuses the whole batch, recording nothing, and names each with why, so the client
// drops those and resends.
func (s *server) featSeenPost(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Session string
		IDs     []string
	}
	if err := readJSONN(r, &b, 1<<15); err != nil || !sessionNameRe.MatchString(b.Session) || len(b.IDs) > 200 {
		writeJSON(w, 400, map[string]any{"error": "session and at most 200 ids"})
		return
	}
	c := s.sup
	c.mu.Lock()
	defer c.mu.Unlock()
	var bad []map[string]string
	var seqs []int64
	for _, id := range b.IDs {
		e, why := c.featEvLookup(b.Session, id)
		if why != "" {
			bad = append(bad, map[string]string{"id": id, "reason": why})
			continue
		}
		seqs = append(seqs, e.Seq)
	}
	if bad != nil {
		writeJSON(w, 400, map[string]any{"error": "not this feature's events; nothing recorded: drop these and resend the rest", "bad": bad})
		return
	}
	old := c.Seen[b.Session]
	cur := featSeen{Frontier: old.Frontier, Above: slices.Clone(old.Above)}
	for _, n := range seqs {
		if n > cur.Frontier && !slices.Contains(cur.Above, n) {
			cur.Above = append(cur.Above, n)
		}
	}
	slices.Sort(cur.Above)
	// the frontier moves only over contiguous retained events: compacted ids are gone (never counted as seen or unseen)
	evs := c.Events[b.Session]
	for len(cur.Above) > 0 {
		i, _ := slices.BinarySearchFunc(evs, cur.Frontier+1, func(x featEv, n int64) int { return cmp.Compare(x.Seq, n) })
		if i >= len(evs) || evs[i].Seq != cur.Above[0] {
			break
		}
		cur.Frontier, cur.Above = cur.Above[0], cur.Above[1:]
	}
	if len(seqs) > 0 {
		c.Seen[b.Session] = cur
		if err := c.save(); err != nil {
			c.Seen[b.Session] = old
			writeJSON(w, 500, map[string]any{"error": "not saved: " + err.Error()})
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "frontier": fmt.Sprintf("ev-%d", cur.Frontier), "unseen": c.unseenLocked(b.Session), "retainedFrom": c.retainedFromLocked(b.Session)})
}

// unseenLocked counts this feature's events that are past the frontier and not individually seen. c.mu must be held.
func (c *featSup) unseenLocked(session string) int {
	sn, n := c.Seen[session], 0
	for _, e := range c.Events[session] {
		if e.Seq > sn.Frontier && !slices.Contains(sn.Above, e.Seq) {
			n++
		}
	}
	return n
}

// featReviewedPost handles POST /api/term/feature/reviewed {session, id, rev}: the owner reviewed this event as it
// stood at rev. It answers 409 {changed} when the event's rev differs or a newer event about the same step has come
// since, so the owner looks again before reviewing.
func (s *server) featReviewedPost(w http.ResponseWriter, r *http.Request) {
	var b struct{ Session, ID, Rev string }
	if err := readJSONN(r, &b, 1<<12); err != nil || !sessionNameRe.MatchString(b.Session) {
		writeJSON(w, 400, map[string]any{"error": "session, id and rev"})
		return
	}
	c := s.sup
	c.mu.Lock()
	defer c.mu.Unlock()
	e, why := c.featEvLookup(b.Session, b.ID)
	if why != "" {
		writeJSON(w, 400, map[string]any{"error": "not this feature's event: " + why})
		return
	}
	if e.Rev != b.Rev {
		writeJSON(w, 409, map[string]any{"error": "it changed since you saw it", "changed": e.ID, "rev": e.Rev})
		return
	}
	if e.Step != "" && e.Kind != "log" { // a log line is reviewed as written; later lines don't supersede it
		for _, x := range c.Events[b.Session] {
			if x.Seq > e.Seq && x.Step == e.Step && x.Kind == e.Kind {
				writeJSON(w, 409, map[string]any{"error": "a newer " + e.Kind + " came for this step", "changed": x.ID, "rev": x.Rev})
				return
			}
		}
	}
	old, had := c.Reviewed[b.Session]
	m := map[string]string{}
	for k, v := range old {
		m[k] = v
	}
	m[e.ID] = e.Rev
	c.Reviewed[b.Session] = m
	if err := c.save(); err != nil {
		if had {
			c.Reviewed[b.Session] = old
		} else {
			delete(c.Reviewed, b.Session)
		}
		writeJSON(w, 500, map[string]any{"error": "not saved: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// retainedFromLocked returns the id of the oldest event still kept ("" if none); ids below it were compacted (gone:
// neither seen nor unseen). c.mu must be held.
func (c *featSup) retainedFromLocked(session string) string {
	if evs := c.Events[session]; len(evs) > 0 {
		return evs[0].ID
	}
	return ""
}
