package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Team news delivery. A thread's lead (threads.go) does not watch its team's panes itself, so the panel hands it
// the news: when one of its agents finishes, asks a question, stalls or exits, and what the feature supervisor
// (featsup.go) raised for it. The news arrives as one message per tick. Delivery is at-least-once and survives
// restarts: a hand-off is parked in an on-disk outbox until the lead's conversation provably contains it.

// teamFor returns the notes in ns that are news for thread t's lead (its team's agents finishing, asking, stuck or
// gone, and what the supervisor raised for it), as message lines, the notes' titles for the thread's timeline, and
// the ids of any owner comments among them ("session/clientId").
func teamFor(t *thread, ns []Note) (lines, titles, msgs []string) {
	for _, n := range ns {
		rest, pane := strings.CutPrefix(n.Go, "pane:")
		if m, ok := strings.CutPrefix(n.Ref, "msg/"); ok && n.Go == "thread:"+t.ID { // an owner comment: only while it leads that feature
			if sess, _, _ := strings.Cut(m, "/"); sess != t.Team {
				continue
			}
			msgs = append(msgs, m)
		}
		switch {
		case pane && (n.Kind == "done" || n.Kind == "asking" || n.Kind == "stalled" || n.Kind == "exited") &&
			rest[:max(0, strings.LastIndex(rest, ":"))] == t.Team:
			lines = append(lines, fmt.Sprintf("- #%d %s: %s [%s]%s", n.ID, n.Kind, n.Title, rest, cmp.Or(" "+n.Body, "")))
		case n.Go == "thread:"+t.ID && n.Ref != "":
			lines = append(lines, fmt.Sprintf("- #%d %s", n.ID, strings.TrimPrefix(n.Title, "🧵 "+t.Title+": "))+cmp.Or("\n  "+strings.ReplaceAll(n.Body, "\n", "\n  "), ""))
		default:
			continue
		}
		titles = append(titles, n.Title)
	}
	return
}

// panelStart is when this panel process started. News from before it (or before a thread existed) was already
// shown on the bell, so it is not replayed to a lead.
var panelStart = time.Now()

// teamPending is the outbox: one hand-off per thread whose lead hasn't acknowledged it yet, mirrored in
// threads.json.outbox.json. The thread's cursor (TeamSeen) moves to UpTo only when Hermes kept the turn that
// carried Text (assistant.go run), or Text is found in the lead's stored history. A turn that failed, a lost
// response, a restart or the owner taking it out of the queue leaves the hand-off here, to go again. a is the
// assistant it was loaded for (a new one, after a restart, reloads it from disk). Lock order: a.mu, then its own lock.
var teamPending = struct {
	sync.Mutex
	a *assistant
	m map[string]teamHand
}{m: map[string]teamHand{}}

// teamHand is one pending hand-off of team news to a lead.
type teamHand struct {
	UpTo     int64  `json:"upTo"`
	Text     string `json:"text"`
	Titles   string `json:"titles,omitempty"`   // for the thread's timeline once delivered
	Attempts int    `json:"attempts,omitempty"` // turns started with it
	Last     int64  `json:"last,omitempty"`     // when the last one started, or the last look found its outcome unknown (unix)
	// Maybe: a turn carrying it started and Hermes didn't refuse it outright: it may be in the lead's history. Then it
	// goes again only once that history is read and doesn't have it. Unknown: looks in a row that couldn't read it.
	Maybe   bool `json:"maybe,omitempty"`
	Unknown int  `json:"unknown,omitempty"`
	// Msgs: the owner comments it carries (session/clientId): it goes (or goes again) only while the thread still
	// leads each one's feature; otherwise it's dropped, recomposed without them, and they are told cancelled
	Msgs []string `json:"msgs,omitempty"`
}

// teamOwnsMsgs reports whether thread c still leads the feature of every owner comment in msgs (a.mu held).
func teamOwnsMsgs(a *assistant, c string, msgs []string) bool {
	t := a.thread(c)
	for _, m := range msgs {
		if sess, _, _ := strings.Cut(m, "/"); t == nil || t.Status == "archived" || t.Team != sess {
			return false
		}
	}
	return true
}

// teamMsgOutcomes records what became of owner comments (delivered, cancelled). It is filled under the outbox's
// locks and drained by featMsgFlush into the supervisor state's message receipts, without those locks held.
var teamMsgOutcomes = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

// teamMsgOutcome notes that the owner comments in msgs ended in state ("delivered" or "cancelled").
func teamMsgOutcome(msgs []string, state string) {
	teamMsgOutcomes.Lock()
	defer teamMsgOutcomes.Unlock()
	for _, m := range msgs {
		teamMsgOutcomes.m[m] = state
	}
}

// teamCancelQueued drops a queued turn that is about to start but carries owner comments for a feature its thread no
// longer leads. It reports whether it dropped the hand-off (a.mu held).
func teamCancelQueued(a *assistant, c, text string) bool {
	teamPending.Lock()
	defer teamPending.Unlock()
	h, ok := teamPending.m[c]
	if !ok || teamPending.a != a || h.Text != text || teamOwnsMsgs(a, c, h.Msgs) {
		return false
	}
	delete(teamPending.m, c)
	saveOutbox(a)
	teamMsgOutcome(h.Msgs, "cancelled")
	return true
}

