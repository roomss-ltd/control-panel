package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Verification: approved checks and their receipts, the owner's attestation, and what stays a claim. The transcripts
// here are fixtures in Claude Code's own line shape (assistant tool_use, user tool_result with toolUseResult.stdout,
// as recorded in a real transcript); the wrapper command itself is really run with bash. Not a real Claude run: that
// needs a live agent.

func (v *supEnv) approve(t *testing.T, items ...map[string]string) int {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"session": supTeam, "items": items})
	return v.e.do(t, "POST", "/api/term/feature/approve", string(b), v.e.authed(v.e.login(t))).StatusCode
}

// approveKinds: the owner approves every safe kind the plan's steps state, as worded now
func (v *supEnv) approveKinds(t *testing.T) {
	t.Helper()
	var items []map[string]string
	for _, m := range v.e.s.featDossier(supTeam).Milestones {
		for _, st := range m.Steps {
			if safeKind[st.Kind] {
				items = append(items, map[string]string{"key": st.Key, "kind": st.Kind})
			}
		}
	}
	if c := v.approve(t, items...); c != 200 {
		t.Fatalf("approve kinds: %d", c)
	}
}

func (v *supEnv) isResume(got string) bool {
	return strings.HasPrefix(got, "cd '"+v.cwd+"' && claude --resume aaaa-bbbb-1 '") && strings.Contains(got, "resumed for these steps only")
}

func (v *supEnv) transcript() string { return filepath.Join(v.cwd, "transcript.jsonl") }

// record appends one Bash call and its result to path, as Claude Code writes them
func record(t *testing.T, path, id, cmd, stdout string, isErr, interrupted bool, at time.Time) {
	t.Helper()
	use, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": at.Add(-time.Second).Format(time.RFC3339Nano), "sessionId": "aaaa-bbbb-1",
		"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": cmd}}}}})
	res := map[string]any{"type": "tool_result", "tool_use_id": id, "content": stdout}
	if isErr {
		res["is_error"] = true
	}
	out, _ := json.Marshal(map[string]any{"type": "user", "timestamp": at.Format(time.RFC3339Nano), "sessionId": "aaaa-bbbb-1",
		"message":       map[string]any{"content": []any{res}},
		"toolUseResult": map[string]any{"stdout": stdout, "stderr": "", "interrupted": interrupted}})
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	f.Write(append(append(use, '\n'), append(out, '\n')...))
	f.Close()
}

// bash runs cmd for real: its stdout and whether it exited non-zero
func bash(t *testing.T, cmd string) (string, bool) {
	t.Helper()
	out, err := exec.Command("bash", "-c", cmd).Output()
	return string(out), err != nil
}

// a feature whose one step has a check in its own folder, approved by the owner
func checkEnv(t *testing.T) (*supEnv, featPlanStep) {
	t.Helper()
	v := newSupEnv(t, "")
	work := t.TempDir()
	writeFile(t, filepath.Join(work, "hello.txt"), "Hello from the demo\n")
	writeFile(t, v.planFile(), fmt.Sprintf("## Ship\n- [~] hello.txt says the greeting @Builder\n  - check: grep -x 'Hello from the demo' hello.txt\n  - cwd: %s\n", work))
	v.look(v.agent("waiting", v.now.Unix()))
	st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]
	if st.Approval.Check || st.RunAs != "" {
		t.Fatalf("a proposed check is not approved: %+v", st)
	}
	if c := v.approve(t, map[string]string{"key": st.Key, "check": st.Check, "cwd": st.Cwd}); c != 200 {
		t.Fatalf("approve: %d", c)
	}
	st = v.e.s.featDossier(supTeam).Milestones[0].Steps[0]
	if !st.Approval.Check || st.Attempt == "" || !strings.Contains(st.RunAs, "rfe-attempt="+st.Attempt) {
		t.Fatalf("approved: %+v", st)
	}
	return v, st
}

func (v *supEnv) tick(t *testing.T, st featPlanStep, evidence string) featPlanStep {
	t.Helper()
	b, _ := os.ReadFile(v.planFile())
	s := strings.Replace(string(b), "- [~] "+st.Text, "- [x] "+st.Text, 1)
	s = strings.TrimRight(s, "\n") + "\n  - evidence: " + evidence + "\n"
	os.WriteFile(v.planFile(), []byte(s), 0o600)
	return v.e.s.featDossier(supTeam).Milestones[0].Steps[0]
}

