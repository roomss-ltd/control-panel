package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Guards for the feature record's integrity: the done check and its commit are one transaction, the owner's
// attestations and acceptance cover only what they reviewed, holds stay with the work, writers are validated, and
// the supervisor's wakes carry the record as it stands.

// attestStep has the owner verify step i of the first milestone, at the plan's current revision.
func attestStep(t *testing.T, v *supEnv, i int) {
	t.Helper()
	d := v.e.s.featDossier(supTeam)
	b, _ := json.Marshal(map[string]string{"session": supTeam, "key": d.Milestones[0].Steps[i].Key, "rev": d.Rev})
	if c, out := call(v.e.s.termFeatureVerify, string(b)); c != 200 {
		t.Fatalf("attest: %d %v", c, out)
	}
}

// preUpgrade makes the feature count as one the panel knew before objectives had ids (as the upgrade records it):
// these tests are of the untagged accepted-outcomes path, which only such features keep.
func preUpgrade(v *supEnv) {
	v.e.s.sup.mu.Lock()
	v.e.s.sup.ObjLegacy[supTeam] = true
	v.e.s.sup.mu.Unlock()
}

// acceptOutcomes has the owner list one desired outcome and accept it, with the plan's results as they stand.
func acceptOutcomes(t *testing.T, v *supEnv) {
	t.Helper()
	preUpgrade(v)
	writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "FEATURE.md"), "# Outcome\n- [ ] It works\n")
	d := v.e.s.featDossier(supTeam)
	if c, out := call(v.e.s.termFeatureAccept, `{"session":"`+supTeam+`","rev":"`+d.Revs["feature"]+`"}`); c != 200 {
		t.Fatalf("accept: %d %v", c, out)
	}
}

// A write made through the panel can't land between the done check and the commit: it waits for the whole update.
func TestFollowupDoneIsOneTransactionWithWriters(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [x] Check output\n")
	attestStep(t, v, 0)
	acceptOutcomes(t, v)
	orig := v.a.featDone
	wrote := make(chan error, 1)
	inDone := false
	v.a.featDone = func(th *thread, p []byte) string {
		why := orig(th, p)
		b, _ := os.ReadFile(v.planFile())
		go func() { wrote <- featWriteRev(v.planFile(), featRev(b), []byte("## Ship\n- [ ] Check output\n")) }()
		select {
		case err := <-wrote:
			t.Errorf("a writer got in between the done check and the commit: %v", err)
			inDone = true
		case <-time.After(300 * time.Millisecond):
		}
		return why
	}
	c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done"}`)
	if c != 200 {
		t.Fatalf("done: %d %v", c, out)
	}
	if !inDone {
		if err := <-wrote; err != nil { // after the commit, as a later edit
			t.Fatalf("the writer after the commit: %v", err)
		}
	}
}

// The owner's attestation covers the step as it was reviewed: its check, folder, owner and kind.
func TestFollowupAttestationBoundToScope(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [x] Check target @Builder\n  - check: test sandbox\n  - cwd: /sandbox\n- [ ] Other\n")
	attestStep(t, v, 0)
	b, _ := os.ReadFile(v.planFile())
	other := strings.Replace(string(b), "- [ ] Other", "- [~] Other", 1) // an unrelated edit keeps it
	if err := featWriteRev(v.planFile(), featRev(b), []byte(other)); err != nil {
		t.Fatal(err)
	}
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.State != "done" {
		t.Fatalf("an unrelated edit dropped the attestation: %+v", st)
	}
	for _, change := range []func(string) string{
		func(s string) string { return strings.ReplaceAll(s, "test sandbox", "test production") },
		func(s string) string { return strings.ReplaceAll(s, "/sandbox", "/production") },
		func(s string) string { return strings.ReplaceAll(s, "@Builder", "@Other") },
		func(s string) string {
			return strings.Replace(s, "  - cwd: /sandbox\n", "  - cwd: /sandbox\n  - kind: mutation\n", 1)
		},
	} {
		cur, _ := os.ReadFile(v.planFile())
		if err := featWriteRev(v.planFile(), featRev(cur), []byte(change(other))); err != nil {
			t.Fatal(err)
		}
		if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.State == "done" {
			t.Fatalf("attestation outlived a changed scope: %+v", st)
		}
	}
}

