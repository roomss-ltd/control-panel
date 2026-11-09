package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Notifications: what changed that you'd want to know (an agent finished, needs your answer or looks
// stuck; a CI job failed; a security check went bad; a disk or memory filling up), kept across restarts.
// The server keeps looking with no page open (sampleLoop's slow pace), so the list holds what happened
// while you were away. A click on one takes the page to its pane or view (Go).

// Note is one notification.
type Note struct {
	ID    int64     `json:"id"`
	T     int64     `json:"t"`
	Kind  string    `json:"kind"`  // done, asking, stalled, exited, ci, disk, mem, thread, feature
	Level string    `json:"level"` // ok, warn, bad
	Title string    `json:"title"`
	Body  string    `json:"body,omitempty"`
	Go    string    `json:"go"`             // pane:<session>:<id>, ci, res, thread:<id>
	Ref   string    `json:"ref,omitempty"`  // the supervisor's dedupe key (featsup.go)
	Exit  *paneSeen `json:"exit,omitempty"` // an exited agent: its tab, conversation and folder, for a resume
}

// maxNotes and fullNotes: the list compacts to this many, but never past a note a thread's lead hasn't acknowledged
// yet (setFloor); past fullNotes with deliveries still pending, every note stays and the store says it's full (stuffed).
const (
	maxNotes  = 200
	fullNotes = 2000
)

