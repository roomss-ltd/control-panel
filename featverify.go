package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Verification of a feature's steps, and the owner's say over it. A ticked box is an agent's claim. A step is done only
//   - by a receipt: the owner explicitly approved the step's check (its exact command and folder, for its exact wording),
//     an agent of this feature ran exactly that check with Claude Code's Bash tool, and Claude Code's own transcript
//     records the run, its output and an exit status of 0 (the command ends with an echo of $?, so the status is in the
//     recorded output, not inferred); cited as "evidence: run <tool_use_id>"; or
//   - by the owner's attestation: "Mark verified" on the feature page, for the step's current wording and the plan's
//     current revision.
// File evidence is never more than a claim. The panel executes nothing. Its limit: transcripts are files the same Unix
// user can write, so a deliberately forged transcript isn't detected; and an approved check proves only what it runs.
// Approval, attestation, answers and hold releases are the owner's: page routes behind the login, never the agent API.
//
// The file also holds the rest of the owner's record routes (accept the outcomes, adopt or discard a requirements
// draft) and the agents' cooperative writer for the record files (GET /agent/feature, POST /agent/feature/doc and
// /log, GET /agent/feature/team).

// featApproval is the owner's approval of one step's check (or of a replay-safe kind), kept in the supervisor's memory
// under "<session>/<step key>". It binds the exact wording, command and folder that were shown.
type featApproval struct {
	Check string `json:"check,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
	Kind  string `json:"kind,omitempty"` // a replay-safe kind the owner approved for this step
	// Covers: the objectives it served when approved: a receipt then verifies it for those only
	Covers string `json:"covers,omitempty"`
	At     int64  `json:"at"`
}

// featAttest is the owner's "Mark verified" on one ticked step, kept under "<session>/<step key>". It holds only for the
// plan revision and step scope it was given for (see featScope), and for the evidence bytes it saw.
type featAttest struct {
	At    int64  `json:"at"`
	Rev   string `json:"rev"`
	Scope string `json:"scope"` // the step as the owner reviewed it (featScope): any change of it voids the attestation
	// Seals: its file evidence's bytes when attested (path → sha256): changed later, the attestation is stale (kept)
	Seals map[string]string `json:"seals,omitempty"`
	Roots []string          `json:"roots,omitempty"` // the folders those seals were resolved in
}

// featScope: what a step's acceptance is about: its milestone and words (the key), owner, check, folder and kind
func featScope(st featPlanStep) string {
	parts := []string{st.Key, strings.ToLower(st.Owner), st.Check, st.Cwd, st.Kind}
	if len(st.Covers) > 0 { // what it proves, and with what: attested for these objectives with this evidence and result only
		parts = append(parts, strings.Join(st.Covers, ","), strings.Join(featEvLines(st), "\x01"), st.Result)
	}
	return featRev([]byte(strings.Join(parts, "\x00")))
}

// featHold: a tab of a feature whose agent stopped in the middle of work the panel can't call safe to redo. Every step
// it owns stays undone until the owner releases it (which authorizes no new change: its lead re-plans).
type featHold struct {
	Why   string   `json:"why"`
	Steps []string `json:"steps"`
	Keys  []string `json:"keys"` // the held steps themselves: they stay held whoever they're given to
	At    int64    `json:"at"`
}

// featResume records one attempt to reopen an exited agent's conversation, until the agent is seen again.
type featResume struct {
	At      int64  `json:"at"`
	State   string `json:"state"` // launching, confirmed, failed, unconfirmed
	Tries   int    `json:"tries"`
	Session string `json:"session"`
}

// resumeConfirm is how long a resumed conversation has to run again before the attempt is reported as unconfirmed.
const resumeConfirm = 2 * time.Minute

// featRunAs: what an agent runs for a receipt. The approved check runs as written, in bash, in a subshell that never
// starts it unless cd to its folder succeeded (status 97 then). The subshell's status is the check's own: with several
// commands that's the last one's, as the shell defines it (`false; true` succeeds): the approved command is responsible
// for its aggregate status (use && or set -e in it), nothing is rewritten. The status is printed as the one marker
// line, last, and the wrapper exits with it, so Claude Code's own error flag agrees.
func featRunAs(check, cwd, attempt string) string {
	return "( cd " + shq(cwd) + " || exit 97\n" + check + "\n); rfe_s=$?; echo \"rfe-exit=$rfe_s rfe-attempt=" + attempt + "\"; exit $rfe_s"
}

// featAttempt: the token of one approval of a step's check, for its exact wording, command and folder
func featAttempt(session, key, check, cwd string, at int64) string {
	return featRev([]byte(strings.Join([]string{session, key, check, cwd, strconv.FormatInt(at, 10)}, "\x00")))[:12]
}

// featQid is a step question's id: it changes with the feature, the step and the question's words, so an answer to a
// question that has since been reworded is refused.
func featQid(session, key, ask string) string { return featStepKey(session+"\x00"+key, ask) }

// ---- the canonical folder ----

// featHasDocs reports whether dir holds any of a feature's record files (FEATURE.md, PLAN.md, ...).
func featHasDocs(dir string) bool {
	for _, n := range featDocs {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

// featHome: the one folder of a feature's record, and a conflict when another folder holds docs too. Pinned once
// docs exist. The owner's folder (~/jarvis-files/features/<session>) wins over a manager's (threads/<id>): attaching a
// manager never shadows what the owner wrote; with both holding docs, writes and completion are refused until one goes.
func (s *server) featHome(session, thread string) (dir, conflict string) {
	own := featDir(s.cfg.home, session)
	th := ""
	if thread != "" {
		th = filepath.Join(s.cfg.home, "jarvis-files", "threads", thread)
	}
	s.sup.mu.Lock()
	pinned := s.sup.Homes[session]
	s.sup.mu.Unlock()
	ownHas, thHas := featHasDocs(own), th != "" && featHasDocs(th)
	switch {
	case pinned != "" && (pinned == own || pinned == th || featHasDocs(pinned)): // a rebind keeps the pinned record
		dir = pinned
	case ownHas:
		dir = own
	case thHas:
		dir = th
	case th != "":
		dir = th
	default:
		dir = own
	}
	for _, other := range []string{own, th} {
		if other != "" && other != dir && featHasDocs(other) && featHasDocs(dir) {
			conflict = "this feature's record is in two folders: " + tilde(dir) + " (used) and " + tilde(other) + ": move what's needed into the first and delete the second"
			break
		}
	}
	if pinned != dir && featHasDocs(dir) {
		s.sup.mu.Lock()
		s.sup.Homes[session] = dir
		s.sup.save()
		s.sup.mu.Unlock()
	}
	return dir, conflict
}

// ---- receipts: a run Claude Code recorded ----

// featRun is one Bash call as Claude Code's transcript recorded it: the tool call id, the command, its output, when it
// finished, and whether it was interrupted or flagged as an error.
type featRun struct {
	ID, Cmd, Out     string
	At               time.Time
	Interrupted, Err bool
}

// featRuns returns every Bash call in the transcripts at paths whose command is exactly cmd and whose result was
// recorded, oldest first. token is a cheap pre-filter: only lines that mention it (or results of a matching call) are
// parsed.
func featRuns(paths []string, cmd, token string) []featRun {
	var out []featRun
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		calls := map[string]bool{}
		rd := bufio.NewReaderSize(f, 1<<20)
		for {
			line, err := rd.ReadString('\n')
			if strings.Contains(line, token) || len(calls) > 0 && strings.Contains(line, `"tool_use_id"`) { // a call names the token; its result, the call's id
				var o struct {
					Type, Timestamp string
					Message         struct{ Content json.RawMessage }
					ToolUseResult   json.RawMessage `json:"toolUseResult"`
				}
				if json.Unmarshal([]byte(line), &o) == nil {
					var parts []struct {
						Type, ID, Name string
						ToolUseID      string `json:"tool_use_id"`
						Input          struct{ Command string }
						IsError        bool `json:"is_error"`
					}
					json.Unmarshal(o.Message.Content, &parts)
					for _, x := range parts {
						switch {
						case o.Type == "assistant" && x.Type == "tool_use" && x.Name == "Bash" && x.Input.Command == cmd:
							calls[x.ID] = true
						case o.Type == "user" && x.Type == "tool_result" && calls[x.ToolUseID]:
							var tr struct {
								Stdout      *string
								Interrupted bool
							}
							if json.Unmarshal(o.ToolUseResult, &tr) == nil && tr.Stdout != nil {
								at, _ := time.Parse(time.RFC3339Nano, o.Timestamp)
								out = append(out, featRun{x.ToolUseID, cmd, *tr.Stdout, at, tr.Interrupted, x.IsError})
							}
						}
					}
				}
			}
			if err != nil {
				break
			}
		}
		f.Close()
	}
	slices.SortFunc(out, func(a, b featRun) int { return a.At.Compare(b.At) })
	return out
}

// featPaths: the transcripts of this feature's agents: those on screen now and those seen before (an agent that exited)
func (s *server) featPaths(session string) []string {
	var out []string
	for _, se := range s.term.listing().Sessions {
		if se.Name != session {
			continue
		}
		for _, tb := range se.Tabs {
			for _, lp := range tb.Panes {
				if p, ok := s.term.pane(session, lp.ID); ok && p.Agent != nil && p.Agent.path != "" {
					out = append(out, p.Agent.path)
				}
			}
		}
	}
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	changed := false
	for _, p := range out {
		if !slices.Contains(s.sup.Paths[session], p) {
			s.sup.Paths[session], changed = append(s.sup.Paths[session], p), true
		}
	}
	if changed {
		s.sup.save()
	}
	return slices.Clone(s.sup.Paths[session])
}

// ---- verification ----

// featVerify turns the ticked boxes of a feature's milestones into verified outcomes or claims, in place. A ticked
// step stays "done" only with a receipt or the owner's attestation; otherwise it becomes "claimed" with Why saying what
// is missing. It also attaches the owner's approvals to steps, marks steps held (Blocked) and steps whose due time has
// passed (Missed). A receipt counts only if its run is newer than the step's criterion time: when the step's words were
// first seen as they are (0 for steps that predate supervision).
func (s *server) featVerify(session string, ms []featMilestone) { s.featVerifyAt(session, ms, false) }

// featVerifyAt is featVerify with a choice of rigor: decisive (the done gate) reads attested evidence bytes again,
// otherwise a cached look may do.
func (s *server) featVerifyAt(session string, ms []featMilestone, decisive bool) {
	var keys []string
	for _, m := range ms {
		for _, st := range m.Steps {
			keys = append(keys, st.Key)
		}
	}
	now := time.Now()
	since := s.sup.crits(session, keys, now)
	paths := s.featPaths(session)
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	for i := range ms {
		m := &ms[i]
		m.Done = 0
		for j := range m.Steps {
			st := &m.Steps[j]
			k := session + "/" + st.Key
			if st.Ask != "" {
				st.Qid = featQid(session, st.Key, st.Ask)
			}
			ap := s.sup.Approvals[k]
			if st.Check != "" && st.Cwd != "" && ap.Check == st.Check && ap.Cwd == st.Cwd && ap.Covers == strings.Join(st.Covers, ",") {
				st.Approval.Check = true
				st.Attempt = featAttempt(session, st.Key, st.Check, st.Cwd, ap.At)
				st.RunAs = featRunAs(st.Check, st.Cwd, st.Attempt)
			}
			st.Approval.Kind = st.Kind != "" && safeKind[st.Kind] && ap.Kind == st.Kind
			if h, ok := s.sup.Holds[session+"/"+strings.ToLower(st.Owner)]; ok && st.Owner != "" { // the whole tab, whatever its steps now say
				st.Blocked = h.Why
			}
			for hk, h := range s.sup.Holds { // and the held work itself, whichever tab it's moved to
				if strings.HasPrefix(hk, session+"/") && (slices.Contains(h.Keys, st.Key) || slices.Contains(h.Steps, st.Text)) {
					st.Blocked = h.Why
				}
			}
			if st.State == "done" {
				switch {
				case st.Blocked != "":
					st.Why = "held: " + st.Blocked
				case s.sup.Attests[k].Rev != "" && s.sup.Attests[k].Scope == featScope(*st):
					st.Proof = "owner verified " + time.Unix(s.sup.Attests[k].At, 0).UTC().Format("2006-01-02 15:04")
					if why := featAttestStale(s.sup.Attests[k].Seals, decisive, s.sup.Attests[k].Roots, s.secretPaths()); why != "" {
						st.Proof, st.Why = "", why
					}
				default:
					st.Proof, st.Why = s.featReceipt(session, st, ap, since[st.Key], paths)
				}
				if st.Why != "" {
					st.State, st.Proof, st.Label = "claimed", "", "claimed"
				} else {
					st.Label = map[bool]string{true: "owner verified", false: "check ran (receipt)"}[strings.HasPrefix(st.Proof, "owner")]
					m.Done++
				}
			}
			if due, ok := featDue(st.Due); ok && st.State != "done" && now.After(due) {
				st.Missed = true
			}
		}
	}
}

// featReceipt returns what proves a ticked step (proof) or why nothing does (why). The step must cite "evidence: run
// <attempt>" for the owner-approved check; the latest recorded run of exactly that command must be newer than the
// approval and the step's criterion time (since), uninterrupted, end in one exit marker saying 0, and show the step's
// expected result. s.sup.mu held.
func (s *server) featReceipt(session string, st *featPlanStep, ap featApproval, since int64, paths []string) (proof, why string) {
	ev := st.Evidence
	for _, l := range featEvLines(*st) { // several lines: the (last) run line is the receipt's
		if strings.HasPrefix(strings.TrimSpace(l), "run ") {
			ev = l
		}
	}
	tok, isRun := strings.CutPrefix(strings.TrimSpace(ev), "run ")
	tok = strings.TrimSpace(tok)
	switch {
	case ev == "":
		return "", "ticked without evidence"
	case !isRun:
		return "", "file evidence is a claim, not a check: run the step's approved check (runAs) and tick it with evidence: run <attempt>, or the owner marks it verified"
	case st.Check == "" || st.Cwd == "":
		return "", "it has no check (check: and cwd: lines) for the owner to approve"
	case !st.Approval.Check:
		return "", "its check isn't approved by the owner for this wording, command and folder"
	case tok != st.Attempt:
		return "", "evidence names attempt " + strconv.Quote(trunc(tok, 20)) + ", not this approval's " + st.Attempt
	}
	runs := featRuns(paths, st.RunAs, st.Attempt)
	if len(runs) == 0 {
		return "", "no run of exactly its runAs is recorded in this feature's agents' transcripts"
	}
	run := runs[len(runs)-1] // the latest decides: a later failure undoes an earlier pass
	lines := strings.Split(strings.TrimRight(run.Out, "\n"), "\n")
	last := lines[len(lines)-1]
	markers := 0
	for _, l := range lines {
		if strings.Contains(l, "rfe-exit=") || strings.Contains(l, "rfe-attempt=") {
			markers++
		}
	}
	switch {
	case run.At.Unix() < max(ap.At, since):
		return "", "the latest run is older than the check's approval or the step's wording"
	case run.Interrupted:
		return "", "the latest run was interrupted"
	case markers != 1 || !strings.HasPrefix(last, "rfe-exit="):
		return "", "the latest run's output needs exactly one exit marker, last (found " + strconv.Itoa(markers) + ")"
	case last == "rfe-exit=0 rfe-attempt="+st.Attempt && run.Err:
		return "", "the latest run's marker says 0 but Claude Code recorded an error: they disagree"
	case last != "rfe-exit=0 rfe-attempt="+st.Attempt:
		return "", "the latest run failed (" + trunc(last, 40) + ")"
	}
	if st.Result != "" {
		want := st.Result
		if i := strings.IndexByte(want, '`'); i >= 0 {
			if j := strings.IndexByte(want[i+1:], '`'); j > 0 {
				want = want[i+1 : i+1+j]
			}
		}
		if !strings.Contains(strings.Join(lines[:len(lines)-1], "\n"), want) {
			return "", "the run's output doesn't show " + strconv.Quote(trunc(want, 80))
		}
	}
	// a proof the plan names is judged against every run, however old: a non-run kind (an image, a doc, ...) is a
	// judgement that only the owner's attestation can meet
	if kind, _, _ := strings.Cut(st.PlanProof, " "); kind != "" && kind != "run" {
		return "", "its proof is " + kind + ", a judgement: your attestation meets it, a check's receipt doesn't"
	}
	if kind, crit, _ := strings.Cut(st.PlanProof, " "); kind == "run" { // a run proof: every `literal` in its criterion must be in the output
		body := strings.Join(lines[:len(lines)-1], "\n")
		for _, lit := range featLiterals(crit) {
			if !strings.Contains(body, lit) {
				return "", "the run's output doesn't show " + strconv.Quote(trunc(lit, 80)) + " (the proof's criterion)"
			}
		}
	}
	sum := featRev([]byte(run.Out))
	k := session + "/" + st.Key
	if x, ok := s.sup.Seals[k]; ok && x.Claim == run.ID && x.Sum != sum {
		return "", "the recorded run changed after it was checked"
	} else if !ok || x.Claim != run.ID {
		s.sup.Seals[k] = featSeal{run.ID, sum}
		s.sup.save()
	}
	return "check ran " + run.At.UTC().Format("2006-01-02 15:04") + ", exit 0 (" + trunc(run.ID, 24) + ")", ""
}