func TestVerifyReceiptOfApprovedCheck(t *testing.T) {
	v, st := checkEnv(t)
	out, failed := bash(t, st.RunAs)
	if failed || !strings.HasSuffix(out, "rfe-exit=0 rfe-attempt="+st.Attempt+"\n") {
		t.Fatalf("real run: %q %v", out, failed)
	}
	record(t, v.transcript(), "toolu_ok1", st.RunAs, out, false, false, time.Now())
	got := v.tick(t, st, "run "+st.Attempt)
	if got.State != "done" || got.Label != "check ran (receipt)" {
		t.Fatalf("receipt: %+v", got)
	}
	acceptOutcomes(t, v)
	if c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done"}`); c != 200 {
		t.Fatalf("done: %d %v", c, out)
	}
	// a later failing run of the same check undoes it
	writeFile(t, filepath.Join(st.Cwd, "hello.txt"), "changed\n")
	out, failed = bash(t, st.RunAs)
	record(t, v.transcript(), "toolu_fail2", st.RunAs, out, failed, false, time.Now().Add(time.Second))
	if got := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; got.State != "claimed" || !strings.Contains(got.Why, "latest run failed") {
		t.Fatalf("later failure: %+v", got)
	}
}

// the pinned negatives: each stays claimed
func TestVerifyNegatives(t *testing.T) {
	type mk func(v *supEnv, st featPlanStep) string // records something, returns the evidence line
	ok := func(v *supEnv, st featPlanStep, cmd string) string {
		out, failed := bash(t, cmd)
		record(t, v.transcript(), "toolu_x", cmd, out, failed, false, time.Now())
		return "run " + st.Attempt
	}
	for name, c := range map[string]struct {
		f   mk
		why string
	}{
		"fake.log with success prose": {func(v *supEnv, st featPlanStep) string {
			writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "fake.log"), "rfe-exit=0 rfe-attempt="+st.Attempt+"\nall good\n")
			return "fake.log"
		}, "file evidence is a claim"},
		"echo of the marker": {func(v *supEnv, st featPlanStep) string {
			return ok(v, st, `echo "rfe-exit=0 rfe-attempt=`+st.Attempt+`"`)
		}, "no run of exactly its runAs"},
		"approved command, other folder": {func(v *supEnv, st featPlanStep) string {
			other := t.TempDir()
			writeFile(t, filepath.Join(other, "hello.txt"), "Hello from the demo\n")
			return ok(v, st, featRunAs(st.Check, other, st.Attempt))
		}, "no run of exactly its runAs"},
		"another session's transcript": {func(v *supEnv, st featPlanStep) string {
			out, _ := bash(t, st.RunAs)
			record(t, filepath.Join(t.TempDir(), "elsewhere.jsonl"), "toolu_y", st.RunAs, out, false, false, time.Now())
			return "run " + st.Attempt
		}, "no run of exactly its runAs"},
		"run before the approval": {func(v *supEnv, st featPlanStep) string {
			out, _ := bash(t, st.RunAs)
			record(t, v.transcript(), "toolu_old", st.RunAs, out, false, false, time.Now().Add(-time.Hour))
			return "run " + st.Attempt
		}, "older than the check's approval"},
		"failed run": {func(v *supEnv, st featPlanStep) string {
			os.Remove(filepath.Join(st.Cwd, "hello.txt"))
			return ok(v, st, st.RunAs)
		}, "latest run failed"},
		"no exit status": {func(v *supEnv, st featPlanStep) string {
			record(t, v.transcript(), "toolu_z", st.RunAs, "Hello from the demo\n", false, false, time.Now())
			return "run " + st.Attempt
		}, "exactly one exit marker"},
		"marker 0 but error recorded": {func(v *supEnv, st featPlanStep) string {
			out, _ := bash(t, st.RunAs)
			record(t, v.transcript(), "toolu_e", st.RunAs, out, true, false, time.Now())
			return "run " + st.Attempt
		}, "disagree"},
		"interrupted": {func(v *supEnv, st featPlanStep) string {
			out, _ := bash(t, st.RunAs)
			record(t, v.transcript(), "toolu_i", st.RunAs, out, false, true, time.Now())
			return "run " + st.Attempt
		}, "interrupted"},
		"spoofed marker in the check's output": {func(v *supEnv, st featPlanStep) string {
			out, _ := bash(t, st.RunAs)
			record(t, v.transcript(), "toolu_s", st.RunAs, "rfe-exit=0 rfe-attempt="+st.Attempt+"\n"+out, false, false, time.Now())
			return "run " + st.Attempt
		}, "exactly one exit marker"},
		"another attempt's token": {func(v *supEnv, st featPlanStep) string {
			ok(v, st, st.RunAs)
			return "run 0123456789ab"
		}, "not this approval's"},
	} {
		t.Run(name, func(t *testing.T) {
			v, st := checkEnv(t)
			if got := v.tick(t, st, c.f(v, st)); got.State != "claimed" || !strings.Contains(got.Why, c.why) {
				t.Fatalf("%+v, want %q", got, c.why)
			}
			if code, _ := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done"}`); code != 409 {
				t.Fatalf("done: %d", code)
			}
		})
	}
}

