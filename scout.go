package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The scout: JARVIS being proactive. The owner starts terminals by hand and forgets them. Every hour the panel looks
// at each zellij session no thread owns; one whose agents have done nothing for a day while it still holds unshipped
// work (lines changed, a PR open) is stalled. At most one every 12 hours, the panel hands Main JARVIS what it knows
// about it, and Main comes to the owner with a takeover proposal (a ```proposal card: what it is, the plan, how it
// will report, the proof of done, what it needs, blockers). The owner's Take it over / Not now / Never come back
// here (POST /api/ai/scout) and go on to Main as their answer; taken, Main opens a thread that adopts the session.
// The owner's answers, kept in scout.json, are also what Main learns from (SOUL.md).

// How long a tab must be idle to count as stalled, the gap between two proposals, and how long Not now holds.
const (
	scoutStall = 24 * time.Hour // no agent activity for this long
	scoutGap   = 12 * time.Hour // between two proposals
	scoutLater = 24 * time.Hour // "Not now"
)

// scoutMark is what is remembered about one tab: when it was proposed, snoozed, refused or adopted.
type scoutMark struct {
	Asked int64  `json:"asked,omitempty"` // when Main was last asked to propose it
	Until int64  `json:"until,omitempty"` // not before this again
	Never bool   `json:"never,omitempty"` // the owner said never
	Taken string `json:"taken,omitempty"` // the thread that adopted it
}

// scout is the scout's saved state: when it last proposed and the mark of each tab.
type scout struct {
	full  atomic.Bool // asks the next tick for a full read of the terminals (scoutFresh)
	mu    sync.Mutex
	file  string
	Last  int64                 `json:"last"` // the last proposal asked for
	Marks map[string]*scoutMark `json:"marks"`
}

// load reads the saved state, starting empty when there is none.
func (c *scout) load() {
	c.Marks = map[string]*scoutMark{}
	if b, err := os.ReadFile(c.file); err == nil {
		_ = json.Unmarshal(b, c)
	}
	if c.Marks == nil {
		c.Marks = map[string]*scoutMark{}
	}
}

// save writes the state to its file atomically (c.mu held).
func (c *scout) save() { // c.mu held
	b, _ := json.MarshalIndent(c, "", "  ")
	if os.WriteFile(c.file+".tmp", b, 0o600) == nil {
		_ = os.Rename(c.file+".tmp", c.file)
	}
}

// mark returns the mark for a session/tab key, creating it on first use (c.mu held).
func (c *scout) mark(session string) *scoutMark { // c.mu held
	if c.Marks == nil { // before load (a test server, or a mark ahead of the first scout pass)
		c.Marks = map[string]*scoutMark{}
	}
	m := c.Marks[session]
	if m == nil {
		m = &scoutMark{}
		c.Marks[session] = m
	}
	return m
}

// a stalled tab: what Main is told about it. The owner thinks in tabs (each agent's tab is a "terminal" in the
// sidebar), and one busy tab in a session mustn't hide a forgotten one beside it, so each tab is judged on its own.
// Its key, for the marks and the proposal card, is "<session>/<tab>".
type scoutFind struct {
	Session, Tab string
	Idle         time.Duration
	Agents       []featAgent
	Trees        []featTree
	PRs          []*PRStatus // the PRs its agents linked
	Add, Del     int
	Open         int // of them, still open
}

// key is the "<session>/<tab>" the marks and the proposal card go by.
func (f *scoutFind) key() string { return f.Session + "/" + f.Tab }

