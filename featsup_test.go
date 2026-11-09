package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests of the feature supervisor (featsup.go): approval stalls, agent exits and resumes, holds, delivery to the lead,
// owner answers, idle-team wakes and missed checkpoints. Each case is driven through what the sampling loop itself
// calls (observeTeams) and the real handlers, on an isolated home, fake zellij and fake Hermes.

type supEnv struct {
	e     *testEnv
	a     *assistant
	turns func(string) []map[string]any
	f     *fakeZellij
	now   time.Time
	cwd   string
}

const supTeam = "demo-feature"

func newSupEnv(t *testing.T, plan string) *supEnv {
	t.Helper()
	e := newTestServer(t)
	a, turns := threadAssistant(t)
	e.s.ai = a
	e.s.hookRecord()
	teamPending.Lock()
	teamPending.m = map[string]teamHand{}
	teamPending.Unlock()
	a.mu.Lock()
	th := a.newThread("Demo feature", "")
	th.Status, th.Created = "working", 1
	a.saveThreads()
	a.mu.Unlock()
	os.WriteFile(filepath.Join(e.s.sys.sockDir, supTeam), nil, 0o600)
	if plan != "" {
		writeFile(t, filepath.Join(e.s.cfg.home, "jarvis-files", "threads", supTeam, "PLAN.md"), plan)
	}
	f := e.fakeZ(map[string]string{
		"--session " + supTeam + " action list-clients": "CLIENT_ID\n1 term",
		"--session " + supTeam + " action new-tab":      "3\n",
		"--session " + supTeam + " action list-panes":   `[{"id":11,"tab_id":3,"tab_name":"Builder"}]`,
	})
	e.s.notes.push("ci", "bad", "older news", "", "ci") // the bell has history: delivery starts after it
	t.Cleanup(func() {                                  // a lead's turn still writing would race the temp folders' removal: give it a moment (not a check)
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			a.mu.Lock()
			cv := a.conv(supTeam)
			idle := cv.live == nil && len(cv.queue) == 0
			a.mu.Unlock()
			if idle {
				return
			}
		}
	})
	return &supEnv{e: e, a: a, turns: turns, f: f, now: time.Now(), cwd: t.TempDir()}
}

func (v *supEnv) agent(state string, since int64) TPane {
	return TPane{ID: 3, Kind: "claude", State: state, Since: since, Agent: &TAgent{PID: 42, sid: "aaaa-bbbb-1", startCwd: v.cwd, path: filepath.Join(v.cwd, "transcript.jsonl")}}
}

// look: one tick's worth, 31 s after the last
func (v *supEnv) look(panes ...TPane) {
	v.now = v.now.Add(31 * time.Second)
	tm := Terminals{Sessions: []TSession{{Name: supTeam, Tabs: []TTab{{ID: 1, Name: "Builder", Panes: panes}}}}}
	v.e.s.term.mu.Lock()
	v.e.s.term.last, v.e.s.term.list = tm, tm
	v.e.s.term.mu.Unlock()
	v.e.s.observeTeams(v.now, tm, CI{}, Resources{})
}

