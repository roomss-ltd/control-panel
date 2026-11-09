package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// A feature's source of truth: the folder ~/jarvis-files/features/<session>/ that every agent in the session reads
// and keeps. FEATURE.md is the owner's: what must be true (the objective, requirements, definition of done, which
// worktree is the main one). PLAN.md is the milestones (## headings) and their steps ("- [ ] … @Tab": the tab that
// owns it), ticked by the agents. TESTS.md the test cases ([x] pass, [!] fail, [-] skipped, [ ] to run). LOG.md the
// team's dated updates and push-back. The panel shows them on the feature page, the owner edits them there, the
// report writer reads them as the truth, and "Brief the team" (and every compaction the panel does) points the
// agents at them, so a fresh context starts from the folder rather than from memory.
//
// This file parses those documents (milestones, steps, dependencies, tests), assembles them into a featDossier,
// and implements the owner's edit, brief and plan-adapt operations on them. In code the folder's contents are called
// the "dossier" or "record"; the rules that decide when a ticked step counts as done are in featverify.go.

// featDocs maps a document's short name to its file name in the feature folder.
var featDocs = map[string]string{"feature": "FEATURE.md", "draft": "FEATURE.draft.md", "plan": "PLAN.md", "tests": "TESTS.md", "log": "LOG.md"}

// featDossier is everything the panel knows of a feature's record folder at one moment: the documents' text, their
// parsed milestones and test counts, the owning thread, and the state of the thread's "done" claim.
type featDossier struct {
	Dir       string   `json:"dir"`
	Feature   string   `json:"feature"`
	Draft     string   `json:"draft"`               // FEATURE.draft.md: requirements an agent proposes, until the owner adopts them
	Outcomes  []string `json:"outcomes,omitempty"`  // FEATURE.md's desired outcomes
	Accepted  bool     `json:"accepted,omitempty"`  // the owner accepted exactly these, with this plan's results
	DoneStale string   `json:"doneStale,omitempty"` // its thread says done, but the record has since changed: why it no longer holds
	// DoneHistorical: its thread was marked done before this panel checked evidence (no gate passed): why it wouldn't now
	DoneHistorical string            `json:"doneHistorical,omitempty"`
	Plan           string            `json:"plan"`
	Tests          string            `json:"tests"`
	Log            string            `json:"log"`
	Main           string            `json:"main,omitempty"` // the main worktree (FEATURE.md's "Main worktree:" line)
	Milestones     []featMilestone   `json:"milestones"`
	TestN          map[string]int    `json:"testN"`               // pass, fail, skip, todo
	Thread         string            `json:"thread,omitempty"`    // the thread that owns it: the feature's one identity
	Paths          map[string]string `json:"paths"`               // each file's one place, read and written there
	Conflicts      []string          `json:"conflicts,omitempty"` // a file in both places with different words: shown, never shadowed
	Rev            string            `json:"rev"`                 // PLAN.md's revision: an edit made against another is refused
	Revs           map[string]string `json:"revs"`                // every file's revision by doc (feature, plan, tests, log): a write names the one it read
	LogWhole       bool              `json:"logWhole,omitempty"`  // log is the whole LOG.md (the agent route), not its newest part
}

// featMilestone is a "## heading" of PLAN.md and the steps under it.
type featMilestone struct {
	Title string         `json:"title"`
	Steps []featPlanStep `json:"steps"`
	// Artifacts: "- artifact:" lines under the heading before its first step (the milestone's own), as written
	Artifacts []string `json:"artifacts,omitempty"`
	Done      int      `json:"done"`
}