// scoutPick: the stalled tab to propose now, if any (oldest stall first)
func scoutPick(now time.Time, stall time.Duration, sessions []string, facts func(string) (featFacts, bool), owned func(string) bool, m map[string]*scoutMark) *scoutFind {
	var found []scoutFind
	for _, se := range sessions {
		if owned(se) {
			continue
		}
		f, ok := facts(se)
		if !ok {
			continue
		}
		tabs := map[string]*scoutFind{}
		var order []string
		for _, a := range f.Team {
			t := tabs[a.Tab]
			if t == nil {
				t = &scoutFind{Session: se, Tab: a.Tab}
				tabs[a.Tab] = t
				order = append(order, a.Tab)
			}
			t.Agents = append(t.Agents, a)
			t.Add, t.Del = t.Add+a.Add, t.Del+a.Del
		}
		for _, tab := range order {
			t := tabs[tab]
			if k := m[t.key()]; k != nil && (k.Never || k.Taken != "" || k.Until > now.Unix()) {
				continue
			}
			var last int64
			busy := false
			mine := map[int]bool{}
			for _, a := range t.Agents {
				last = max(last, a.LastAt)
				busy = busy || a.State == "working"
				for _, n := range a.PRs {
					mine[n] = true
				}
			}
			for _, p := range f.PRs {
				if p != nil && mine[p.Number] {
					t.PRs = append(t.PRs, p)
					if p.State == "OPEN" {
						t.Open++
					}
				}
			}
			for _, tr := range f.Trees {
				if slices.Contains(tr.Agents, tab) {
					t.Trees = append(t.Trees, tr)
				}
			}
			if busy || last == 0 || now.Sub(time.Unix(last, 0)) < stall || (t.Add+t.Del == 0 && t.Open == 0) {
				continue
			}
			t.Idle = now.Sub(time.Unix(last, 0))
			found = append(found, *t)
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Idle > found[j].Idle })
	return &found[0]
}

// scoutFresh: the panel reads each tab's agents in full only while the Terminals page is open; the scout asks for one
// such read and waits for the next tick (every 10 s) to make it
func (s *server) scoutFresh() {
	s.scout.full.Store(true)
	for i := 0; i < 30 && s.scout.full.Load(); i++ {
		time.Sleep(time.Second)
	}
	time.Sleep(time.Second) // the tick that took it, done
}

// scoutOwned reports whether a thread already leads the zellij session.
func (s *server) scoutOwned(session string) bool {
	s.ai.mu.Lock()
	defer s.ai.mu.Unlock()
	for _, t := range s.ai.threads {
		if t.Team == session && t.Status != "archived" {
			return true
		}
	}
	return false
}

// scoutLoop runs the scout every hour, after letting the panes settle following a start.
func (s *server) scoutLoop() {
	s.scout.load()
	time.Sleep(10 * time.Minute) // after a restart, let the panes settle
	for {
		s.scoutRun(time.Now(), 1, false, scoutStall)
		time.Sleep(time.Hour)
	}
}

// scoutRun: on the hour, one proposal at most every 12 hours; asked for (POST /agent/scout), up to n at once, now
func (s *server) scoutRun(now time.Time, n int, force bool, stall time.Duration) []string {
	s.scoutFresh()
	s.scout.mu.Lock()
	defer s.scout.mu.Unlock()
	if !force && now.Sub(time.Unix(s.scout.Last, 0)) < scoutGap {
		return nil
	}
	var names []string
	for _, se := range s.term.listing().Sessions {
		names = append(names, se.Name)
	}
	var briefs, picked []string
	for len(picked) < n {
		f := scoutPick(now, stall, names, s.featFacts, s.scoutOwned, s.scout.Marks)
		if f == nil {
			break
		}
		m := s.scout.mark(f.key())
		m.Asked, m.Until = now.Unix(), now.Add(scoutLater).Unix() // unanswered, it can come back in a day
		briefs, picked = append(briefs, scoutBrief(f)), append(picked, f.key())
	}
	if len(picked) == 0 {
		return nil
	}
	if _, err := s.ai.send("", strings.Join(briefs, "\n\n")); err != nil { // several: one turn, a proposal card each
		for _, se := range picked {
			s.scout.Marks[se].Until = 0
		}
		return nil
	}
	s.scout.Last = now.Unix()
	s.scout.save()
	return picked
}