// featLiterals returns the backtick-quoted literals of a proof criterion: the only part of it the panel checks
// mechanically (the run's output must contain each); the rest of the criterion is description.
func featLiterals(s string) []string {
	parts := strings.Split(s, "`")
	var out []string
	for i := 1; i+1 < len(parts); i += 2 {
		if parts[i] != "" {
			out = append(out, parts[i])
		}
	}
	return out
}

// ---- the owner's routes ----

// featOwnerReq is the JSON body the owner's record routes share; each route reads the fields it needs.
type featOwnerReq struct {
	Session, Key, Rev, Tab string
	Items                  []struct{ Key, Check, Cwd, Kind string }
}

// featStep returns the step with key in the feature's current plan, and that plan's revision.
func (s *server) featStep(session, key string) (featPlanStep, string, bool) {
	d := s.featDossier(session)
	for _, m := range d.Milestones {
		for _, st := range m.Steps {
			if st.Key == key {
				return st, d.Rev, true
			}
		}
	}
	return featPlanStep{}, d.Rev, false
}

// termFeatureApprove handles POST /api/term/feature/approve {session, items:[{key, check, cwd} or {key, kind}]}: the
// owner approves exactly these checks (or replay-safe kinds) for these steps as worded now. Several at once.
func (s *server) termFeatureApprove(w http.ResponseWriter, r *http.Request) {
	var b featOwnerReq
	if err := readJSONN(r, &b, 1<<16); err != nil || !sessionNameRe.MatchString(b.Session) || len(b.Items) == 0 {
		writeJSON(w, 400, map[string]any{"error": "bad request"})
		return
	}
	featPlanMu.Lock() // judged and recorded with no record write in between
	defer featPlanMu.Unlock()
	release, why := s.featTicketGuard(r)
	if why != "" {
		writeJSON(w, 409, map[string]any{"error": why})
		return
	}
	defer release()
	covers := map[string]string{}
	for _, it := range b.Items {
		st, _, ok := s.featStep(b.Session, it.Key)
		covers[it.Key] = strings.Join(st.Covers, ",")
		switch {
		case !ok:
			writeJSON(w, 409, map[string]any{"error": "a step changed or is gone: reload and look again"})
			return
		case it.Kind == "" && (it.Check == "" || it.Check != st.Check || it.Cwd != st.Cwd || !filepath.IsAbs(it.Cwd)):
			writeJSON(w, 409, map[string]any{"error": "the check of " + strconv.Quote(st.Text) + " isn't what was shown (or its folder isn't absolute): reload"})
			return
		case it.Kind != "" && (it.Kind != st.Kind || !safeKind[it.Kind]):
			writeJSON(w, 409, map[string]any{"error": "only read, build, test or check steps can be approved as safe to redo, as written"})
			return
		}
	}
	now := time.Now().Unix()
	s.sup.mu.Lock()
	was := maps.Clone(s.sup.Approvals)
	for _, it := range b.Items {
		k := b.Session + "/" + it.Key
		a := s.sup.Approvals[k]
		if it.Kind != "" {
			a.Kind = it.Kind
		} else {
			a.Check, a.Cwd, a.Covers = it.Check, it.Cwd, covers[it.Key]
		}
		a.At = now
		s.sup.Approvals[k] = a
	}
	if err := s.sup.save(); err != nil {
		s.sup.Approvals = was
		s.sup.mu.Unlock()
		writeJSON(w, 500, map[string]any{"error": errOwnerSave.Error()})
		return
	}
	s.sup.mu.Unlock()
	s.featLog(b.Session, fmt.Sprintf("owner approved %d check(s)/kind(s) on the page", len(b.Items)))
	s.audit.add("feature-approve", fmt.Sprintf("%s %d items", b.Session, len(b.Items)), clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true, "dossier": s.featDossier(b.Session)})
}