func (v *supEnv) notes(kind string) []Note {
	var out []Note
	for _, n := range v.e.s.notes.since(0) {
		if kind == "" || n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

// the lead's turns so far (waits for the first)
func (v *supEnv) lead(t *testing.T) []string {
	t.Helper()
	got := v.turns("thread-" + supTeam)
	waitIdle(t, v.a, supTeam)
	var out []string
	for _, g := range v.turns("thread-" + supTeam) {
		out = append(out, g["message"].(string))
	}
	_ = got
	return out
}

func (v *supEnv) typed() []string {
	var out []string
	for _, c := range v.f.calls {
		for _, a := range c[:len(c)-1] { // the zellij action, never the text typed (a brief saying "skills" isn't a kill)
			if a == "write-chars" || a == "close-pane" || strings.HasPrefix(a, "kill") {
				out = append(out, c[len(c)-1])
				break
			}
		}
	}
	return out
}

func (v *supEnv) planFile() string {
	return filepath.Join(v.e.s.cfg.home, "jarvis-files", "threads", supTeam, "PLAN.md")
}

func call(h http.HandlerFunc, body string) (int, map[string]any) {
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// Approval stall: a dialog left waiting is raised once, to the owner and the lead
func TestSupApprovalStall(t *testing.T) {
	v := newSupEnv(t, "")
	since := v.now.Unix()
	v.look(v.agent("asking", since))
	if n := v.notes("thread"); len(n) != 0 {
		t.Fatalf("a fresh dialog is not yet a stall: %+v", n)
	}
	for range 8 { // 4 min
		v.look(v.agent("asking", since))
	}
	n := v.notes("thread")
	if len(n) != 1 || !strings.HasPrefix(n[0].Ref, supTeam+"/approval:"+supTeam+":3:") || !strings.Contains(n[0].Title, "on a dialog") {
		t.Fatalf("approval stall: %+v", n)
	}
	if l := v.lead(t); !strings.Contains(l[len(l)-1], "on a dialog") || !strings.Contains(l[len(l)-1], "#"+itoa(int(n[0].ID))) {
		t.Fatalf("lead told: %q", l)
	}
	v.look(v.agent("asking", since))
	if len(v.notes("thread")) != 1 {
		t.Fatal("raised twice")
	}
}

// Agent exit on a safe step: resumed in its own conversation, once; the lead hears both
func TestSupAgentExitResumesSafeStep(t *testing.T) {
	v := newSupEnv(t, "## Build\n- [~] Run the build @Builder\n  - kind: build\n")
	v.approveKinds(t)
	v.look(v.agent("working", v.now.Unix()))
	v.look()
	if len(v.notes("exited")) != 0 {
		t.Fatal("one missing look is not an exit yet")
	}
	v.look()
	if ex := v.notes("exited"); len(ex) != 1 || ex[0].Exit == nil || ex[0].Exit.Sid != "aaaa-bbbb-1" || ex[0].Exit.Tab != "Builder" {
		t.Fatalf("exit note: %+v", ex)
	}
	if got := strings.Join(v.typed(), " | "); !v.isResume(got) {
		t.Fatalf("resume typed: %q", got)
	}
	l := strings.Join(v.lead(t), "\n")
	if !strings.Contains(l, "exited mid-work") || !strings.Contains(l, "resumed Builder for its safe steps only") {
		t.Fatalf("lead: %s", l)
	}
	v.look()
	v.look()
	if len(v.typed()) != 1 {
		t.Fatalf("resumed twice: %v", v.typed())
	}
	if b, _ := os.ReadFile(v.e.audit); !strings.Contains(string(b), "feature-resume") {
		t.Fatal("not audited")
	}
}

// Restart: the panel was down while the agent died; the saved look still sees it and the resume happens
func TestSupRestartResume(t *testing.T) {
	v := newSupEnv(t, "## Build\n- [~] Run the tests @Builder\n  - kind: test\n")
	v.approveKinds(t)
	v.look(v.agent("working", v.now.Unix()))
	// restart: notes, supervisor memory and threads come back from their files
	v.e.s.notes = newNotifier(v.e.s.notes.file)
	v.e.s.sup = newFeatSup(v.e.s.sup.file)
	b := newAssistant(v.a.base, v.a.keyFile, "")
	b.threadsFile, b.leadFile = v.a.threadsFile, v.a.leadFile
	b.loadThreads()
	v.e.s.ai, v.a = b, b
	v.e.s.hookRecord()
	v.look()
	v.look()
	if got := strings.Join(v.typed(), " | "); !v.isResume(got) {
		t.Fatalf("resume after restart: %q", got)
	}
	if l := strings.Join(v.lead(t), "\n"); !strings.Contains(l, "resumed Builder") {
		t.Fatalf("lead: %s", l)
	}
}

// Mutation uncertainty: the whole tab is held, nothing replayed, other tabs go on; agent prose doesn't release it,
// the owner does (which authorizes nothing new); a receipt line makes no difference
func TestSupMutationUncertaintyBlocks(t *testing.T) {
	plan := "## Stage\n- [~] Apply the DNS change @Builder\n  - kind: mutation\n- [~] Read the logs @Other\n  - kind: read\n"
	v := newSupEnv(t, plan)
	v.look(v.agent("working", v.now.Unix()))
	v.look()
	v.look()
	if len(v.typed()) != 0 {
		t.Fatalf("a mutation was replayed: %v", v.typed())
	}
	bl := v.notes("thread")
	if len(bl) != 1 || bl[0].Level != "bad" || !strings.Contains(bl[0].Title, "held") {
		t.Fatalf("hold: %+v", bl)
	}
	ms := v.e.s.featDossier(supTeam).Milestones[0].Steps
	if ms[0].Blocked == "" || ms[1].Blocked != "" || ms[1].State != "doing" {
		t.Fatalf("held tab / independent tab: %+v", ms)
	}
	// the agent's own "reconciled" or ticking it with a reworded step doesn't release it
	os.WriteFile(v.planFile(), []byte("## Stage\n- [x] Apply the DNS change, reworded @Builder\n  - kind: mutation\n  - reconciled: all fine\n- [~] Read the logs @Other\n  - kind: read\n"), 0o600)
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.Blocked == "" || st.State == "done" {
		t.Fatalf("prose released the hold: %+v", st)
	}
	h := v.e.authed(v.e.login(t))
	d := v.e.s.featDossier(supTeam)
	if r := v.e.do(t, "POST", "/api/term/feature/release", `{"session":"demo-feature","tab":"Builder","rev":"stale"}`, h); r.StatusCode != 409 {
		t.Fatalf("stale release: %d", r.StatusCode)
	}
	if r := v.e.do(t, "POST", "/api/term/feature/release", `{"session":"demo-feature","tab":"Builder","rev":"`+d.Rev+`"}`, h); r.StatusCode != 200 {
		t.Fatalf("owner release: %d", r.StatusCode)
	}
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.Blocked != "" || st.State != "claimed" {
		t.Fatalf("released (still only a claim): %+v", st)
	}
	if l := strings.Join(v.lead(t), "\n"); !strings.Contains(l, "authorizes no change") {
		t.Fatalf("lead: %s", l)
	}

	// with a receipt named: held all the same, nothing replayed
	w := newSupEnv(t, "## Stage\n- [~] Rotate the key @Builder\n  - kind: mutation\n  - receipt: receipt.json\n")
	writeFile(t, filepath.Join(filepath.Dir(w.planFile()), "receipt.json"), `{"version":3}`)
	w.look(w.agent("working", w.now.Unix()))
	w.look()
	w.look()
	if r := w.notes("thread"); len(r) != 1 || !strings.Contains(r[0].Body, "receipt") || len(w.typed()) != 0 {
		t.Fatalf("receipt: %+v %v", r, w.typed())
	}
}

// Callback lost and duplicates: the cursor moves only once the lead has the message; restart replays; never twice
func TestSupDeliveryDurableAndDeduped(t *testing.T) {
	v := newSupEnv(t, "")
	v.look(v.agent("working", v.now.Unix())) // starts the lead's cursor
	v.a.mu.Lock()
	cv := v.a.conv(supTeam)
	cv.live = &aiLive{}
	for range 10 {
		cv.queue = append(cv.queue, aiQueued{Text: "busy"})
	}
	v.a.mu.Unlock()
	v.look(v.agent("waiting", v.now.Unix())) // done; its lead's queue is full: not handed over
	v.a.mu.Lock()
	seen := v.a.thread(supTeam).TeamSeen
	v.a.mu.Unlock()
	done := v.notes("done")
	if len(done) != 1 || seen >= done[0].ID {
		t.Fatalf("cursor moved past an undelivered note: seen %d, notes %+v", seen, done)
	}
	// the lead frees up but its queue is lost with a restart: the note goes again
	v.a.mu.Lock()
	cv.queue = nil
	v.a.mu.Unlock()
	v.e.s.teamDeliver(v.now) // queued behind the live turn
	b := newAssistant(v.a.base, v.a.keyFile, "")
	b.threadsFile, b.leadFile = v.a.threadsFile, v.a.leadFile
	b.loadThreads()
	v.e.s.ai, v.a = b, b
	v.e.s.hookRecord()
	teamPending.Lock()
	teamPending.m = map[string]teamHand{}
	teamPending.Unlock()
	v.e.s.teamDeliver(v.now)
	l := v.lead(t)
	if len(l) != 1 || !strings.Contains(l[0], "#"+itoa(int(done[0].ID))+" done") {
		t.Fatalf("after restart: %q", l)
	}
	v.e.s.teamDeliver(v.now)
	v.e.s.teamDeliver(v.now)
	if l := v.lead(t); len(l) != 1 {
		t.Fatalf("delivered twice: %q", l)
	}
	// a raised event is raised once, across a restart of the supervisor too
	th := &thread{ID: supTeam, Title: "Demo feature"}
	v.e.s.raise(v.now, th, true, "x:1", "warn", "once", "")
	v.e.s.sup = newFeatSup(v.e.s.sup.file)
	v.e.s.raise(v.now, th, true, "x:1", "warn", "once", "")
	if n := v.notes("thread"); len(n) != 1 {
		t.Fatalf("duplicate: %+v", n)
	}
}

// Owner reply: lands on the asked step only, resumes it, and the lead hears it scoped; chat or time don't answer
func TestSupOwnerReplyResumesStep(t *testing.T) {
	plan := "## Stage\n- [?] Pick the test database @Builder\n  - ask: Which database should stage use?\n- [~] Build the image @Other\n  - kind: build\n"
	v := newSupEnv(t, plan)
	v.look(v.agent("waiting", v.now.Unix()))
	v.look(v.agent("waiting", v.now.Unix()))
	q := v.notes("thread")
	if len(q) != 1 || !strings.Contains(q[0].Body, "Which database") {
		t.Fatalf("question raised: %+v", q)
	}
	v.a.send(supTeam, "[From the owner, in the panel] sure, whatever") // ordinary chat
	waitIdle(t, v.a, supTeam)
	for range 20 {
		v.look(v.agent("waiting", v.now.Unix()))
	}
	if b, _ := os.ReadFile(v.planFile()); string(b) != plan {
		t.Fatalf("chat or time changed the plan:\n%s", b)
	}
	d := v.e.s.featDossier(supTeam)
	key, other := d.Milestones[0].Steps[0].Key, d.Milestones[0].Steps[1].Key
	h := v.e.authed(v.e.login(t))
	qid, rev := d.Milestones[0].Steps[0].Qid, d.Rev
	ans := func(key, text string) string {
		return `{"session":"demo-feature","key":"` + key + `","qid":"` + qid + `","rev":"` + rev + `","text":"` + text + `","resume":true}`
	}
	if r := v.e.do(t, "POST", "/api/term/feature/answer", ans(other, "x"), h); r.StatusCode != 409 {
		t.Fatalf("an answer to a step that asks nothing: %d", r.StatusCode)
	}
	if r := v.e.do(t, "POST", "/api/term/feature/answer", `{"session":"demo-feature","key":"`+key+`","text":"Use the Foo sandbox"}`, h); r.StatusCode != 400 {
		t.Fatalf("no qid/rev/resume: %d", r.StatusCode)
	}
	if r := v.e.do(t, "POST", "/api/term/feature/answer", ans(key, "Use the Foo sandbox"), h); r.StatusCode != 200 {
		t.Fatalf("answer: %d", r.StatusCode)
	}
	b, _ := os.ReadFile(v.planFile())
	if !strings.Contains(string(b), "- [~] Pick the test database @Builder\n  - ask: Which database should stage use?\n  - answer: Use the Foo sandbox (owner, ") ||
		!strings.Contains(string(b), "- [~] Build the image @Other\n  - kind: build") {
		t.Fatalf("plan after the answer:\n%s", b)
	}
	if r := v.e.do(t, "POST", "/api/term/feature/answer", ans(key, "again"), h); r.StatusCode != 409 {
		t.Fatalf("answered twice: %d", r.StatusCode)
	}
	v.look(v.agent("waiting", v.now.Unix()))
	l := strings.Join(v.lead(t), "\n")
	if !strings.Contains(l, "A: Use the Foo sandbox") || !strings.Contains(l, "chose to resume this step") || !strings.Contains(l, "authorizes nothing else") {
		t.Fatalf("lead: %s", l)
	}
}

// A healthy long job is never stopped, nor called stuck, however long it runs; an idle team is nudged, not killed
func TestSupHealthyLongJobLeftAlone(t *testing.T) {
	v := newSupEnv(t, "## Build\n- [~] Nightly build @Builder\n  - kind: build\n")
	start := v.now.Unix()
	for range 400 { // ~3.4 h of looks
		p := v.agent("working", start)
		p.Output, p.CPU = v.now.Unix(), 95
		v.look(p)
	}
	for _, n := range v.notes("") {
		if n.Kind != "ci" {
			t.Fatalf("a working job raised %+v", n)
		}
	}
	if len(v.typed()) != 0 {
		t.Fatalf("touched a working agent: %v", v.typed())
	}
	// the same team, idle with nothing written for an hour: its lead is nudged (not the owner), nothing is touched
	old := v.now.Add(-time.Hour)
	os.Chtimes(v.planFile(), old, old)
	idle := v.agent("waiting", old.Unix())
	idle.Agent.LastAt = old.Unix()
	v.look(idle)
	v.look(idle)
	if n := v.notes("feature"); len(n) != 1 || !strings.Contains(n[0].Title, "idle") || len(v.notes("thread")) != 0 || len(v.typed()) != 0 {
		t.Fatalf("stagnation: %+v", v.notes(""))
	}
}

// Missed promised checkpoint: raised once, to the owner and the lead
func TestSupMissedCheckpoint(t *testing.T) {
	v := newSupEnv(t, "## Stage\n- [~] Telemetry live @Builder\n  - due: 2026-01-01 09:00\n- [ ] Later @Builder\n  - due: 2999-01-01\n")
	v.look(v.agent("working", v.now.Unix()))
	v.look(v.agent("working", v.now.Unix()))
	n := v.notes("thread")
	if len(n) != 1 || !strings.Contains(n[0].Title, "missed the checkpoint promised for 2026-01-01 09:00") {
		t.Fatalf("missed: %+v", n)
	}
	if l := strings.Join(v.lead(t), "\n"); !strings.Contains(l, "missed the checkpoint") {
		t.Fatalf("lead: %s", l)
	}
}

// One record: a worker's report, the feature page and the manager all read and write the same PLAN.md
func TestFeatureRecordIsOneSource(t *testing.T) {
	plan := "## Build\n- [~] Write the file @Builder\n  - kind: build\n- [ ] Review it @Reviewer\n"
	v := newSupEnv(t, plan)
	v.look(v.agent("waiting", v.now.Unix()))
	h := v.e.authed(v.e.login(t))
	get := func() featFacts {
		r := v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", h)
		var f featFacts
		json.NewDecoder(r.Body).Decode(&f)
		return f
	}
	f := get()
	if f.Thread == nil || f.Thread.ID != supTeam || f.Dossier.Thread != supTeam || f.Dossier.Paths["PLAN.md"] != v.planFile() {
		t.Fatalf("one identity: %+v %+v", f.Thread, f.Dossier.Paths)
	}

	// the owner approves the step's check; a worker runs it and reports through the panel's writer: the page, the
	// thread view and the lead's next turn show the same result
	dir := filepath.Dir(v.planFile())
	writeFile(t, filepath.Join(dir, "out.txt"), "hello from the demo\n")
	withCheck := strings.Replace(plan, "  - kind: build\n", "  - kind: build\n  - check: grep -x 'hello from the demo' out.txt\n  - cwd: "+dir+"\n", 1)
	os.WriteFile(v.planFile(), []byte(withCheck), 0o600)
	st := get().Dossier.Milestones[0].Steps[0]
	if c := v.approve(t, map[string]string{"key": st.Key, "check": st.Check, "cwd": st.Cwd}); c != 200 {
		t.Fatalf("approve: %d", c)
	}
	st = get().Dossier.Milestones[0].Steps[0]
	out, _ := bash(t, st.RunAs)
	record(t, v.transcript(), "toolu_w1", st.RunAs, out, false, false, time.Now())
	ticked := strings.Replace(withCheck, "- [~] Write the file @Builder", "- [x] Write the file @Builder", 1)
	ticked = strings.Replace(ticked, "  - cwd: "+dir+"\n", "  - cwd: "+dir+"\n  - evidence: run "+st.Attempt+"\n", 1)
	wr, _ := json.Marshal(map[string]string{"session": supTeam, "doc": "plan", "text": ticked, "rev": get().Dossier.Rev})
	rq := httptest.NewRequest("POST", "/agent/feature/doc", strings.NewReader(string(wr)))
	rq.Header.Set("Authorization", "Bearer tok")
	rw := httptest.NewRecorder()
	v.e.s.agentHandler("tok").ServeHTTP(rw, rq)
	if rw.Code != 200 {
		t.Fatalf("worker write: %d %s", rw.Code, rw.Body)
	}
	if s := get().Dossier.Milestones[0].Steps[0]; s.State != "done" || s.Label != "check ran (receipt)" {
		t.Fatalf("page: %+v", s)
	}
	v.a.mu.Lock()
	tv := v.a.threadViews()[0]
	ins := v.a.instructions(supTeam)
	v.a.mu.Unlock()
	if tv.Plan[0].State != "done" || tv.Plan[1].State != "todo" || !strings.Contains(ins, "[Feature record]") ||
		!strings.Contains(ins, "- Build [done] Write the file @Builder") || !strings.Contains(ins, tilde(dir)) {
		t.Fatalf("thread view %+v\nlead:\n%s", tv.Plan, ins)
	}

	// the manager's flat checklist moves boxes, keeps everything else; a line it can't place is refused, file untouched
	if c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","plan":"- [x] Write the file\n- [~] Review it"}`); c != 200 {
		t.Fatalf("manager update: %d %v", c, out)
	}
	b, _ := os.ReadFile(v.planFile())
	if !strings.Contains(string(b), "  - evidence: run "+st.Attempt+"\n- [~] Review it @Reviewer") || !strings.HasPrefix(string(b), "## Build\n") {
		t.Fatalf("lossy adapter:\n%s", b)
	}
	if c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","plan":"- [x] Write the file\n- [x] Deploy to prod"}`); c != 409 ||
		!strings.Contains(out["error"].(string), tilde(v.planFile())) || !strings.Contains(out["error"].(string), `"Deploy to prod" (no such step)`) {
		t.Fatalf("unplaceable line: %d %v", c, out)
	}
	if b2, _ := os.ReadFile(v.planFile()); string(b2) != string(b) {
		t.Fatal("a refused checklist changed the file")
	}

	// the owner's edit on the page, against the revision it read; the worker's next read is that file; a stale edit is refused
	rev := get().Dossier.Rev
	edit := string(b) + "- [ ] Owner added: screenshot the page @Reviewer\n"
	body, _ := json.Marshal(map[string]string{"session": supTeam, "doc": "plan", "text": edit, "rev": rev})
	if r := v.e.do(t, "POST", "/api/term/feature/doc", string(body), h); r.StatusCode != 200 {
		t.Fatalf("page edit: %d", r.StatusCode)
	}
	if b3, _ := os.ReadFile(v.planFile()); string(b3) != edit {
		t.Fatalf("worker reads:\n%s", b3)
	}
	if r := v.e.do(t, "POST", "/api/term/feature/doc", string(body), h); r.StatusCode != 409 {
		t.Fatalf("stale edit: %d", r.StatusCode)
	}

	// the same file in the session's folder with other words: surfaced, and writes refused until resolved
	writeFile(t, filepath.Join(featDir(v.e.s.cfg.home, supTeam), "PLAN.md"), "- [ ] other\n")
	if d := v.e.s.featDossier(supTeam); len(d.Conflicts) != 1 || d.Paths["PLAN.md"] != v.planFile() {
		t.Fatalf("conflict: %+v", d.Conflicts)
	}
	body, _ = json.Marshal(map[string]string{"session": supTeam, "doc": "plan", "text": "x"})
	if r := v.e.do(t, "POST", "/api/term/feature/doc", string(body), h); r.StatusCode != 409 {
		t.Fatalf("write into a conflict: %d", r.StatusCode)
	}
}

// Binding: a thread's team is set only to an exact live session another thread doesn't hold; never guessed
func TestFeatureBindingExplicit(t *testing.T) {
	v := newSupEnv(t, "")
	os.WriteFile(filepath.Join(v.e.s.sys.sockDir, "Data-Migration"), nil, 0o600)
	if c, _ := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","team":"data-migration"}`); c != 400 {
		t.Fatalf("a near name: %d", c)
	}
	if v.e.s.featOwner("Data-Migration") != nil {
		t.Fatal("guessed an owner")
	}
	v.a.mu.Lock()
	o := v.a.newThread("Other", "")
	o.Team = "Data-Migration"
	v.a.mu.Unlock()
	if c, _ := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","team":"Data-Migration"}`); c != 409 {
		t.Fatalf("held by another thread: %d", c)
	}
	v.a.mu.Lock()
	o.Status = "archived"
	v.a.mu.Unlock()
	h := v.e.authed(v.e.login(t))
	if r := v.e.do(t, "POST", "/api/term/feature/bind", `{"session":"Data-Migration","thread":"demo-feature"}`, h); r.StatusCode != 200 {
		t.Fatalf("bind: %d", r.StatusCode)
	}
	if th := v.e.s.featOwner("Data-Migration"); th == nil || th.ID != "demo-feature" {
		t.Fatalf("bound: %+v", th)
	}
}