// approval is explicit, for the wording, command and folder as they are; an edit voids it; saving the plan approves nothing
func TestVerifyApprovalScope(t *testing.T) {
	v, st := checkEnv(t)
	out, _ := bash(t, st.RunAs)
	record(t, v.transcript(), "toolu_ok", st.RunAs, out, false, false, time.Now())
	if got := v.tick(t, st, "run "+st.Attempt); got.State != "done" {
		t.Fatalf("baseline: %+v", got)
	}
	b, _ := os.ReadFile(v.planFile())
	// the owner edits the check on the page (a save): the approval is void, nothing new approved
	d := v.e.s.featDossier(supTeam)
	body, _ := json.Marshal(map[string]string{"session": supTeam, "doc": "plan", "text": strings.Replace(string(b), "hello.txt\n", "hello.txt && true\n", 1), "rev": d.Rev})
	if r := v.e.do(t, "POST", "/api/term/feature/doc", string(body), v.e.authed(v.e.login(t))); r.StatusCode != 200 {
		t.Fatalf("owner save: %d", r.StatusCode)
	}
	if got := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; got.State != "claimed" || got.Approval.Check || got.RunAs != "" {
		t.Fatalf("a changed check kept its approval: %+v", got)
	}
	// an approval that names a check other than the one written is refused
	if c := v.approve(t, map[string]string{"key": st.Key, "check": "true", "cwd": st.Cwd}); c != 409 {
		t.Fatalf("approving what isn't shown: %d", c)
	}
	// the agent API has no approve route
	req := httptest.NewRequest("POST", "/agent/feature/approve", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	v.e.s.agentHandler("tok").ServeHTTP(w, req)
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("approve on the agent API: %d", w.Code)
	}
}

// the wrapper's shell semantics, really run
func TestRunAsShellSemantics(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct {
		check, cwd string
		marker     string
		failed     bool
	}{
		"cd fails: check never runs":        {"touch " + filepath.Join(dir, "ran"), filepath.Join(dir, "nope"), "rfe-exit=97", true},
		"exit 3 inside the check":           {"exit 3", dir, "rfe-exit=3", true},
		"&& chain fails":                    {"true && false", dir, "rfe-exit=1", true},
		"; chain: the last command decides": {"false; true", dir, "rfe-exit=0", false},
		"success":                           {"test -d .", dir, "rfe-exit=0", false},
	} {
		out, failed := bash(t, featRunAs(c.check, c.cwd, "abc"))
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if lines[len(lines)-1] != c.marker+" rfe-attempt=abc" || failed != c.failed {
			t.Errorf("%s: %q failed=%v", name, out, failed)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Fatal("the check ran although cd failed")
	}
}

// the owner's attestation: owner-only, for this revision and wording; rewording voids it
func TestVerifyOwnerAttestation(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [x] Looks right on the phone @Builder\n")
	st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]
	h := v.e.authed(v.e.login(t))
	if r := v.e.do(t, "POST", "/api/term/feature/verify", `{"session":"demo-feature","key":"`+st.Key+`","rev":"old"}`, h); r.StatusCode != 409 {
		t.Fatalf("stale: %d", r.StatusCode)
	}
	d := v.e.s.featDossier(supTeam)
	if r := v.e.do(t, "POST", "/api/term/feature/verify", `{"session":"demo-feature","key":"`+st.Key+`","rev":"`+d.Rev+`"}`, h); r.StatusCode != 200 {
		t.Fatalf("verify: %d", r.StatusCode)
	}
	if got := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; got.State != "done" || got.Label != "owner verified" {
		t.Fatalf("attested: %+v", got)
	}
	os.WriteFile(v.planFile(), []byte("## Ship\n- [x] Looks right on the phone and tablet @Builder\n"), 0o600)
	if got := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; got.State != "claimed" {
		t.Fatalf("reworded keeps the attestation: %+v", got)
	}
}