// termFeatureVerify handles POST /api/term/feature/verify {session, key, rev}: the owner attests a ticked step, as
// worded, at this plan revision.
func (s *server) termFeatureVerify(w http.ResponseWriter, r *http.Request) {
	var b featOwnerReq
	if err := readJSON(r, &b); err != nil || !sessionNameRe.MatchString(b.Session) {
		writeJSON(w, 400, map[string]any{"error": "bad request"})
		return
	}
	featPlanMu.Lock()
	defer featPlanMu.Unlock()
	st, rev, ok := s.featStep(b.Session, b.Key)
	switch {
	case !ok || rev != b.Rev:
		writeJSON(w, 409, map[string]any{"error": "the plan changed since you looked: reload"})
		return
	case st.Blocked != "":
		writeJSON(w, 409, map[string]any{"error": "it's held: release the hold first"})
		return
	}
	k := b.Session + "/" + b.Key
	s.sup.mu.Lock()
	was, had := s.sup.Attests[k]
	s.sup.mu.Unlock()
	s.featSealSteps(b.Session, "attest")
	ad := s.featDossier(b.Session)
	seals, roots := s.featAttestSeals(ad, st), s.featEvidenceRoots(ad)
	s.sup.mu.Lock()
	was, had = s.sup.Attests[k]
	s.sup.Attests[k] = featAttest{time.Now().Unix(), rev, featScope(st), seals, roots}
	if err := s.sup.save(); err != nil {
		if delete(s.sup.Attests, k); had {
			s.sup.Attests[k] = was
		}
		s.sup.mu.Unlock()
		writeJSON(w, 500, map[string]any{"error": errOwnerSave.Error()})
		return
	}
	s.sup.mu.Unlock()
	s.featLog(b.Session, "owner verified on the page: "+trunc(st.Text, 100))
	s.audit.add("feature-verify", b.Session+" "+b.Key, clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true, "dossier": s.featDossier(b.Session)})
}