// featPlanStep is a step of PLAN.md and what backs it. Lines indented under it carry the rest ("  - evidence:
// <file>", "result:", "kind: read|build|test|mutation", "due: 2026-11-08 09:00", "ask:", "answer:", "receipt:",
// "reconciled:"). A ticked box is a claim: it counts as done only once its evidence is a file the panel finds,
// written after the step's words last changed (featVerify); until then it's "claimed".
type featPlanStep struct {
	Text     string `json:"text"`
	State    string `json:"state"` // done (verified), claimed (ticked, not verified), doing, asking (waits on the owner), todo
	Owner    string `json:"owner,omitempty"`
	Key      string `json:"key,omitempty"`      // its milestone and words, hashed: its criterion revision
	Evidence string `json:"evidence,omitempty"` // the last evidence line (the field older clients read)
	// Evidences: every evidence line of the step, in order (several typed pieces of proof: none is lost)
	Evidences []string `json:"evidences,omitempty"`
	Result    string   `json:"result,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Due       string   `json:"due,omitempty"`
	Ask       string   `json:"ask,omitempty"`
	Answer    string   `json:"answer,omitempty"`
	Receipt   string   `json:"receipt,omitempty"`
	Check     string   `json:"check,omitempty"` // the command that checks it (proposed by anyone, approved only by the owner)
	Cwd       string   `json:"cwd,omitempty"`   // the folder it runs in
	RunAs     string   `json:"runAs,omitempty"` // exactly what an agent runs for a receipt (featverify.go)
	Approval  struct {
		Check bool `json:"check"` // the owner approved this check, folder and wording
		Kind  bool `json:"kind"`  // the owner approved its kind as safe to redo
	} `json:"approval"`
	Attempt string `json:"attempt,omitempty"` // the approval's token, in runAs and in "evidence: run <attempt>"
	Label   string `json:"label,omitempty"`   // claimed, check ran (receipt), owner verified
	Qid     string `json:"qid,omitempty"`     // its question's id (the feature, the step and the question's words)
	Proof   string `json:"proof,omitempty"`   // what made it done: a receipt, or the owner
	Why     string `json:"why,omitempty"`     // why a ticked box isn't done
	Missed  bool   `json:"missed,omitempty"`  // its due time passed before it was done
	Blocked string `json:"blocked,omitempty"` // why its tab is held (featverify.go)
	// After lists the steps it waits for, from an explicit "after: <key>, <key>" line. AfterInvalid holds refs that
	// can't be honoured (an unknown step, itself, a cycle), kept as written with why: such a step isn't ready
	// (featDepBlocks gates on it).
	After        []string       `json:"after,omitempty"`
	AfterInvalid []featDepIssue `json:"afterInvalid,omitempty"`
	afterRaw     []string
	// Covers lists the objectives (FEATURE.md D-ids) it serves, from "covers: D1, D2". PlanProof is the proof the plan
	// names for it (the planner's choice; the owner's is in FEATURE.md). Assessment is a judgement written as
	// "pass|fail|insufficient — why — cites …".
	Covers    []string `json:"covers,omitempty"`
	PlanProof string   `json:"planProof,omitempty"`
	// Artifacts holds its "- artifact: <path> | ui|issue | <title>" lines as written, in order (shown as the page's
	// artifacts, see featv8arts.go).
	Artifacts  []string `json:"artifacts,omitempty"`
	Assessment string   `json:"assessment,omitempty"`
}

// featDepIssue is an "after:" reference that can't be honoured, with the reason.
type featDepIssue struct {
	Ref string `json:"ref"`
	Why string `json:"why"` // unknown step, self, cycle
}

// safeKind lists the step kinds whose work can simply run again after an interruption; anything else (a mutation, or
// no kind given) can't.
var safeKind = map[string]bool{"read": true, "build": true, "test": true, "check": true}

// Regular expressions for the PLAN.md / TESTS.md line formats: a checklist step, an indented "key: value" line under
// a step, a milestone's own "- artifact:" line, a trailing "@Tab" owner, and FEATURE.md's "Main worktree:" line.
var (
	featStepRe  = regexp.MustCompile(`^\s*(?:[-*]|\d+[.)])\s*\[([ xX~>!?-])\]\s*(.+?)\s*$`)
	featSubRe   = regexp.MustCompile(`^\s+(?:[-*]\s*)?(evidence|result|kind|due|ask|answer|receipt|check|cwd|after|covers|assessment|proof|artifact)\s*:\s*(.+?)\s*$`)
	featMsArtRe = regexp.MustCompile(`^\s*[-*]\s*artifact\s*:\s*(.+?)\s*$`)
	featOwnerRe = regexp.MustCompile(`\s+@(.+)$`)
	featMainRe  = regexp.MustCompile(`(?im)^\s*[-*]?\s*\**main worktree\**\s*:\s*` + "`?" + `([^` + "`" + `\n]+?)` + "`?" + `\s*$`)
)

