package main

import (
	"bytes"
	"cmp"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// A feature's session ledger, for the feature page's timeline (its "sessions" and "relations" keys, see featPage).
// Each entry is one agent session (a "bar" on the timeline) and the step it worked on. It is built from:
//   - the lead's assignment events (featAssignments in featv8.go), which say who was put on which step and when;
//   - the last completed terminal sample, which says who runs in which pane now;
//   - each live transcript's first entry, which says when it started;
//   - the supervisor's resume records.
// Relations come only from explicit records: an assignment's recorded relation, or a resume record. They are
// never inferred from adjacent bars, timing or names. When a step or identity can't be tied down, the entry
// says so (identity, why) instead of guessing.
// Nothing here samples, runs or fetches anything. It reads memory, the dossier the caller already has, and,
// once per transcript, its first 64 KB.

// featSession is one entry of the ledger: an agent session, the step it was put on, from when to when, and how
// sure the panel is about its identity.
type featSession struct {
	ID         string   `json:"id"` // the assignment's event id, or "live:<session>:<pane>:<sid>"
	Sid        string   `json:"sid"`
	Tab        string   `json:"tab"`
	Pane       int      `json:"pane,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Model      string   `json:"model,omitempty"` // live bars, and assignments the sample confirmed: what its transcript's latest reply ran on
	Step       string   `json:"step"`
	StepSource string   `json:"stepSource"`
	Owns       []string `json:"owns,omitempty"` // a live agent with no assignment: the steps its tab owns in PLAN.md now (no history)
	From       int64    `json:"from"`           // 0 = unknown
	FromSource string   `json:"fromSource"`
	To         *int64   `json:"to"`
	End        *string  `json:"end"` // reassigned, exited (not in the last sample), null = still on it
	EndSource  string   `json:"endSource,omitempty"`
	Branch     string   `json:"branch,omitempty"`
	Worktree   string   `json:"worktree,omitempty"`
	// Identity:
	//   - observed: the pane runs this sid now;
	//   - claimed: the lead recorded it but the sample hasn't confirmed it;
	//   - mismatch: the pane runs another sid now;
	//   - ambiguous: the tab name is shared;
	//   - unknown: no sample.
	Identity string `json:"identity"`
	// Blocked: why its step can't move yet (see featDepBlocks in featdoc.go: an unfinished after: step, or an invalid
	// after: ref, which the gates treat as not ready too); DepIssues: the step's invalid after: refs, shown, never drawn
	Blocked   string         `json:"blocked,omitempty"`
	DepIssues []featDepIssue `json:"depIssues,omitempty"`
	Why       string         `json:"why,omitempty"`
	Source    string         `json:"source"`
}

// featRelation links two ledger entries: a handoff or blocking dependency between steps, a pair working together,
// or a supervisor resume. Src names the explicit record it came from.
type featRelation struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // handoff, blocks, pair, resume
	From     string `json:"from"` // featSession ids; "" when that end has no session yet (see fromStep/toStep, why)
	To       string `json:"to"`
	FromStep string `json:"fromStep,omitempty"`
	ToStep   string `json:"toStep,omitempty"`
	At       int64  `json:"at"`
	Why      string `json:"why,omitempty"`
	Src      string `json:"src"`
}

// liveAgent is a live agent pane as the last terminal sample saw it.
type liveAgent struct {
	key, sid, tab, kind, branch, root, path, model string
	pane                                           int
}

// sessIn is everything the ledger is built from, gathered by featSessionsPage; build does the pure work, so it needs
// no locks, IO or terminal access.
type sessIn struct {
	assigns  []featAssign
	live     []liveAgent
	sampleAt time.Time           // zero: no sample yet
	owners   map[string][]string // tab (lower case) -> step keys it owns in PLAN.md
	resumes  map[string]featResume
	started  func(path string) int64
	ms       []featMilestone // PLAN.md, for after: edges
	rev      string
}

// build returns the ledger and its relations: one entry per assignment event (ended by a later assignment of the same
// pane or tab, or by the agent leaving the sample), one per live agent no assignment accounts for, then the relations
// from assignments, PLAN.md after: lines and supervisor resumes.
func (in sessIn) build() ([]featSession, []featRelation) {
	out := []featSession{}
	tabN := map[string]int{}
	for _, l := range in.live {
		tabN[strings.ToLower(l.tab)]++
	}
	as := slices.Clone(in.assigns)
	slices.SortStableFunc(as, func(a, b featAssign) int { return cmp.Compare(a.Seq, b.Seq) })
	usedLive := map[string]bool{}
	for i, a := range as {
		e := featSession{ID: a.ID, Sid: a.Sid, Tab: a.Tab, Pane: a.Pane, Step: a.Step, StepSource: "assignment " + a.ID,
			From: a.At.Unix(), FromSource: "assignment " + a.ID, Source: "assignment event (lead)", Identity: "claimed"}
		// a later assignment of the same pane (or tab, when no pane was named) ends this one
		for _, b := range as[i+1:] {
			if a.Pane > 0 && b.Pane == a.Pane || a.Pane == 0 && strings.EqualFold(b.Tab, a.Tab) {
				e.To, e.End, e.EndSource = refp(b.At.Unix()), refp("reassigned"), "assignment "+b.ID
				break
			}
		}
		if e.End == nil {
			in.matchLive(&e, tabN, usedLive)
		}
		out = append(out, e)
	}
	for _, l := range in.live {
		if usedLive[l.key] {
			continue
		}
		e := featSession{ID: "live:" + l.key + ":" + l.sid, Sid: l.sid, Tab: l.tab, Kind: l.kind, Model: l.model, Branch: l.branch, Worktree: l.root,
			Source: "live sample", Identity: "observed", StepSource: "", FromSource: "unknown"}
		fmt.Sscanf(l.key[strings.LastIndex(l.key, ":")+1:], "%d", &e.Pane)
		if in.started != nil && l.path != "" {
			if t := in.started(l.path); t > 0 {
				e.From, e.FromSource = t, "transcript first entry"
			}
		}
		if tabN[strings.ToLower(l.tab)] > 1 {
			e.Identity, e.Why = "ambiguous", "tab name shared by several panes: plan @Owner can't tell them apart"
		} else if owns := in.owners[strings.ToLower(l.tab)]; len(owns) > 0 {
			e.Owns = owns
			e.StepSource = "plan @Owner (current only, no history)"
			if len(owns) == 1 {
				e.Step = owns[0]
			}
		} else {
			e.Why = "no assignment recorded and its tab owns no step"
		}
		out = append(out, e)
	}

	blocked := featDepBlocks(in.ms)
	issues := map[string][]featDepIssue{}
	for _, m := range in.ms {
		for _, st := range m.Steps {
			issues[st.Key] = st.AfterInvalid
		}
	}
	for i := range out {
		if k := out[i].Step; k != "" {
			out[i].Blocked, out[i].DepIssues = blocked[k], issues[k]
		}
	}

	rels := []featRelation{}
	latestOn := func(step string, at int64) string { // the session put on step most recently, at or before at
		id := ""
		for _, e := range out {
			if e.Step == step && e.Source != "live sample" && e.From <= at {
				id = e.ID
			}
		}
		return id
	}
	for _, a := range as {
		if a.Rel == nil || a.Rel.Kind == "" {
			continue
		}
		r := featRelation{ID: "rel-" + a.ID, Kind: a.Rel.Kind, From: a.ID, FromStep: a.Step, ToStep: a.Rel.With, At: a.At.Unix(), Why: a.Rel.Why, Src: "assignment " + a.ID}
		if r.To = latestOn(a.Rel.With, a.At.Unix()); r.To == "" {
			r.Why = strings.TrimSpace(r.Why + " (no session assigned to step " + a.Rel.With + " yet)")
		}
		if a.Rel.Kind == "handoff" || a.Rel.Kind == "blocks" { // recorded on the later step: the other one comes first
			r.From, r.To, r.FromStep, r.ToStep = r.To, r.From, r.ToStep, r.FromStep
		}
		rels = append(rels, r)
	}
	onStep := func(step string) string { // the bar on step: the newest still open, else the newest
		id, open := "", ""
		for _, e := range out {
			if e.Step == step {
				id = e.ID
				if e.End == nil {
					open = e.ID
				}
			}
		}
		return cmp.Or(open, id)
	}
	for _, m := range in.ms { // PLAN.md after: lines, the valid ones only (invalid refs are DepIssues, never edges)
		for _, st := range m.Steps {
			for _, r := range st.After {
				rel := featRelation{ID: "after-" + r + "-" + st.Key, Kind: "blocks", FromStep: r, ToStep: st.Key, From: onStep(r), To: onStep(st.Key),
					Src: "PLAN.md after: @rev " + in.rev, Why: blocked[st.Key]}
				var miss []string
				if rel.From == "" {
					miss = append(miss, r)
				}
				if rel.To == "" {
					miss = append(miss, st.Key)
				}
				if len(miss) > 0 {
					rel.Why = strings.TrimSpace(rel.Why + " (no session on step " + strings.Join(miss, ", ") + ")")
				}
				rels = append(rels, rel)
			}
		}
	}
	for _, sid := range slices.Sorted(func(yield func(string) bool) {
		for k := range in.resumes {
			if !yield(k) {
				return
			}
		}
	}) {
		r := in.resumes[sid]
		var before, after string // the sid's last bar before the resume; its live bar, else its first one after
		for _, e := range out {
			switch {
			case e.Sid != sid:
			case e.Source == "live sample":
				after = e.ID
			case e.From < r.At:
				before = e.ID
			case after == "":
				after = e.ID
			}
		}
		if before == "" && after == "" {
			continue
		}
		if before == after {
			before = ""
		}
		why := fmt.Sprintf("supervisor resume, %s (try %d)", r.State, r.Tries)
		if before == "" {
			why += "; the session before it isn't in the ledger"
		}
		if after == "" {
			why += "; no later bar: the same session continues"
		}
		rels = append(rels, featRelation{ID: "resume-" + sid + fmt.Sprintf("-%d", r.At), Kind: "resume", From: before, To: after, At: r.At, Why: why, Src: "featsup resume record"})
	}
	return out, rels
}

// matchLive ties an open assignment to the pane the sample shows. Only an exact pane, or a tab name no other
// pane shares, counts. A sid is confirmed only when the pane runs that same sid.
func (in sessIn) matchLive(e *featSession, tabN map[string]int, used map[string]bool) {
	if in.sampleAt.IsZero() {
		e.Identity, e.Why = "unknown", "no terminal sample yet"
		return
	}
	var hit *liveAgent
	for i := range in.live {
		l := &in.live[i]
		if e.Pane > 0 && l.pane == e.Pane || e.Pane == 0 && strings.EqualFold(l.tab, e.Tab) {
			hit = l
			break
		}
	}
	if hit == nil {
		e.To, e.End = refp(in.sampleAt.Unix()), refp("exited")
		e.EndSource = "not in the terminal sample of " + in.sampleAt.UTC().Format(time.RFC3339)
		return
	}
	if e.Pane == 0 && tabN[strings.ToLower(e.Tab)] > 1 {
		e.Identity, e.Why = "ambiguous", "the assignment names a tab shared by several panes"
		return
	}
	switch {
	case e.Sid != "" && hit.sid != e.Sid:
		e.Identity, e.Why = "mismatch", "pane "+hit.key+" runs "+hit.sid+" now, not the assigned "+e.Sid
		return
	case e.Sid == "":
		e.Identity, e.Why = "observed", "no sid recorded: matched by "+map[bool]string{true: "pane", false: "tab"}[e.Pane > 0]
		e.Sid = hit.sid
	default:
		e.Identity = "observed"
	}
	used[hit.key] = true
	e.Pane, e.Kind, e.Model, e.Branch, e.Worktree = hit.pane, hit.kind, hit.model, hit.branch, hit.root
}

// tsRe finds a timestamp in a transcript's JSON lines.
var tsRe = regexp.MustCompile(`"timestamp":"([^"]+)"`)

