package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFeatSessionsNoRecordNoRelations(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	// two bars back to back on consecutive steps, a resume-shaped gap: with no relation recorded, there is none
	in := sessIn{
		assigns: []featAssign{
			{ID: "ev-1", Seq: 1, At: t0, Step: "s1", Tab: "Coder", Pane: 3, Sid: "aaa"},
			{ID: "ev-2", Seq: 2, At: t0.Add(time.Hour), Step: "s2", Tab: "Coder", Pane: 3, Sid: "aaa"},
		},
		live:     []liveAgent{{key: "f:3", pane: 3, sid: "aaa", tab: "Coder", kind: "claude"}},
		sampleAt: t0.Add(2 * time.Hour),
	}
	ss, rs := in.build()
	if len(rs) != 0 {
		t.Fatalf("relations inferred from adjacency: %+v", rs)
	}
	if len(ss) != 2 || ss[0].End == nil || *ss[0].End != "reassigned" || *ss[0].To != t0.Add(time.Hour).Unix() || ss[1].End != nil || ss[1].Identity != "observed" || ss[1].Kind != "claude" {
		t.Fatalf("ledger: %+v", ss)
	}
}

func TestFeatSessionsExplicitRelationsAndIdentity(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	in := sessIn{
		assigns: []featAssign{
			{ID: "ev-1", Seq: 1, At: t0, Step: "s1", Tab: "Planner", Pane: 2, Sid: "p1"},
			{ID: "ev-2", Seq: 2, At: t0.Add(time.Hour), Step: "s2", Tab: "Coder", Pane: 3, Sid: "c1", Rel: &featRel{Kind: "handoff", With: "s1", Why: "plan ready"}},
			{ID: "ev-3", Seq: 3, At: t0.Add(2 * time.Hour), Step: "s3", Tab: "Tester", Pane: 4, Sid: "t1", Rel: &featRel{Kind: "pair", With: "s9"}},
			{ID: "ev-4", Seq: 4, At: t0.Add(3 * time.Hour), Step: "s4", Tab: "Dup", Sid: ""},
		},
		live: []liveAgent{
			{key: "f:3", pane: 3, sid: "OTHER", tab: "Coder"}, // the pane runs another agent now
			{key: "f:4", pane: 4, sid: "t1", tab: "Tester"},   // confirmed
			{key: "f:5", pane: 5, sid: "d1", tab: "Dup"},      // two panes share a tab name
			{key: "f:6", pane: 6, sid: "d2", tab: "Dup"},
			{key: "f:7", pane: 7, sid: "x1", tab: "Docs", path: "/tr"}, // live, never assigned
		},
		sampleAt: t0.Add(4 * time.Hour),
		owners:   map[string][]string{"docs": {"s7"}},
		resumes:  map[string]featResume{"t1": {At: t0.Add(150 * time.Minute).Unix(), State: "confirmed", Tries: 1}},
		started:  func(string) int64 { return t0.Add(-time.Minute).Unix() },
	}
	ss, rs := in.build()
	by := map[string]featSession{}
	for _, e := range ss {
		by[e.ID] = e
	}
	if e := by["ev-1"]; e.End == nil || *e.End != "exited" || !strings.Contains(e.EndSource, "not in the terminal sample") {
		t.Fatalf("planner gone from the sample: %+v", e)
	}
	if e := by["ev-2"]; e.Identity != "mismatch" || e.Sid != "c1" {
		t.Fatalf("pane runs another sid: %+v", e)
	}
	if e := by["ev-3"]; e.Identity != "observed" {
		t.Fatalf("tester: %+v", e)
	}
	if e := by["ev-4"]; e.Identity != "ambiguous" || e.Sid != "" {
		t.Fatalf("shared tab: %+v", e)
	}
	if e := by["live:f:7:x1"]; e.Step != "s7" || !strings.HasPrefix(e.StepSource, "plan @Owner") || e.FromSource != "transcript first entry" {
		t.Fatalf("unassigned live agent: %+v", e)
	}
	if e := by["live:f:5:d1"]; e.Identity != "ambiguous" || e.Step != "" {
		t.Fatalf("live agent in a shared tab: %+v", e)
	}
	kinds := map[string]featRelation{}
	for _, r := range rs {
		kinds[r.Kind] = r
	}
	if r := kinds["handoff"]; r.From != "ev-1" || r.To != "ev-2" || r.Src != "assignment ev-2" {
		t.Fatalf("handoff: %+v", r)
	}
	if r := kinds["pair"]; r.To != "" || r.ToStep != "s9" || !strings.Contains(r.Why, "no session assigned") {
		t.Fatalf("pair with an unassigned step: %+v", r)
	}
	if r := kinds["resume"]; r.Src != "featsup resume record" || r.To != "ev-3" && r.To != "" {
		t.Fatalf("resume: %+v", r)
	}
	if len(rs) != 3 {
		t.Fatalf("relations: %+v", rs)
	}
}