// A hold stays on the work it was put on, whoever the step is given to; and no feature with a hold is done.
func TestFollowupHoldSurvivesReassignment(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [x] Change DNS @Builder\n")
	attestStep(t, v, 0)
	st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]
	th := v.a.thread(supTeam)
	v.e.s.featHold(v.now, th, supTeam, "Builder", "unknown DNS mutation", []featPlanStep{st}, false)
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.State == "done" {
		t.Fatal("the hold didn't apply")
	}
	b, _ := os.ReadFile(v.planFile())
	if err := featWriteRev(v.planFile(), featRev(b), []byte(strings.ReplaceAll(string(b), "@Builder", "@Other"))); err != nil {
		t.Fatal(err)
	}
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.State == "done" || st.Blocked == "" {
		t.Fatalf("reassignment escaped the hold: %+v", st)
	}
	b, _ = os.ReadFile(v.planFile())
	if err := featWriteRev(v.planFile(), featRev(b), []byte("## Ship\n- [x] Change DNS records @Other\n")); err != nil {
		t.Fatal(err)
	}
	if c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done"}`); c != 409 || !strings.Contains(out["error"].(string), "held") {
		t.Fatalf("rewording escaped the hold: %d %v", c, out)
	}
}

// The feature's desired outcomes take part in done. They must be listed (prose defines nothing checkable); the owner
// accepts them once the plan's results are verified, bound to that outcome set and plan revision; a later change of
// either voids the acceptance, and a feature already done shows as stale rather than keeping an obsolete done.
func TestFollowupOutcomesNeedOwnerAcceptance(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [x] Read a log\n")
	preUpgrade(v)
	feat := filepath.Join(filepath.Dir(v.planFile()), "FEATURE.md")
	done := func() (int, string) {
		c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done"}`)
		e, _ := out["error"].(string)
		return c, e
	}
	accept := func() int {
		d := v.e.s.featDossier(supTeam)
		c, _ := call(v.e.s.termFeatureAccept, `{"session":"`+supTeam+`","rev":"`+d.Revs["feature"]+`"}`)
		return c
	}
	writeFile(t, feat, "# Goal\nProduction should be healthy.\n")
	attestStep(t, v, 0)
	if c, e := done(); c != 409 || !strings.Contains(e, "lists no desired outcomes") {
		t.Fatalf("prose-only FEATURE.md counted as no outcomes: %d %s", c, e)
	}
	writeFile(t, feat, "# Desired outcome\n- [ ] Production is healthy\n")
	if c, _ := done(); c != 409 {
		t.Fatalf("an outcome nobody accepted: %d", c)
	}
	d := v.e.s.featDossier(supTeam)
	if len(d.Outcomes) != 1 || d.Accepted {
		t.Fatalf("outcomes on the page: %+v %v", d.Outcomes, d.Accepted)
	}
	if c, _ := call(v.e.s.termFeatureAccept, `{"session":"`+supTeam+`","rev":"stale"}`); c != 409 {
		t.Fatalf("accept at a stale FEATURE.md: %d", c)
	}
	// results first: with a step not yet verified, acceptance is refused (no blanket leave to finish later)
	b, _ := os.ReadFile(v.planFile())
	featWriteRev(v.planFile(), featRev(b), []byte("## Ship\n- [x] Read a log\n- [ ] Check it\n"))
	if c := accept(); c != 409 {
		t.Fatalf("accepted before the results: %d", c)
	}
	b, _ = os.ReadFile(v.planFile())
	featWriteRev(v.planFile(), featRev(b), []byte("## Ship\n- [x] Read a log\n"))
	if c := accept(); c != 200 {
		t.Fatalf("accept: %d", c)
	}
	writeFile(t, feat, "# Desired outcome\n- [ ] Production is healthy\n- [ ] Costs are down\n")
	if c, _ := done(); c != 409 {
		t.Fatalf("a changed outcome set went through on the old acceptance: %d", c)
	}
	if c := accept(); c != 200 {
		t.Fatal("accept again")
	}
	if c, e := done(); c != 200 {
		t.Fatalf("accepted outcomes, verified plan: %d %s", c, e)
	}
	if d := v.e.s.featDossier(supTeam); d.DoneStale != "" {
		t.Fatalf("fresh done shown stale: %s", d.DoneStale)
	}
	b, _ = os.ReadFile(v.planFile()) // an edit after done: the done is shown stale, with why
	featWriteRev(v.planFile(), featRev(b), []byte("## Ship\n- [x] Read a log\n- [ ] Roll back the flag\n"))
	if d := v.e.s.featDossier(supTeam); d.DoneStale == "" || d.Accepted {
		t.Fatalf("an obsolete done kept its badge: stale=%q accepted=%v", d.DoneStale, d.Accepted)
	}
}

