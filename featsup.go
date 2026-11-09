package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The feature supervisor: on every sampling-loop tick (server.go) it takes a second look at each thread's team,
// against the dossier's PLAN.md (featdoc.go), so a long feature keeps moving when nobody is watching. It raises,
// once each: an agent waiting on an approval dialog, an agent that exited mid-step (and resumes it when the step is
// safe to run again: read, build, test, check), a team gone quiet with nothing running, a promised checkpoint
// ("due:") passed, a question waiting on the owner, a milestone and the whole feature verified. What it raises is a note
// (notes.go) its lead receives through the durable team-news cursor (threads.go teamDeliver), so a restart or a
// failed hand-off delivers it later instead of losing it. It never stops or kills anything: a long job that's
// still working is left alone however long it takes, and silence or a timeout never stands in for the owner.

const (
	// supEvery is the minimum time between supervisor passes (a pass runs on a panel terminal sample).
	supEvery     = 30 * time.Second
	approvalWait = 3 * time.Minute // an approval dialog waiting this long is a blocker
)

// stagnantFor: a working team with nothing running and nothing written for this long wakes its lead.
// CP_FEATURE_QUIET (a Go duration) shortens it for a scratch instance's replay only.
var stagnantFor = func() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("CP_FEATURE_QUIET")); err == nil && d > 0 {
		return d
	}
	return 45 * time.Minute
}()

// featSup is the supervisor's persistent memory (a JSON file), every key prefixed with its feature (the session's
// name). It also holds the state the feature page needs between visits: the event log, seen receipts, seals, holds.
type featSup struct {
	mu   sync.Mutex
	file string
	at   time.Time // the last look (not saved: the first look after a restart runs at once)
	// Crit: when each step's current words (its criterion) were first seen, by feature/key; 0 = before supervision
	Crit   map[string]int64 `json:"crit"`
	Primed map[string]bool  `json:"primed"` // features (crit) and threads (events) already learned
	// PrimedAt: when the panel first learned each feature's steps (their crit 0): the criterion time that evidence
	// provenance (featr1.go) uses for those steps
	PrimedAt  map[string]int64        `json:"primedAt,omitempty"`
	Sent      map[string]int64        `json:"sent"`  // events raised (their note is in), by feature/key: never twice
	Exits     int64                   `json:"exits"` // the notes read for exits, up to this id
	Seals     map[string]featSeal     `json:"seals"` // a step's receipt as first checked, by feature/key
	Approvals map[string]featApproval `json:"approvals"`
	Attests   map[string]featAttest   `json:"attests"`
	Accepts   map[string]string       `json:"accepts"`  // each feature's outcome set as the owner accepted it (its hash)
	Holds     map[string]featHold     `json:"holds"`    // by feature/tab (lowercase)
	Resumes   map[string]featResume   `json:"resumes"`  // by conversation id
	Homes     map[string]string       `json:"homes"`    // each feature's record folder, once it has docs
	Paths     map[string][]string     `json:"paths"`    // each feature's agents' transcripts, ever seen
	Nudges    map[string]featNudge    `json:"nudges"`   // the reconcile wakes sent to each thread's lead
	Promoted  map[string]string       `json:"promoted"` // each feature's last committed requirements promote (its txn id)
	// Completed: threads whose done passed this panel's done gate, by thread id. Only those reopen when their record
	// changes; a done from before the gate (an older panel) is left as it is, its page saying it no longer holds.
	Completed map[string]int64 `json:"completed"`
	// Events: each feature's append-only event log (featevents.go); EvSeq its last id ever given, never reused
	Events map[string][]featEv `json:"events"`
	EvSeq  map[string]int64    `json:"evSeq"`
	// Seen: the owner's seen receipts per feature (featseen.go); Reviewed: explicit reviews, event id → the rev seen
	Seen     map[string]featSeen          `json:"seen"`
	Reviewed map[string]map[string]string `json:"reviewed"`
	// ObjLegacy: features this panel knew before objectives (D-ids) existed, recorded once at that upgrade (ObjV):
	// only their untagged outcomes don't gate; a missing id never makes a feature historical by itself
	ObjLegacy map[string]bool `json:"objLegacy"`
	// Msgs: owner comment receipts by session/clientId, kept apart from the (compactable) events: dedupe and state
	Msgs map[string]featMsgRec `json:"msgs"`
	// AssessBy: who an agent write claimed to be (X-Agent-Sid) when it put a step's assessment in, by feature/key
	AssessBy map[string]featAssessBy `json:"assessBy"`
	// Sealed: file evidence as first seen through a write path, by feature/step key/evidence line (featr1.go)
	Sealed map[string]featSealRec `json:"sealed"`
	// ReconciledAt: when a supervisor pass over each feature last completed (unix), by feature; absent: never
	ReconciledAt map[string]int64 `json:"reconciledAt,omitempty"`
	ObjV         int              `json:"objV"`
}

// featNudge is the last reconcile wake sent to a thread's lead: on what record (Fp), when, and how many wakes have
// gone out since the record last changed.
type featNudge struct {
	Fp string `json:"fp"`
	At int64  `json:"at"`
	N  int    `json:"n"`
}

// nudgeBackoff returns how long to wait before wake number n+1 on an unchanged record: the wait doubles from
// stagnantFor (45 minutes by default) up to 6 hours.
func nudgeBackoff(n int) time.Duration {
	return min(stagnantFor<<max(n-1, 0), 6*time.Hour)
}

// featSeal pins a step's receipt as it was first checked, so a later change to the run's output is detectable.
type featSeal struct {
	Claim string `json:"claim"` // the run's tool_use id
	Sum   string `json:"sum"`   // its recorded output's hash then
}