// teamBackoff is how long after an attempt the next one waits: 30 s, doubling, at most 10 min.
func teamBackoff(attempts int) time.Duration {
	return min(30*time.Second<<min(max(attempts-1, 0), 5), 10*time.Minute)
}

// outboxFile is where the outbox is persisted, next to threads.json.
func outboxFile(a *assistant) string { return a.threadsFile + ".outbox.json" }

// saveOutbox writes the outbox to disk (teamPending held). It does nothing when threads are not persisted.
func saveOutbox(a *assistant) error {
	if a.threadsFile == "" {
		return nil
	}
	b, err := json.Marshal(teamPending.m)
	if err == nil {
		err = writeFileAtomic(outboxFile(a), b, 0o600)
	}
	if err != nil {
		log.Printf("team news outbox: %v", err)
	}
	return err
}

// teamAcked records that the turn which carried text to thread c's lead was kept by Hermes: the lead has its news
// (a.mu held).
func teamAcked(a *assistant, c, text string) {
	teamPending.Lock()
	defer teamPending.Unlock()
	if h, ok := teamPending.m[c]; ok && teamPending.a == a && h.Text == text {
		teamAck(a, c, h)
	}
}

// teamAck moves thread c's news cursor past hand-off h and drops it from the outbox (a.mu and teamPending held).
func teamAck(a *assistant, c string, h teamHand) {
	if x := a.thread(c); x != nil {
		x.TeamSeen = max(x.TeamSeen, h.UpTo)
		if h.Titles != "" {
			x.logEvent("team", h.Titles)
		}
		a.bump()
		if err := a.saveThreads(); err != nil { // the cursor isn't saved: keep the hand-off, a restart finds it delivered
			log.Printf("team news for %s: %v", c, err)
			return
		}
	}
	delete(teamPending.m, c)
	saveOutbox(a)
	teamMsgOutcome(h.Msgs, "delivered")
}

// teamStarted notes that a turn carrying text to thread c's lead starts (a.mu held): from here it may reach Hermes,
// so it must not be sent again until the lead's history says it never arrived.
func teamStarted(a *assistant, c, text string) {
	teamPending.Lock()
	defer teamPending.Unlock()
	if h, ok := teamPending.m[c]; ok && teamPending.a == a && h.Text == text && !h.Maybe {
		h.Maybe = true
		teamPending.m[c] = h
		saveOutbox(a)
	}
}

// teamNotKept notes that the turn which carried text to thread c's lead ended without Hermes keeping it (a.mu held).
// refused means Hermes turned it down before storing anything, so it is surely not in the history.
func teamNotKept(a *assistant, c, text string, refused bool) {
	teamPending.Lock()
	defer teamPending.Unlock()
	if h, ok := teamPending.m[c]; ok && teamPending.a == a && h.Text == text && refused && h.Maybe {
		h.Maybe = false
		teamPending.m[c] = h
		saveOutbox(a)
	}
}

// teamSendLocked makes one attempt at delivering h to thread c's lead (a.mu and teamPending held). The hand-off is
// saved before it goes, so a crash right after still knows to look for it in the lead's history instead of
// composing it anew.
func teamSendLocked(a *assistant, c string, h teamHand, now time.Time) {
	sent := h
	sent.Maybe = sent.Maybe || a.conv(c).live == nil // its turn starts now (queued: when it leaves the queue, teamStarted)
	teamPending.m[c] = sent
	if saveOutbox(a) != nil {
		teamPending.m[c] = h
		return // not durable: not sent either
	}
	queued, err := a.sendLocked(c, h.Text)
	if err != nil { // refused here: as it was
		teamPending.m[c] = h
		saveOutbox(a)
	}
	h = sent
	switch {
	case err != nil: // nothing reached Hermes: not an attempt, the next tick tries again
		log.Printf("team news for %s: %v", c, err)
	case !queued: // its turn started; queued, it starts when the lead's queue reaches it
		h.Attempts, h.Last = h.Attempts+1, now.Unix()
		teamPending.m[c] = h
		saveOutbox(a)
	}
}

// inHistory reports whether text is a user-side message in lead c's stored Hermes history. err is set when the
// history couldn't be read.
func inHistory(ctx context.Context, a *assistant, c, text string) (bool, error) {
	msgs, err := a.history(ctx, threadPrefix+c)
	for _, m := range msgs {
		if m.Role == "user" && (m.Kind == "" || m.Kind == "steer") && contentText(m.Content) == text {
			return true, nil
		}
	}
	return false, err
}