// termFeatureRelease handles POST /api/term/feature/release {session, tab, rev}: the owner lifts a hold. It authorizes
// nothing new: the lead re-plans.
func (s *server) termFeatureRelease(w http.ResponseWriter, r *http.Request) {
	var b featOwnerReq
	if err := readJSON(r, &b); err != nil || !sessionNameRe.MatchString(b.Session) {
		writeJSON(w, 400, map[string]any{"error": "bad request"})
		return
	}
	featPlanMu.Lock()
	release, why := s.featTicketGuard(r)
	if why != "" {
		featPlanMu.Unlock()
		writeJSON(w, 409, map[string]any{"error": why})
		return
	}
	defer release()
	if d := s.featDossier(b.Session); d.Rev != b.Rev {
		featPlanMu.Unlock()
		writeJSON(w, 409, map[string]any{"error": "the plan changed since you looked: reload"})
		return
	}
	k := b.Session + "/" + strings.ToLower(b.Tab)
	s.sup.mu.Lock()
	h, ok := s.sup.Holds[k]
	var err error
	if ok {
		delete(s.sup.Holds, k)
		if err = s.sup.save(); err != nil {
			s.sup.Holds[k] = h
		}
	}
	s.sup.mu.Unlock()
	featPlanMu.Unlock()
	switch {
	case !ok:
		writeJSON(w, 409, map[string]any{"error": "no hold on " + b.Tab})
		return
	case err != nil:
		writeJSON(w, 500, map[string]any{"error": errOwnerSave.Error()})
		return
	}
	s.featLog(b.Session, "owner released the hold on "+b.Tab)
	s.audit.add("feature-release", b.Session+" "+b.Tab, clientIP(r))
	if th := s.featOwner(b.Session); th != nil {
		s.raise(time.Now(), th, false, b.Session+"/released:"+b.Tab+":"+strconv.FormatInt(h.At, 10), "ok", "the owner released the hold on "+b.Tab,
			"Held because: "+h.Why+"\nThis lets you re-plan those steps. It authorizes no change: check the live state first, and any new change goes through the plan.")
	}
	writeJSON(w, 200, map[string]any{"ok": true, "dossier": s.featDossier(b.Session)})
}