// ---- regressions found in review: each reproduces a bad outcome and asserts it no longer happens ----

func TestReviewBogusEvidenceRefused(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [x] Production is healthy @Builder\n  - evidence: bogus.txt\n  - result: success\n")
	writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "bogus.txt"), "success\n")
	if c, _ := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done"}`); c != 409 {
		t.Fatalf("bogus evidence completed the feature: %d", c)
	}
}

func TestReviewMixedMutationNotResumed(t *testing.T) {
	v := newSupEnv(t, "## Work\n- [~] Read logs @Builder\n  - kind: read\n- [~] Change DNS @Builder\n  - kind: mutation\n")
	v.approveKinds(t)
	v.look(v.agent("working", v.now.Unix()))
	v.look()
	v.look()
	if len(v.typed()) != 0 {
		t.Fatalf("resumed a conversation that owns an uncertain change: %v", v.typed())
	}
	for _, st := range v.e.s.featDossier(supTeam).Milestones[0].Steps {
		if st.Blocked == "" {
			t.Fatalf("not held: %+v", st)
		}
	}
	waitIdle(t, v.a, supTeam) // the hold's news is still reaching its lead
}

func TestReviewStaleQuestionRefused(t *testing.T) {
	v := newSupEnv(t, "## Work\n- [?] Choose target @Builder\n  - ask: Use sandbox?\n")
	d := v.e.s.featDossier(supTeam)
	st := d.Milestones[0].Steps[0]
	writeFile(t, v.planFile(), "## Work\n- [?] Choose target @Builder\n  - ask: Use production?\n")
	body := `{"session":"demo-feature","key":"` + st.Key + `","qid":"` + st.Qid + `","rev":"` + d.Rev + `","text":"Yes","resume":true}`
	if c, _ := call(v.e.s.termFeatureAnswer, body); c != 409 {
		t.Fatalf("a stale answer was taken: %d", c)
	}
	d2 := v.e.s.featDossier(supTeam) // the right revision, the old question: still refused
	body = `{"session":"demo-feature","key":"` + st.Key + `","qid":"` + st.Qid + `","rev":"` + d2.Rev + `","text":"Yes","resume":true}`
	if c, _ := call(v.e.s.termFeatureAnswer, body); c != 409 {
		t.Fatalf("an answer to the old question was taken: %d", c)
	}
	if b, _ := os.ReadFile(v.planFile()); strings.Contains(string(b), "answer:") {
		t.Fatalf("written: %s", b)
	}
	// "no" doesn't resume
	q := d2.Milestones[0].Steps[0]
	body = `{"session":"demo-feature","key":"` + q.Key + `","qid":"` + q.Qid + `","rev":"` + d2.Rev + `","text":"No","resume":false}`
	if c, out := call(v.e.s.termFeatureAnswer, body); c != 200 {
		t.Fatalf("answer: %d %v", c, out)
	}
	if got := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; got.State != "todo" {
		t.Fatalf("a no resumed it: %+v", got)
	}
}

func TestReviewDoneJudgedOnTheCommittedPlan(t *testing.T) {
	v := newSupEnv(t, "## Work\n- [x] Check output\n")
	d := v.e.s.featDossier(supTeam)
	st := d.Milestones[0].Steps[0]
	h := v.e.authed(v.e.login(t))
	v.e.do(t, "POST", "/api/term/feature/verify", `{"session":"demo-feature","key":"`+st.Key+`","rev":"`+d.Rev+`"}`, h)
	if c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done","plan":"- [ ] Check output"}`); c != 409 {
		t.Fatalf("done with a todo plan: %d %v", c, out)
	}
	v.a.mu.Lock()
	status := v.a.thread(supTeam).Status
	v.a.mu.Unlock()
	if b, _ := os.ReadFile(v.planFile()); status == "done" || !strings.Contains(string(b), "[x] Check output") {
		t.Fatalf("partly applied: %s %s", status, b)
	}
}