// The owner starts a feature on the Terminals page and writes its steps; a manager attached later works on that same
// record: nothing copied, moved or reworded, the step ids unchanged, the page and the thread showing one plan.
func TestOwnerFeatureThenManagerAttached(t *testing.T) {
	v := newSupEnv(t, "")
	const sess = "Owner-Feature"
	os.WriteFile(filepath.Join(v.e.s.sys.sockDir, sess), nil, 0o600)
	tm := Terminals{Sessions: []TSession{{Name: sess, Tabs: []TTab{{ID: 1, Name: "Builder", Panes: []TPane{v.agent("waiting", v.now.Unix())}}}}}}
	v.e.s.term.mu.Lock()
	v.e.s.term.last, v.e.s.term.list = tm, tm
	v.e.s.term.mu.Unlock()
	h := v.e.authed(v.e.login(t))
	plan := "## M1 Owner's milestone\n- [~] Owner's own step @Builder\n  - kind: build\n- [ ] Second step\n"
	for doc, text := range map[string]string{"feature": "What must be true: the demo file exists.\n", "plan": plan} {
		body, _ := json.Marshal(map[string]string{"session": sess, "doc": doc, "text": text, "rev": featRev(nil)}) // a new file: the empty file's revision
		if r := v.e.do(t, "POST", "/api/term/feature/doc", string(body), h); r.StatusCode != 200 {
			t.Fatalf("owner writes %s: %d", doc, r.StatusCode)
		}
	}
	own := filepath.Join(featDir(v.e.s.cfg.home, sess), "PLAN.md")
	before := v.e.s.featDossier(sess)
	if before.Thread != "" || before.Paths["PLAN.md"] != own {
		t.Fatalf("an owner's feature needs no thread: %+v", before.Paths)
	}
	// a manager thread that already wrote its own PLAN.md can't attach: the owner's would be shadowed (409, nothing moves)
	v.a.mu.Lock()
	m := v.a.newThread("Manager", "")
	v.a.mu.Unlock()
	mine := filepath.Join(v.e.s.cfg.home, "jarvis-files", "threads", m.ID, "PLAN.md")
	writeFile(t, mine, "Goal: the manager's own prose plan.\n")
	if r := v.e.do(t, "POST", "/api/term/feature/bind", `{"session":"`+sess+`","thread":"`+m.ID+`"}`, h); r.StatusCode != 409 {
		t.Fatalf("attach over a second record: %d", r.StatusCode)
	}
	if v.e.s.featOwner(sess) != nil {
		t.Fatal("bound despite the conflict")
	}
	os.Remove(mine) // its lead folds its prose into the owner's record first; then it attaches by exact name
	if r := v.e.do(t, "POST", "/api/term/feature/bind", `{"session":"`+sess+`","thread":"`+m.ID+`"}`, h); r.StatusCode != 200 {
		t.Fatalf("attach: %d", r.StatusCode)
	}
	after := v.e.s.featDossier(sess)
	if after.Thread != m.ID || after.Paths["PLAN.md"] != own || after.Plan != plan || after.Milestones[0].Steps[0].Key != before.Milestones[0].Steps[0].Key {
		t.Fatalf("attached record: %+v", after)
	}
	if _, err := os.Stat(filepath.Join(v.e.s.cfg.home, "jarvis-files", "threads", m.ID, "PLAN.md")); err == nil {
		t.Fatal("attaching made a second PLAN.md")
	}
	// the manager's checklist lands in the owner's file, keeping the owner's lines
	if c, out := call(v.e.s.agentThreadUpdate, `{"id":"`+m.ID+`","plan":"- [x] Owner's own step"}`); c != 200 {
		t.Fatalf("manager update: %d %v", c, out)
	}
	if b, _ := os.ReadFile(own); string(b) != strings.Replace(plan, "[~] Owner's own step", "[x] Owner's own step", 1) {
		t.Fatalf("owner's file after the manager:\n%s", b)
	}
	v.a.mu.Lock()
	var tv threadView
	for _, x := range v.a.threadViews() {
		if x.ID == m.ID {
			tv = x
		}
	}
	v.a.mu.Unlock()
	if len(tv.Plan) != 2 || tv.Plan[0].State != "claimed" {
		t.Fatalf("thread view = the page's plan: %+v", tv.Plan)
	}
}