// parseMilestones parses PLAN.md: "## Title" starts a milestone, checklist lines are its steps, and steps before any
// heading go in a milestone called "Plan". Indented "key: value" lines attach to the step above them. Each step's
// key is a hash of its milestone and words, and its "after:" references are resolved.
func parseMilestones(md string) []featMilestone {
	out := []featMilestone{}
	var last *featPlanStep
	for _, l := range strings.Split(md, "\n") {
		if m := featSubRe.FindStringSubmatch(l); m != nil && last != nil {
			v := trunc(m[2], 300)
			switch m[1] {
			case "evidence":
				last.Evidence = v
				last.Evidences = append(last.Evidences, v)
			case "result":
				last.Result = v
			case "kind":
				last.Kind = strings.ToLower(v)
			case "due":
				last.Due = v
			case "ask":
				last.Ask = v
			case "answer":
				last.Answer = v
			case "receipt":
				last.Receipt = v
			case "check":
				last.Check = v
			case "cwd":
				last.Cwd = v
			case "covers":
				for _, r := range strings.Split(v, ",") {
					if r = strings.TrimSpace(r); r != "" {
						last.Covers = append(last.Covers, r)
					}
				}
			case "assessment":
				last.Assessment = v
			case "proof":
				last.PlanProof = v
			case "artifact": // "<path> | ui|issue | <title>", raw (never cut): page.arts (featv8arts.go)
				last.Artifacts = append(last.Artifacts, m[2])
			case "after":
				for _, r := range strings.Split(v, ",") {
					if r = strings.TrimSpace(r); r != "" {
						last.afterRaw = append(last.afterRaw, r)
					}
				}
			}
			continue
		}
		if a := featMsArtRe.FindStringSubmatch(l); a != nil && last == nil && len(out) > 0 { // the milestone's own
			out[len(out)-1].Artifacts = append(out[len(out)-1].Artifacts, a[1])
			continue
		}
		if t, ok := strings.CutPrefix(strings.TrimSpace(l), "## "); ok {
			last = nil
			out = append(out, featMilestone{Title: strings.TrimSpace(t), Steps: []featPlanStep{}})
			continue
		}
		m := featStepRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if len(out) == 0 {
			out = append(out, featMilestone{Title: "Plan", Steps: []featPlanStep{}})
		}
		st := map[string]string{"x": "done", "X": "done", "~": "doing", ">": "doing", "?": "asking"}[m[1]]
		if st == "" {
			st = "todo"
		}
		text, owner := m[2], ""
		if o := featOwnerRe.FindStringSubmatch(text); o != nil {
			owner, text = strings.TrimSpace(o[1]), strings.TrimSpace(text[:len(text)-len(o[0])])
		}
		ms := &out[len(out)-1]
		ms.Steps = append(ms.Steps, featPlanStep{Text: trunc(text, 300), State: st, Owner: owner, Key: featStepKey(ms.Title, text)})
		last = &ms.Steps[len(ms.Steps)-1]
		if st == "done" {
			ms.Done++
		}
	}
	featResolveAfter(out)
	return out
}

// featDepBlocks returns, by step key, why a step can't start yet: an invalid dependency, or one not verified done.
// Every dispatch (assign), the supervisor's reconcile wake and the done gate use it, so it is enforced and not only
// shown.
func featDepBlocks(ms []featMilestone) map[string]string {
	state := map[string]string{}
	for _, m := range ms {
		for _, st := range m.Steps {
			state[st.Key] = st.State
		}
	}
	out := map[string]string{}
	for _, m := range ms {
		for _, st := range m.Steps {
			if len(st.AfterInvalid) > 0 {
				out[st.Key] = fmt.Sprintf("invalid dependency: after %q (%s)", st.AfterInvalid[0].Ref, st.AfterInvalid[0].Why)
				continue
			}
			for _, r := range st.After {
				if state[r] != "done" {
					out[st.Key] = "waits for " + r + " (" + state[r] + ")"
					break
				}
			}
		}
	}
	return out
}