// errOwnerSave is returned when an owner action couldn't be saved; the action was rolled back.
var errOwnerSave = errors.New("couldn't record it; nothing changed")

// termFeatureAccept handles POST /api/term/feature/accept {session, rev}: the owner accepts the feature's desired
// outcomes (FEATURE.md) exactly as they stand at that revision, as met. A changed outcome set needs accepting again
// before the feature can be done.
func (s *server) termFeatureAccept(w http.ResponseWriter, r *http.Request) {
	var b featOwnerReq
	if err := readJSON(r, &b); err != nil || !sessionNameRe.MatchString(b.Session) {
		writeJSON(w, 400, map[string]any{"error": "bad request"})
		return
	}
	featPlanMu.Lock()
	d := s.featDossier(b.Session)
	if d.Revs["feature"] != b.Rev || len(d.Outcomes) == 0 {
		featPlanMu.Unlock()
		writeJSON(w, 409, map[string]any{"error": "FEATURE.md changed since you looked, or lists no outcomes: reload"})
		return
	}
	if why := s.featGate(b.Session, d.Thread, nil, false); why != "" { // acceptance is of results, not leave to finish later
		featPlanMu.Unlock()
		writeJSON(w, 409, map[string]any{"error": "accept the outcomes once the plan's results are verified: " + why})
		return
	}
	s.sup.mu.Lock()
	was, had := s.sup.Accepts[b.Session]
	s.sup.Accepts[b.Session] = featAcceptRev(d.Outcomes, d.Rev)
	err := s.sup.save()
	if err != nil {
		if delete(s.sup.Accepts, b.Session); had {
			s.sup.Accepts[b.Session] = was
		}
	}
	s.sup.mu.Unlock()
	featPlanMu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": errOwnerSave.Error()})
		return
	}
	s.featLog(b.Session, fmt.Sprintf("owner accepted the %d desired outcome(s) as met", len(d.Outcomes)))
	s.audit.add("feature-accept", b.Session, clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true, "dossier": s.featDossier(b.Session)})
}

// ---- adopting a requirements draft (FEATURE.draft.md: an agent's, through the writer; adopted only by the owner) ----

// promoteTxnFile is the intent file a promote writes in the record folder before it changes anything.
const promoteTxnFile = ".promote-txn"

// promoteHook lets tests fail a promote at a stage, or cut it short after one (a crash); nil in production.
var promoteHook func(stage string) error

// promoteAt runs the test hook for a promote stage, if there is one.
func promoteAt(stage string) error {
	if promoteHook != nil {
		return promoteHook(stage)
	}
	return nil
}

// promoteTxn is one promote's intent, written in the record folder before anything changes. Its commit point is the
// supervisor's memory saved without the feature's acceptance; until then a failure or a crash puts FEATURE.md back.
type promoteTxn struct {
	ID        string `json:"id"` // committed exactly when the supervisor's memory says Promoted[session] == ID
	Session   string `json:"session"`
	Old       []byte `json:"old"` // FEATURE.md before; nil: there was none
	Draft     []byte `json:"draft"`
	DraftRev  string `json:"draftRev"`
	Accept    string `json:"accept"` // the acceptance the promote clears
	HadAccept bool   `json:"hadAccept"`
}

// undo puts the old FEATURE.md back, if it is still the draft this promote wrote (a later edit stays)
func (t *promoteTxn) undo(feat string) error {
	cur, err := os.ReadFile(feat)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err != nil || string(cur) != string(t.Draft) {
		return nil
	}
	if t.Old == nil {
		return os.Remove(feat)
	}
	return writeFileAtomic(feat, t.Old, 0o600)
}

// finish removes the adopted draft, if it is still the one adopted (a newer draft stays)
func (t *promoteTxn) finish(draft string) error {
	cur, err := os.ReadFile(draft)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if featRev(cur) != t.DraftRev {
		return nil
	}
	return os.Remove(draft)
}

// recoverPromoteTxn settles a promote cut short in dir. If it was committed (the supervisor's saved memory records this
// promote's id, saved together with the cleared acceptance), the draft goes, if it's still the adopted one. Otherwise
// FEATURE.md goes back, only if it still holds the draft (a newer write stays), and the acceptance was never touched.
// featPlanMu held, or before serving.
func (s *server) recoverPromoteTxn(dir string) {
	p := filepath.Join(dir, promoteTxnFile)
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var t promoteTxn
	if json.Unmarshal(b, &t) == nil {
		s.sup.mu.Lock()
		committed := t.ID != "" && s.sup.Promoted[t.Session] == t.ID
		s.sup.mu.Unlock()
		if committed { // the commit point is recorded, not inferred: FEATURE.md and the acceptance stand as committed
			err = t.finish(filepath.Join(dir, featDocs["draft"]))
		} else {
			err = t.undo(filepath.Join(dir, "FEATURE.md"))
		}
		if err != nil {
			log.Printf("feature %s: interrupted promote not settled: %v (the intent stays)", t.Session, err)
			return
		}
		log.Printf("feature %s: an interrupted promote was settled", t.Session)
	}
	os.Remove(p)
}

