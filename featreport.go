package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// A feature's report: the page a zellij session opens to (Terminals → a session). The panel gathers the facts it
// can read itself (the team and what each agent last reported, the worktrees and branches, every PR the agents
// linked and how far each went, lines changed, the JARVIS thread's plan), and a Claude run (claude -p) reads them
// and writes the parts a project manager would: the objective and requirements, the plan with who owns each step,
// the risks, where it stands and how far from done, the testing, how the PRs were merged. The report is kept in
// ~/jarvis-files/features/<session>/report.json and rewritten only when the owner asks, since it costs a model run.
// The facts gathered here (featFacts) are also the payload of the feature page's GET.

// featAgent is one agent pane of the feature's team, as the page and the report writer see it.
type featAgent struct {
	Key    string `json:"key"`
	Tab    string `json:"tab"`
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Title  string `json:"title,omitempty"`
	Branch string `json:"branch,omitempty"`
	Root   string `json:"root,omitempty"`
	Add    int    `json:"add,omitempty"`
	Del    int    `json:"del,omitempty"`
	Ctx    int    `json:"ctx,omitempty"` // % of its context window
	Last   string `json:"last,omitempty"`
	LastAt int64  `json:"lastAt,omitempty"`
	PRs    []int  `json:"prs,omitempty"`
	path   string
}

// featTree is a git worktree (root and branch) in which some of the team works.
type featTree struct {
	Root      string   `json:"root"`
	Branch    string   `json:"branch"`
	Agents    []string `json:"agents"`              // tabs working in it
	Main      bool     `json:"main,omitempty"`      // the feature's main worktree: the others merge into it before stage
	Ahead     int      `json:"ahead,omitempty"`     // its commits not in the main worktree's branch yet
	Checked   bool     `json:"checked,omitempty"`   // Ahead was measured
	OtherRepo bool     `json:"otherRepo,omitempty"` // a different repository from the main worktree's
}

// featFacts is everything the panel knows of a session as a feature: the team, worktrees, PRs, the record folder
// (Dossier) and the report, if any. It is the feature page's payload and the report writer's input.
type featFacts struct {
	Session string      `json:"session"`
	Thread  *featThread `json:"thread,omitempty"`
	Team    []featAgent `json:"team"`
	Trees   []featTree  `json:"trees"`
	TreesAt int64       `json:"treesAt"` // when trees' merge fields were measured in the background (0: not yet)
	PRs     []*PRStatus `json:"prs"`
	Add     int         `json:"add"`
	Del     int         `json:"del"`
	Report  *featReport `json:"report,omitempty"`
	Dossier featDossier `json:"dossier"`       // the source of truth (featdoc.go)
	Busy    bool        `json:"busy"`          // a report is being written
	Err     string      `json:"err,omitempty"` // the last attempt's failure
	// Page: the feature page's own data (featPage in featv8page.go), set on the owner's GET only; a key that is absent
	// means "not tracked yet", never a placeholder
	Page map[string]any `json:"page,omitempty"`
}

// featReport is what the report writer returns (its JSON, as the prompt asks), with when it was written. The types
// below it are the shapes of its parts.
type featReport struct {
	At           int64       `json:"at"`
	Summary      string      `json:"summary"`
	Stage        string      `json:"stage"` // requirements, planning, implementing, testing, iterating, suite, release, done
	Progress     int         `json:"progress"`
	Objective    string      `json:"objective"`
	Requirements []string    `json:"requirements"`
	Plan         []featStep  `json:"plan"`
	Stages       []featStage `json:"stages"`     // the stages this feature goes through, as the writer judges them
	Milestones   []featRepMs `json:"milestones"` // detailed: a goal and the steps (with owners) of each
	Risks        []string    `json:"risks"`
	Testing      featTesting `json:"testing"`
	Remaining    []string    `json:"remaining"`
	Merges       string      `json:"merges"`
	Roles        []featRole  `json:"roles"`
	Timeline     []featEvent `json:"timeline"`
}

// featStage is one stage of the feature's own life cycle, as the report writer judges it.
type featStage struct {
	Name   string `json:"name"`
	State  string `json:"state"` // done, now, todo
	Detail string `json:"detail,omitempty"`
}

// featRepMs is a milestone in the report: its goal, state and steps.
type featRepMs struct {
	Title string     `json:"title"`
	Goal  string     `json:"goal,omitempty"`
	State string     `json:"state"` // done, now, todo
	Steps []featStep `json:"steps"`
}

// featStep is a step of a report milestone, with the tab that owns it.
type featStep struct {
	Step  string `json:"step"`
	State string `json:"state"` // done, doing, todo
	Owner string `json:"owner,omitempty"`
}