// newFeatSup loads the supervisor's state from file (a missing or unreadable file starts empty) and makes sure every
// map exists. On the first start after objectives (D-ids) were introduced, every feature already known is marked
// legacy so its untagged outcomes keep working.
func newFeatSup(file string) *featSup {
	c := &featSup{file: file}
	b, rerr := os.ReadFile(file)
	if rerr == nil {
		_ = json.Unmarshal(b, c)
	}
	initMap(&c.Crit)
	initMap(&c.Sent)
	initMap(&c.Primed)
	initMap(&c.PrimedAt)
	initMap(&c.Seals)
	initMap(&c.Approvals)
	initMap(&c.Attests)
	initMap(&c.Accepts)
	initMap(&c.Holds)
	initMap(&c.Resumes)
	initMap(&c.Homes)
	initMap(&c.Paths)
	initMap(&c.Events)
	initMap(&c.EvSeq)
	initMap(&c.Seen)
	initMap(&c.Reviewed)
	initMap(&c.ObjLegacy)
	initMap(&c.Msgs)
	initMap(&c.AssessBy)
	initMap(&c.Sealed)
	initMap(&c.ReconciledAt)
	if c.ObjV == 0 { // the upgrade: every feature known until now keeps its untagged outcomes
		if rerr == nil {
			for k := range c.Homes {
				c.ObjLegacy[k] = true
			}
		}
		c.ObjV = 1
		c.save()
	}
	initMap(&c.Nudges)
	initMap(&c.Promoted)
	initMap(&c.Completed)
	return c
}

// initMap makes *m an empty map if it is nil (a JSON file written by an older version lacks newer fields).
func initMap[V any](m *map[string]V) {
	if *m == nil {
		*m = map[string]V{}
	}
}

// save writes the state file atomically; c.mu must be held. A panel with no state file (tests) just skips it.
func (c *featSup) save() error {
	if c.file == "" {
		return nil
	}
	b, _ := json.Marshal(c)
	err := writeFileAtomic(c.file, b, 0o600)
	if err != nil {
		log.Printf("feature supervisor: save: %v", err)
		return err
	}
	return nil
}

// accepted returns the hash of the outcome set the owner accepted for session ("" if none); see featAcceptRev.
func (c *featSup) accepted(session string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Accepts[session]
}

// crits: when each of a session's steps got its current words. A session's first look learns them all as 0 (their
// evidence counts as it stands); a step reworded or added later starts now, so older evidence no longer backs it.
// Keys of removed steps stay in the file; prune them per session if it ever grows past a few thousand.
func (c *featSup) crits(session string, keys []string, now time.Time) map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out, changed := map[string]int64{}, false
	first := !c.Primed["crit:"+session]
	for _, k := range keys {
		v, ok := c.Crit[session+"/"+k]
		if !ok {
			if !first {
				v = now.Unix()
			}
			c.Crit[session+"/"+k], changed = v, true
		}
		out[k] = v
	}
	if first && len(keys) > 0 {
		c.Primed["crit:"+session], c.PrimedAt[session], changed = true, now.Unix(), true
	}
	if changed {
		c.save()
	}
	return out
}