// featPromote makes the draft the new FEATURE.md, as one change with clearing the acceptance (the commit). Order:
// write the intent, write FEATURE.md, commit; only then does the draft go. After the commit nothing is rolled back:
// what's left over is returned as a warning. It returns an HTTP status and, on failure, the reason.
func (s *server) featPromote(session, draftRev, featureRev string) (code int, why string, warns []string) {
	featPlanMu.Lock()
	defer featPlanMu.Unlock()
	dir, conflict := s.featHome(session, s.featOwnerID(session))
	if conflict != "" {
		return 409, conflict, nil
	}
	s.recoverPromoteTxn(dir)
	fp, dp, ip := filepath.Join(dir, "FEATURE.md"), filepath.Join(dir, featDocs["draft"]), filepath.Join(dir, promoteTxnFile)
	draft, _ := os.ReadFile(dp)
	old, err := os.ReadFile(fp)
	if err != nil && !os.IsNotExist(err) {
		return 500, "couldn't read FEATURE.md; nothing changed", nil
	}
	if strings.TrimSpace(string(draft)) == "" || featRev(draft) != draftRev || featRev(old) != featureRev {
		return 409, "the draft or FEATURE.md changed since you looked, or there's no draft: reload", nil
	}
	const nothing = "couldn't adopt the draft; nothing changed"
	s.sup.mu.Lock()
	acc, had := s.sup.Accepts[session]
	s.sup.mu.Unlock()
	t := promoteTxn{strconv.FormatInt(time.Now().UnixNano(), 36), session, old, draft, draftRev, acc, had}
	tb, _ := json.Marshal(t)
	if err = promoteAt("intent"); err == nil {
		err = writeFileAtomic(ip, tb, 0o600)
	}
	if err != nil {
		log.Printf("feature %s: promote intent: %v", session, err)
		return 500, nothing, nil
	}
	if promoteAt("after-intent") != nil {
		return 500, "interrupted", nil
	}
	if err = promoteAt("feature"); err == nil {
		err = featWriteRevLocked(fp, featureRev, draft)
	}
	if err != nil {
		os.Remove(ip)
		log.Printf("feature %s: promote write: %v", session, err)
		return 500, nothing, nil
	}
	if promoteAt("after-feature") != nil {
		return 500, "interrupted", nil
	}
	s.sup.mu.Lock() // the commit point: the acceptance cleared and this promote recorded, in one save
	prevID, hadID := s.sup.Promoted[session]
	delete(s.sup.Accepts, session)
	s.sup.Promoted[session] = t.ID
	if err = promoteAt("commit"); err == nil {
		err = s.sup.save()
	}
	if err != nil {
		if had {
			s.sup.Accepts[session] = acc
		}
		if delete(s.sup.Promoted, session); hadID {
			s.sup.Promoted[session] = prevID
		}
	}
	s.sup.mu.Unlock()
	if err != nil {
		if uerr := t.undo(fp); uerr != nil {
			log.Printf("feature %s: promote undo: %v (the intent stays: the next read puts it back)", session, uerr)
		} else {
			os.Remove(ip)
		}
		return 500, nothing, nil
	}
	if promoteAt("after-commit") != nil {
		return 500, "interrupted", nil
	}
	if err = promoteAt("draft"); err == nil {
		err = t.finish(dp)
	}
	if err != nil {
		log.Printf("feature %s: promote, draft removal: %v (the intent stays)", session, err)
		warns = append(warns, "adopted, but the draft couldn't be removed yet: the panel removes it on its next read")
	} else {
		os.Remove(ip)
	}
	if err := s.featLog(session, "owner promoted the requirements draft"); err != nil {
		warns = append(warns, "adopted, but its LOG.md line couldn't be written")
	}
	return 200, "", warns
}

// termFeaturePromote handles POST /api/term/feature/promote {session, draftRev, featureRev}: the owner adopts the draft
// as FEATURE.md. It authorizes nothing new: the acceptance is cleared, so done needs the outcomes accepted again.
func (s *server) termFeaturePromote(w http.ResponseWriter, r *http.Request) {
	var b struct{ Session, DraftRev, FeatureRev string }
	if err := readJSON(r, &b); err != nil || !sessionNameRe.MatchString(b.Session) {
		writeJSON(w, 400, map[string]any{"error": "bad request"})
		return
	}
	code, why, warns := s.featPromote(b.Session, b.DraftRev, b.FeatureRev)
	if code == 500 { // cut short: settle it now (recovery runs first) so no done stands against a changed FEATURE.md
		s.featReopen(b.Session)
	}
	if code != 200 {
		writeJSON(w, code, map[string]any{"error": why})
		return
	}
	s.audit.add("feature-promote", b.Session, clientIP(r))
	s.featReopen(b.Session) // a done thread reopens: its acceptance is gone
	out := map[string]any{"ok": true, "dossier": s.featDossier(b.Session)}
	if len(warns) > 0 {
		out["warning"] = strings.Join(warns, "; ")
	}
	writeJSON(w, 200, out)
}

// termFeatureDiscard handles POST /api/term/feature/discard {session, draftRev}: the owner drops the draft.
func (s *server) termFeatureDiscard(w http.ResponseWriter, r *http.Request) {
	var b struct{ Session, DraftRev string }
	if err := readJSON(r, &b); err != nil || !sessionNameRe.MatchString(b.Session) {
		writeJSON(w, 400, map[string]any{"error": "bad request"})
		return
	}
	code, why := func() (int, string) {
		featPlanMu.Lock()
		defer featPlanMu.Unlock()
		dir, conflict := s.featHome(b.Session, s.featOwnerID(b.Session))
		if conflict != "" {
			return 409, conflict
		}
		s.recoverPromoteTxn(dir)
		dp := filepath.Join(dir, featDocs["draft"])
		if cur, err := os.ReadFile(dp); err != nil || featRev(cur) != b.DraftRev {
			return 409, "the draft changed since you looked, or is gone: reload"
		}
		if err := os.Remove(dp); err != nil {
			return 500, "couldn't remove the draft"
		}
		return 200, ""
	}()
	if code != 200 {
		writeJSON(w, code, map[string]any{"error": why})
		return
	}
	s.featLog(b.Session, "owner discarded the requirements draft")
	s.audit.add("feature-discard", b.Session, clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true, "dossier": s.featDossier(b.Session)})
}

