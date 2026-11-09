package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Threads. Main JARVIS is the owner's executive assistant; a thread is a fresh JARVIS (its own Hermes session,
// "thread-<id>", same SOUL.md and memory) that leads one feature. It grows from a message in Main's chat
// ("Reply in thread") or Main opens it (vm-agent's thread_open tool). Its lead runs a team of Claude or Codex
// agents in the zellij session named after it, on one git branch (vm-agent bin/feature). The panel drives every
// thread turn, sending the lead playbook (LEAD.md) with each one.
//
// This file holds the thread record and its persistence (threads.json), the owner-facing and agent-facing HTTP
// handlers, and the per-agent to-do lists that show what a thread's team is really working through.

// threadPrefix is prepended to a thread's id to form its Hermes session name.
const threadPrefix = "thread-"

// thread is one feature-leading JARVIS's record, persisted in threads.json.
type thread struct {
	ID      string        `json:"id"`
	Title   string        `json:"title"`
	Parent  string        `json:"parent,omitempty"` // the Main chat item it grew from
	Status  string        `json:"status"`           // planning, working, needs_you, done, archived
	Note    string        `json:"note,omitempty"`   // where it stands, one line (the lead's)
	Team    string        `json:"team"`             // its zellij session
	Repo    string        `json:"repo,omitempty"`
	Branch  string        `json:"branch,omitempty"`
	Created float64       `json:"created"`
	Updated float64       `json:"updated"`
	Log     []threadEvent `json:"log,omitempty"`  // what happened lately, oldest first: the card's timeline
	Plan    []planStep    `json:"plan,omitempty"` // the plan the owner agreed to, as its lead ticks it off
	// TeamSeen is the newest note (notes.go id) its lead has been handed (-1: none yet; 0: delivery not started). It
	// moves only once the message is in the lead's conversation, so news cut off by a restart or a failed hand-off
	// goes out again on a later tick (teamdeliver.go).
	TeamSeen int64 `json:"teamSeen,omitempty"`
}

// planStep is one step of a thread's agreed plan. State is todo, doing or done.
type planStep struct {
	Text  string `json:"text"`
	State string `json:"state"`
}

// planLine matches a markdown checklist line such as "- [x] step" or "2. [~] step".
var planLine = regexp.MustCompile(`^\s*(?:[-*]|\d+[.)])\s*\[([ xX~>])\]\s*(.+?)\s*$`)

// parsePlan reads a markdown checklist: "- [x] done", "- [~] doing" (or [>]), "- [ ] to do". Other lines are skipped.
func parsePlan(md string) []planStep {
	var out []planStep
	for _, l := range strings.Split(md, "\n") {
		m := planLine.FindStringSubmatch(l)
		if m == nil || len(out) == 30 {
			continue
		}
		st := map[string]string{"x": "done", "X": "done", "~": "doing", ">": "doing"}[m[1]]
		out = append(out, planStep{trunc(m[2], 160), cmp.Or(st, "todo")})
	}
	return out
}

// planDone counts the steps of p that are done.
func planDone(p []planStep) (n int) {
	for _, s := range p {
		if s.State == "done" {
			n++
		}
	}
	return n
}

// threadEvent is a line on a thread's timeline. Kind is status (Text: the new status), note (the lead's), team (its
// agents' news) or plan (a plan was agreed, or a step finished).
type threadEvent struct {
	T    float64 `json:"t"`
	Kind string  `json:"kind"`
	Text string  `json:"text"`
}

// threadLogMax is how many timeline events a thread keeps; older ones are dropped.
const threadLogMax = 12

// logEvent appends an event to the thread's timeline, keeping only the newest threadLogMax.
func (t *thread) logEvent(kind, text string) {
	t.Log = append(t.Log, threadEvent{nowSec(), kind, text})
	if len(t.Log) > threadLogMax {
		t.Log = slices.Clone(t.Log[len(t.Log)-threadLogMax:])
	}
}

// threadView is a thread as the page lists it: the record plus live information derived from what is running.
type threadView struct {
	thread
	Replies int     `json:"replies"` // messages in it (yours, the lead's answers)
	Last    float64 `json:"last"`    // its latest message
	Live    bool    `json:"live"`    // the lead is working on a turn
	Queued  int     `json:"queued,omitempty"`
	// State is what the thread is doing now, judged from what runs rather than from the status its lead last set:
	// working (its lead's turn runs, a message waits for it, or an agent of its team is at work), waiting (on the
	// owner: nothing runs), done or archived.
	State string `json:"state"`
}

// Thread errors, the allowed statuses, and the pattern that strips a title down to a slug.
var (
	errNoThread     = errors.New("no such thread")
	errTeamTaken    = errors.New("another thread's team")
	threadStatuses  = []string{"planning", "working", "needs_you", "done", "archived"}
	threadSlugStrip = regexp.MustCompile(`[^a-z0-9]+`)
)

// nowSec is the current time as unix seconds with millisecond precision.
func nowSec() float64 { return float64(time.Now().UnixMilli()) / 1000 }