func TestFeatSessionsNoSample(t *testing.T) {
	ss, _ := sessIn{assigns: []featAssign{{ID: "ev-1", Seq: 1, At: time.Unix(1, 0), Step: "s", Tab: "A"}}}.build()
	if ss[0].Identity != "unknown" || ss[0].End != nil {
		t.Fatalf("with no sample nothing is concluded: %+v", ss[0])
	}
}

func TestTranscriptStart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(p, []byte(`{"type":"summary"}`+"\n"+`{"type":"user","timestamp":"2026-11-08T10:00:00.123Z"}`+"\n"), 0o600)
	if got := transcriptStart(p); got != time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("start: %d", got)
	}
	if transcriptStart(filepath.Join(t.TempDir(), "missing")) != 0 {
		t.Fatal("a missing transcript has a start")
	}
}

// Both session-ledger routes (code and sessions) are registered through v8Register, sit behind the owner check and
// fetch nothing.
func TestV8BRoutesOwnerOnlyAndCached(t *testing.T) {
	resetFeatCode()
	defer func() { setRefreshHook(nil); resetFeatCode() }()
	setRefreshHook(func(s *server, session string) codeIn { return codeIn{now: time.Now()} })
	e := newTestServer(t)
	e.s.prs.fetch = func(string, ...string) ([]byte, error) { t.Error("gh called for a GET"); return nil, nil }
	cookie := e.login(t)
	auth := map[string]string{"Cf-Access-Jwt-Assertion": e.jwt, "Cookie": "cp_session=" + cookie}
	for _, path := range []string{"/api/term/feature/code?session=demo", "/api/term/feature/sessions?session=demo"} {
		if r := e.do(t, "GET", path, "", map[string]string{"Cf-Access-Jwt-Assertion": e.jwt}); r.StatusCode == 200 {
			t.Fatalf("%s without the owner's session: 200", path)
		}
		r := e.do(t, "GET", path, "", auth)
		var v map[string]any
		if err := json.NewDecoder(r.Body).Decode(&v); r.StatusCode != 200 || err != nil {
			t.Fatalf("%s: %d %v", path, r.StatusCode, err)
		}
	}
	if r := e.do(t, "GET", "/api/term/feature/code?session=-x", "", auth); r.StatusCode != 400 {
		t.Fatalf("bad session: %d", r.StatusCode)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		featCode.Lock()
		busy := featCode.busy["demo"]
		featCode.Unlock()
		if !busy {
			break
		}
	}
}