// featTesting is the report's summary of testing: a short status and the test cases.
type featTesting struct {
	Status string     `json:"status"`
	Cases  []featCase `json:"cases"`
}

// featCase is one test case in the report.
type featCase struct {
	Name  string `json:"name"`
	State string `json:"state"` // pass, fail, todo
}

// featRole says what one tab of the team is for and what it owns.
type featRole struct {
	Tab  string `json:"tab"`
	Role string `json:"role"`
	Owns string `json:"owns"`
}

// featEvent is one entry of the report's timeline.
type featEvent struct {
	When string `json:"when"`
	What string `json:"what"`
}

// featBusy tracks which sessions have a report being written, and the last failure of each.
var featBusy = struct {
	sync.Mutex
	m   map[string]bool
	err map[string]string
}{m: map[string]bool{}, err: map[string]string{}}

// featDir is the folder holding a feature's record files and report under home.
func featDir(home, session string) string {
	return filepath.Join(home, "jarvis-files", "features", session)
}

// featFacts gathers what the panel itself knows of a session as a feature, from memory (no git or network in a
// request). ok is false when there is neither a sampled session, a managing thread nor a record folder.
func (s *server) featFacts(session string) (featFacts, bool) {
	f := featFacts{Session: session, Team: []featAgent{}, Trees: []featTree{}, PRs: []*PRStatus{}}
	found := false
	trees := map[string]*featTree{}
	seenPR := map[string]bool{}
	for _, se := range s.term.listing().Sessions {
		if se.Name != session {
			continue
		}
		found = true
		for _, tb := range se.Tabs {
			for _, lp := range tb.Panes {
				p, ok := s.term.pane(session, lp.ID)
				if !ok || p.Agent == nil {
					continue
				}
				a := featAgent{Key: fmt.Sprintf("%s:%d", session, p.ID), Tab: tb.Name, Kind: cmp.Or(p.Kind, "claude"), State: p.State,
					Title: p.Agent.Title, Add: p.Agent.Add, Del: p.Agent.Del, Last: p.Agent.Last, LastAt: p.Agent.LastAt, PRs: p.Agent.PRs, path: p.Agent.path}
				if p.Agent.path != "" && a.Add == 0 && a.Del == 0 {
					a.Add, a.Del = s.arts.lines(p.Agent.path)
				}
				if c := p.Agent.Ctx; c != nil && c.Window > 0 {
					a.Ctx = int(c.Used * 100 / c.Window)
				}
				if p.Git != nil {
					a.Branch, a.Root = p.Git.Branch, p.Git.Root
					k := p.Git.Root + "@" + p.Git.Branch
					if trees[k] == nil {
						trees[k] = &featTree{Root: p.Git.Root, Branch: p.Git.Branch}
					}
					trees[k].Agents = append(trees[k].Agents, tb.Name)
				}
				f.Add, f.Del = f.Add+a.Add, f.Del+a.Del
				f.Team = append(f.Team, a)
				if len(p.Agent.PRs) > 0 || p.Git != nil {
					if prs, ok := s.cards.peek(p, s.prCard)["prs"].([]*PRStatus); ok {
						for _, pr := range prs {
							if pr.Ext {
								continue // a release it didn't link itself
							}
							if k := fmt.Sprint(pr.Repo, "#", pr.Number); !seenPR[k] {
								seenPR[k] = true
								f.PRs = append(f.PRs, pr)
							}
						}
					}
				}
			}
		}
	}
	for _, t := range trees {
		f.Trees = append(f.Trees, *t)
	}
	sort.Slice(f.Trees, func(i, j int) bool { return f.Trees[i].Root+f.Trees[i].Branch < f.Trees[j].Root+f.Trees[j].Branch })
	f.Dossier = s.featDossier(session)
	f.Trees, f.TreesAt = s.featCodeTrees(session, f.Trees, f.Dossier.Main) // from memory: no git in a request
	sort.SliceStable(f.PRs, func(i, j int) bool { return f.PRs[i].Number > f.PRs[j].Number })
	s.ai.mu.Lock()
	for _, t := range s.ai.threadViews() {
		if t.Team == session && t.Status != "archived" {
			f.Thread = &featThread{t.ID, t.Title, t.Status, t.Note, t.Branch, t.Plan, t.Live}
			break
		}
	}
	s.ai.mu.Unlock()
	if b, err := os.ReadFile(filepath.Join(featDir(s.cfg.home, session), "report.json")); err == nil {
		var r featReport
		if json.Unmarshal(b, &r) == nil {
			f.Report = &r
		}
	}
	featBusy.Lock()
	f.Busy, f.Err = featBusy.m[session], featBusy.err[session]
	featBusy.Unlock()
	// a record (or its managing thread) is the feature, whether or not the team has been sampled yet
	return f, found || f.Thread != nil || featHasDocs(f.Dossier.Dir)
}