// threadSlug makes a zellij-, git- and Hermes-safe id from a title: its first words, lowercased and dash-joined.
func threadSlug(title string) string {
	words := strings.Fields(threadSlugStrip.ReplaceAllString(strings.ToLower(title), " "))
	s := strings.Join(words[:min(len(words), 5)], "-")
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-")
	}
	return cmp.Or(s, "thread")
}

// loadThreads reads threads.json and finishes any plan change a crash interrupted.
func (a *assistant) loadThreads() {
	b, err := os.ReadFile(a.threadsFile)
	if err == nil {
		json.Unmarshal(b, &a.threads)
	}
	a.recoverPlanTxn()
}

// saveThreads writes threads.json atomically (a.mu held). It does nothing when threads are not persisted.
func (a *assistant) saveThreads() error {
	if a.threadsFile == "" {
		return nil
	}
	b, _ := json.MarshalIndent(a.threads, "", " ")
	tmp := a.threadsFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.threadsFile)
}

// thread returns the thread with this id, or nil (a.mu held).
func (a *assistant) thread(id string) *thread {
	for _, t := range a.threads {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// shortTitle returns the first sentence or about six words of what started a thread (its lead renames it once it
// knows more).
func shortTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.IndexAny(s, ".:;!?\n"); i > 0 {
		s = s[:i]
	}
	w := strings.Fields(s)
	if len(w) > 7 {
		return strings.Join(w[:6], " ") + "…"
	}
	return trunc(s, 60)
}

// newThread records a thread (a.mu held). Its id is unique among all threads, archived ones included.
func (a *assistant) newThread(title, parent string) *thread {
	title = shortTitle(title)
	base := threadSlug(title)
	id := base
	for n := 2; a.thread(id) != nil; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	now := nowSec()
	t := &thread{ID: id, Title: cmp.Or(title, id), Parent: parent, Status: "planning", Team: id, Created: now, Updated: now}
	a.threads = append(a.threads, t)
	return t
}

// planOf returns a thread's plan as its feature record has it (PLAN.md, with verified states), else as its lead last
// published it (a.mu held).
func (a *assistant) planOf(t *thread) []planStep {
	if a.featPlan != nil {
		if p := a.featPlan(t); p != nil {
			return p
		}
	}
	return t.Plan
}

// threadViews returns every thread as the page lists it, including whether it is working or waiting (a.mu held).
func (a *assistant) threadViews() []threadView {
	out := make([]threadView, 0, len(a.threads))
	for _, t := range a.threads {
		v := threadView{thread: *t}
		v.Plan = a.planOf(t)
		for _, it := range a.thItems[t.ID] {
			if it.Role == "user" || it.Role == "assistant" {
				v.Replies++
			}
			v.Last = max(v.Last, it.T)
		}
		if cv := a.convs[t.ID]; cv != nil {
			v.Live, v.Queued = cv.live != nil, len(cv.queue)
		}
		v.State = t.Status
		if t.Status != "done" && t.Status != "archived" {
			v.State = "waiting"
			if t.Status != "needs_you" && (v.Live || v.Queued > 0 || a.teamBusy != nil && a.teamBusy(a.teamSessions(t))) { // a question to the owner shows, whatever runs
				v.State = "working"
			}
		}
		out = append(out, v)
	}
	return out
}

// instructions: what a turn of conversation c carries besides SOUL.md and memory (a.mu held). Main: the
// threads that are open, so it keeps the whole picture. A thread: the lead playbook, filled in with its record.
func (a *assistant) instructions(c string) string {
	if c == "" {
		var b strings.Builder
		for _, t := range a.threads {
			if t.Status == "archived" {
				continue
			}
			fmt.Fprintf(&b, "- %s \"%s\" %s, updated %s ago", t.ID, t.Title, t.Status, fmtAgo(nowSec()-t.Updated))
			if t.Note != "" {
				b.WriteString(": " + t.Note)
			}
			if p := a.planOf(t); len(p) > 0 {
				fmt.Fprintf(&b, " [plan: %d of %d steps verified done]", planDone(p), len(p))
			}
			for _, e := range slices.Backward(t.Log) {
				if e.Kind == "team" { // what its agents last reported (the owner sees it in the bell)
					fmt.Fprintf(&b, " (its team, %s ago: %s)", fmtAgo(nowSec()-e.T), e.Text)
					break
				}
			}
			b.WriteString("\n")
		}
		if b.Len() == 0 {
			return ""
		}
		return "Open threads (each one a product lead JARVIS running a feature; thread_send messages one, thread_list has more):\n" + b.String()
	}
	t := a.thread(c)
	if t == nil {
		return ""
	}
	lead, err := os.ReadFile(a.leadFile)
	if err != nil {
		lead = []byte("You are the product lead for one feature only: this thread. Plan it, run a team of Claude or Codex agents in your zellij session, keep your record with thread_update.")
	}
	out := strings.NewReplacer("{id}", t.ID, "{title}", t.Title, "{team}", t.Team, "{repo}", cmp.Or(t.Repo, "(not chosen yet)"),
		"{branch}", cmp.Or(t.Branch, "feature/"+t.ID), "{status}", t.Status, "{note}", t.Note).Replace(string(lead))
	if a.featBlock != nil {
		out += "\n\n" + a.featBlock(t)
	}
	return out
}

// fmtAgo formats an age in seconds as "1m", "5m", "3h" or "2d".
func fmtAgo(s float64) string {
	switch {
	case s < 90:
		return "1m"
	case s < 3600:
		return fmt.Sprintf("%dm", int(s/60))
	case s < 86400:
		return fmt.Sprintf("%dh", int(s/3600))
	}
	return fmt.Sprintf("%dd", int(s/86400))
}

// threadContext: what a thread grown from Main's item parent starts with: that message and the few before it,
// so the lead can piece together what the owner means (a.mu held).
func (a *assistant) threadContext(parent string) string {
	at := slices.IndexFunc(a.items, func(it aiItem) bool { return it.K == parent })
	if at < 0 {
		return ""
	}
	who := func(it aiItem) string {
		if it.Role == "user" {
			return "The owner"
		}
		return "Main JARVIS"
	}
	p := a.items[at]
	var b strings.Builder
	fmt.Fprintf(&b, "[Thread context]\nThe owner started this thread from this message in Main JARVIS's chat (%s, %s):\n%s\n",
		who(p), time.Unix(int64(p.T), 0).UTC().Format("2006-01-02 15:04 UTC"), trunc(p.Text, 4000))
	var before []string
	for i := at - 1; i >= 0 && len(before) < 6; i-- {
		if it := a.items[i]; (it.Role == "user" || it.Role == "assistant") && it.Text != "" {
			before = append(before, who(it)+": "+trunc(strings.Join(strings.Fields(it.Text), " "), 600))
		}
	}
	if len(before) > 0 {
		slices.Reverse(before)
		b.WriteString("\nEarlier in Main, oldest first:\n" + strings.Join(before, "\n") + "\n")
	}
	return b.String() + "[/Thread context]\n\n"
}

// openThread records a thread and starts its first turn with first (and images).
func (a *assistant) openThread(title, parent, team, first string, images ...string) (*thread, error) {
	a.mu.Lock()
	for _, o := range a.threads { // one manager per session: never two threads leading one team
		if team != "" && o.Team == team && o.Status != "archived" {
			a.mu.Unlock()
			return nil, fmt.Errorf("%w: %s already leads %s", errTeamTaken, o.ID, team)
		}
	}
	t := a.newThread(title, parent)
	if team != "" { // it adopts a session that already exists (the scout's takeover): its agents become its team
		t.Team = team
	}
	err := a.saveThreads()
	cp := *t
	a.bump()
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if _, err := a.send(t.ID, first, images...); err != nil {
		return nil, err
	}
	return &cp, nil
}

// updateThread changes the fields given (nil: unchanged) and returns the thread before and after.
func (a *assistant) updateThread(id string, f map[string]*string) (thread, thread, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.thread(id)
	if t == nil {
		return thread{}, thread{}, errNoThread
	}
	if st := f["status"]; st != nil && !slices.Contains(threadStatuses, *st) {
		return thread{}, thread{}, fmt.Errorf("status must be one of %s", strings.Join(threadStatuses, ", "))
	}
	// Everything is checked before anything changes: a refused update leaves the record, its team and its files as they were.
	// The feature record's lock (featPlanMu) is held from the first look at the record to the commit (and any undo),
	// so no other writer or owner action lands between the done check and what this update commits.
	featPlanMu.Lock()
	defer featPlanMu.Unlock()
	team := t.Team
	if v := f["team"]; v != nil && *v != t.Team {
		if !sessionNameRe.MatchString(*v) {
			return thread{}, thread{}, errors.New("team must be a zellij session's exact name")
		}
		for _, o := range a.threads {
			if o.ID != id && o.Team == *v && o.Status != "archived" {
				return thread{}, thread{}, fmt.Errorf("%w: %s already leads %s", errTeamTaken, o.ID, *v)
			}
		}
		team = *v
	}
	old := *t
	now := nowSec()
	var edit *planEdit
	target := *t
	target.Team = team
	if team != t.Team && a.featAttach != nil { // a record in two folders would be shadowed: refused before anything changes
		if err := a.featAttach(&target); err != nil {
			return thread{}, thread{}, err
		}
	}
	if v := f["plan"]; v != nil && a.featAdapt != nil { // merged onto the PLAN.md of the team it will have
		e, err := a.featAdapt(&target, *v)
		if err != nil {
			if errors.Is(err, errPlanConflict) {
				return thread{}, thread{}, err
			}
			log.Printf("thread %s: plan: %v", id, err)
			return thread{}, thread{}, errThreadSave
		}
		e.ID, e.Updated = id, now
		edit = &e
	}
	// A done thread whose plan this update changes stays done only if the change keeps it done; otherwise it reopens
	// in this same commit.
	reopening := false
	if st := f["status"]; st == nil && t.Status == "done" && edit != nil && a.featDone != nil {
		if why := a.featDone(&target, edit.New); why != "" {
			w, n := "working", trunc(reopenWhy+": "+why, 300)
			f["status"], f["note"], reopening = &w, &n, true
		}
	}
	// "Done" is judged on exactly what this update commits: its plan (or the current one) and its team.
	if st := f["status"]; st != nil && *st == "done" && a.featDone != nil {
		var plan []byte
		if edit != nil {
			plan = edit.New
		}
		if why := a.featDone(&target, plan); why != "" {
			return thread{}, thread{}, fmt.Errorf("%w: %s", errNotDone, why)
		}
	}
	// The new record is built on a copy: t changes only when it's saved.
	build := func(plan []planStep) thread {
		nt := *t
		set := func(k string, dst *string, n int) {
			if v := f[k]; v != nil {
				*dst = trunc(strings.Join(strings.Fields(*v), " "), n)
			}
		}
		if team != nt.Team {
			nt.Team = team
			nt.logEvent("team", "its team is the session "+team)
		}
		set("status", &nt.Status, 20)
		set("note", &nt.Note, 300)
		set("repo", &nt.Repo, 300)
		set("branch", &nt.Branch, 120)
		if v := f["title"]; v != nil && strings.TrimSpace(*v) != "" {
			set("title", &nt.Title, 80)
		}
		if v := f["plan"]; v != nil {
			nt.Plan = plan
			was := map[string]bool{}
			for _, s := range old.Plan {
				was[s.Text] = s.State == "done"
			}
			switch {
			case len(old.Plan) == 0 && len(nt.Plan) > 0:
				nt.logEvent("plan", fmt.Sprintf("agreed a plan of %d steps", len(nt.Plan)))
			default:
				for _, s := range nt.Plan {
					if s.State == "done" && !was[s.Text] {
						nt.logEvent("plan", "finished: "+s.Text)
					}
				}
			}
		}
		if nt.Status != old.Status {
			nt.logEvent("status", map[bool]string{true: "reopened: " + nt.Note, false: nt.Status}[reopening])
		}
		if nt.Note != old.Note && nt.Note != "" {
			nt.logEvent("note", nt.Note)
		}
		nt.Updated = now
		return nt
	}
	commit := func(plan []planStep) error { // writing threads.json is the one commit point
		*t = build(plan)
		if err := a.saveThreads(); err != nil {
			*t = old
			log.Printf("thread %s: save: %v", id, err)
			return errThreadSave
		}
		return nil
	}
	var err error
	if edit == nil {
		var plan []planStep
		if v := f["plan"]; v != nil {
			plan = parsePlan(*v)
		}
		err = commit(plan)
	} else {
		err = a.planTxn(edit, func() error {
			p := a.planOf(&target)
			return commit(p)
		})
	}
	if err != nil {
		return thread{}, thread{}, err
	}
	a.bump()
	return old, *t, nil
}

// reopenWhy prefixes the note of a thread that went back to working because its feature stopped passing the done gate.
const reopenWhy = "requirements/plan changed; verification needed"

// reopen sends a done thread whose feature no longer passes its done gate (why says so) back to working, keeping the
// earlier done in its timeline. It is judged and committed under a.mu and featPlanMu, like the done it undoes, and
// launches nothing: the thread's lead decides what runs.
func (a *assistant) reopen(id string, why func(t *thread) string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	featPlanMu.Lock()
	defer featPlanMu.Unlock()
	t := a.thread(id)
	if t == nil || t.Status != "done" {
		return "", false
	}
	w := why(t)
	if w == "" {
		return "", false
	}
	old := *t
	t.Status, t.Note = "working", trunc(reopenWhy+": "+w, 300)
	t.logEvent("status", "reopened: "+t.Note)
	t.Updated = nowSec()
	if err := a.saveThreads(); err != nil {
		*t = old
		log.Printf("thread %s: reopen: %v", id, err)
		return "", false
	}
	a.bump()
	return t.Note, true
}

// planEdit is a thread_update's change to its feature's PLAN.md, committed together with the thread's record.
type planEdit struct {
	Path    string  `json:"path"`
	Old     []byte  `json:"old"` // nil: there was no PLAN.md
	New     []byte  `json:"new"`
	ID      string  `json:"id"`
	Updated float64 `json:"updated"` // the thread's Updated in the record that commits it
}

// errThreadSave means a thread's record couldn't be saved and nothing was changed; errNotDone means a thread
// can't be marked done because its feature doesn't pass the done gate.
var (
	errThreadSave = errors.New("couldn't save the thread's record; nothing was changed")
	errNotDone    = errors.New("not done")
)

// txnFile is where the intent of an in-flight plan change is written, so a crash can be undone at the next load.
func (a *assistant) txnFile() string { return a.threadsFile + ".plan-txn" }

// planTxn writes PLAN.md and commits the thread's record (threads.json, by commit) as one change: the intent is
// written first, so a failure puts the old PLAN.md back, and a crash in between is undone at the next load
// (recoverPlanTxn). a.mu and featPlanMu held.
func (a *assistant) planTxn(e *planEdit, commit func() error) error {
	if a.threadsFile == "" {
		if err := featWriteRevLocked(e.Path, featRev(e.Old), e.New); err != nil {
			return err
		}
		return commit()
	}
	b, _ := json.Marshal(e)
	if err := writeFileAtomic(a.txnFile(), b, 0o600); err != nil {
		log.Printf("thread %s: plan intent: %v", e.ID, err)
		return errThreadSave
	}
	if err := featWriteRevLocked(e.Path, featRev(e.Old), e.New); err != nil {
		os.Remove(a.txnFile())
		if errors.Is(err, errStale) {
			return err
		}
		log.Printf("thread %s: plan write: %v", e.ID, err)
		return errThreadSave
	}
	if err := commit(); err != nil {
		if rerr := e.undo(); rerr != nil {
			log.Printf("thread %s: plan undo: %v (the intent stays for the next load)", e.ID, rerr)
			return err
		}
		os.Remove(a.txnFile())
		return err
	}
	os.Remove(a.txnFile())
	return nil
}

// undo puts the old PLAN.md back, if the file is still what this change wrote (a later edit by someone else stays).
func (e *planEdit) undo() error {
	cur, err := os.ReadFile(e.Path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if string(cur) != string(e.New) {
		return nil
	}
	if e.Old == nil {
		return os.Remove(e.Path)
	}
	return writeFileAtomic(e.Path, e.Old, 0o600)
}

// recoverPlanTxn finishes a plan change a crash interrupted: committed (the saved thread carries its Updated) it
// stands, else PLAN.md goes back to what it was. a.mu held (or before serving).
func (a *assistant) recoverPlanTxn() {
	b, err := os.ReadFile(a.txnFile())
	if err != nil {
		return
	}
	var e planEdit
	if json.Unmarshal(b, &e) == nil {
		if t := a.thread(e.ID); t == nil || t.Updated != e.Updated {
			if err := e.undo(); err != nil {
				log.Printf("thread %s: interrupted plan change not undone: %v", e.ID, err)
				return
			}
			log.Printf("thread %s: an interrupted plan change was undone", e.ID)
		}
	}
	os.Remove(a.txnFile())
}

// tellThread sends a message to a thread's lead as a new turn (queued behind one that is running). from is "main",
// "owner", "team" or "agent:<session>"; it only labels the message, and an owner message also clears needs_you.
func (a *assistant) tellThread(id, from, text string) (bool, error) {
	q, err := a.send(id, threadFrom(from)+"\n"+strings.TrimSpace(text))
	if err == nil && from == "owner" {
		a.ownerAnswered(id)
	}
	return q, err
}

// ownerAnswered handles the owner writing into a thread that was waiting on them: it no longer is, so it goes back to
// working (its lead sets needs_you again, with the new question, if it still needs them). Only the lead sets
// needs_you, and leads tend to forget to clear it, so without this a thread would keep showing "Needs you" long after
// the owner had replied.
func (a *assistant) ownerAnswered(id string) {
	a.mu.Lock()
	t := a.thread(id)
	waiting := t != nil && t.Status == "needs_you"
	a.mu.Unlock()
	if waiting {
		st, note := "working", ""
		a.updateThread(id, map[string]*string{"status": &st, "note": &note})
	}
}

// threadFrom is the label that opens a message to a lead, saying who it comes from.
func threadFrom(from string) string {
	switch {
	case from == "main":
		return "[From Main JARVIS]"
	case from == "owner":
		return "[From the owner, passed on by Main JARVIS from Telegram]"
	case from == "team":
		return "[Team update from the panel]"
	case strings.HasPrefix(from, "agent:"):
		return "[From the Claude agent in " + strings.TrimPrefix(from, "agent:") + ", on your team]"
	}
	return "[From " + from + "]"
}

// ---- the team's news: a team pane that finishes, asks or stalls tells its lead (teamdeliver.go), not the owner ----

// lastID returns the id of the newest note.
func (n *notifier) lastID() int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.Next
}

// since returns the notes newer than id.
func (n *notifier) since(id int64) []Note {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []Note
	for _, x := range n.Notes {
		if x.ID > id {
			out = append(out, x)
		}
	}
	return out
}

// push saves a note for the owner (the bell, and Telegram through the fleet pings) and returns any save error.
func (n *notifier) push(kind, level, title, body, goTo string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, err := n.addSaved(time.Now(), kind, level, title, body, goTo, "")
	return err
}

// termFeatureBind handles POST /api/term/feature/bind {session, thread}: the owner says which thread leads a session
// (a lead may have started the thread under one name and work in a session of another). Names must match exactly,
// and a session another thread already leads is refused.
func (s *server) termFeatureBind(w http.ResponseWriter, r *http.Request) {
	var b struct{ Session, Thread string }
	if err := readJSON(r, &b); err != nil || !s.sessionLive(b.Session) {
		writeJSON(w, 400, map[string]any{"error": "no live session by that exact name"})
		return
	}
	_, t, err := s.ai.updateThread(b.Thread, map[string]*string{"team": &b.Session})
	if err != nil {
		code := 400
		if errors.Is(err, errNoThread) {
			code = 404
		} else if errors.Is(err, errTeamTaken) || errors.Is(err, errPlanConflict) {
			code = 409
		}
		writeJSON(w, code, map[string]any{"error": err.Error()})
		return
	}
	s.audit.add("feature-bind", b.Session+" -> "+b.Thread, clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true, "thread": t})
}