// featResolveAfter turns each step's raw "after:" references into After (valid) or AfterInvalid (with the reason:
// unknown step, itself or a cycle). Nothing is inferred.
func featResolveAfter(ms []featMilestone) {
	edges := map[string][]string{} // candidate edges: key → refs that name another existing step
	for _, m := range ms {
		for _, st := range m.Steps {
			edges[st.Key] = nil
		}
	}
	for _, m := range ms {
		for _, st := range m.Steps {
			for _, r := range st.afterRaw {
				if _, ok := edges[r]; ok && r != st.Key {
					edges[st.Key] = append(edges[st.Key], r)
				}
			}
		}
	}
	reaches := func(from, to string) bool { // a depth-first search per reference is fine: plans are tens of steps
		seen := map[string]bool{}
		var walk func(string) bool
		walk = func(k string) bool {
			if k == to {
				return true
			}
			if seen[k] {
				return false
			}
			seen[k] = true
			return slices.ContainsFunc(edges[k], walk)
		}
		return walk(from)
	}
	for i := range ms {
		for j := range ms[i].Steps {
			st := &ms[i].Steps[j]
			for _, r := range st.afterRaw {
				_, ok := edges[r]
				switch {
				case !ok:
					st.AfterInvalid = append(st.AfterInvalid, featDepIssue{trunc(r, 80), "unknown step"})
				case r == st.Key:
					st.AfterInvalid = append(st.AfterInvalid, featDepIssue{r, "self"})
				case reaches(r, st.Key):
					st.AfterInvalid = append(st.AfterInvalid, featDepIssue{r, "cycle"})
				default:
					st.After = append(st.After, r)
				}
			}
			st.afterRaw = nil
		}
	}
}

// featStepKey identifies a step by a hash of its milestone and words, so editing a step's words gives it a new key
// (a new criterion revision).
func featStepKey(milestone, text string) string {
	h := sha256.Sum256([]byte(milestone + "\x00" + strings.TrimSpace(text)))
	return hex.EncodeToString(h[:6])
}