// termFeature handles GET /api/term/feature?session=: the feature's facts plus the page data (featPage).
func (s *server) termFeature(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	if !sessionNameRe.MatchString(session) {
		writeJSON(w, 400, map[string]any{"error": "bad session"})
		return
	}
	s.featReopen(session) // the owner's page never shows a done its record no longer supports
	f, ok := s.featFacts(session)
	if !ok {
		writeJSON(w, 404, map[string]any{"error": "no such session"})
		return
	}
	f.Page = s.featPage(session, f)
	writeJSON(w, 200, f)
}

// termFeatureReport handles POST /api/term/feature/report {session, explicit:true}: write (or rewrite) the report, in
// the background.
func (s *server) termFeatureReport(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Session  string
		Explicit bool // the owner pressed the button: a report is a paid claude -p run, never a side effect of viewing
	}
	if err := readJSON(r, &b); err != nil || !sessionNameRe.MatchString(b.Session) {
		writeJSON(w, 400, map[string]any{"error": "bad request"})
		return
	}
	if !b.Explicit {
		writeJSON(w, 400, map[string]any{"error": "a report runs a paid model call: it needs an explicit owner action (explicit:true)"})
		return
	}
	f, ok := s.featFacts(b.Session)
	if !ok {
		writeJSON(w, 404, map[string]any{"error": "no such session"})
		return
	}
	featBusy.Lock()
	if featBusy.m[b.Session] {
		featBusy.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true, "busy": true})
		return
	}
	featBusy.m[b.Session] = true
	delete(featBusy.err, b.Session)
	featBusy.Unlock()
	s.audit.add("feature-report", b.Session, clientIP(r))
	go func() {
		err := s.featWrite(f)
		featBusy.Lock()
		delete(featBusy.m, b.Session)
		if err != nil {
			featBusy.err[b.Session] = err.Error()
		}
		featBusy.Unlock()
		s.wakeUp()
	}()
	writeJSON(w, 200, map[string]any{"ok": true, "busy": true})
}

// featPrompt is the report writer's instruction; the gathered facts are appended after "FACTS".
const featPrompt = `You are the project manager of a software feature built by a team of AI coding agents (Claude Code and Codex
sessions) in one zellij session. From the facts below, write the feature's status report for its owner, as a PM and
business analyst would keep it in a ticket: the requirements, the plan and who owns each step, the risks, where it
stands, the testing, what's left, how its PRs were merged. Work only from the facts; when something isn't known, say
so briefly (e.g. "No tests reported yet") rather than inventing it. Plain, short sentences; name concrete things.

The stages are THIS feature's own, not a template: decide the 4 to 8 stages its work actually goes through (a migration
might be inventory, stage infrastructure, data move, cutover rehearsal, production cutover, decommission; a UI feature
might be design, build, review, rollout), in order, exactly one "now" unless it is all done. When the feature's PLAN.md
has milestones, the stages are those milestones. The milestones are detailed: each a goal you can check and the concrete
steps to get there, every step owned by one tab.

Reply with JSON only, this shape:
{"summary": "3-5 sentences: what the feature is and where it stands today",
 "stages": [{"name": "2-4 words", "state": "done|now|todo", "detail": "one line: what this stage is and where it got to"}],
 "progress": 0-100 (how far from done, your estimate),
 "objective": "the measurable outcome it aims at",
 "requirements": ["..."],
 "milestones": [{"title": "M1 ...", "goal": "what is true when it's done", "state": "done|now|todo",
   "steps": [{"step": "a concrete, checkable step", "state": "done|doing|todo", "owner": "the tab name that owns it"}]}],
 "risks": ["..."],
 "testing": {"status": "one or two sentences", "cases": [{"name": "...", "state": "pass|fail|todo"}]},
 "remaining": ["what is left before release"],
 "merges": "how its PRs were merged and where they went (stage, production), from the PR facts",
 "roles": [{"tab": "...", "role": "planner|analyst|implementer|tester|reviewer|ops|lead", "owns": "what it is responsible for"}],
 "timeline": [{"when": "date or relative", "what": "..."}]}

FACTS
`