// ---- HTTP: the owner's ----

// aiThreadNew handles "Reply in thread" on a Main message: it opens a thread with that message as its context and the
// owner's text as its first turn.
func (s *server) aiThreadNew(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Parent, Text  string
		Images, Files []string
	}
	if err := readJSONN(r, &b, 1<<20); err != nil || strings.TrimSpace(b.Text) == "" || len(b.Text) > maxPaste {
		writeJSON(w, 400, map[string]any{"error": "say what the thread is for"})
		return
	}
	text, images, err := s.aiAttach(strings.TrimSpace(b.Text), b.Images, b.Files)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	s.ai.mu.Lock()
	ctx := s.ai.threadContext(b.Parent)
	s.ai.mu.Unlock()
	t, err := s.ai.openThread(b.Text, b.Parent, "", ctx+"[From the owner, in the panel]\n"+text, images...)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	s.audit.add("assistant-thread", t.ID+" "+trunc(b.Text, 80), clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true, "thread": t})
}

// aiThreadArchive takes a thread off the open list; its lead is told to wrap up and close its team.
func (s *server) aiThreadArchive(w http.ResponseWriter, r *http.Request) {
	var b struct{ ID string }
	if err := readJSONN(r, &b, 1<<10); err != nil {
		writeJSON(w, 400, map[string]any{"error": "which thread?"})
		return
	}
	s.ai.mu.Lock()
	t := s.ai.thread(b.ID)
	team := ""
	if t != nil {
		team = t.Team
	}
	s.ai.mu.Unlock()
	if t == nil {
		writeJSON(w, 404, map[string]any{"error": errNoThread.Error()})
		return
	}
	s.ai.send(b.ID, "[From the owner, in the panel]\nI archived this thread. Wrap up: make sure your team's work is committed and pushed "+
		"to the feature branch, then stop the team and close its zellij session ("+team+", close_terminal what=session). "+
		"Set your status to archived with a one-line note on where it ended, and answer with that line.")
	archived := "archived"
	if _, _, err := s.ai.updateThread(b.ID, map[string]*string{"status": &archived}); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	s.audit.add("assistant-thread-archive", b.ID, clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true})
}