// POST /agent/scout {n, hours}: look now, past the 12-hour gap (and idle for hours, if given, instead of a day), and have Main propose up to n takeovers (the owner asked to see some)
func (s *server) agentScout(w http.ResponseWriter, r *http.Request) {
	var b struct {
		N, Hours int
		Dry      bool // only say what each session looks like, and why it is or isn't stalled
	}
	_ = readJSONN(r, &b, 256)
	if b.Dry {
		s.scoutFresh()
		out := map[string]any{}
		for _, se := range s.term.listing().Sessions {
			f, _ := s.featFacts(se.Name)
			for _, a := range f.Team {
				idle := ""
				if a.LastAt > 0 {
					idle = time.Since(time.Unix(a.LastAt, 0)).Round(time.Minute).String()
				}
				out[se.Name+"/"+a.Tab] = map[string]any{"state": a.State, "idle": idle, "add": a.Add, "del": a.Del, "prs": a.PRs, "owned": s.scoutOwned(se.Name)}
			}
		}
		writeJSON(w, 200, out)
		return
	}
	stall := scoutStall
	if b.Hours > 0 {
		stall = time.Duration(b.Hours) * time.Hour
	}
	got := s.scoutRun(time.Now(), min(max(b.N, 1), 5), true, stall)
	writeJSON(w, 200, map[string]any{"ok": true, "sessions": got})
}

// what Main gets: the facts, and what to do with them
func scoutBrief(f *scoutFind) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Proactive check from the panel]\nThe owner's terminal tab %q (in zellij session %q) looks stalled: no agent in it has done anything for %s, and its agents changed +%d -%d lines in it (edits they made, not a measured diff: check what is really unshipped", f.Tab, f.Session, scoutAgo(f.Idle), f.Add, f.Del)
	if f.Open > 0 {
		fmt.Fprintf(&b, ", %d PR(s) open", f.Open)
	}
	b.WriteString("). No thread owns it.\n\nIts agents:\n")
	for _, a := range f.Agents {
		fmt.Fprintf(&b, "- %s pane %s: %s", a.Kind, a.Key, cmp.Or(a.Title, "untitled"))
		if a.Branch != "" {
			fmt.Fprintf(&b, " (branch %s)", a.Branch)
		}
		if a.Last != "" {
			fmt.Fprintf(&b, ". Last said: %s", trunc(a.Last, 300))
		}
		b.WriteString("\n")
	}
	for _, t := range f.Trees {
		fmt.Fprintf(&b, "- worktree %s on branch %s\n", t.Root, t.Branch)
	}
	for _, p := range f.PRs {
		fmt.Fprintf(&b, "- PR #%d %s (%s, review %s) %s\n", p.Number, p.Title, p.State, cmp.Or(p.Review, "none"), p.URL)
	}
	fmt.Fprintf(&b, "\nRead its agents (pane_chat, pane_screen) and its branch until you understand what the owner was building and how far it got. "+
		"Then, if you can see how to ship it, come to the owner with a takeover proposal. Write it as this fenced block, which the panel draws as a card with "+
		"Take it over / Not now / Never buttons (prose is not enough: the owner answers on the card):\n```proposal\n"+
		`{"session": %q, "title": "<a few words>", "stalled": "<how long>", "what": "<what they were building and how far it got, two sentences>", `+
		`"plan": ["<step>", "..."], "updates": ["<how and when you will report>"], "proof": ["<the check that shows it's done>"], `+
		`"needs": ["<a decision or access you need>"], "blockers": ["<what could stop it>"]}`+"\n```\nKeep the text around it to one line. "+
		"Check your memory for what the owner approved or declined before, and shape the plan to it. Then send one short Telegram line saying "+
		"you have a proposal for %q waiting in the panel. If it isn't worth taking over (abandoned on purpose, done elsewhere), say so in one line and stop.", f.key(), f.Tab)
	return b.String()
}

// scoutAgo says how long ago in hours, or in days from 48 hours.
func scoutAgo(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
	return fmt.Sprintf("%d hours", int(d.Hours()))
}