// featDue parses a step's "due:" value (several date and time layouts, optionally ending in " UTC").
func featDue(s string) (time.Time, bool) {
	for _, f := range []string{"2006-01-02 15:04", "2006-01-02T15:04", time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(f, strings.TrimSuffix(strings.TrimSpace(s), " UTC")); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseTests counts TESTS.md's cases by state: [x] pass, [!] fail, [-] skip, anything else (to run) todo.
func parseTests(md string) map[string]int {
	n := map[string]int{"pass": 0, "fail": 0, "skip": 0, "todo": 0}
	for _, l := range strings.Split(md, "\n") {
		if m := featStepRe.FindStringSubmatch(l); m != nil {
			n[map[string]string{"x": "pass", "X": "pass", "!": "fail", "-": "skip"}[m[1]]+map[bool]string{true: "todo"}[!strings.Contains("xX!-", m[1])]]++
		}
	}
	return n
}

// featOwner returns a copy of the thread whose team works in session, if one does.
func (s *server) featOwner(session string) *thread {
	s.ai.mu.Lock()
	defer s.ai.mu.Unlock()
	for _, t := range s.ai.threads {
		if t.Team == session && t.Status != "archived" {
			c := *t
			return &c
		}
	}
	return nil
}

// featOutcomeRe matches a checklist line in FEATURE.md.
var featOutcomeRe = regexp.MustCompile(`(?m)^\s*[-*] \[[ xX~?]\]\s+(.+?)\s*$`)

// featOutcomes returns the desired outcomes FEATURE.md lists, as checklist lines. The owner accepts them as a set.
func featOutcomes(feature string) []string {
	var out []string
	for _, m := range featOutcomeRe.FindAllStringSubmatch(feature, -1) {
		out = append(out, strings.Join(strings.Fields(m[1]), " "))
	}
	return out
}

// featRev is a file's revision: a short hash of its content. A write names the revision it read and is refused if
// the file has changed since.
func featRev(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// featPlanMu is the record's one transaction boundary: every write of a record file through the panel (owner page,
// agent API, a thread's plan), every owner action that judges the record (approve, verify, accept, release, answer)
// and a thread's done check with its commit hold it, so nothing lands between a judgement and what it commits.
// Writes outside the panel (an editor, a raw shell redirect) are caught only by the revision.
var featPlanMu sync.Mutex

// featWriteRev replaces a record file, atomically, if it is still at revision rev (what the writer read).
func featWriteRev(path, rev string, text []byte) error {
	featPlanMu.Lock()
	defer featPlanMu.Unlock()
	return featWriteRevLocked(path, rev, text)
}

// featWriteRevLocked is featWriteRev for a caller that already holds featPlanMu.
func featWriteRevLocked(path, rev string, text []byte) error {
	if rev == "" {
		return errNoRev
	}
	cur, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if rev != "" && featRev(cur) != rev {
		return fmt.Errorf("%w: %s changed since it was read (now at %s); read it again and redo the change", errStale, tilde(path), featRev(cur))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(path, text, 0o600); err != nil {
		return err
	}
	if featWriteHook != nil {
		featWriteHook(path)
	}
	return nil
}

// errNotTyped means featTell failed before typing anything, so trying again is safe.
var errNotTyped = errors.New("nothing was typed")

// featWriteHook, when set (tests only), is called after a record file was written.
var featWriteHook func(path string)

// Errors for record writes: the file changed since it was read, or the writer did not name the revision it read.
var (
	errStale = errors.New("stale")
	errNoRev = errors.New("the revision you read is required (rev): read the record first")
)

// featAdapt applies a lead's flat checklist (thread_update plan=, the format LEAD.md teaches) to the record's
// PLAN.md: each line's box onto the step with the same words, keeping headings, owners, evidence, questions and
// every step it doesn't mention. A line no step has can't be placed without guessing, so it is refused with the
// file to edit. With no PLAN.md yet, the checklist becomes it.
// It only computes the new text (cur is PLAN.md as read, nil when there is none); the caller writes it (threads.go
// updateThread, in one transaction with the thread's record).
func featAdapt(path string, cur []byte, flat string) (string, error) {
	if cur == nil {
		return strings.TrimSpace(flat) + "\n", nil
	}
	if len(parseMilestones(string(cur))) == 0 { // prose only (the goal, what done means): the steps go under it
		return strings.TrimRight(string(cur), "\n") + "\n\n" + strings.TrimSpace(flat) + "\n", nil
	}
	lines := strings.Split(string(cur), "\n")
	at := map[string][]int{}
	for i, l := range lines {
		if m := featStepRe.FindStringSubmatch(l); m != nil {
			txt := m[2]
			if o := featOwnerRe.FindStringSubmatch(txt); o != nil {
				txt = strings.TrimSpace(txt[:len(txt)-len(o[0])])
			}
			at[txt] = append(at[txt], i)
		}
	}
	var unknown []string
	for _, st := range parsePlan(flat) {
		txt := st.Text
		if o := featOwnerRe.FindStringSubmatch(txt); o != nil {
			txt = strings.TrimSpace(txt[:len(txt)-len(o[0])])
		}
		is := at[txt]
		if len(is) != 1 {
			unknown = append(unknown, strconv.Quote(trunc(txt, 60))+map[bool]string{true: " (no such step)", false: " (several steps say this)"}[len(is) == 0])
			continue
		}
		box := map[string]string{"done": "x", "doing": "~", "todo": " "}[st.State]
		l := lines[is[0]]
		j := strings.Index(l, "[")
		if l[j+1] == '?' && box != "x" {
			continue // a step waiting on the owner stays so until they answer (or it's done)
		}
		lines[is[0]] = l[:j+1] + box + l[j+2:]
	}
	if len(unknown) > 0 {
		return "", fmt.Errorf("%w: %s holds the plan; these lines match none of its steps exactly: %s. Edit that file to add or reword steps "+
			"(keep each step's indented evidence/result lines), then send only state changes here", errPlanConflict, tilde(path), strings.Join(unknown, ", "))
	}
	return strings.Join(lines, "\n"), nil
}

// errPlanConflict means a plan update names steps the PLAN.md doesn't have.
var errPlanConflict = errors.New("plan conflict")

// featDossier reads a feature's record folder and assembles its dossier: the documents with their revisions,
// the parsed and verified milestones, the owning thread, and whether a "done" thread still holds. It also finishes
// an interrupted requirements promote it finds.
func (s *server) featDossier(session string) featDossier {
	t := s.featOwner(session)
	id := ""
	if t != nil {
		id = t.ID
	}
	dir, conflict := s.featHome(session, id)
	d := featDossier{Dir: dir, Thread: id, Paths: map[string]string{}, Revs: map[string]string{}}
	if conflict != "" {
		d.Conflicts = []string{conflict}
	}
	if _, err := os.Stat(filepath.Join(dir, promoteTxnFile)); err == nil && featPlanMu.TryLock() { // a promote cut short; never one in progress
		s.recoverPromoteTxn(dir)
		featPlanMu.Unlock()
	}
	if _, err := os.Stat(filepath.Join(dir, promoteTxnFile)); err == nil { // in progress, or not settled: nothing here is current yet
		d.Conflicts = append(d.Conflicts, "a requirements promote is in progress or was cut short and isn't settled: the requirements and verification shown may not be current; reload")
	}
	read := func(name string) string {
		p := filepath.Join(dir, name)
		d.Paths[name] = p
		b, _ := os.ReadFile(p)
		d.Revs[strings.TrimPrefix(strings.ToLower(strings.TrimSuffix(name, ".md")), "feature.")] = featRev(b) // FEATURE.draft.md: draft
		return string(b)
	}
	d.Feature, d.Draft, d.Plan, d.Tests, d.Log = read("FEATURE.md"), read(featDocs["draft"]), read("PLAN.md"), read("TESTS.md"), read("LOG.md")
	d.Rev = d.Revs["plan"]
	if len(d.Log) > 20000 { // the newest part
		d.Log = d.Log[len(d.Log)-20000:]
	}
	if m := featMainRe.FindStringSubmatch(d.Feature); m != nil {
		d.Main = strings.TrimSpace(m[1])
		if strings.HasPrefix(d.Main, "~/") {
			d.Main = filepath.Join(s.cfg.home, d.Main[2:])
		}
	}
	d.Outcomes = featOutcomes(d.Feature)
	d.Accepted = len(d.Outcomes) > 0 && s.sup.accepted(session) == featAcceptRev(d.Outcomes, d.Rev)
	d.Milestones = parseMilestones(d.Plan)
	if strings.TrimSpace(d.Plan) == "" && t != nil && len(t.Plan) > 0 { // the plan its lead published (threads.json), until a PLAN.md exists
		var b strings.Builder
		for _, st := range t.Plan {
			fmt.Fprintf(&b, "- [%s] %s\n", cmp.Or(map[string]string{"done": "x", "doing": "~"}[st.State], " "), st.Text)
		}
		d.Milestones = parseMilestones(b.String())
	}
	s.featVerify(session, d.Milestones)
	d.TestN = parseTests(d.Tests)
	if t != nil && t.Status == "done" { // done was committed on an earlier record: is it still true of this one?
		if why := s.featGate(session, id, nil, true); why != "" {
			s.sup.mu.Lock()
			gated := s.sup.Completed[id] != 0
			s.sup.mu.Unlock()
			if gated {
				d.DoneStale = why // it reopens (featReopen)
			} else {
				d.DoneHistorical = why // marked done before this panel checked evidence: shown as such, never reopened
			}
		}
	}
	return d
}

// featMerge marks which of the feature's worktrees is the main one (main, or else the one most agents work in) and
// fills in how many commits each other worktree in the same repo is ahead of it (Ahead, Checked), or that it belongs
// to another repo (OtherRepo).
func featMerge(trees []featTree, main string) []featTree {
	if main == "" { // the one most of the team works in
		best := 0
		for _, t := range trees {
			if len(t.Agents) > best {
				best, main = len(t.Agents), t.Root
			}
		}
	}
	git := func(dir string, args ...string) string {
		out, _ := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		return strings.TrimSpace(string(out))
	}
	var mainBranch, mainRepo string
	for _, t := range trees {
		if t.Root == main {
			mainBranch, mainRepo = t.Branch, git(t.Root, "rev-parse", "--path-format=absolute", "--git-common-dir")
		}
	}
	if mainBranch == "" && main != "" {
		mainBranch, mainRepo = git(main, "branch", "--show-current"), git(main, "rev-parse", "--path-format=absolute", "--git-common-dir")
	}
	for i := range trees {
		t := &trees[i]
		t.Main = t.Root == main
		if t.Main || mainBranch == "" || t.Branch == "" {
			continue
		}
		if git(t.Root, "rev-parse", "--path-format=absolute", "--git-common-dir") != mainRepo {
			t.OtherRepo = true
			continue
		}
		n, err := strconv.Atoi(git(t.Root, "rev-list", "--count", mainBranch+"..HEAD"))
		if err == nil {
			t.Ahead = n
			t.Checked = true
		}
	}
	return trees
}

// termFeatureDoc handles POST /api/term/feature/doc {session, doc: feature|plan|tests, text, rev}: the owner's edit
// of the feature's source of truth. The log is not editable, and rev must be the revision the editor read.
func (s *server) termFeatureDoc(w http.ResponseWriter, r *http.Request) {
	var b struct{ Session, Doc, Text, Rev string }
	if err := readJSONN(r, &b, 1<<18); err != nil || !sessionNameRe.MatchString(b.Session) || featDocs[b.Doc] == "" || b.Doc == "log" || len(b.Text) > 200000 {
		writeJSON(w, 400, map[string]any{"error": "bad request"})
		return
	}
	s.audit.add("feature-doc", b.Session+" "+featDocs[b.Doc], clientIP(r))
	s.featWriteDoc(w, b.Session, b.Doc, b.Text, b.Rev, "owner", "", nil)
}

// featPath returns the path of a record file (name, such as "LOG.md") in the feature's one folder (featHome).
func (s *server) featPath(session, name string) string {
	id := ""
	if t := s.featOwner(session); t != nil {
		id = t.ID
	}
	dir, _ := s.featHome(session, id)
	return filepath.Join(dir, name)
}

// featLog appends a dated line to the feature's LOG.md.
func (s *server) featLog(session, line string) error {
	p := s.featPath(session, "LOG.md")
	os.MkdirAll(filepath.Dir(p), 0o700)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "- %s · %s\n", time.Now().UTC().Format("2006-01-02 15:04"), line)
	return cmp.Or(err, f.Close())
}

// featBrief composes the message that points one agent (the one in tab) at the feature folder dir: what the files are,
// how to tick off a step with evidence, how to push back or ask the owner, and which worktree to merge into (main).
func featBrief(dir, session, tab, main string) string {
	m := ""
	if main != "" {
		m = " Work in your own worktree; it merges into the main worktree (" + tilde(main) + ") before anything goes to stage."
	}
	return panelMark + fmt.Sprintf("You are on the feature %q. Its source of truth is %s/: FEATURE.md (what must be true: the owner's), "+
		"PLAN.md (the milestones; the steps tagged @%s are yours) and TESTS.md. Read them now and work to them. If something in them is "+
		"wrong or can't be done, push back: add a dated line with your tab name to LOG.md and tell the owner, rather than working around it. "+
		"When you finish a step, tick it in PLAN.md with an indented \"- evidence: <kind> <ref>\" (kind run|file|image|doc|db|link|ui: "+
		"proof that the step's objective was met, not a note that you did it) and \"- result: <what it showed>\" "+
		"under it (a tick without evidence the panel can find doesn't count), and add a line to LOG.md. Mark a step that waits on the owner "+
		"[?] with \"- ask: <the question>\"; their answer comes back for that step only. Skills fit the work (agent-skills): "+
		"spec-driven-development for requirements, planning-and-task-breakdown for the plan, incremental-implementation and "+
		"test-driven-development to build, code-review-and-quality/security-and-hardening to review another's work, "+
		"debugging-and-error-recovery when stuck; using a skill is not proof. /ship gives a go/no-go report only: it never deploys, "+
		"pushes, merges or rolls back without the owner's approval.%s", session, dir, tab, m)
}

// featTell types text into an idle agent's pane, never over what's in its input box (errBoxTaken then).
func (s *server) featTell(session string, id int, kind, text string) error {
	if kind == "codex" {
		if err := s.z.paste(session, id, text); err != nil {
			return fmt.Errorf("%w: %v", errNotTyped, err)
		}
		return s.z.keys(session, id, "Enter")
	}
	screen, err := s.z.dumpANSI(session, id)
	if err != nil {
		return fmt.Errorf("%w: %v", errNotTyped, err)
	}
	if typedBox(screen) != "" {
		return errBoxTaken
	}
	_, err = s.sendToClaude(session, id, text)
	return err
}

// termFeatureBrief handles POST /api/term/feature/brief {session}: point every agent in the session at the folder.
// An agent whose turn is running, that is asking the owner something, or whose input box has text in it is skipped
// (and named in the answer).
func (s *server) termFeatureBrief(w http.ResponseWriter, r *http.Request) {
	var b struct{ Session string }
	if err := readJSON(r, &b); err != nil || !sessionNameRe.MatchString(b.Session) {
		writeJSON(w, 400, map[string]any{"error": "bad request"})
		return
	}
	f, ok := s.featFacts(b.Session)
	if !ok {
		writeJSON(w, 404, map[string]any{"error": "no such session"})
		return
	}
	d := s.featDossier(b.Session)
	if strings.TrimSpace(d.Feature) == "" {
		writeJSON(w, 409, map[string]any{"error": "Write what must be true (FEATURE.md) first: there's nothing to brief them on"})
		return
	}
	main := d.Main
	for _, t := range featMerge(f.Trees, d.Main) {
		if t.Main {
			main = t.Root
		}
	}
	sent, skipped := []string{}, []string{}
	for _, a := range f.Team {
		id, _ := strconv.Atoi(a.Key[strings.LastIndexByte(a.Key, ':')+1:])
		if a.State == "working" || a.State == "stalled" || a.State == "asking" || a.State == "starting" {
			skipped = append(skipped, a.Tab+" (busy)")
			continue
		}
		if err := s.featTell(b.Session, id, a.Kind, featBrief(tilde(d.Dir), b.Session, a.Tab, main)); err != nil {
			skipped = append(skipped, a.Tab+" ("+err.Error()+")")
			continue
		}
		sent = append(sent, a.Tab)
	}
	if len(sent) > 0 {
		s.featLog(b.Session, "panel: briefed "+strings.Join(sent, ", "))
	}
	s.audit.add("feature-brief", b.Session+": "+strings.Join(sent, ", "), clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true, "sent": sent, "skipped": skipped})
}

// featArtLimits returns why a plan's artifact lines can't be written, or "" if they can: at most 20 per step or
// milestone, each at most 500 characters, and each '<path> | ui|issue | <title>' with a title of 1 to 200 characters
// and no control characters. Such a plan is refused whole, never cut or dropped.
func featArtLimits(ms []featMilestone) string {
	check := func(where string, as []string) string {
		if len(as) > 20 {
			return fmt.Sprintf("%s has %d artifact lines; at most 20", where, len(as))
		}
		for _, a := range as {
			if n := len([]rune(a)); n > 500 {
				return fmt.Sprintf("an artifact line of %s is %d characters; at most 500", where, n)
			}
			if why := featArtLineWhy(a); why != "" {
				return "an artifact line of " + where + " " + why
			}
		}
		return ""
	}
	for _, m := range ms {
		if why := check("milestone "+strconv.Quote(trunc(m.Title, 60)), m.Artifacts); why != "" {
			return why
		}
		for _, st := range m.Steps {
			if why := check("job "+strconv.Quote(trunc(st.Text, 60)), st.Artifacts); why != "" {
				return why
			}
		}
	}
	return ""
}

// featArtLineWhy returns why one artifact line isn't '<path> | ui|issue | <title>' with a 1–200 character title and
// no control characters ("" when it is). The plan writer refuses such a line.
func featArtLineWhy(a string) string {
	parts := strings.SplitN(a, "|", 3)
	if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" {
		return "isn't '<path> | ui|issue | <title>'"
	}
	if p := strings.ToLower(strings.TrimSpace(parts[1])); p != "ui" && p != "issue" {
		return fmt.Sprintf("has purpose %q; it must be ui or issue", trunc(p, 30))
	}
	if n := len([]rune(strings.TrimSpace(parts[2]))); n == 0 || n > 200 {
		return fmt.Sprintf("has a title of %d characters; it must be 1 to 200", n)
	}
	if strings.IndexFunc(a, unicode.IsControl) >= 0 {
		return "holds a control character"
	}
	return ""
}