// aiThreadFiles lists every file a thread's team wrote, from its panes' transcripts (artifacts.go), newest first,
// together with the lead's own answers and file writes (the page finds files mentioned in those).
func (s *server) aiThreadFiles(w http.ResponseWriter, r *http.Request) {
	s.ai.mu.Lock()
	t := s.ai.thread(r.URL.Query().Get("id"))
	team := ""
	lead := []aiItem{} // what its lead wrote or linked: the page finds the files in them (aiConvFiles)
	if t != nil {
		team = t.Team
		for _, it := range s.ai.thItems[t.ID] {
			if it.Role == "assistant" || (it.Role == "tool" && (it.Name == "write_file" || it.Name == "patch")) {
				lead = append(lead, it)
			}
		}
	}
	s.ai.mu.Unlock()
	if t == nil {
		writeJSON(w, 404, map[string]any{"error": errNoThread.Error()})
		return
	}
	type teamFile struct {
		Artifact
		Pane string `json:"pane"` // session:pane
		Who  string `json:"who"`  // the pane's title
	}
	files := []teamFile{}
	for _, se := range s.term.listing().Sessions {
		if se.Name != team {
			continue
		}
		for _, tb := range se.Tabs {
			for _, p := range tb.Panes {
				full, ok := s.term.pane(team, p.ID)
				if !ok || full.Agent == nil || full.Agent.path == "" {
					continue
				}
				root := ""
				if full.Git != nil {
					root = full.Git.Root
				}
				for _, f := range s.arts.list(full.Agent.path, root) {
					if f.Exists {
						files = append(files, teamFile{f, fmt.Sprintf("%s:%d", team, p.ID), cmp.Or(tb.Name, p.Title)})
					}
				}
			}
		}
	}
	slices.SortFunc(files, func(x, y teamFile) int { return cmp.Compare(y.Last, x.Last) })
	writeJSON(w, 200, map[string]any{"files": files, "lead": lead})
}