// A terminal the owner runs alone stays theirs: no supervision, no brief, no resume, no record, whatever it does
func TestStandaloneTerminalUnmanaged(t *testing.T) {
	v := newSupEnv(t, "")
	const sess = "scratch"
	os.WriteFile(filepath.Join(v.e.s.sys.sockDir, sess), nil, 0o600)
	look := func(panes ...TPane) {
		v.now = v.now.Add(31 * time.Second)
		tm := Terminals{Sessions: []TSession{{Name: sess, Tabs: []TTab{{ID: 1, Name: "Mine", Panes: panes}}}}}
		v.e.s.observeTeams(v.now, tm, CI{}, Resources{})
	}
	long := v.now.Add(-30 * time.Hour).Unix()
	for range 10 {
		look(v.agent("asking", long)) // a dialog open for a day
	}
	look(v.agent("working", long))
	look()
	look() // and it quit mid-work
	for _, n := range v.notes("") {
		if n.Kind == "thread" || n.Kind == "feature" || n.Kind == "exited" {
			t.Fatalf("supervised a standalone terminal: %+v", n)
		}
	}
	if len(v.f.calls) != 0 {
		t.Fatalf("touched it: %v", v.f.calls)
	}
	if _, err := os.Stat(featDir(v.e.s.cfg.home, sess)); err == nil {
		t.Fatal("made it a record")
	}
	if v.e.s.featOwner(sess) != nil {
		t.Fatal("enrolled it")
	}
}