// featWrite asks a model (claude -p, with no tools or project settings) to write the report from the facts, and saves
// it as report.json in the feature's folder.
func (s *server) featWrite(f featFacts) error {
	var b strings.Builder
	b.WriteString(featPrompt)
	fmt.Fprintf(&b, "Session: %s\nLines changed by the team's edit tools: +%d -%d\n", f.Session, f.Add, f.Del)
	if d := f.Dossier; strings.TrimSpace(d.Feature+d.Plan+d.Tests) != "" {
		b.WriteString("\nTHE FEATURE'S SOURCE OF TRUTH (the owner's FEATURE.md, the team's PLAN.md with milestones and owners, TESTS.md). " +
			"Treat it as what must be true: report against it, and keep the plan's milestones and steps in your plan.\n")
		fmt.Fprintf(&b, "--- FEATURE.md\n%s\n--- PLAN.md\n%s\n--- TESTS.md\n%s\n--- LOG.md (newest last)\n%s\n", trunc(d.Feature, 12000), trunc(d.Plan, 12000), trunc(d.Tests, 6000), trunc(d.Log[max(0, len(d.Log)-6000):], 6000))
	}
	if t := f.Thread; t != nil {
		fmt.Fprintf(&b, "\nJARVIS thread running it: %q, status %s. Its note: %s\n", t.Title, t.Status, t.Note)
		for _, st := range t.Plan {
			fmt.Fprintf(&b, "  plan [%s] %s\n", st.State, st.Text)
		}
	} else {
		b.WriteString("\nNo JARVIS thread runs it: the owner started it by hand.\n")
	}
	b.WriteString("\nTEAM (each agent: its tab, tool, state, branch, and the start of its latest reply)\n")
	for _, a := range f.Team {
		fmt.Fprintf(&b, "- tab %q (%s, %s, branch %s, +%d -%d lines, context %d%%, title %q, last reply %s):\n  %s\n",
			a.Tab, a.Kind, a.State, cmp.Or(a.Branch, "?"), a.Add, a.Del, a.Ctx, a.Title, time.Unix(a.LastAt, 0).UTC().Format(time.RFC3339), trunc(a.Last, 1500))
		if set := todos.of(a.path); len(set.Todos) > 0 {
			for _, t := range set.Todos {
				fmt.Fprintf(&b, "  todo [%s] %s\n", t.Status, t.Content)
			}
		}
	}
	b.WriteString("\nWORKTREES and their recent commits\n")
	for _, t := range f.Trees {
		fmt.Fprintf(&b, "- %s on %s (tabs: %s)%s\n", t.Root, t.Branch, strings.Join(t.Agents, ", "), map[bool]string{true: " MAIN WORKTREE"}[t.Main]+map[bool]string{true: fmt.Sprintf(" %d commits not merged into main yet", t.Ahead)}[t.Ahead > 0])
		if out, err := exec.Command("git", "-C", t.Root, "log", "--format=%h %ad %s", "--date=short", "-15").Output(); err == nil {
			for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				fmt.Fprintf(&b, "    %s\n", l)
			}
		}
	}
	b.WriteString("\nPULL REQUESTS\n")
	for _, p := range f.PRs {
		checks := map[string]int{}
		for _, c := range p.Checks {
			checks[c.State]++
		}
		fmt.Fprintf(&b, "- %s#%d %q %s %s→%s review %s checks %v", p.Repo, p.Number, p.Title, p.State, p.Head, p.Base, cmp.Or(p.Review, "-"), checks)
		if !p.MergedAt.IsZero() {
			fmt.Fprintf(&b, " merged %s", p.MergedAt.UTC().Format("2006-01-02 15:04"))
		}
		if p.Release > 0 {
			fmt.Fprintf(&b, " released by #%d", p.Release)
		}
		for _, d := range p.Deploys {
			fmt.Fprintf(&b, " deploy %v", d)
		}
		b.WriteString("\n")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dir := filepath.Join(os.TempDir(), "cp-features")
	_ = os.MkdirAll(dir, 0o700)
	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", "sonnet", "--tools", "", "--strict-mcp-config",
		"--setting-sources", "", "--no-session-persistence", "--output-format", "text")
	cmd.Dir = dir // no project here: no CLAUDE.md, no hooks
	cmd.Stdin = strings.NewReader(b.String())
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("claude -p: %w", err)
	}
	o := string(out)
	i, j := strings.Index(o, "{"), strings.LastIndex(o, "}")
	if i < 0 || j < i {
		return fmt.Errorf("the report wasn't JSON: %q", trunc(o, 200))
	}
	var rep featReport
	if err := json.Unmarshal([]byte(o[i:j+1]), &rep); err != nil {
		return fmt.Errorf("the report wasn't JSON: %w", err)
	}
	rep.At = time.Now().Unix()
	js, _ := json.MarshalIndent(rep, "", "  ")
	d := featDir(s.cfg.home, f.Session)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(d, "report.json"), js, 0o600)
}