// ---- HTTP: the agent API's (vm-agent's thread_* tools, bin/jarvis, fleet pings) ----

// agentThreads lists every thread for the agent API (vm-agent's thread_list).
func (s *server) agentThreads(w http.ResponseWriter, r *http.Request) {
	s.reopenStale()
	s.ai.mu.Lock()
	v := s.ai.threadViews()
	s.ai.mu.Unlock()
	writeJSON(w, 200, map[string]any{"threads": v})
}

// agentThreadOpen lets Main open a thread with a brief; its lead starts on it at once. Passing an existing zellij
// session as Team makes the thread take it over: that session's agents become its team.
func (s *server) agentThreadOpen(w http.ResponseWriter, r *http.Request) {
	var b struct{ Title, Brief, Repo, Team string } // Team: an existing zellij session to adopt (a takeover)
	if err := readJSONN(r, &b, 1<<20); err != nil || strings.TrimSpace(b.Title) == "" || strings.TrimSpace(b.Brief) == "" {
		writeJSON(w, 400, map[string]any{"error": "a thread needs a title and a brief"})
		return
	}
	s.ai.mu.Lock()
	parent := ""
	if n := len(s.ai.items); n > 0 {
		parent = s.ai.items[n-1].K // where Main was when it opened it
	}
	s.ai.mu.Unlock()
	first := "[Brief from Main JARVIS, who opened this thread]\n" + strings.TrimSpace(b.Brief)
	if b.Repo != "" {
		first += "\n\nRepo: " + b.Repo
	}
	if b.Team != "" {
		first += "\n\nYou are taking over the owner's existing zellij session " + fmt.Sprintf("%q", b.Team) + ": it is your team now. Read its agents (pane_chat) before you change anything, keep the work on its branch, and carry it to shipped."
		s.scout.mu.Lock()
		s.scout.mark(b.Team).Taken = "pending"
		s.scout.mu.Unlock()
	}
	t, err := s.ai.openThread(b.Title, parent, b.Team, first)
	if err != nil {
		code := 500
		if errors.Is(err, errTeamTaken) {
			code = 409
		}
		writeJSON(w, code, map[string]any{"error": err.Error()})
		return
	}
	if b.Repo != "" {
		s.ai.updateThread(t.ID, map[string]*string{"repo": &b.Repo})
	}
	s.audit.add("assistant-thread", t.ID+" (opened by JARVIS)", clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true, "thread": t})
}