// critAt returns a step's criterion time for evidence provenance (featr1.go): when its words were first seen, else
// when the panel first learned the feature (0 = unknown: a feature primed before PrimedAt existed; no provenance then).
func (c *featSup) critAt(session string, crit int64) int64 {
	if crit != 0 {
		return crit
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.PrimedAt[session]
}

// sentAt returns when the event key was raised (unix seconds), or 0 if it never was.
func (c *featSup) sentAt(key string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Sent[key]
}

// once: true the first time key is raised (and remembers it)
func (c *featSup) once(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.Sent[key]; ok {
		return false
	}
	c.Sent[key] = now.Unix()
	c.save()
	return true
}

// raise: an event for a thread's lead, once, under the feature's own key. Material ones (blockers, missed checkpoints,
// the owner's questions, verified milestones) go to the owner too, as a "thread" note (the bell and Telegram); the rest
// only to the lead. The note goes in first, idempotent by its Ref; then it's marked sent: a crash in between neither
// loses the event nor adds it twice. A note that couldn't be saved isn't sent: the next look raises it again.
func (s *server) raise(now time.Time, th *thread, material bool, key, level, title, body string) {
	full := th.Team + "/" + key
	if s.sup.sentAt(full) != 0 {
		return
	}
	kind := "feature"
	if material {
		kind = "thread"
	}
	var err error
	s.notes.mu.Lock()
	if !slices.ContainsFunc(s.notes.Notes, func(n Note) bool { return n.Ref == full }) {
		_, err = s.notes.addSaved(now, kind, level, "🧵 "+th.Title+": "+title, body, "thread:"+th.ID, full)
	}
	s.notes.mu.Unlock()
	if err != nil {
		log.Printf("feature supervisor: %s not raised: %v", full, err)
		return
	}
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	if _, ok := s.sup.Sent[full]; ok {
		return
	}
	s.sup.Sent[full] = now.Unix()
	if err := s.sup.save(); err != nil { // not saved as sent: raised again, its note (by Ref) isn't added twice
		delete(s.sup.Sent, full)
	}
}

// supervise is called on every sampling tick but only looks every supEvery. One look handles exits and resume
// confirmations across all threads, then runs supThread for each thread whose team session is live.
func (s *server) supervise(now time.Time, tm Terminals) {
	s.sup.mu.Lock()
	if now.Sub(s.sup.at) < supEvery {
		s.sup.mu.Unlock()
		return
	}
	s.sup.at = now
	s.sup.mu.Unlock()

	s.ai.mu.Lock()
	var threads []thread
	for _, t := range s.ai.threads {
		if t.Status != "archived" {
			threads = append(threads, *t)
		}
	}
	s.ai.mu.Unlock()
	owner := map[string]*thread{}
	for i := range threads {
		owner[threads[i].Team] = &threads[i]
	}

	s.supExits(now, tm, owner)
	s.supConfirm(now, tm, owner)
	var done []string
	for i := range threads {
		th := &threads[i]
		var se *TSession
		for j := range tm.Sessions {
			if tm.Sessions[j].Name == th.Team {
				se = &tm.Sessions[j]
			}
		}
		if se == nil {
			continue
		}
		s.supThread(now, th, se)
		done = append(done, th.Team) // reached only when the pass returned normally
	}
	// reconciledAt: the pass over each of these features completed (it ran to its end, whatever it found or sent): a
	// completed check, not a claim that its work succeeded or its steps were verified. Kept only once it's saved, so
	// the page never shows a freshness a restart would lose.
	if len(done) > 0 {
		s.sup.mu.Lock()
		was := maps.Clone(s.sup.ReconciledAt)
		for _, team := range done {
			s.sup.ReconciledAt[team] = now.Unix()
		}
		if err := s.sup.save(); err != nil {
			s.sup.ReconciledAt = was
			log.Printf("supervisor: reconciledAt not saved, kept at the last saved pass: %v", err)
		}
		s.sup.mu.Unlock()
	}
}

// supThread is one look at one thread's team session se: it briefs agents that are new to the team, raises approval
// stalls, missed checkpoints, owner questions and verified milestones, and wakes the lead when work that could move
// has sat idle (see the reconcile block below).
func (s *server) supThread(now time.Time, th *thread, se *TSession) {
	s.featSealSteps(th.Team, "look") // first sight only: never replaces a seal
	d := s.featDossier(th.Team)
	s.sup.mu.Lock()
	first := !s.sup.Primed["thread:"+th.ID]
	s.sup.Primed["thread:"+th.ID] = true
	s.sup.mu.Unlock()

	// Approval stalls, and which lanes have something running. A lane is one tab of the team's session (the workstream
	// of one agent); a plan step belongs to the lane named by its "@Tab" owner.
	busyTab, tabLast := map[string]bool{}, map[string]int64{} // each lane (tab): an agent running in it; its latest activity
	hasRecord := strings.TrimSpace(d.Plan+d.Feature) != ""
	for _, tb := range se.Tabs {
		for _, p := range tb.Panes {
			if p.Agent == nil {
				continue
			}
			// an agent new to the team (started, or resumed) reads the record before anything else: once per process
			if key := fmt.Sprintf("brief:%s:%d", p.Agent.sid, p.Agent.PID); hasRecord && p.Agent.sid != "" && p.State == "waiting" {
				if s.sup.sentAt(key) == 0 { // the first look too: a team already running gets the record as well
					// once: tried again only when certainly nothing was typed (the owner's text in the box, or zellij failing
					// before typing); a failure while typing may have left part of it, so it isn't repeated
					err := s.featTell(se.Name, p.ID, p.Kind, featBrief(tilde(d.Dir), se.Name, tb.Name, d.Main))
					switch {
					case errors.Is(err, errBoxTaken), errors.Is(err, errNotTyped):
						log.Printf("feature %s: brief %s: %v (tried again next look)", se.Name, tb.Name, err)
					case err != nil:
						s.sup.once(key, now)
						s.featLog(se.Name, "panel: the brief to "+tb.Name+" failed while typing ("+err.Error()+"); not repeated")
					default:
						s.sup.once(key, now)
						s.featLog(se.Name, "panel: briefed "+tb.Name+" on the record as it started")
					}
				}
			}
			lane := strings.ToLower(tb.Name)
			tabLast[lane] = max(tabLast[lane], p.Since, p.Agent.LastAt, p.Output)
			switch p.State {
			case "working", "background", "starting", "stalled":
				busyTab[lane] = true // stalled has its own note (notes.go); a slow job is not its lane being idle
			case "asking":
				busyTab[lane] = true
				if p.Since > 0 && now.Unix()-p.Since >= int64(approvalWait/time.Second) {
					s.raise(now, th, true, fmt.Sprintf("approval:%s:%d:%d", se.Name, p.ID, p.Since), "warn",
						tb.Name+" has waited "+fmtDur(now.Unix()-p.Since)+" on a dialog", cmp.Or(p.Detail, "Needs an answer")+" [pane:"+se.Name+":"+strconv.Itoa(p.ID)+"]")
				}
			}
		}
	}

	// the plan: checkpoints, questions, verified milestones
	all, verified := 0, 0
	for _, m := range d.Milestones {
		for _, st := range m.Steps {
			all++
			if st.State == "done" {
				verified++
			}
			if st.Missed {
				s.raise(now, th, true, "missed:"+st.Key+":"+st.Due, "warn", "missed the checkpoint promised for "+st.Due, st.Text)
			}
			if st.State == "asking" && st.Ask != "" && st.Answer == "" {
				s.raise(now, th, true, "ask:"+st.Key+":"+featStepKey(st.Ask, ""), "warn", "a question for you on a step", st.Text+"\nQ: "+st.Ask)
			}
		}
		if len(m.Steps) > 0 && m.Done == len(m.Steps) {
			s.raiseOrLearn(now, th, first, "milestone:"+featStepKey(th.Team, m.Title), "ok", "milestone verified: "+m.Title, fmt.Sprintf("%d steps, each with its evidence", len(m.Steps)))
		}
	}
	if all > 0 && verified == all {
		s.raiseOrLearn(now, th, first, "verified:"+featStepKey(th.Team, d.Plan), "ok", "every step verified", "The lead can mark the feature done.")
	}

	// reconcile, lane by lane: a step that can move (not done, not held, not waiting on the owner) whose owner's tab has
	// nothing running and has been quiet, with the plan unwritten, for stagnantFor, is idle work. Any idle work wakes the
	// thread's lead, through the durable outbox, with the record as it stands (featWake) and those lanes named; busy,
	// held or blocked lanes elsewhere neither hide it nor are touched. Again on the same idle work only after a growing,
	// persisted pause. The lead writing PLAN.md restarts every lane's quiet period, so a wake can't call the next one up;
	// a lead still in its turn isn't woken.
	var planAt int64
	if fi, err := os.Stat(s.featPath(th.Team, "PLAN.md")); err == nil {
		planAt = fi.ModTime().Unix()
	}
	quiet := int64(stagnantFor / time.Second)
	var idle, keys []string
	deps := featDepBlocks(d.Milestones) // waiting on another step, or a bad dependency: not movable
	for _, m := range d.Milestones {
		for _, st := range m.Steps {
			lane := strings.ToLower(st.Owner)
			if st.State == "done" || st.State == "asking" || st.Blocked != "" || busyTab[lane] || deps[st.Key] != "" {
				continue
			}
			if now.Unix()-max(planAt, tabLast[lane]) < quiet {
				continue
			}
			who := "no one"
			if st.Owner != "" {
				who = "@" + st.Owner
				if _, ok := tabLast[lane]; !ok {
					who += " (no agent in that tab)"
				}
			}
			idle = append(idle, fmt.Sprintf("%s %q, %s, %s", st.Key, trunc(st.Text, 80), st.State, who))
			keys = append(keys, st.Key)
		}
	}
	if (th.Status != "working" && th.Status != "needs_you") || len(idle) == 0 { // needs_you is scoped to its asking steps (skipped above), not a stop for the other lanes
		return
	}
	s.ai.mu.Lock()
	cv := s.ai.conv(th.ID)
	busy := cv.live != nil || len(cv.queue) > 0
	s.ai.mu.Unlock()
	fp := d.Rev + "/" + d.Revs["feature"] + "/" + featRev([]byte(strings.Join(keys, ",")))
	s.sup.mu.Lock()
	w := s.sup.Nudges[th.ID]
	if w.Fp != fp {
		w.N = 0
	}
	due := !busy && (w.N == 0 || now.Unix()-w.At >= int64(nudgeBackoff(w.N)/time.Second))
	s.sup.mu.Unlock()
	if !due {
		return
	}
	s.raise(now, th, false, fmt.Sprintf("stagnant:%s:%d", th.ID, now.Unix()), "warn",
		fmt.Sprintf("%d step(s) that can move have been idle %s or more", len(idle), fmtDur(quiet)),
		"Idle lanes (nothing running in the owner's tab, the plan unwritten since):\n  - "+strings.Join(idle, "\n  - ")+
			"\nOther lanes are left as they are. Reconcile with the record below: research what blocks, re-plan, put an agent on these, or say why they wait.")
	s.sup.mu.Lock()
	s.sup.Nudges[th.ID] = featNudge{fp, now.Unix(), w.N + 1}
	if err := s.sup.save(); err != nil { // not saved: the next look may wake it once more, never less
		log.Printf("feature %s: nudge: %v", th.Team, err)
	}
	s.sup.mu.Unlock()
}

// raiseOrLearn is raise for states that should not be announced when first discovered: on the first look at a thread
// (after a restart, or when the supervisor is first deployed) the key is only recorded as already sent.
func (s *server) raiseOrLearn(now time.Time, th *thread, first bool, key, level, title, body string) {
	if first {
		s.sup.once(key, now)
		return
	}
	s.raise(now, th, true, key, level, title, body)
}

// sidRe matches a conversation id that is safe to put in a shell command (see supResume).
var sidRe = regexp.MustCompile(`^[0-9A-Za-z-]{8,64}$`)

// supExits: an agent of a thread's team that exited mid-work. Its whole context is judged, not one step: when every
// step its tab was in the middle of is one the owner approved as safe to redo, the conversation is resumed with the
// scoped instructions as its very first message (nothing runs before them). Anything else (a change, an unsaid kind,
// a Codex, a hold already there) holds the tab: none of its steps can be done until the owner releases it, and
// nothing is resumed or replayed.
func (s *server) supExits(now time.Time, tm Terminals, owner map[string]*thread) {
	s.sup.mu.Lock()
	from := s.sup.Exits
	s.sup.mu.Unlock()
	upTo := from
	for _, n := range s.notes.since(from) {
		upTo = max(upTo, n.ID)
		if n.Kind != "exited" || n.Exit == nil {
			continue
		}
		rest, _ := strings.CutPrefix(n.Go, "pane:")
		i := strings.LastIndexByte(rest, ':')
		if i < 0 {
			continue
		}
		session, pane := rest[:i], rest[i+1:]
		th := owner[session]
		if th == nil {
			continue
		}
		d := s.featDossier(session)
		var doing, unsafe []featPlanStep
		receipt := false
		for _, m := range d.Milestones {
			for _, st := range m.Steps {
				if (st.State == "doing" || st.State == "claimed") && strings.EqualFold(st.Owner, n.Exit.Tab) {
					doing = append(doing, st)
					if !st.Approval.Kind {
						unsafe = append(unsafe, st)
					}
					receipt = receipt || st.Receipt != ""
				}
			}
		}
		if len(doing) == 0 {
			continue // it was between steps: its lead hears of the exit (teamFor) and decides
		}
		s.sup.mu.Lock()
		_, held := s.sup.Holds[session+"/"+strings.ToLower(n.Exit.Tab)]
		s.sup.mu.Unlock()
		switch {
		case held:
		case len(unsafe) > 0 || n.Exit.Kind == "codex" || receipt:
			why := n.Exit.Tab + " exited in the middle of work whose effect is unknown"
			s.featHold(now, th, session, n.Exit.Tab, why, doing, receipt)
		default:
			s.supResume(now, th, session, pane, n.Exit, doing, d)
		}
	}
	s.sup.mu.Lock()
	if upTo != s.sup.Exits {
		s.sup.Exits = upTo
		s.sup.save()
	}
	s.sup.mu.Unlock()
}

// featHold holds a tab's whole context: none of its unfinished steps can be verified done until the owner releases the
// hold on the feature page, and the lead is told why. Nothing is resumed or replayed.
func (s *server) featHold(now time.Time, th *thread, session, tab, why string, steps []featPlanStep, receipt bool) {
	var names, keys []string
	for _, st := range steps {
		names, keys = append(names, st.Text), append(keys, st.Key)
	}
	s.sup.mu.Lock()
	s.sup.Holds[session+"/"+strings.ToLower(tab)] = featHold{why, names, keys, now.Unix()}
	err := s.sup.save()
	s.sup.mu.Unlock()
	if err != nil { // held in memory, so nothing resumes now; the note says it may not survive a restart
		why += " (the hold couldn't be saved: " + err.Error() + ")"
	}
	body := "Steps: " + strings.Join(names, "; ") + "\nNothing was resumed or replayed. Reconcile the live state"
	if receipt {
		body += " (a receipt is named: check it against the live state)"
	}
	body += ". The owner releases the hold on the feature page; that only lets you re-plan, it authorizes no change. Other tabs' steps go on."
	s.raise(now, th, true, "held:"+strings.ToLower(tab)+":"+strconv.FormatInt(now.UnixNano(), 10), "bad", "held: "+why, body)
}

// supResume reopens an exited agent's conversation for the given safe steps only. Each try is recorded before it's
// made and confirmed when the conversation is seen running again (supConfirm); a try that isn't confirmed is reported,
// never blindly repeated.
func (s *server) supResume(now time.Time, th *thread, session, pane string, ex *paneSeen, steps []featPlanStep, d featDossier) {
	var names []string
	for _, st := range steps {
		names = append(names, fmt.Sprintf("%q (%s)", st.Text, st.Kind))
	}
	switch fi, err := os.Stat(ex.Cwd); {
	case !sidRe.MatchString(ex.Sid) || ex.Cwd == "" || err != nil || !fi.IsDir():
		s.featHold(now, th, session, ex.Tab, ex.Tab+" exited and its conversation or folder can't be found to resume it", steps, false)
		return
	}
	s.sup.mu.Lock()
	r := s.sup.Resumes[ex.Sid]
	if r.State == "launching" || r.Tries >= 3 {
		s.sup.mu.Unlock()
		if r.Tries >= 3 {
			s.featHold(now, th, session, ex.Tab, ex.Tab+" was resumed 3 times and keeps exiting", steps, false)
		}
		return
	}
	r = featResume{At: now.Unix(), State: "launching", Tries: r.Tries + 1, Session: session}
	prev := s.sup.Resumes[ex.Sid]
	s.sup.Resumes[ex.Sid] = r
	if err := s.sup.save(); err != nil { // no launch the panel couldn't account for after a restart
		s.sup.Resumes[ex.Sid] = prev
		s.sup.mu.Unlock()
		s.featHold(now, th, session, ex.Tab, ex.Tab+" exited and its resume couldn't be recorded, so it wasn't started", steps, false)
		return
	}
	s.sup.mu.Unlock()
	prompt := panelMark + "You were interrupted and are resumed for these steps only: " + strings.Join(names, ", ") +
		" (the owner approved them as safe to redo). Redo them from their start. Change nothing else and don't continue any other step " +
		"until your lead says so. The feature's record: " + tilde(d.Dir) + " (PLAN.md rev " + d.Rev + "); update it only through the panel's record writer."
	cmd := "cd " + shq(ex.Cwd) + " && " + claudeCmd + " --resume " + ex.Sid + " " + shq(prompt)
	id, _ := strconv.Atoi(pane)
	p, ok := s.term.pane(session, id)
	var err error
	if !ok || p.Agent != nil || p.Kind != "shell" || p.State != "idle" { // not a shell at its prompt: a tab of its own
		id, err = s.z.newTab(session, ex.Tab, ex.Cwd)
	}
	if err == nil {
		if err = s.z.write(session, id, cmd); err == nil {
			err = s.z.keys(session, id, "Enter")
		}
	}
	what := fmt.Sprintf("resume %s:%d %s (%s) for %s", session, id, ex.Sid, tilde(ex.Cwd), strings.Join(names, ", "))
	s.sup.mu.Lock()
	if err != nil {
		r.State = "failed"
	}
	s.sup.Resumes[ex.Sid] = r
	s.sup.save()
	s.sup.mu.Unlock()
	if err != nil {
		s.audit.add("feature-resume-fail", what+": "+err.Error(), "panel")
		s.raise(now, th, true, "resumefail:"+ex.Sid+":"+strconv.Itoa(r.Tries), "bad", ex.Tab+" exited mid-step and its resume failed", err.Error())
		return
	}
	s.audit.add("feature-resume", what, "panel")
	s.term.invalidate(session)
	s.raise(now, th, false, "resumed:"+ex.Sid+":"+strconv.Itoa(r.Tries), "ok", "resumed "+ex.Tab+" for its safe steps only",
		strings.Join(names, ", ")+". Its first message limits it to those steps.")
}

// supConfirm: a resume is confirmed once its conversation runs again; one still unseen after resumeConfirm is
// reported (its pane needs a look), not tried again
func (s *server) supConfirm(now time.Time, tm Terminals, owner map[string]*thread) {
	alive := map[string]bool{}
	for _, se := range tm.Sessions {
		for _, tb := range se.Tabs {
			for _, p := range tb.Panes {
				if p.Agent != nil {
					alive[p.Agent.sid] = true
				}
			}
		}
	}
	s.sup.mu.Lock()
	var lost []string
	lostR := map[string]featResume{}
	for sid, r := range s.sup.Resumes {
		switch {
		case r.State != "launching":
		case alive[sid]:
			r.State = "confirmed"
			s.sup.Resumes[sid] = r
		case now.Unix()-r.At >= int64(resumeConfirm/time.Second):
			r.State = "unconfirmed"
			s.sup.Resumes[sid] = r
			lost, lostR[sid] = append(lost, sid), r
		default:
			continue
		}
		s.sup.save()
	}
	s.sup.mu.Unlock()
	for _, sid := range lost {
		r := lostR[sid]
		if th := owner[r.Session]; th != nil {
			s.raise(now, th, true, "unconfirmed:"+sid+":"+strconv.Itoa(r.Tries), "bad", "a resume wasn't confirmed",
				"Conversation "+sid+" didn't come back within "+fmtDur(int64(resumeConfirm/time.Second))+". Look at its pane; nothing is re-run.")
		}
	}
}

// featReopen: a done feature whose record no longer passes the done gate (a plan or outcome changed, through the panel
// or straight in the files) is reopened: its thread goes back to working and its lead and the owner hear why. Run after
// every record write, on every supervisor look and on reads, so a direct file edit isn't shown as a current done.
func (s *server) featReopen(session string) {
	th := s.featOwner(session)
	if th == nil || th.Status != "done" {
		return
	}
	s.sup.mu.Lock()
	gated := s.sup.Completed[th.ID] != 0
	s.sup.mu.Unlock()
	if !gated { // a done from before the gate: shown as no longer holding (doneStale), never flipped
		return
	}
	note, ok := s.ai.reopen(th.ID, func(t *thread) string {
		if dir, _ := s.featHome(t.Team, t.ID); dir != "" {
			s.recoverPromoteTxn(dir) // featPlanMu held: judged on the settled record
		}
		return s.featGate(t.Team, t.ID, nil, true)
	})
	if !ok {
		return
	}
	s.featCompleted(th.ID, false)
	s.featLog(session, "panel: reopened (was done): "+note)
	s.audit.add("feature-reopen", session+" "+th.ID+": "+note, "panel")
	s.notes.push("thread", "warn", "🧵 "+th.Title+" reopened", note, "thread:"+th.ID)
}

// featCompleted records (or forgets) that a thread's done passed the gate
func (s *server) featCompleted(id string, done bool) {
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	if done {
		s.sup.Completed[id] = time.Now().Unix()
	} else {
		delete(s.sup.Completed, id)
	}
	if err := s.sup.save(); err != nil {
		log.Printf("thread %s: completion mark: %v", id, err)
	}
}

// featGate: why the feature can't be done with plan as its PLAN.md (nil: the current one); "" when it can. Every step
// verified, no hold, and (outcomes) the owner's acceptance of FEATURE.md's desired outcomes, which must be listed
// (prose alone defines nothing checkable), as they stand, with this plan's results (its revision).
func (s *server) featGate(session, id string, plan []byte, outcomes bool) string {
	dir, conflict := s.featHome(session, id)
	if conflict != "" {
		return conflict
	}
	if _, err := os.Stat(filepath.Join(dir, promoteTxnFile)); err == nil {
		return "a requirements promote was cut short and isn't settled yet"
	}
	if plan == nil {
		plan, _ = os.ReadFile(filepath.Join(dir, "PLAN.md"))
	}
	s.sup.mu.Lock()
	var held []string
	for k, h := range s.sup.Holds {
		if strings.HasPrefix(k, session+"/") {
			held = append(held, h.Why)
		}
	}
	s.sup.mu.Unlock()
	if len(held) > 0 { // whatever the plan now says: held work may have unknown effects until the owner releases it
		slices.Sort(held)
		return "held until the owner releases it: " + strings.Join(held, "; ")
	}
	ms := parseMilestones(string(plan))
	s.featVerifyAt(session, ms, true) // the gate reads evidence bytes again, never a cached look
	var open []string
	n := 0
	for _, m := range ms {
		for _, st := range m.Steps {
			n++
			if st.State != "done" {
				open = append(open, fmt.Sprintf("%q is %s%s", trunc(st.Text, 60), st.State, map[bool]string{true: " (" + st.Why + ")"}[st.Why != ""]))
			}
		}
	}
	for _, m := range ms {
		for _, st := range m.Steps {
			if len(st.AfterInvalid) > 0 {
				return fmt.Sprintf("%q has an invalid dependency: %s", trunc(st.Text, 60), featDepBlocks(ms)[st.Key])
			}
		}
	}
	feat, _ := os.ReadFile(filepath.Join(dir, "FEATURE.md"))
	s.sup.mu.Lock()
	legacy := s.sup.ObjLegacy[session]
	s.sup.mu.Unlock()
	for _, it := range featObjectives(featDossier{Feature: string(feat), Milestones: ms}).Items {
		switch {
		case it.ID == "D?" && !legacy:
			return "the outcome " + strconv.Quote(trunc(it.Text, 60)) + " has no id: write it as D<n>: … with the steps that cover it"
		case it.ID != "D?" && len(it.Steps) == 0:
			return "objective " + it.ID + " (" + trunc(it.Text, 60) + ") has no step covering it"
		case it.ID != "D?" && !it.Met && len(open) == 0:
			return "objective " + it.ID + " isn't met: " + strings.Join(it.Missing, "; ")
		}
	}
	switch {
	case n == 0:
		return "its plan has no steps to verify"
	case len(open) > 0:
		return fmt.Sprintf("%d of %d steps aren't verified: %s", len(open), n, strings.Join(open[:min(len(open), 5)], "; "))
	case !outcomes:
		return ""
	}
	feature, _ := os.ReadFile(filepath.Join(dir, "FEATURE.md"))
	oc := featOutcomes(string(feature))
	switch {
	case len(oc) == 0:
		return "FEATURE.md lists no desired outcomes (\"- [ ] ...\" lines) for the owner to accept"
	case s.sup.accepted(session) != featAcceptRev(oc, featRev(plan)):
		return fmt.Sprintf("the owner hasn't accepted its %d desired outcome(s) as they stand, with this plan's verified results, on the feature page", len(oc))
	}
	return ""
}

// featAcceptRev: what the owner's acceptance is bound to: the outcome set and the plan revision whose results they saw
func featAcceptRev(outcomes []string, planRev string) string {
	return featRev([]byte(strings.Join(outcomes, "\n") + "\x00" + planRev))
}

// featWake: what a lead's team update carries besides the news: the record as it stands now (read from its files at
// this moment, so the lead reasons from it, not from older messages), how fresh the team sample is, and what can move
// next. The panel enforces delivery and safety; choosing the path to done is the lead's.
func (s *server) featWake(t *thread) string {
	if t.Team == "" {
		return ""
	}
	d := s.featDossier(t.Team)
	if len(d.Milestones) == 0 && strings.TrimSpace(d.Feature) == "" {
		return ""
	}
	_, at := s.term.sample()
	seen := "never yet"
	if !at.IsZero() {
		seen = at.UTC().Format("15:04:05") + " UTC (" + fmtDur(int64(time.Since(at)/time.Second)) + " ago)"
	}
	var verified, n int
	var held, asking, claimed, next []string
	for _, m := range d.Milestones {
		for _, st := range m.Steps {
			n++
			line := fmt.Sprintf("%s %q%s", st.Key, trunc(st.Text, 80), map[bool]string{true: " @" + st.Owner}[st.Owner != ""])
			switch {
			case st.State == "done":
				verified++
			case st.Blocked != "":
				held = append(held, line+": "+trunc(st.Blocked, 120))
			case st.State == "asking":
				asking = append(asking, line)
			case st.State == "claimed":
				claimed = append(claimed, line+": "+trunc(st.Why, 120))
			default:
				next = append(next, line+" ["+st.State+map[bool]string{true: ", missed its checkpoint"}[st.Missed]+"]")
			}
		}
	}
	list := func(h string, xs []string) string {
		if len(xs) == 0 {
			return ""
		}
		more := ""
		if len(xs) > 8 {
			xs, more = xs[:8], fmt.Sprintf("\n  … and %d more", len(xs)-8)
		}
		return "\n" + h + ":\n  - " + strings.Join(xs, "\n  - ") + more
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n[Record snapshot as of %s] %s (PLAN.md rev %s); the team was last sampled %s.\n"+
		"Read current feature_record/get_feature_plan before acting; this may be a retried hand-off.\nSteps verified: %d of %d.",
		time.Now().UTC().Format("2006-01-02 15:04:05")+" UTC", tilde(d.Dir), d.Rev, seen, verified, n)
	b.WriteString(list("Held until the owner releases them (don't work around them)", held))
	b.WriteString(list("Waiting on the owner's answer", asking))
	b.WriteString(list("Ticked but not verified (why)", claimed))
	b.WriteString(list("Can move next (not held, not waiting on the owner)", next))
	switch {
	case len(d.Outcomes) == 0:
		b.WriteString("\nFEATURE.md lists no desired outcomes yet: the owner defines them; propose them in LOG.md.")
	case !d.Accepted:
		fmt.Fprintf(&b, "\nDesired outcomes: %d, not accepted yet (the owner accepts them once the results are verified).", len(d.Outcomes))
	}
	if d.DoneStale != "" {
		b.WriteString("\nMarked done, but it no longer holds: " + d.DoneStale)
	}
	if strings.TrimSpace(d.Draft) != "" {
		b.WriteString("\nA requirements draft (FEATURE.draft.md rev " + d.Revs["draft"] + ") waits for the owner to adopt or discard; FEATURE.md stays the approved requirements until then.")
	}
	b.WriteString("\nWork from the current record, not from earlier messages: decide the path to done, re-plan through the record writer " +
		"(at the rev you read) within what the owner asked for, put idle agents on independent safe steps, research what's blocked, " +
		"and log decisions and evidence in LOG.md. Approvals, verification and releases stay the owner's.")
	return b.String()
}

// shq quotes s as one POSIX shell word.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// POST /api/term/feature/answer {session, key, qid, rev, text, resume}: the owner's answer to exactly the question a
// step asks ([?] with "ask:") as they saw it: qid names the feature, the step and the question's words, rev the plan's
// revision. It goes under that step; resume:true sends the step back to doing, resume:false to todo. Either way it
// answers that question only: no hold lifted, nothing else authorized.
func (s *server) termFeatureAnswer(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Session, Key, Qid, Rev, Text string
		Resume                       *bool
	}
	if err := readJSONN(r, &b, 1<<14); err != nil || !sessionNameRe.MatchString(b.Session) || strings.TrimSpace(b.Text) == "" || b.Qid == "" || b.Rev == "" || b.Resume == nil {
		writeJSON(w, 400, map[string]any{"error": "session, key, qid, rev, text and resume are required"})
		return
	}
	defer s.featReopen(b.Session)
	th := s.featOwner(b.Session)
	if th == nil {
		writeJSON(w, 409, map[string]any{"error": "no thread leads this session, so nobody would act on the answer"})
		return
	}
	dir, conflict := s.featHome(b.Session, th.ID)
	if conflict != "" {
		writeJSON(w, 409, map[string]any{"error": conflict})
		return
	}
	path := filepath.Join(dir, "PLAN.md")
	raw, err := os.ReadFile(path)
	if err != nil || featRev(raw) != b.Rev {
		writeJSON(w, 409, map[string]any{"error": "the plan changed since you looked: reload and answer again"})
		return
	}
	text := strings.Join(strings.Fields(b.Text), " ")
	lines, ms, step, at := strings.Split(string(raw), "\n"), "Plan", featPlanStep{}, -1
	for i, l := range lines {
		if t, ok := strings.CutPrefix(strings.TrimSpace(l), "## "); ok {
			ms = strings.TrimSpace(t)
			continue
		}
		if m := featStepRe.FindStringSubmatch(l); m != nil {
			txt := m[2]
			if o := featOwnerRe.FindStringSubmatch(txt); o != nil {
				txt = strings.TrimSpace(txt[:len(txt)-len(o[0])])
			}
			if featStepKey(ms, txt) == b.Key && m[1] == "?" {
				at, step.Text = i, txt
			}
		}
	}
	end := at + 1
	for at >= 0 && end < len(lines) && featSubRe.MatchString(lines[end]) {
		if m := featSubRe.FindStringSubmatch(lines[end]); m[1] == "ask" {
			step.Ask = m[2]
		}
		end++
	}
	if at < 0 || step.Ask == "" || featQid(b.Session, b.Key, step.Ask) != b.Qid {
		writeJSON(w, 409, map[string]any{"error": "that question isn't asked any more as you saw it (it changed, or was answered): reload"})
		return
	}
	box := map[bool]string{true: "[~]", false: "[ ]"}[*b.Resume]
	lines[at] = strings.Replace(lines[at], "[?]", box, 1)
	indent := lines[at][:len(lines[at])-len(strings.TrimLeft(lines[at], " \t"))]
	ans := fmt.Sprintf("%s  - answer: %s (owner, %s)", indent, text, time.Now().UTC().Format("2006-01-02 15:04"))
	lines = append(lines[:end], append([]string{ans}, lines[end:]...)...)
	featPlanMu.Lock() // the effect boundary: the ticket's agent is checked again, then written, nothing in between
	release, why := s.featTicketGuard(r)
	if why != "" {
		featPlanMu.Unlock()
		writeJSON(w, 409, map[string]any{"error": why})
		return
	}
	err = featWriteRevLocked(path, b.Rev, []byte(strings.Join(lines, "\n")))
	release()
	featPlanMu.Unlock()
	if err != nil {
		code := 500
		if errors.Is(err, errStale) {
			code = 409
		} else {
			log.Printf("feature %s: answer: %v", b.Session, err)
			err = errors.New("couldn't record it; nothing changed")
		}
		writeJSON(w, code, map[string]any{"error": err.Error()})
		return
	}
	s.featLog(b.Session, "owner answered: "+trunc(step.Text, 80)+": "+trunc(text, 200))
	s.audit.add("feature-answer", b.Session+" "+b.Key, clientIP(r))
	now := time.Now()
	what := "The owner chose not to resume it: the step is back to todo."
	if *b.Resume {
		what = "The owner chose to resume this step."
	}
	s.raise(now, th, false, "answer:"+b.Qid, "ok", "the owner answered a step's question",
		"Step: "+step.Text+"\nQ: "+step.Ask+"\nA: "+text+"\n"+what+" The answer covers that question only: it lifts no hold and authorizes nothing else.")
	writeJSON(w, 200, map[string]any{"ok": true, "dossier": s.featDossier(b.Session)})
}