// teamDeliver hands each lead its team's news since its cursor, one message per thread, every tick. Each line
// carries its note's id, so a message delivered twice reads as the same news; a hand-off goes again only when its
// text isn't in the lead's history, and the notes it carries outlive the store's cap until it's acknowledged.
func (s *server) teamDeliver(now time.Time) {
	top := s.notes.lastID()
	a := s.ai
	a.mu.Lock()
	teamPending.Lock()
	if teamPending.a != a { // a new assistant (a restart): what's pending is on disk
		teamPending.a, teamPending.m = a, map[string]teamHand{}
		if b, err := os.ReadFile(outboxFile(a)); err == nil {
			if err := json.Unmarshal(b, &teamPending.m); err != nil {
				log.Printf("team news outbox: %v", err)
			}
		}
	}
	var due []thread
	retry := map[string]string{} // thread -> the hand-off to look for, then send again
	changed := false
	for _, t := range a.threads {
		if t.Status == "archived" {
			continue
		}
		if t.TeamSeen == 0 { // new to delivery: news from before the thread (or before this panel started) was the bell's
			t.TeamSeen, changed = -1, true
			cut := max(int64(t.Created), panelStart.Unix())
			for _, n := range s.notes.since(0) {
				if n.T < cut {
					t.TeamSeen = n.ID
				}
			}
		}
		h, pending := teamPending.m[t.ID]
		cv := a.conv(t.ID)
		switch {
		case pending && h.UpTo <= t.TeamSeen: // acknowledged before the outbox was saved
			delete(teamPending.m, t.ID)
			saveOutbox(a)
		case pending && (cv.live != nil && cv.live.User == h.Text || slices.ContainsFunc(cv.queue, func(q aiQueued) bool { return q.Text == h.Text })):
			// in its lead's conversation now: its turn's end says
		case pending:
			if h.Attempts+h.Unknown == 0 || !now.Before(time.Unix(h.Last, 0).Add(teamBackoff(h.Attempts+h.Unknown))) {
				retry[t.ID] = h.Text
			}
		case t.TeamSeen < top:
			due = append(due, *t)
		}
	}
	if changed {
		a.saveThreads()
	}
	teamPending.Unlock()
	a.mu.Unlock()

	// a hand-off whose outcome is unknown: Hermes may have kept it while its answer was lost
	found, unread := map[string]bool{}, map[string]error{}
	if len(retry) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		for id, text := range retry {
			found[id], unread[id] = inHistory(ctx, a, id, text)
		}
		cancel()
	}
	texts, msgs := map[string][2]string{}, map[string][]string{}
	for _, t := range due {
		lines, titles, ms := teamFor(&t, s.notes.since(t.TeamSeen))
		msgs[t.ID] = ms
		if len(lines) > 0 {
			texts[t.ID] = [2]string{threadFrom("team") + "\n" + strings.Join(lines, "\n") + "\nRead them with pane_chat / pane_screen and keep your team moving." + s.featWake(&t),
				trunc(strings.Join(titles, "; "), 300)}
		}
	}

	a.mu.Lock()
	teamPending.Lock()
	for id, text := range retry {
		h, ok := teamPending.m[id]
		switch {
		case !ok || h.Text != text || a.thread(id) == nil:
		case found[id]:
			teamAck(a, id, h)
		case unread[id] != nil && h.Maybe: // it may be there: sending it again could run the lead twice
			if h.Unknown++; h.Unknown == 1 {
				a.thread(id).logEvent("team", "delivery unknown: its lead's history can't be read; not sent again until it can")
				a.bump()
				a.saveThreads()
			}
			h.Last = now.Unix()
			teamPending.m[id] = h
			saveOutbox(a)
			log.Printf("team news for %s: delivery unknown, its history: %v", id, unread[id])
		case !teamOwnsMsgs(a, id, h.Msgs): // not there, and a comment in it no longer belongs to this lead: never again
			delete(teamPending.m, id)
			saveOutbox(a)
			teamMsgOutcome(h.Msgs, "cancelled")
		default: // not in the history (or surely refused before): it goes again
			h.Unknown = 0
			teamSendLocked(a, id, h, now)
		}
	}
	for _, t := range due {
		x := a.thread(t.ID)
		if _, busy := teamPending.m[t.ID]; x == nil || busy {
			continue
		}
		if tx, ok := texts[t.ID]; ok {
			teamSendLocked(a, t.ID, teamHand{UpTo: top, Text: tx[0], Titles: tx[1], Msgs: msgs[t.ID]}, now)
		} else if x.TeamSeen < top { // nothing in it for this lead
			x.TeamSeen = top
			a.saveThreads()
		}
	}
	floor := int64(math.MaxInt64) // the notes a lead still hasn't acknowledged stay in the store
	for _, t := range a.threads {
		if t.Status != "archived" {
			floor = min(floor, max(t.TeamSeen, 0))
		}
	}
	teamPending.Unlock()
	a.mu.Unlock()
	s.notes.setFloor(floor)
	s.featMsgFlush()
}

// deliveryUnknown returns the threads whose team news may or may not have reached their lead, and whose history
// can't be read yet to tell.
func (s *server) deliveryUnknown() []string {
	s.ai.mu.Lock()
	defer s.ai.mu.Unlock()
	teamPending.Lock()
	defer teamPending.Unlock()
	var out []string
	if teamPending.a == s.ai {
		for id, h := range teamPending.m {
			if h.Unknown > 0 {
				out = append(out, id)
			}
		}
	}
	slices.Sort(out)
	return out
}