// agentThreadSend sends a message to a thread's lead for the agent API (vm-agent's thread_send).
func (s *server) agentThreadSend(w http.ResponseWriter, r *http.Request) {
	var b struct{ ID, Text, From string }
	if err := readJSONN(r, &b, maxPaste+1024); err != nil || strings.TrimSpace(b.Text) == "" {
		writeJSON(w, 400, map[string]any{"error": "which thread, and what to say?"})
		return
	}
	queued, err := s.ai.tellThread(b.ID, cmp.Or(b.From, "main"), b.Text)
	if err != nil {
		code := 409
		if errors.Is(err, errNoThread) {
			code = 404
		}
		writeJSON(w, code, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "queued": queued})
}

// agentThreadUpdate is how a lead keeps its record (status, note, title, repo, branch, plan, team). Turning a thread
// to needs_you notifies the owner (the bell, and Telegram through fleet pings, tagged [thread:<id>] so a reply finds
// its way back), as do done and reopened.
func (s *server) agentThreadUpdate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ID                                            string
		Status, Note, Title, Repo, Branch, Plan, Team *string
	}
	if err := readJSONN(r, &b, 1<<14); err != nil {
		writeJSON(w, 400, map[string]any{"error": "bad request"})
		return
	}
	if b.Team != nil && !s.sessionLive(*b.Team) {
		writeJSON(w, 400, map[string]any{"error": "no live zellij session is named exactly " + strconv.Quote(*b.Team) + " (names are matched exactly, never guessed)"})
		return
	}
	old, t, err := s.ai.updateThread(b.ID, map[string]*string{"status": b.Status, "note": b.Note, "title": b.Title, "repo": b.Repo, "branch": b.Branch, "plan": b.Plan, "team": b.Team})
	if err != nil {
		code := 400
		if errors.Is(err, errNoThread) {
			code = 404
		} else if errors.Is(err, errTeamTaken) || errors.Is(err, errPlanConflict) || errors.Is(err, errStale) || errors.Is(err, errNotDone) {
			code = 409
		} else if errors.Is(err, errThreadSave) {
			code = 500
		}
		writeJSON(w, code, map[string]any{"error": err.Error()})
		return
	}
	var nerr error
	switch {
	case t.Status == "done" && old.Status != "done" && t.Team != "":
		s.featCompleted(t.ID, true) // passed the gate just now: a later change of its record reopens it
	case old.Status == "done" && t.Status != "done":
		s.featCompleted(t.ID, false)
	}
	if old.Status == "done" && t.Status != "done" && strings.HasPrefix(t.Note, reopenWhy) {
		s.featLog(t.Team, "panel: reopened (was done): "+t.Note)
		s.audit.add("feature-reopen", t.Team+" "+t.ID+": "+t.Note, "panel")
		nerr = s.notes.push("thread", "warn", "🧵 "+t.Title+" reopened", t.Note, "thread:"+t.ID)
	}
	if t.Status == "needs_you" && (old.Status != "needs_you" || old.Note != t.Note) {
		nerr = s.notes.push("thread", "warn", "🧵 "+t.Title+" needs you", t.Note, "thread:"+t.ID)
	}
	if t.Status == "done" && old.Status != "done" {
		nerr = s.notes.push("thread", "ok", "🧵 "+t.Title+" is done", t.Note, "thread:"+t.ID)
	}
	s.ai.mu.Lock()
	t.Plan = s.ai.planOf(&t) // as thread_list shows it: the record's plan, not a copy kept in threads.json
	s.ai.mu.Unlock()
	out := map[string]any{"ok": true, "thread": t}
	if nerr != nil { // the update stands; the owner's bell didn't get it: say so, so the lead tells the owner itself
		log.Printf("thread %s: notification: %v", t.ID, nerr)
		out["warning"] = "saved, but the owner's notification couldn't be saved: tell the owner yourself"
	}
	writeJSON(w, 200, out)
}