// ---- the agents' cooperative writer (agent API, bearer) ----

// agentFeature handles GET /agent/feature?session=: the record as the panel sees it (paths, revisions, steps with
// keys, questions, checks), with the whole LOG.md. Read only.
func (s *server) agentFeature(w http.ResponseWriter, r *http.Request) {
	sess := r.URL.Query().Get("session")
	if !sessionNameRe.MatchString(sess) {
		writeJSON(w, 400, map[string]any{"error": "bad session"})
		return
	}
	s.featReopen(sess) // a direct edit since done shows as reopened, not as a current done
	d := s.featDossier(sess)
	if !s.sessionLive(sess) && !featHasDocs(d.Dir) {
		writeJSON(w, 404, map[string]any{"error": "no live session and no record named exactly " + strconv.Quote(sess)})
		return
	}
	b, _ := os.ReadFile(d.Paths["LOG.md"])
	d.Log, d.LogWhole = string(b), true
	s.sup.mu.Lock()
	rv := map[string]string{}
	for k, v := range s.sup.Reviewed[sess] {
		rv[k] = v
	}
	s.sup.mu.Unlock()
	writeJSON(w, 200, map[string]any{"dossier": d, "tickets": s.featTickets(sess), "reviewed": rv})
}

// agentFeatureDoc handles POST /agent/feature/doc {session, doc: plan|tests|draft, text, rev}: an agent's update of a
// record file, against the revision it read: the one write path the panel keeps consistent (editing the files
// directly races with everyone). FEATURE.md is the owner's: agents write a draft of it (FEATURE.draft.md, created at
// the empty file's revision), which only the owner adopts (POST /api/term/feature/promote), or propose changes in
// LOG.md. LOG.md is appended to (POST /agent/feature/log).
func (s *server) agentFeatureDoc(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Session, Doc, Text, Rev string
		Thread                  *string // the thread the writer believes manages it: refused unless it does
	}
	switch err := readJSONN(r, &b, 1<<18); {
	case err != nil || !sessionNameRe.MatchString(b.Session) || featDocs[b.Doc] == "":
		writeJSON(w, 400, map[string]any{"error": "session, doc (plan, tests or draft), text and rev are required"})
	case b.Doc == "feature":
		writeJSON(w, 403, map[string]any{"error": "FEATURE.md is the owner's: write FEATURE.draft.md (doc: draft) for the owner to adopt, or propose the change in LOG.md"})
	case b.Doc == "log":
		writeJSON(w, 400, map[string]any{"error": "LOG.md is appended to: POST /agent/feature/log {session, line}"})
	case b.Rev == "":
		writeJSON(w, 400, map[string]any{"error": "rev is required: read the record first (GET /agent/feature)"})
	default:
		sid := r.Header.Get("X-Agent-Sid") // the writer's word for who it is: attribution only
		if len(sid) > 128 || strings.ContainsAny(sid, "\x00\n\r") {
			sid = ""
		}
		s.featWriteDoc(w, b.Session, b.Doc, b.Text, b.Rev, "agent", sid, b.Thread)
	}
}

// featExpect refuses (409) a write by a writer that names a managing thread other than the session's actual one (none
// included). featPlanMu held, so no rebind lands between this and the write. It guards against mistakes, not
// impersonation: every agent shares the agent API's bearer token.
func (s *server) featExpect(w http.ResponseWriter, session string, thread *string) bool {
	if thread == nil {
		return true
	}
	if got := s.featOwnerID(session); got != *thread {
		writeJSON(w, 409, map[string]any{"error": "this feature isn't managed by " + strconv.Quote(*thread) + " now: read it again", "thread": got})
		return false
	}
	return true
}

// agentFeatureLog handles POST /agent/feature/log {session, line}: a dated line at the end of LOG.md (appends
// commute, so no revision is needed).
func (s *server) agentFeatureLog(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Session, Line string
		Thread        *string
	}
	if err := readJSONN(r, &b, 1<<14); err != nil || !sessionNameRe.MatchString(b.Session) || strings.TrimSpace(b.Line) == "" {
		writeJSON(w, 400, map[string]any{"error": "session and line are required"})
		return
	}
	featPlanMu.Lock()
	defer featPlanMu.Unlock()
	if !s.featExpect(w, b.Session, b.Thread) {
		return
	}
	if _, conflict := s.featHome(b.Session, s.featOwnerID(b.Session)); conflict != "" {
		writeJSON(w, 409, map[string]any{"error": conflict})
		return
	}
	// kept exactly as written (structural trim only): one physical line of at most 2000 characters, refused whole
	// otherwise, before anything is written (the client escapes line breaks and tabs inside its fields)
	line := strings.TrimSpace(b.Line)
	if n := utf8.RuneCountInString(line); n > 2000 {
		writeJSON(w, 400, map[string]any{"error": fmt.Sprintf("the line is %d characters; at most 2000 (split it into several)", n)})
		return
	}
	if strings.ContainsAny(line, "\n\r\v\f\x00\u00a0") {
		writeJSON(w, 400, map[string]any{"error": "the line holds a line break, VT, FF, NBSP or NUL: escape it (%0A, %0D) or remove it"})
		return
	}
	if err := s.featLog(b.Session, line); err != nil {
		writeJSON(w, 500, map[string]any{"error": "LOG.md: " + err.Error()})
		return
	}
	// the line's event: its read identity on the page (seen, reviewed); the line itself stands in LOG.md either way
	e, err := s.featEvent(b.Session, featEv{Kind: "log", Text: line, Rev: s.featDossier(b.Session).Rev, Sid: trunc(r.Header.Get("X-Agent-Sid"), 64)}) // the plan as the panel read it
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": true, "warning": "logged, but its event wasn't saved: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": e.ID})
}