// A brief that couldn't even be looked at (zellij failed before typing) is tried again on the next look; once.
func TestFollowupBriefRetriedAfterTransientFailure(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Check output @Builder\n")
	dump := "--session " + supTeam + " action dump-screen"
	v.f.answers[dump] = "ERR:zellij is busy"
	briefs := func() int {
		n := 0
		for _, c := range v.f.calls {
			if j := strings.Join(c, " "); strings.Contains(j, " paste ") && strings.Contains(j, "source of truth") {
				n++
			}
		}
		return n
	}
	v.look(v.agent("waiting", v.now.Unix()))
	if briefs() != 0 {
		t.Fatal("pasted while the screen couldn't be read")
	}
	delete(v.f.answers, dump)
	v.look(v.agent("waiting", v.now.Unix()))
	v.look(v.agent("waiting", v.now.Unix()))
	if n := briefs(); n != 1 {
		t.Fatalf("the brief after a transient failure: pasted %d times", n)
	}
	waitIdle(t, v.a, supTeam)
}

// A thread's "needs you" whose notification couldn't be saved says so to its lead (the update itself stands).
func TestFollowupThreadNotificationSaveFailureSaid(t *testing.T) {
	v := newSupEnv(t, "")
	parent := filepath.Join(t.TempDir(), "not-a-folder")
	writeFile(t, parent, "x")
	v.e.s.notes.file = filepath.Join(parent, "notes.json")
	n := len(v.e.s.notes.since(0))
	c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"needs_you","note":"pick a target"}`)
	if c != 200 || out["warning"] == nil {
		t.Fatalf("a lost notification went unsaid: %d %v", c, out)
	}
	if len(v.e.s.notes.since(0)) != n {
		t.Fatal("an unsaved note stayed in memory")
	}
	waitIdle(t, v.a, supTeam)
}

func agentDo(t *testing.T, v *supEnv) func(method, path, body string) (int, map[string]any) {
	hd := v.e.s.agentHandler("tok")
	return func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		hd.ServeHTTP(w, req)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
}

// A writer naming the thread it believes manages the feature is refused, before writing, when another (or none) does.
func TestAgentFeatureExpectedThread(t *testing.T) {
	v := newSupEnv(t, "## A\n- [ ] one\n")
	do := agentDo(t, v)
	before, _ := os.ReadFile(v.planFile())
	rev := featRev(before)
	if c, out := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"plan","text":"## A\n- [~] one\n","rev":"`+rev+`","thread":"other-thread"}`); c != 409 || out["thread"] != "demo-feature" {
		t.Fatalf("wrong thread: %d %v", c, out)
	}
	if b, _ := os.ReadFile(v.planFile()); string(b) != string(before) {
		t.Fatal("a refused write wrote")
	}
	logBefore, _ := os.ReadFile(filepath.Join(filepath.Dir(v.planFile()), "LOG.md"))
	if c, _ := do("POST", "/agent/feature/log", `{"session":"demo-feature","line":"x","thread":"other-thread"}`); c != 409 {
		t.Fatalf("log, wrong thread: %d", c)
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(v.planFile()), "LOG.md")); string(b) != string(logBefore) {
		t.Fatal("a refused log line was written")
	}
	if c, out := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"plan","text":"## A\n- [~] one\n","rev":"`+rev+`","thread":"demo-feature"}`); c != 200 {
		t.Fatalf("right thread: %d %v", c, out)
	}
	if c, _ := do("POST", "/agent/feature/log", `{"session":"demo-feature","line":"y"}`); c != 200 {
		t.Fatalf("no thread named (a worker): %d", c)
	}
}

// The team as one sample saw it: its time, not the request's; exact @Tab ownership; duplicate tabs flagged.
func TestAgentFeatureTeam(t *testing.T) {
	v := newSupEnv(t, "## A\n- [ ] one @Builder\n- [ ] two @Build\n")
	do := agentDo(t, v)
	if c, out := do("GET", "/agent/feature/team?session="+supTeam, ""); c != 200 || out["observedAt"] != 0.0 || out["stale"] != true || out["present"] != false {
		t.Fatalf("before any sample: %d %v", c, out)
	}
	if c, _ := do("GET", "/agent/feature/team?session=nobody-here", ""); c != 404 {
		t.Fatalf("unknown session: %d", c)
	}
	taken := time.Now().Add(-5 * time.Minute)
	ag := func(id int) TPane { p := v.agent("working", 1); p.ID = id; return p }
	v.e.s.term.mu.Lock()
	v.e.s.term.last = Terminals{Sessions: []TSession{{Name: supTeam, Tabs: []TTab{{ID: 1, Name: "Builder", Panes: []TPane{ag(3)}}, {ID: 2, Name: "builder", Panes: []TPane{ag(4)}}}}}}
	v.e.s.term.lastAt = taken
	v.e.s.term.mu.Unlock()
	c, out := do("GET", "/agent/feature/team?session="+supTeam, "")
	if c != 200 || int64(out["observedAt"].(float64)) != taken.Unix() || out["stale"] != true || out["present"] != true {
		t.Fatalf("sampled: %d %v", c, out)
	}
	team := out["team"].([]any)
	if len(team) != 2 {
		t.Fatalf("team: %v", team)
	}
	for _, m := range team {
		m := m.(map[string]any)
		if m["ambiguousTab"] != true || len(m["owns"].([]any)) != 1 {
			t.Fatalf("member %v: duplicate tab not flagged, or @Build owns Builder's steps", m)
		}
	}
}

// A lead's team update wakes it with the record as it stands: revision, freshness, holds, questions and what can move.
func TestFollowupWakeCarriesTheRecordNow(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [~] Change DNS @Builder\n- [ ] Write docs @Writer\n- [?] Pick a region @Writer\n  - ask: eu or us?\n")
	d := v.e.s.featDossier(supTeam)
	v.e.s.featHold(v.now, v.a.thread(supTeam), supTeam, "Builder", "unknown DNS change", []featPlanStep{d.Milestones[0].Steps[0]}, false)
	v.e.s.teamDeliver(v.now)
	got := strings.Join(v.lead(t), "\n")
	for _, want := range []string{"[Record snapshot as of ", "Read current feature_record/get_feature_plan before acting; this may be a retried hand-off.", "PLAN.md rev " + d.Rev, "last sampled never yet", "Held until the owner releases them",
		"unknown DNS change", "Waiting on the owner's answer", "Can move next", d.Milestones[0].Steps[1].Key + ` "Write docs" @Writer [todo]`,
		"lists no desired outcomes", "Work from the current record"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the wake lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "requirements draft") {
		t.Fatal("a draft mentioned when there is none")
	}
	writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "FEATURE.draft.md"), "# Proposed\n- [ ] Faster\n")
	v.a.mu.Lock()
	th := *v.a.thread(supTeam)
	v.a.mu.Unlock()
	if w := v.e.s.featWake(&th); !strings.Contains(w, "requirements draft") || !strings.Contains(w, "FEATURE.md stays the approved") {
		t.Fatalf("a waiting draft unsaid: %s", w)
	}
	if strings.Contains(got[strings.Index(got, "Can move next"):], "Change DNS") {
		t.Fatal("held work offered as next")
	}
}

// A done feature whose record changes reopens: through the writer, through thread_update, straight in the files (caught
// on read and on the next look). The earlier done stays in its timeline; approvals of unchanged steps stay; nothing runs.
func TestFollowupDoneReopensWhenTheRecordChanges(t *testing.T) {
	setup := func(t *testing.T) *supEnv {
		v := newSupEnv(t, "## Ship\n- [x] Check output\n")
		attestStep(t, v, 0)
		acceptOutcomes(t, v)
		if c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done"}`); c != 200 {
			t.Fatalf("done: %d %v", c, out)
		}
		return v
	}
	reopened := func(t *testing.T, v *supEnv, how string) {
		t.Helper()
		v.a.mu.Lock()
		th := *v.a.thread(supTeam)
		v.a.mu.Unlock()
		if th.Status != "working" || !strings.Contains(th.Note, reopenWhy) {
			t.Fatalf("%s: still %s (%s)", how, th.Status, th.Note)
		}
		var done, again bool
		for _, e := range th.Log {
			done = done || e.Kind == "status" && e.Text == "done"
			again = again || strings.HasPrefix(e.Text, "reopened: ")
		}
		if !done || !again {
			t.Fatalf("%s: history lost: %+v", how, th.Log)
		}
		if len(v.typed()) != 0 {
			t.Fatalf("%s: something was launched: %v", how, v.typed())
		}
		waitIdle(t, v.a, supTeam)
	}
	t.Run("writer adds a step", func(t *testing.T) {
		v := setup(t)
		do := agentDo(t, v)
		b, _ := os.ReadFile(v.planFile())
		if c, out := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"plan","text":"## Ship\n- [x] Check output\n- [ ] Roll out\n","rev":"`+featRev(b)+`"}`); c != 200 {
			t.Fatalf("write: %d %v", c, out)
		}
		reopened(t, v, "writer")
		if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.State != "done" {
			t.Fatalf("the unchanged step lost its attestation: %+v", st)
		}
	})
	t.Run("thread_update unticks", func(t *testing.T) {
		v := setup(t)
		if c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","plan":"- [ ] Check output"}`); c != 200 {
			t.Fatalf("update: %d %v", c, out)
		}
		reopened(t, v, "thread_update")
	})
	t.Run("outcomes change in the file, caught on read", func(t *testing.T) {
		v := setup(t)
		writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "FEATURE.md"), "# Outcome\n- [ ] It works\n- [ ] It's fast\n")
		agentDo(t, v)("GET", "/agent/threads", "")
		reopened(t, v, "read")
	})
	t.Run("plan edited in the file, caught on the next look", func(t *testing.T) {
		v := setup(t)
		writeFile(t, v.planFile(), "## Ship\n- [x] Check output\n- [ ] One more\n")
		v.look()
		reopened(t, v, "look")
	})
	t.Run("an unrelated FEATURE.md prose edit doesn't reopen", func(t *testing.T) {
		v := setup(t)
		writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "FEATURE.md"), "# Outcome (reworded heading)\n- [ ] It works\n")
		v.look()
		v.a.mu.Lock()
		st := v.a.thread(supTeam).Status
		v.a.mu.Unlock()
		if st != "done" {
			t.Fatalf("reopened on prose: %s", st)
		}
		waitIdle(t, v.a, supTeam)
	})
}