// ---- the plan its team actually follows: each agent's own to-do list (a Claude's TodoWrite, a Codex's update_plan,
// which its mirrored transcript carries as TodoWrite), read from its transcript, not reported by anyone ----

// teamTodo is one item of an agent's own to-do list.
type teamTodo struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending, in_progress, completed
}

// teamPlan is one agent of a thread's team as the Team view shows it: who it is, what it is doing, and its to-do list.
type teamPlan struct {
	Pane  string     `json:"pane"` // session:pane
	Who   string     `json:"who"`  // its tab's name (the role), or the pane's title
	Kind  string     `json:"kind"` // claude, codex
	Todos []teamTodo `json:"todos"`
	T     int64      `json:"t"`             // when it last wrote the list
	State string     `json:"state"`         // working, waiting, asking, idle… (terminals.go)
	Doing string     `json:"doing"`         // what it's doing right now, in a few words
	Since int64      `json:"since"`         // in this state since
	Out   int64      `json:"out,omitempty"` // its last output: proof it's moving
}

// lastTodos returns the newest to-do list in a transcript's tail and when it was written (nil when the agent never
// kept one).
func lastTodos(path string) ([]teamTodo, int64) {
	ls, _, _ := readTail(path, 2<<20)
	items, _ := parseChat(ls)
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Name != "TodoWrite" || items[i].In == "" {
			continue
		}
		var in struct {
			Todos []teamTodo `json:"todos"`
		}
		if json.Unmarshal([]byte(items[i].In), &in) == nil && len(in.Todos) > 0 {
			return in.Todos, items[i].T
		}
	}
	return nil, 0
}