// featOwnerID returns the id of the thread that manages session's feature, or "" when there is none.
func (s *server) featOwnerID(session string) string {
	if t := s.featOwner(session); t != nil {
		return t.ID
	}
	return ""
}

// featWriteDoc is the single place a record file is written for the owner's page and the agents' API: it needs one
// unambiguous folder and the revision the writer read, and writes the response itself.
func (s *server) featWriteDoc(w http.ResponseWriter, session, doc, text, rev, who, sid string, thread *string) {
	defer s.featReopen(session) // after the lock below is released
	featPlanMu.Lock()
	defer featPlanMu.Unlock()
	if !s.featExpect(w, session, thread) {
		return
	}
	if d, _ := s.featHome(session, s.featOwnerID(session)); d != "" {
		s.recoverPromoteTxn(d) // a write lands on the settled record, never on a half-promoted one
	}
	dir, conflict := s.featHome(session, s.featOwnerID(session))
	if conflict != "" {
		writeJSON(w, 409, map[string]any{"error": conflict})
		return
	}
	p := filepath.Join(dir, featDocs[doc])
	if doc == "plan" {
		if why := featArtLimits(parseMilestones(text)); why != "" {
			writeJSON(w, 400, map[string]any{"error": why})
			return
		}
	}
	before, _ := os.ReadFile(p)
	if err := featWriteRevLocked(p, rev, []byte(text)); err != nil {
		cur, _ := os.ReadFile(p)
		code := 500
		if errors.Is(err, errStale) || errors.Is(err, errNoRev) {
			code = 409
		} else {
			log.Printf("feature %s: write %s: %v", session, featDocs[doc], err)
			err = errors.New("couldn't write it; nothing changed")
		}
		writeJSON(w, code, map[string]any{"error": err.Error(), "rev": featRev(cur)})
		return
	}
	s.featLog(session, who+": edited "+featDocs[doc])
	if doc == "plan" {
		s.featCoverageEvents(session, string(before), text)
		if sid != "" {
			s.featAssessClaims(session, string(before), text, sid)
		}
		s.featSealSteps(session, "writer")
	}
	writeJSON(w, 200, map[string]any{"ok": true, "rev": featRev([]byte(text)), "dossier": s.featDossier(session)})
}

// agentFeatureTeam handles GET /agent/feature/team?session=: the feature's team as ONE completed terminal sample saw
// it (the sampler's last full update, read under its lock with the time it was taken), with the steps each tab owns
// (@Tab, exact up to case). observedAt is that sample's time (0: none yet), never this request's; nothing is sampled,
// fetched or run for the request: the caller judges freshness by ageSec/stale. Members are identified by key
// (session:pane); two panes in tabs of the same name are both marked ambiguousTab, and own the same steps.
// 404 only when neither the sample has the session nor a record or managing thread exists.
func (s *server) agentFeatureTeam(w http.ResponseWriter, r *http.Request) {
	sess := r.URL.Query().Get("session")
	if !sessionNameRe.MatchString(sess) {
		writeJSON(w, 400, map[string]any{"error": "bad session"})
		return
	}
	snap, at := s.term.sample()
	d := s.featDossier(sess)
	type member struct {
		Key          string   `json:"key"`
		Pane         int      `json:"pane"`
		Tab          string   `json:"tab"`
		AmbiguousTab bool     `json:"ambiguousTab,omitempty"`
		Kind         string   `json:"kind"`
		State        string   `json:"state"`
		Title        string   `json:"title,omitempty"`
		Branch       string   `json:"branch,omitempty"`
		Root         string   `json:"root,omitempty"`
		Ctx          int      `json:"ctx,omitempty"`
		Last         string   `json:"last,omitempty"`
		LastAt       int64    `json:"lastAt,omitempty"` // the agent's own last activity (its transcript), not the sample time
		Owns         []string `json:"owns"`
	}
	team, present, tabs := []member{}, false, map[string]int{}
	for _, se := range snap.Sessions {
		if se.Name != sess {
			continue
		}
		present = true
		for _, tb := range se.Tabs {
			for _, p := range tb.Panes {
				if p.Agent == nil {
					continue
				}
				m := member{Key: fmt.Sprintf("%s:%d", sess, p.ID), Pane: p.ID, Tab: tb.Name, Kind: cmp.Or(p.Kind, "claude"), State: p.State,
					Title: p.Agent.Title, Last: p.Agent.Last, LastAt: p.Agent.LastAt, Owns: []string{}}
				if c := p.Agent.Ctx; c != nil && c.Window > 0 {
					m.Ctx = int(c.Used * 100 / c.Window)
				}
				if p.Git != nil {
					m.Branch, m.Root = p.Git.Branch, p.Git.Root
				}
				for _, ms := range d.Milestones {
					for _, st := range ms.Steps {
						if st.Owner != "" && strings.EqualFold(st.Owner, tb.Name) {
							m.Owns = append(m.Owns, st.Key)
						}
					}
				}
				tabs[strings.ToLower(tb.Name)]++
				team = append(team, m)
			}
		}
	}
	for i := range team {
		team[i].AmbiguousTab = tabs[strings.ToLower(team[i].Tab)] > 1
	}
	if !present && !featHasDocs(d.Dir) && d.Thread == "" {
		writeJSON(w, 404, map[string]any{"error": "no sampled session, record or managing thread named exactly " + strconv.Quote(sess)})
		return
	}
	obs, age := int64(0), int64(-1)
	if !at.IsZero() {
		obs, age = at.Unix(), int64(time.Since(at)/time.Second)
	}
	writeJSON(w, 200, map[string]any{"session": sess, "thread": d.Thread, "present": present, "observedAt": obs, "ageSec": age,
		"stale": age < 0 || age > 90, "team": team, "dossierRev": d.Rev})
}