// One request binding a team and moving its plan: checked whole first, applied to the new team's record; refused
// whole, nothing changes (not the team, not either file). thread_open on a held session: 409. A thread may be opened
// for a session that doesn't exist yet (its lead creates it next); binding an existing thread needs a live session.
func TestThreadUpdateTeamAndPlanAtomic(t *testing.T) {
	v := newSupEnv(t, "")
	const sess = "Owner-Feature"
	os.WriteFile(filepath.Join(v.e.s.sys.sockDir, sess), nil, 0o600)
	own := filepath.Join(featDir(v.e.s.cfg.home, sess), "PLAN.md")
	plan := "## M1\n- [ ] Owner step @Builder\n  - kind: build\n"
	writeFile(t, own, plan)
	mine := filepath.Join(v.e.s.cfg.home, "jarvis-files", "threads", supTeam, "PLAN.md")
	team := func() string { v.a.mu.Lock(); defer v.a.mu.Unlock(); return v.a.thread(supTeam).Team }
	same := func(why string) {
		t.Helper()
		if b, _ := os.ReadFile(own); string(b) != plan {
			t.Fatalf("%s: owner's file changed:\n%s", why, b)
		}
		if _, err := os.Stat(mine); err == nil {
			t.Fatalf("%s: a second PLAN.md appeared", why)
		}
		if team() != supTeam {
			t.Fatalf("%s: team moved to %s", why, team())
		}
	}
	// refused plan: no bind either
	if c, _ := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","team":"Owner-Feature","plan":"- [x] Not a step"}`); c != 409 {
		t.Fatalf("refused plan: %d", c)
	}
	same("refused plan")
	// occupied target: no plan write either
	v.a.mu.Lock()
	o := v.a.newThread("Other", "")
	o.Team = sess
	v.a.mu.Unlock()
	if c, _ := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","team":"Owner-Feature","plan":"- [~] Owner step"}`); c != 409 {
		t.Fatalf("occupied: %d", c)
	}
	same("occupied target")
	// thread_open on the held session: 409, no thread made
	v.e.s.scout.load()
	n := len(v.a.threads)
	if c, out := call(v.e.s.agentThreadOpen, `{"title":"Third","brief":"x","team":"Owner-Feature"}`); c != 409 || len(v.a.threads) != n {
		t.Fatalf("open on a held session: %d %v", c, out)
	}
	v.a.mu.Lock()
	o.Status = "archived"
	v.a.mu.Unlock()
	// both at once, now free: bound, and the box moved in the owner's file (its lines kept), nothing under threads/
	if c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","team":"Owner-Feature","plan":"- [~] Owner step"}`); c != 200 {
		t.Fatalf("combined: %d %v", c, out)
	}
	if b, _ := os.ReadFile(own); string(b) != strings.Replace(plan, "[ ]", "[~]", 1) || team() != sess {
		t.Fatalf("combined applied to:\n%s (team %s)", b, team())
	}
	if _, err := os.Stat(mine); err == nil {
		t.Fatal("combined wrote the old team's record")
	}
	// create-before-session: open names a session its lead starts next (allowed); bind needs it live (400)
	if c, out := call(v.e.s.agentThreadOpen, `{"title":"Fresh","brief":"x","team":"fresh-session"}`); c != 200 || out["thread"].(map[string]any)["team"] != "fresh-session" {
		t.Fatalf("open before its session: %d %v", c, out)
	}
	if c, _ := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","team":"not-started-yet"}`); c != 400 || team() != sess {
		t.Fatalf("bind to a session that isn't live: %d", c)
	}
}