// End to end through the real assignment route and persisted event log: identity, mismatch, restart,
// reassignment, exit and a resume record.
func TestFeatSessionsFromRealAssignments(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n- [ ] Review docs @Reviewer\n- [ ] Fix review @Builder\n")
	do := agentDo(t, v)
	k := v8Keys(v)
	assign := func(body string) string {
		t.Helper()
		c, out := do("POST", "/agent/feature/assign", body)
		if c != 200 {
			t.Fatalf("assign %s: %d %v", body, c, out)
		}
		return out["id"].(string)
	}
	sample := func(tabs ...TTab) {
		tm := Terminals{Sessions: []TSession{{Name: supTeam, Tabs: tabs}}}
		v.e.s.term.mu.Lock()
		v.e.s.term.last, v.e.s.term.lastAt = tm, time.Now()
		v.e.s.term.mu.Unlock()
	}
	pane := func(id int, sid string) TPane {
		return TPane{ID: id, Kind: "claude", State: "working", Agent: &TAgent{PID: 40 + id, sid: sid, Model: "model-of-" + sid}}
	}
	ledger := func() (map[string]featSession, []featRelation) {
		ss, rs, at := v.e.s.featSessionsPage(supTeam, v.e.s.featDossier(supTeam))
		if at == 0 {
			t.Fatal("no observedAt")
		}
		m := map[string]featSession{}
		for _, e := range ss {
			m[e.ID] = e
		}
		return m, rs
	}

	e1 := assign(`{"session":"demo-feature","step":"` + k[0] + `","tab":"Builder","pane":3,"sid":"aaaa-bbbb-1"}`)
	e2 := assign(`{"session":"demo-feature","step":"` + k[1] + `","tab":"Reviewer","pane":12,"sid":"s-2","relation":{"kind":"handoff","with":"` + k[0] + `","why":"reviews the docs"}}`)
	sample(TTab{ID: 1, Name: "Builder", Panes: []TPane{pane(3, "aaaa-bbbb-1")}}, TTab{ID: 2, Name: "Reviewer", Panes: []TPane{pane(12, "s-OTHER")}})
	for round, restart := range []bool{false, true} {
		if restart {
			v.e.s.sup = newFeatSup(v.e.s.sup.file)
		}
		m, rs := ledger()
		if m[e1].Identity != "observed" || m[e1].Step != k[0] || m[e1].End != nil || m[e1].Model != "model-of-aaaa-bbbb-1" {
			t.Fatalf("round %d: builder: %+v", round, m[e1])
		}
		if m[e2].Identity != "mismatch" || m[e2].Sid != "s-2" || !strings.Contains(m[e2].Why, "s-OTHER") || m[e2].Model != "" {
			t.Fatalf("round %d: the claimed sid isn't the pane's: %+v", round, m[e2])
		}
		if len(rs) != 1 || rs[0].Kind != "handoff" || rs[0].From != e1 || rs[0].To != e2 || rs[0].Src != "assignment "+e2 {
			t.Fatalf("round %d: relations: %+v", round, rs)
		}
		if l, live := m["live:"+supTeam+":12:s-OTHER"]; !live || l.Model != "model-of-s-OTHER" {
			t.Fatalf("round %d: the agent really in pane 12 is missing: %+v", round, m)
		}
	}

	// the builder is reassigned to the fix; then its pane leaves the sample
	e3 := assign(`{"session":"demo-feature","step":"` + k[2] + `","tab":"Builder","pane":3,"sid":"aaaa-bbbb-1"}`)
	m, rs := ledger()
	if m[e1].End == nil || *m[e1].End != "reassigned" || m[e1].EndSource != "assignment "+e3 || m[e3].Identity != "observed" {
		t.Fatalf("reassignment: %+v / %+v", m[e1], m[e3])
	}
	if len(rs) != 1 {
		t.Fatalf("the reassignment made a relation of its own: %+v", rs)
	}
	v.e.s.sup.mu.Lock()
	v.e.s.sup.Resumes["aaaa-bbbb-1"] = featResume{At: time.Now().Add(time.Minute).Unix(), State: "confirmed", Tries: 1, Session: supTeam}
	v.e.s.sup.Resumes["other-sid"] = featResume{At: time.Now().Unix(), State: "confirmed", Tries: 1, Session: "another-feature"}
	v.e.s.sup.mu.Unlock()
	sample(TTab{ID: 2, Name: "Reviewer", Panes: []TPane{pane(12, "s-OTHER")}})
	m, rs = ledger()
	if m[e3].End == nil || *m[e3].End != "exited" {
		t.Fatalf("builder gone from the sample: %+v", m[e3])
	}
	var resumes []featRelation
	for _, r := range rs {
		if r.Kind == "resume" {
			resumes = append(resumes, r)
		}
	}
	if len(resumes) != 1 || resumes[0].From != e3 || resumes[0].Src != "featsup resume record" {
		t.Fatalf("resume relation (only this feature's): %+v", resumes)
	}
	if len(v.typed()) != 0 {
		t.Fatalf("reading the ledger typed into a pane: %v", v.typed())
	}
}

// TAgent.Model is what the transcript's latest real reply ran on: a later synthetic entry or a sidechain doesn't change it.
func TestAgentModelFromTranscript(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, "sessions")
	writeFile(t, filepath.Join(claudeDir, "950.json"), `{"pid":950,"sessionId":"aaaaaaaa-0950","procStart":"7","status":"idle"}`)
	writeFile(t, filepath.Join(dir, "projects", "p", "aaaaaaaa-0950.jsonl"), strings.Join([]string{
		`{"type":"assistant","timestamp":"2026-11-08T10:00:00Z","message":{"model":"claude-sonnet-5-5","usage":{"input_tokens":10}}}`,
		`{"type":"assistant","timestamp":"2026-11-08T10:01:00Z","message":{"model":"claude-opus-5-5","usage":{"input_tokens":20}}}`,
		`{"type":"assistant","timestamp":"2026-11-08T10:02:00Z","isSidechain":true,"message":{"model":"claude-haiku-5-5","usage":{"input_tokens":5}}}`,
		`{"type":"assistant","timestamp":"2026-11-08T10:03:00Z","message":{"model":"<synthetic>","usage":{"input_tokens":0}}}`,
	}, "\n")+"\n")
	z, _ := newFakeZellij(map[string]string{"--session s action list-panes": `[{"id":1,"title":"a","tab_id":2,"tab_position":0,"tab_name":"First"}]`})
	tt := newTermTracker(z, claudeDir, newTranscripts(filepath.Join(dir, "projects")), &agentsTracker{dir: filepath.Join(dir, "status")})
	tt.startOf = func(pid int) (procStat, bool) { return procStat{Start: 7, PGRP: pid}, true }
	res := Resources{panes: map[string]paneInfo{"pane:s:1": {Root: 11, Comm: "zsh", Agent: "claude"}}}
	got := tt.update(time.Now(), []string{"s"}, res, func(pid int) string { return map[int]string{950: "pane:s:1"}[pid] }, true)
	a := got.Sessions[0].Tabs[0].Panes[0].Agent
	if a == nil || a.Model != "claude-opus-5-5" {
		t.Fatalf("model: %+v", a)
	}
}