// sentTo matches the answer of the pane_send tool ("sent to <session>:<pane>").
var sentTo = regexp.MustCompile(`^sent to (.+):\d+$`)

// teamSessions returns thread t's team: the session it was given, and every session its lead has messaged (pane_send
// answers "sent to <session>:<pane>"). A lead that took over terminals the owner had started runs those, not a
// session of its own (a.mu held).
func (a *assistant) teamSessions(t *thread) []string {
	out := []string{t.Team}
	for _, it := range a.thItems[t.ID] {
		if m := sentTo.FindStringSubmatch(it.Res); it.Role == "tool" && strings.Contains(it.Name, "pane_send") && m != nil && !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// teamBusy reports whether an agent in one of these sessions is at work, judged by the latest full terminal update
// (it asks for a fresh one when that is older than 20 s).
func (s *server) teamBusy(sessions []string) bool {
	tm, at := s.term.sample()
	if time.Since(at) > 20*time.Second {
		s.scout.full.Store(true)
	}
	for _, se := range tm.Sessions {
		if !slices.Contains(sessions, se.Name) {
			continue
		}
		for _, tb := range se.Tabs {
			for _, p := range tb.Panes {
				if p.State == "working" || p.State == "background" || p.State == "stalled" {
					return true
				}
			}
		}
	}
	return false
}

// aiThreadTeam handles GET /api/ai/thread/team?id=: each agent in the thread's team sessions, with its state and the
// to-do list it is working through.
func (s *server) aiThreadTeam(w http.ResponseWriter, r *http.Request) {
	s.ai.mu.Lock()
	t := s.ai.thread(r.URL.Query().Get("id"))
	teams := map[string]bool{}
	if t != nil {
		for _, se := range s.ai.teamSessions(t) {
			teams[se] = true
		}
	}
	s.ai.mu.Unlock()
	if t == nil {
		writeJSON(w, 404, map[string]any{"error": errNoThread.Error()})
		return
	}
	s.scout.full.Store(true) // the panes' states come from the full terminal update: one within 10 s, even with no Terminals page open
	plans := []teamPlan{}
	for _, se := range s.term.listing().Sessions {
		team := se.Name
		if team == "" || !teams[team] {
			continue
		}
		for _, tb := range se.Tabs {
			for _, p := range tb.Panes {
				full, ok := s.term.pane(team, p.ID)
				if !ok || full.Agent == nil || full.Agent.path == "" {
					continue
				}
				todos, at := lastTodos(full.Agent.path)
				plans = append(plans, teamPlan{fmt.Sprintf("%s:%d", team, p.ID), cmp.Or(tb.Name, p.Title), cmp.Or(full.Kind, "claude"), todos, at,
					full.State, cmp.Or(full.Agent.Doing, full.Detail), full.Since, full.Output})
			}
		}
	}
	writeJSON(w, 200, map[string]any{"agents": plans})
}