// threads.json is the commit point of a thread_update that also changes PLAN.md: a failed save leaves both as they
// were (500, no file details); a crash between the two writes is undone at the next load, a committed one kept.
func TestThreadUpdatePlanTransaction(t *testing.T) {
	v := newSupEnv(t, "")
	const sess = "Owner-Feature"
	os.WriteFile(filepath.Join(v.e.s.sys.sockDir, sess), nil, 0o600)
	own := filepath.Join(featDir(v.e.s.cfg.home, sess), "PLAN.md")
	plan := "## M1\n- [ ] Owner step @Builder\n"
	writeFile(t, own, plan)
	file := v.a.threadsFile
	team := func() string { v.a.mu.Lock(); defer v.a.mu.Unlock(); return v.a.thread(supTeam).Team }

	// 1. the final save fails AFTER the intent and PLAN.md were written (threads.json is a folder now, so only its
	// rename fails; the intent beside it and PLAN.md elsewhere are written): 500, memory and PLAN.md put back, no intent left
	saved, _ := os.ReadFile(file)
	os.Remove(file)
	os.Mkdir(file, 0o700)
	wrote := false
	v.e.s.ai.featAdapt = func(orig func(*thread, string) (planEdit, error)) func(*thread, string) (planEdit, error) {
		return func(t *thread, flat string) (planEdit, error) {
			e, err := orig(t, flat)
			wrote = err == nil
			return e, err
		}
	}(v.e.s.ai.featAdapt)
	var seenNew bool
	featWriteHook = func(path string) { // the PLAN.md write happened before the failing save
		if b, _ := os.ReadFile(path); strings.Contains(string(b), "[~] Owner step") {
			seenNew = true
		}
	}
	defer func() { featWriteHook = nil }()
	c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","team":"Owner-Feature","plan":"- [~] Owner step"}`)
	if c != 500 || strings.Contains(out["error"].(string), "/") || team() != supTeam {
		t.Fatalf("failed save: %d %v team %s", c, out, team())
	}
	if b, _ := os.ReadFile(own); string(b) != plan {
		t.Fatalf("PLAN.md after a failed save:\n%s", b)
	}
	if _, err := os.Stat(v.a.txnFile()); err == nil {
		t.Fatal("intent left behind")
	}
	if !wrote || !seenNew {
		t.Fatal("the failure didn't come after the PLAN.md write: this case wouldn't test the rollback")
	}
	os.Remove(file)
	os.WriteFile(file, saved, 0o600)

	// 2. a crash after PLAN.md is written, before threads.json: the next load puts PLAN.md back
	reload := func() *assistant {
		b := newAssistant(v.a.base, v.a.keyFile, "")
		b.threadsFile, b.leadFile = file, v.a.leadFile
		b.loadThreads()
		return b
	}
	crash := func(f string) {
		defer func() { recover() }()
		v.a.mu.Lock()
		defer v.a.mu.Unlock()
		v.a.planTxn(&planEdit{Path: own, Old: []byte(plan), New: []byte(strings.Replace(plan, "[ ]", "[~]", 1)), ID: supTeam, Updated: 42}, func() error {
			if f == "after" {
				v.a.thread(supTeam).Updated = 42
				v.a.saveThreads()
			}
			panic("crash")
		})
	}
	crash("before")
	if b, _ := os.ReadFile(own); !strings.Contains(string(b), "[~]") {
		t.Fatal("the crash came after the PLAN.md write")
	}
	reload()
	if b, _ := os.ReadFile(own); string(b) != plan {
		t.Fatalf("uncommitted change survived a restart:\n%s", b)
	}
	if _, err := os.Stat(v.a.txnFile()); err == nil {
		t.Fatal("intent not cleared")
	}
	// 3. the crash came after threads.json was saved: committed, kept
	crash("after")
	if b := reload(); b.thread(supTeam).Updated != 42 {
		t.Fatal("commit not saved")
	}
	if b, _ := os.ReadFile(own); !strings.Contains(string(b), "[~]") {
		t.Fatalf("a committed change was undone:\n%s", b)
	}
	// and the whole update, healthy: both applied
	if c, _ := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","team":"Owner-Feature","plan":"- [x] Owner step"}`); c != 200 || team() != sess {
		t.Fatalf("healthy update: %d", c)
	}
	var th []thread
	b, _ := os.ReadFile(file)
	json.Unmarshal(b, &th)
	if b2, _ := os.ReadFile(own); !strings.Contains(string(b2), "[x] Owner step") || th[0].Team != sess {
		t.Fatalf("committed both: %s %+v", b2, th[0].Team)
	}
}