// POST /api/ai/scout {session, act: take|later|never}: the owner's answer to a proposal. It's kept (so it isn't
// asked again too soon, or ever) and goes on to Main as the owner's own message.
func (s *server) aiScout(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Session, Act, Title string
		Plan                json.RawMessage // take: the proposal itself, which becomes the new thread's brief
	}
	if err := readJSONN(r, &b, 4096); err != nil || b.Session == "" {
		writeJSON(w, 400, map[string]any{"error": "which session?"})
		return
	}
	now := time.Now()
	var say string
	s.scout.mu.Lock()
	m := s.scout.mark(b.Session)
	switch b.Act {
	case "take":
		m.Until = now.Add(30 * 24 * time.Hour).Unix()
		say = fmt.Sprintf("Approved: take over %q (%s) with the plan you proposed. Open its thread with thread_open(…, team=%q) so the lead adopts that session and its agents, and save to memory that I approve takeovers like this one.", b.Session, cmp.Or(b.Title, "your proposal"), b.Session)
	case "later":
		m.Until = now.Add(scoutLater).Unix()
		say = fmt.Sprintf("Not now for %q. Ask me again tomorrow if it's still stalled.", b.Session)
	case "never":
		m.Never = true
		say = fmt.Sprintf("Never take over %q; leave it to me. Save to memory why this kind of work isn't for you to take over, if you can tell.", b.Session)
	default:
		s.scout.mu.Unlock()
		writeJSON(w, 400, map[string]any{"error": "act: take, later or never"})
		return
	}
	s.scout.save()
	s.scout.mu.Unlock()
	if b.Act == "take" { // the panel opens the thread itself: the owner's yes doesn't hang on Main calling a tool right
		t, err := s.scoutAdopt(b.Session, b.Title, b.Plan)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		say = fmt.Sprintf("Approved: take over %q (%s) with the plan you proposed. The panel opened thread %s for it, which adopts that session and its agents; don't open another. Save to memory that I approve takeovers like this one.", b.Session, cmp.Or(b.Title, "your proposal"), t.ID)
	}
	if _, err := s.ai.send("", "[From the owner, in the panel]\n"+say); err != nil {
		writeJSON(w, 409, map[string]any{"error": err.Error()})
		return
	}
	s.audit.add("assistant-scout", b.Session+" "+b.Act, clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true})
}

// scoutAdopt: a thread whose lead takes over the session, briefed with the proposal the owner approved
func (s *server) scoutAdopt(session, title string, plan json.RawMessage) (*thread, error) {
	var p struct {
		What                                  string
		Plan, Updates, Proof, Needs, Blockers []string
	}
	_ = json.Unmarshal(plan, &p)
	var b strings.Builder
	fmt.Fprintf(&b, "[Brief from Main JARVIS, who opened this thread]\nThe owner approved Main's proposal to take over their stalled terminal tab %q (session/tab). Its agents are your team now: read them (pane_chat) and their branch before you change anything, keep the work on that branch, and carry it to shipped.\n", session)
	if p.What != "" {
		fmt.Fprintf(&b, "\nWhat it is: %s\n", p.What)
	}
	for _, sec := range []struct {
		h  string
		xs []string
	}{{"The plan the owner approved (publish it with thread_update plan=)", p.Plan}, {"How the owner hears from you", p.Updates}, {"The proof of done you promised", p.Proof}, {"What you need from the owner", p.Needs}, {"Blockers", p.Blockers}} {
		if len(sec.xs) > 0 {
			fmt.Fprintf(&b, "\n%s:\n- %s\n", sec.h, strings.Join(sec.xs, "\n- "))
		}
	}
	s.ai.mu.Lock()
	parent := ""
	if n := len(s.ai.items); n > 0 {
		parent = s.ai.items[n-1].K
	}
	s.ai.mu.Unlock()
	team, tab, _ := strings.Cut(session, "/")
	t, err := s.ai.openThread(cmp.Or(title, tab, team), parent, team, b.String())
	if err == nil {
		s.scout.mu.Lock()
		s.scout.mark(session).Taken = t.ID
		s.scout.save()
		s.scout.mu.Unlock()
	}
	return t, err
}

// scoutFile is where the scout's state lives, beside the threads file.
func scoutFile(threadsFile string) string {
	return filepath.Join(filepath.Dir(threadsFile), "scout.json")
}