// The reconcile wake: an idle team with work that can move wakes its lead with the record; again on the same record
// only after a growing pause (persisted across a restart); never for questions waiting on the owner, a held step, a
// running job, or a lead still in its turn.
func TestFollowupReconcileWake(t *testing.T) {
	briefed := map[*supEnv]bool{}
	idleLook := func(v *supEnv) {
		if !briefed[v] { // the first look briefs the agent (a LOG.md line: activity), as it should
			briefed[v] = true
			v.look(v.agent("waiting", v.now.Unix()))
		}
		old := v.now.Add(-time.Hour)
		for _, n := range []string{"PLAN.md", "LOG.md", "TESTS.md"} {
			os.Chtimes(filepath.Join(filepath.Dir(v.planFile()), n), old, old)
		}
		p := v.agent("waiting", old.Unix())
		p.Agent.LastAt = old.Unix()
		waitIdle(t, v.a, supTeam) // a lead in its turn isn't woken (a subtest of its own checks that)
		v.look(p)
	}
	wakes := func(v *supEnv) int {
		n := 0
		for _, x := range v.notes("feature") {
			if strings.Contains(x.Title, "can move") {
				n++
			}
		}
		return n
	}
	t.Run("wakes, then backs off, across a restart", func(t *testing.T) {
		v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
		idleLook(v)
		if wakes(v) != 1 {
			t.Fatalf("no wake: %+v", v.notes(""))
		}
		if l := strings.Join(v.lead(t), "\n"); !strings.Contains(l, "[Record snapshot as of ") || !strings.Contains(l, "Write docs") {
			t.Fatalf("the wake didn't carry the record: %s", l)
		}
		v.e.s.sup = newFeatSup(v.e.s.sup.file) // a restart, straight after the wake
		idleLook(v)
		if wakes(v) != 1 {
			t.Fatalf("woke again on the same record at once: %d", wakes(v))
		}
		v.now = v.now.Add(46 * time.Minute)
		idleLook(v)
		if wakes(v) != 2 {
			t.Fatalf("no second wake after the pause: %d", wakes(v))
		}
		v.now = v.now.Add(46 * time.Minute) // the next pause is longer (90 min)
		idleLook(v)
		if wakes(v) != 2 {
			t.Fatalf("no backoff: %d", wakes(v))
		}
	})
	for name, plan := range map[string]string{
		"only a question for the owner": "## Ship\n- [?] Pick a region @Builder\n  - ask: eu or us?\n",
		"only verified work":            "",
	} {
		t.Run(name, func(t *testing.T) {
			v := newSupEnv(t, plan)
			idleLook(v)
			if wakes(v) != 0 {
				t.Fatalf("woke for nothing movable: %+v", v.notes(""))
			}
		})
	}
	t.Run("a running job", func(t *testing.T) {
		v := newSupEnv(t, "## Ship\n- [~] Nightly build @Builder\n")
		old := v.now.Add(-time.Hour)
		os.Chtimes(v.planFile(), old, old)
		p := v.agent("working", old.Unix())
		v.look(p)
		if wakes(v) != 0 {
			t.Fatal("woke over a running job")
		}
	})
	// lanes: a busy or blocked tab neither hides another tab's idle work nor is touched
	lanes := func(t *testing.T, v *supEnv, builder TPane) {
		t.Helper()
		v.look(v.agent("waiting", v.now.Unix())) // the brief
		waitIdle(t, v.a, supTeam)
		old := v.now.Add(-time.Hour)
		os.Chtimes(v.planFile(), old, old)
		v.now = v.now.Add(31 * time.Second)
		tm := Terminals{Sessions: []TSession{{Name: supTeam, Tabs: []TTab{{ID: 1, Name: "Builder", Panes: []TPane{builder}}}}}}
		v.e.s.term.mu.Lock()
		v.e.s.term.last, v.e.s.term.list = tm, tm
		v.e.s.term.mu.Unlock()
		v.e.s.observeTeams(v.now, tm, CI{}, Resources{})
		var got []Note
		for _, x := range v.notes("feature") {
			if strings.Contains(x.Title, "can move") {
				got = append(got, x)
			}
		}
		if len(got) != 1 || !strings.Contains(got[0].Body, `"Write docs"`) || strings.Contains(got[0].Body, "Nightly") || strings.Contains(got[0].Body, "Pick a region") {
			t.Fatalf("the idle lane's wake: %+v", got)
		}
		if len(v.typed()) != 0 {
			t.Fatalf("the busy lane was touched: %v", v.typed())
		}
	}
	t.Run("an owner-blocked lane and an idle one", func(t *testing.T) {
		v := newSupEnv(t, "## Ship\n- [?] Pick a region @Builder\n  - ask: eu or us?\n- [ ] Write docs @Writer\n")
		p := v.agent("asking", v.now.Unix())
		lanes(t, v, p)
	})
	t.Run("a thread the lead set to needs_you still wakes for another lane's idle work", func(t *testing.T) {
		v := newSupEnv(t, "## Ship\n- [?] Pick a region @Builder\n  - ask: eu or us?\n- [ ] Write docs @Writer\n")
		v.a.mu.Lock()
		v.a.thread(supTeam).Status = "needs_you"
		v.a.mu.Unlock()
		lanes(t, v, v.agent("asking", v.now.Unix()))
	})
	t.Run("a healthy long job and an idle lane", func(t *testing.T) {
		v := newSupEnv(t, "## Ship\n- [~] Nightly build @Builder\n- [ ] Write docs @Writer\n")
		p := v.agent("working", v.now.Add(-2*time.Hour).Unix())
		p.Output, p.CPU = v.now.Unix(), 95
		lanes(t, v, p)
	})
	t.Run("a lead still in its turn", func(t *testing.T) {
		v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
		v.look(v.agent("waiting", v.now.Unix())) // the brief
		waitIdle(t, v.a, supTeam)
		v.a.mu.Lock()
		v.a.conv(supTeam).queue = append(v.a.conv(supTeam).queue, aiQueued{Text: "busy"})
		v.a.mu.Unlock()
		old := v.now.Add(-time.Hour)
		os.Chtimes(v.planFile(), old, old)
		os.Chtimes(filepath.Join(filepath.Dir(v.planFile()), "LOG.md"), old, old)
		p := v.agent("waiting", old.Unix())
		p.Agent.LastAt = old.Unix()
		v.look(p)
		v.a.mu.Lock()
		v.a.conv(supTeam).queue = nil
		v.a.mu.Unlock()
		if wakes(v) != 0 {
			t.Fatal("woke a lead mid-turn")
		}
	})
}

// The panel finds zellij's sessions where zellij puts them: under $TMPDIR (else /tmp), not a fixed /tmp.
func TestSockDirFollowsTmpdir(t *testing.T) {
	d := t.TempDir()
	want := filepath.Join(d, fmt.Sprintf("zellij-%d", os.Getuid()), "contract_version_1")
	os.MkdirAll(want, 0o700)
	t.Setenv("TMPDIR", d)
	if got := defaultSockDir(); got != want {
		t.Fatalf("socket dir %q, want %q", got, want)
	}
}

// thread_update answers with the plan as the record has it (what thread_list shows), so its caller sees the truth.
func TestAgentThreadUpdateAnswersWithTheRecordPlan(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [~] Write docs @Builder\n- [ ] Ship it\n")
	c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","note":"looking"}`)
	plan, _ := out["thread"].(map[string]any)["plan"].([]any)
	if c != 200 || len(plan) != 2 || plan[0].(map[string]any)["state"] != "doing" {
		t.Fatalf("thread_update's answer: %d %v", c, out)
	}
}