// identical plans in two features don't share holds, questions or deadlines
func TestFeaturesDontCollide(t *testing.T) {
	v := newSupEnv(t, "## Deploy\n- [~] Apply change @Builder\n  - kind: mutation\n")
	const other = "other-feature"
	os.WriteFile(filepath.Join(v.e.s.sys.sockDir, other), nil, 0o600)
	v.a.mu.Lock()
	o := v.a.newThread("Other feature", "")
	v.a.mu.Unlock()
	writeFile(t, filepath.Join(v.e.s.cfg.home, "jarvis-files", "threads", o.ID, "PLAN.md"), "## Deploy\n- [~] Apply change @Builder\n  - kind: mutation\n")
	v.look(v.agent("working", v.now.Unix()))
	v.look()
	v.look()
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.Blocked == "" {
		t.Fatal("not held here")
	}
	if st := v.e.s.featDossier(o.ID).Milestones[0].Steps[0]; st.Blocked != "" {
		t.Fatalf("the other feature is held too: %+v", st)
	}
}

// the agents' writer: rev required, FEATURE.md refused, log appended
func TestAgentFeatureWriter(t *testing.T) {
	v := newSupEnv(t, "## A\n- [ ] one\n")
	hd := v.e.s.agentHandler("tok")
	do := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		hd.ServeHTTP(w, req)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	c, out := do("GET", "/agent/feature?session="+supTeam, "")
	d, _ := out["dossier"].(map[string]any)
	if c != 200 || d["logWhole"] != true {
		t.Fatalf("get: %d %v", c, out)
	}
	rev := d["revs"].(map[string]any)["plan"].(string)
	if c, _ := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"plan","text":"x"}`); c != 400 {
		t.Fatalf("no rev: %d", c)
	}
	if c, _ := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"feature","text":"x","rev":"`+rev+`"}`); c != 403 {
		t.Fatalf("FEATURE.md: %d", c)
	}
	if c, out := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"plan","text":"## A\n- [~] one\n","rev":"`+rev+`"}`); c != 200 || out["rev"] == rev {
		t.Fatalf("write: %d %v", c, out)
	}
	if c, out := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"plan","text":"## A\n- [x] one\n","rev":"`+rev+`"}`); c != 409 || out["rev"] == "" {
		t.Fatalf("stale: %d %v", c, out)
	}
	if c, _ := do("POST", "/agent/feature/log", `{"session":"demo-feature","line":"Builder: started"}`); c != 200 {
		t.Fatalf("log: %d", c)
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(v.planFile()), "LOG.md")); !strings.Contains(string(b), "Builder: started") {
		t.Fatalf("log: %s", b)
	}
}

// each guard on its own: an unapproved check says so; a held tab's valid receipt still isn't done; the page needs a rev too
func TestVerifyGuardsEachOnTheirOwn(t *testing.T) {
	v := newSupEnv(t, "")
	work := t.TempDir()
	writeFile(t, v.planFile(), "## S\n- [x] a @Builder\n  - check: true\n  - cwd: "+work+"\n  - evidence: run abc\n")
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.State != "claimed" || !strings.Contains(st.Why, "isn't approved") {
		t.Fatalf("unapproved: %+v", st)
	}
	body, _ := json.Marshal(map[string]string{"session": supTeam, "doc": "plan", "text": "x"})
	if r := v.e.do(t, "POST", "/api/term/feature/doc", string(body), v.e.authed(v.e.login(t))); r.StatusCode != 409 {
		t.Fatalf("page write without rev: %d", r.StatusCode)
	}

	// a valid receipt on a held tab
	w, st := checkEnv(t)
	b, _ := os.ReadFile(w.planFile())
	os.WriteFile(w.planFile(), append(b, []byte("- [~] Change DNS @Builder\n  - kind: mutation\n")...), 0o600)
	w.look(w.agent("working", w.now.Unix()))
	w.look()
	w.look()
	out, _ := bash(t, st.RunAs)
	record(t, w.transcript(), "toolu_h", st.RunAs, out, false, false, time.Now())
	if got := w.tick(t, st, "run "+st.Attempt); got.State != "claimed" || !strings.HasPrefix(got.Why, "held") {
		t.Fatalf("held tab completed a step: %+v", got)
	}
}