// featStarted caches transcript start times, which never change: each path is read once.
var featStarted = struct {
	sync.Mutex
	m map[string]int64
}{m: map[string]int64{}}

// transcriptStart returns when the transcript at path began (unix seconds, from the first timestamp in its first
// 64 KB), or 0 if the file or its first entry isn't there yet.
func transcriptStart(path string) int64 {
	featStarted.Lock()
	t, ok := featStarted.m[path]
	featStarted.Unlock()
	if ok {
		return t
	}
	if f, err := os.Open(path); err == nil {
		b := make([]byte, 64<<10)
		n, _ := f.Read(b)
		f.Close()
		if m := tsRe.FindSubmatch(b[:n]); m != nil {
			if at, err := time.Parse(time.RFC3339Nano, string(bytes.TrimSpace(m[1]))); err == nil {
				t = at.Unix()
			}
		}
	}
	if t == 0 {
		return 0 // not there (or no entry) yet: asked again next time
	}
	featStarted.Lock()
	if len(featStarted.m) > 4096 { // a crude cap (the cache is simply reset); sessions number in the hundreds
		featStarted.m = map[string]int64{}
	}
	featStarted.m[path] = t
	featStarted.Unlock()
	return t
}

// featSessionsPage returns the ledger and relations for the feature page's "sessions" and "relations", built from
// memory and the dossier the caller already read. The third value is the terminal sample's time (0: none yet), never
// this request's.
func (s *server) featSessionsPage(session string, d featDossier) ([]featSession, []featRelation, int64) {
	snap, at := s.term.sample()
	in := sessIn{assigns: s.featAssignments(session), sampleAt: at, owners: map[string][]string{}, resumes: map[string]featResume{}, started: transcriptStart,
		ms: d.Milestones, rev: d.Rev}
	for _, se := range snap.Sessions {
		if se.Name != session {
			continue
		}
		for _, tb := range se.Tabs {
			for _, p := range tb.Panes {
				if p.Agent == nil {
					continue
				}
				l := liveAgent{key: fmt.Sprintf("%s:%d", session, p.ID), pane: p.ID, sid: p.Agent.sid, tab: tb.Name, kind: cmp.Or(p.Kind, "claude"), path: p.Agent.path, model: p.Agent.Model}
				if p.Git != nil {
					l.branch, l.root = p.Git.Branch, p.Git.Root
				}
				in.live = append(in.live, l)
			}
		}
	}
	for _, ms := range d.Milestones {
		for _, st := range ms.Steps {
			if st.Owner != "" && st.State != "done" {
				k := strings.ToLower(st.Owner)
				in.owners[k] = append(in.owners[k], st.Key)
			}
		}
	}
	s.sup.mu.Lock()
	for sid, r := range s.sup.Resumes {
		if r.Session == session {
			in.resumes[sid] = r
		}
	}
	s.sup.mu.Unlock()
	ss, rs := in.build()
	return ss, rs, unixOr0(at)
}