// PLAN.md after: through the real plan parser. A valid edge is a blocks relation, and only the dependent is blocked.
// An invalid ref (self, unknown, cycle) is never drawn but is kept as a diagnostic, and its step counts as blocked.
// A missing session leaves that end "" with why, never an invented bar.
func TestFeatSessionsAfterEdges(t *testing.T) {
	k := func(s string) string { return featStepKey("M", s) }
	for _, tc := range []struct{ a string }{{"todo"}, {"claimed"}} { // ticked but not verified (the dossier's state) still blocks
		ms := parseMilestones("## M\n- [ ] A\n- [ ] B\n  - after: " + k("A") + "\n- [ ] C\n  - after: " + k("C") + ", nope\n- [ ] D\n  - after: " + k("E") + "\n- [ ] E\n  - after: " + k("D") + "\n- [ ] F\n  - after: " + k("A") + "\n")
		ms[0].Steps[0].State = tc.a
		t0 := time.Unix(1_800_000_000, 0)
		in := sessIn{ms: ms, rev: "r1", sampleAt: t0.Add(time.Hour), assigns: []featAssign{
			{ID: "ev-1", Seq: 1, At: t0, Step: k("A"), Tab: "Builder", Pane: 3},
			{ID: "ev-2", Seq: 2, At: t0, Step: k("B"), Tab: "Reviewer", Pane: 4},
			{ID: "ev-3", Seq: 3, At: t0, Step: k("C"), Tab: "Tester", Pane: 5},
			{ID: "ev-4", Seq: 4, At: t0, Step: k("D"), Tab: "Ops", Pane: 6},
		}, live: []liveAgent{{key: "f:3", pane: 3, sid: "a", tab: "Builder"}, {key: "f:4", pane: 4, sid: "b", tab: "Reviewer"}, {key: "f:5", pane: 5, sid: "c", tab: "Tester"}, {key: "f:6", pane: 6, sid: "d", tab: "Ops"}}}
		ss, rs := in.build()
		by := map[string]featSession{}
		for _, e := range ss {
			by[e.ID] = e
		}
		if len(rs) != 2 {
			t.Fatalf("%s: want only the 2 valid edges (A->B, A->F): %+v", tc.a, rs)
		}
		for _, r := range rs {
			if r.Kind != "blocks" || r.FromStep != k("A") || r.From != "ev-1" || !strings.HasPrefix(r.Src, "PLAN.md after: @rev r1") {
				t.Fatalf("%s: edge: %+v", tc.a, r)
			}
			switch r.ToStep {
			case k("B"):
				if r.To != "ev-2" || !strings.Contains(r.Why, "waits for") {
					t.Fatalf("%s: A->B: %+v", tc.a, r)
				}
			case k("F"):
				if r.To != "" || !strings.Contains(r.Why, "no session on step "+k("F")) {
					t.Fatalf("%s: A->F with no session on F: %+v", tc.a, r)
				}
			default:
				t.Fatalf("%s: an edge that isn't in PLAN.md: %+v", tc.a, r)
			}
		}
		if by["ev-1"].Blocked != "" || !strings.HasPrefix(by["ev-2"].Blocked, "waits for "+k("A")) {
			t.Fatalf("%s: only the dependent waits: A %q, B %q", tc.a, by["ev-1"].Blocked, by["ev-2"].Blocked)
		}
		if c := by["ev-3"]; !strings.HasPrefix(c.Blocked, "invalid dependency") || toJSON(c.DepIssues) != `[{"ref":"`+k("C")+`","why":"self"},{"ref":"nope","why":"unknown step"}]` {
			t.Fatalf("%s: C: %+v", tc.a, c)
		}
		if d := by["ev-4"]; !strings.HasPrefix(d.Blocked, "invalid dependency") || len(d.DepIssues) != 1 || d.DepIssues[0].Why != "cycle" {
			t.Fatalf("%s: D: %+v", tc.a, d)
		}
	}
	ms := parseMilestones("## M\n- [ ] A\n- [ ] B\n  - after: " + k("A") + "\n")
	ms[0].Steps[0].State = "done" // verified
	ss, rs := sessIn{ms: ms, assigns: []featAssign{{ID: "ev-2", Seq: 1, Step: k("B"), Tab: "Reviewer"}}}.build()
	if ss[0].Blocked != "" || len(rs) != 1 || rs[0].To != "ev-2" || rs[0].From != "" || rs[0].Why != "(no session on step "+k("A")+")" {
		t.Fatalf("after a verified predecessor: %+v %+v", ss[0], rs)
	}
}