// ---- the thread's side of the record: its plan, its lead's view of it ----

// recordPlan: a thread's PLAN.md, verified (nil when it has none). Takes no assistant lock: called with it held.
func (s *server) recordPlan(t *thread) ([]featMilestone, string) {
	dir, _ := s.featHome(t.Team, t.ID)
	p := filepath.Join(dir, "PLAN.md")
	b, err := os.ReadFile(p)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return nil, p
	}
	ms := parseMilestones(string(b))
	s.featVerify(t.Team, ms)
	return ms, p
}

// flatPlan flattens milestones into the thread's simple step list (text and state), as thread_list shows it.
func flatPlan(ms []featMilestone) []planStep {
	out := []planStep{}
	for _, m := range ms {
		for _, st := range m.Steps {
			out = append(out, planStep{trunc(st.Text, 160), st.State})
		}
	}
	return out
}

// hookRecord connects the assistant (threads.go) to the feature record: the hooks it calls to read a thread's plan from
// PLAN.md, apply a lead's plan update to it, judge the done gate, and add the record summary to the lead's instructions.
func (s *server) hookRecord() {
	s.ai.featPlan = func(t *thread) []planStep {
		if ms, _ := s.recordPlan(t); ms != nil {
			return flatPlan(ms)
		}
		return nil
	}
	s.ai.featAdapt = func(t *thread, flat string) (planEdit, error) {
		dir, conflict := s.featHome(t.Team, t.ID)
		if conflict != "" {
			return planEdit{}, fmt.Errorf("%w: %s", errPlanConflict, conflict)
		}
		p := filepath.Join(dir, "PLAN.md")
		cur, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			return planEdit{}, err
		}
		out, err := featAdapt(p, cur, flat)
		if err != nil {
			return planEdit{}, err
		}
		return planEdit{Path: p, Old: cur, New: []byte(out)}, nil
	}
	// featDone: why t can't be done with plan as its PLAN.md ("" when every step is verified). Checked inside the
	// update that sets done, on the exact plan (and team) that update commits.
	s.ai.featDone = func(t *thread, plan []byte) string { return s.featGate(t.Team, t.ID, plan, true) }
	s.ai.featAttach = func(t *thread) error {
		if _, conflict := s.featHome(t.Team, t.ID); conflict != "" {
			return fmt.Errorf("%w: %s", errPlanConflict, conflict)
		}
		return nil
	}
	s.ai.featBlock = func(t *thread) string {
		ms, p := s.recordPlan(t)
		var b strings.Builder
		fmt.Fprintf(&b, "[Feature record] This feature's one source of truth is %s/ (PLAN.md, FEATURE.md, TESTS.md, LOG.md). The owner's feature "+
			"page (Terminals → %s) shows exactly these files, your team reads and writes them, and the panel checks them. Execution state lives "+
			"only there: steps, owners (@Tab), checks, evidence, questions, holds. Your chat and note are the story, not the state. Write it only "+
			"through the panel's record writer (GET /agent/feature, POST /agent/feature/doc with the rev you read, /agent/feature/log); FEATURE.md is the owner's. "+
			"thread_update(plan=) only moves the boxes of steps PLAN.md already has. A ticked box is a claim: a step is done only when the owner "+
			"approved its check (check: and cwd: lines) and an agent ran exactly its runAs and ticked it with \"evidence: run <attempt>\", or the "+
			"owner marked it verified. File evidence stays claimed. A [?] step with \"- ask:\" waits on the owner for that step only; a held tab's "+
			"steps wait for the owner to release them; everything else goes on.\n", tilde(filepath.Dir(p)), t.Team)
		if ms == nil {
			b.WriteString("It has no PLAN.md yet: write it there (## milestones, - [ ] steps @Tab).\n")
		}
		n := 0
		for _, m := range ms {
			for _, st := range m.Steps {
				if n++; n > 40 {
					break
				}
				fmt.Fprintf(&b, "- %s [%s] %s", m.Title, st.State, trunc(st.Text, 120))
				if st.Owner != "" {
					b.WriteString(" @" + st.Owner)
				}
				for _, x := range []string{st.Why, map[bool]string{true: "held: " + st.Blocked}[st.Blocked != ""], map[bool]string{true: "runAs: " + st.RunAs}[st.RunAs != "" && st.State != "done"], map[bool]string{true: "missed its checkpoint " + st.Due}[st.Missed], map[bool]string{true: "asks: " + st.Ask}[st.State == "asking"]} {
					if x != "" {
						b.WriteString(" — " + x)
					}
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("[/Feature record]")
		return b.String()
	}
}