// paneSeen is an agent pane's state at the last look, and when Claude last changed its status. Saved, so a restart
// compares against what was there before it (a turn that finished or an agent that exited while the panel was down
// is still news), and so an exit carries the conversation to resume.
type paneSeen struct {
	State string `json:"state"`
	Since int64  `json:"since,omitempty"`
	Tab   string `json:"tab,omitempty"`
	Kind  string `json:"kind,omitempty"` // claude, codex
	Sid   string `json:"sid,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
	Gone  int    `json:"gone,omitempty"` // looks in a row it was missing: one miss can be a status file mid-write
}

// midWork lists states an agent can't leave by exiting without something being cut short
var midWork = map[string]bool{"working": true, "stalled": true, "background": true, "asking": true, "starting": true}

// notifier keeps the notification list and what the last look saw, and saves them to a file so they survive restarts.
type notifier struct {
	mu    sync.Mutex
	file  string
	Notes []Note              `json:"notes"` // oldest first
	Seen  int64               `json:"seen"`  // read up to this id
	Next  int64               `json:"next"`
	Panes map[string]paneSeen `json:"panes,omitempty"` // the agents at the last look
	// what else the last look saw; not saved, so a restart learns them again instead of announcing them
	primed  bool
	teams   map[string]bool // the sessions a manager thread leads: only their agents' exits are news (setTeams)
	jobs    map[string]bool // completed CI jobs seen
	full    map[string]bool // a disk or memory past 90%, until it's back under 85%
	stuffed bool            // past fullNotes, every one held for a pending delivery
	// Floor: notes above this id are still owed to a thread's lead (teamdeliver.go): never dropped. Saved, so a restart
	// keeps them before anything says who's behind; 0 (a new store): nothing is dropped until it's known.
	Floor int64 `json:"floor"`
}

// newNotifier loads the saved notifications from file (a missing file starts empty).
func newNotifier(file string) *notifier {
	n := &notifier{file: file}
	if b, err := os.ReadFile(file); err == nil {
		json.Unmarshal(b, n)
	}
	return n
}

// add appends a notification and compacts the list (call with mu held).
func (n *notifier) add(now time.Time, kind, level, title, body, goTo string) *Note {
	n.Next++
	n.Notes = append(n.Notes, Note{ID: n.Next, T: now.Unix(), Kind: kind, Level: level, Title: trunc(title, 200), Body: trunc(body, 300), Go: goTo})
	drop := 0
	for len(n.Notes)-drop > maxNotes && n.Notes[drop].ID <= n.Floor {
		drop++
	}
	n.Notes = n.Notes[drop:]
	if full := len(n.Notes) > fullNotes; full != n.stuffed {
		n.stuffed = full
		if full {
			log.Printf("notification store full: deliveries pending (%d notes kept after #%d)", len(n.Notes), n.Floor)
		}
	}
	return &n.Notes[len(n.Notes)-1]
}

// setFloor sets the lowest note id every thread's lead has acknowledged up to (teamdeliver.go); the notes after it stay.
func (n *notifier) setFloor(id int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if id != n.Floor {
		n.Floor = id
		n.saveLocked()
	}
}

// addSaved adds a note, then saves; a note that couldn't be saved is taken back (err), so its caller doesn't count it as made.
func (n *notifier) addSaved(now time.Time, kind, level, title, body, goTo, ref string) (*Note, error) { // n.mu held
	notes, next, stuffed := n.Notes, n.Next, n.stuffed
	x := n.add(now, kind, level, title, body, goTo)
	x.Ref = ref
	if err := n.saveLocked(); err != nil {
		n.Notes, n.Next, n.stuffed = notes, next, stuffed
		return nil, err
	}
	return x, nil
}

// observe compares this look with the last and notes what changed. said gives an agent's last words
// (the start of its final message), read only when it finishes.
func (n *notifier) observe(now time.Time, tm Terminals, ci CI, res Resources, said func(TPane) string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	before := n.Next
	panes, jobs := map[string]paneSeen{}, map[string]bool{}
	known0 := n.Panes != nil // a saved look to compare with, from before a restart too

	for _, s := range tm.Sessions {
		for _, tb := range s.Tabs {
			for _, p := range tb.Panes {
				if p.Agent == nil {
					continue
				}
				key := s.Name + ":" + strconv.Itoa(p.ID)
				panes[key] = paneSeen{State: p.State, Since: p.Since, Tab: tb.Name, Kind: p.Kind, Sid: p.Agent.sid, Cwd: p.Agent.startCwd}
				prev, known := n.Panes[key]
				was := prev.State
				if was == "waiting" && p.State == "waiting" && p.Since > prev.Since {
					was = "working" // a whole turn fit between two looks: Claude's status moved on and came back
				}
				if !known0 || !known || was == p.State {
					continue
				}
				name := tb.Name
				if len(tb.Panes) > 1 {
					name += " · " + cmp.Or(p.Title, "pane "+strconv.Itoa(p.ID))
				}
				switch {
				case p.State == "waiting" && (was == "working" || was == "stalled" || was == "background"):
					n.add(now, "done", "ok", name+" is done", said(p), "pane:"+key)
				case p.State == "asking":
					n.add(now, "asking", "warn", name+" needs your answer", p.Detail, "pane:"+key)
				case p.State == "stalled":
					n.add(now, "stalled", "warn", name+" looks stuck", p.Detail, "pane:"+key)
				}
			}
		}
	}
	// an agent gone from its pane (the pane closed, or Claude quit to the shell) in the middle of its work: twice
	// in a row, so a session file being rewritten isn't an exit. Its conversation goes with the note (featsup.go).
	for key, prev := range n.Panes {
		if _, ok := panes[key]; ok || !midWork[prev.State] || !n.teams[key[:strings.LastIndexByte(key, ':')]] {
			continue
		}
		if prev.Gone++; prev.Gone < 2 {
			panes[key] = prev
			continue
		}
		x := n.add(now, "exited", "warn", prev.Tab+"'s agent exited mid-work", "It was "+prev.State+" when it went.", "pane:"+key)
		prev.Gone = 0
		x.Exit = &prev
	}

	for _, j := range ci.Recent {
		id := j.Runner + "@" + strconv.FormatInt(j.Start, 10)
		jobs[id] = true
		if !n.primed || n.jobs[id] || j.Result != "Failed" {
			continue
		}
		what := ""
		if m := j.Meta; m != nil {
			what = m.Repo
			if m.PR > 0 {
				what += " PR #" + strconv.Itoa(m.PR)
			} else if m.Branch != "" {
				what += " " + m.Branch
			}
			if m.Title != "" {
				what += " · " + m.Title
			}
		}
		n.add(now, "ci", "bad", "CI failed: "+j.Meta.workflowOr(j.Name), strings.TrimSpace(what), "ci")
	}

	if n.full == nil {
		n.full = map[string]bool{}
	}
	over := func(key string, used, size uint64, kind, title string) {
		if size == 0 {
			return
		}
		pct := float64(used) / float64(size) * 100
		switch {
		case pct >= 90 && !n.full[key]:
			n.full[key] = true
			if n.primed { // full at start: learned, not announced again after every restart
				n.add(now, kind, "warn", fmt.Sprintf(title, int(math.Round(pct))), fmtBytes(int64(used))+" of "+fmtBytes(int64(size)), "res")
			}
		case pct < 85:
			delete(n.full, key)
		}
	}
	for _, d := range res.Disks {
		over("disk:"+d.Mount, d.Used, d.Size, "disk", d.Mount+" is %d%% full")
	}
	over("mem", res.MemTotal-min(res.MemAvail, res.MemTotal), res.MemTotal, "mem", "Memory is %d%% used")

	changed := !maps.Equal(panes, n.Panes)
	n.Panes, n.jobs, n.primed = panes, jobs, true
	if n.Next != before || changed {
		n.saveLocked()
	}
}

// setTeams records the sessions a thread manages (featsup.go): a standalone terminal's agent quitting is the owner's business
func (n *notifier) setTeams(m map[string]bool) {
	n.mu.Lock()
	n.teams = m
	n.mu.Unlock()
}

// workflowOr returns "workflow / name" for a job whose workflow is known, else name.
func (j *JobMeta) workflowOr(name string) string {
	if j == nil || j.Workflow == "" {
		return name
	}
	return j.Workflow + " / " + name
}

// saveLocked writes the notifier to its file (call with mu held); a notifier with no file keeps everything in memory.
func (n *notifier) saveLocked() error {
	if n.file == "" {
		return nil
	}
	b, err := json.Marshal(n)
	if err == nil {
		err = writeFileAtomic(n.file, b, 0o600)
	}
	if err != nil {
		log.Printf("notifications: save: %v", err)
	}
	return err
}

// unread counts the notes after the last one seen (call with mu held).
func (n *notifier) unread() int {
	c := 0
	for _, x := range n.Notes {
		if x.ID > n.Seen {
			c++
		}
	}
	return c
}

// markSeen records that the notes up to id have been read.
func (n *notifier) markSeen(id int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if id > n.Seen {
		n.Seen = min(id, n.Next)
		n.saveLocked()
	}
}

// badge returns the newest id (the page fetches the list when it moves) and how many are unread.
func (n *notifier) badge() (latest int64, unread int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.Next, n.unread()
}

// lastSaid returns the start of the agent's last message, for a "done" note.
func lastSaid(path string) string {
	ls, _, _ := readTail(path, 256<<10)
	for i := len(ls) - 1; i >= 0; i-- {
		var e tEntry
		if json.Unmarshal(ls[i], &e) != nil || e.Type != "assistant" || e.Sidechain || e.IsAPIError {
			continue
		}
		_, bs := blocks(e.Message.Content)
		for j := len(bs) - 1; j >= 0; j-- {
			if bs[j].Type == "text" && strings.TrimSpace(bs[j].Text) != "" {
				return trunc(strings.Join(strings.Fields(bs[j].Text), " "), 240)
			}
		}
	}
	return ""
}

// notesList serves GET /api/notes: the notifications newest first, and the read marker.
func (s *server) notesList(w http.ResponseWriter, r *http.Request) {
	unknown := s.deliveryUnknown() // threads whose team news may or may not have reached their lead (teamdeliver.go)
	s.notes.mu.Lock()
	defer s.notes.mu.Unlock()
	out := make([]Note, len(s.notes.Notes))
	for i, x := range s.notes.Notes {
		out[len(out)-1-i] = x // newest first
	}
	writeJSON(w, 200, map[string]any{"notes": out, "seen": s.notes.Seen, "full": s.notes.stuffed, "deliveryUnknown": unknown})
}

// notesSeen serves POST /api/notes/seen: it marks notes up to {id} as read.
func (s *server) notesSeen(w http.ResponseWriter, r *http.Request) {
	var b struct{ ID int64 }
	if err := readJSON(r, &b); err != nil {
		writeJSON(w, 400, map[string]any{"error": "bad request"})
		return
	}
	s.notes.markSeen(b.ID)
	writeJSON(w, 200, map[string]any{"ok": true})
}
