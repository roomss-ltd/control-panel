package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Opening the page never spends a model run: the report starts only on an explicit owner action.
func TestV8ReportNeedsExplicitAction(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	h := v.e.authed(v.e.login(t))
	if r := v.e.do(t, "POST", "/api/term/feature/report", `{"session":"`+supTeam+`"}`, h); r.StatusCode != 400 {
		t.Fatalf("an implicit report request started a model run: %d", r.StatusCode)
	}
	featBusy.Lock()
	busy := featBusy.m[supTeam]
	featBusy.Unlock()
	if busy {
		t.Fatal("a report run started")
	}
}

// Routes added through v8Register are served behind the owner login.
func TestV8RegisterBehindOwnerLogin(t *testing.T) {
	old := v8Register
	t.Cleanup(func() { v8Register = old })
	v8Register = append(v8Register, func(s *server, mux *http.ServeMux, owner func(http.HandlerFunc) http.HandlerFunc) {
		mux.HandleFunc("GET /api/term/feature/v8probe", owner(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) }))
	})
	e := newTestServer(t)
	if r := e.do(t, "GET", "/api/term/feature/v8probe", "", nil); r.StatusCode == 200 {
		t.Fatal("served without the owner login")
	}
	if r := e.do(t, "GET", "/api/term/feature/v8probe", "", e.authed(e.login(t))); r.StatusCode != 200 {
		t.Fatalf("not registered: %d", r.StatusCode)
	}
}

// The worker brief names the evidence kinds and the role skills, and fences /ship: a report, never a deploy.
func TestV8BriefSkillsAndProof(t *testing.T) {
	b := featBrief("~/rec", "s", "Builder", "")
	for _, want := range []string{"evidence: <kind> <ref>", "run|file|image|doc|db|link|ui", "test-driven-development", "/ship", "never deploys, pushes, merges or rolls back", "not proof"} {
		if !strings.Contains(b, want) {
			t.Fatalf("the brief lacks %q:\n%s", want, b)
		}
	}
}

// The supervisor tests' "touched a pane" probe reads the zellij action, not the text: a brief that says "skills" isn't
// a kill, a real kill or typed keys are.
func TestV8TypedProbeReadsTheAction(t *testing.T) {
	v := newSupEnv(t, "")
	v.f.calls = [][]string{{"--session", "s", "action", "paste", "--pane-id", "terminal_1", "--", "use the skills; kill nothing"}}
	if got := v.typed(); len(got) != 0 {
		t.Fatalf("a paste mentioning skills counted: %v", got)
	}
	v.f.calls = append(v.f.calls, []string{"--session", "s", "action", "close-pane", "--pane-id", "terminal_1"},
		[]string{"kill-session", "s"}, []string{"--session", "s", "action", "write-chars", "--pane-id", "terminal_1", "--", "y"})
	if got := v.typed(); len(got) != 3 {
		t.Fatalf("destructive actions missed: %v", got)
	}
}

// The brief is typed only into an idle agent with an empty input box: never over a running turn, an approval or
// question waiting on the owner, or text the owner has started typing (which stays untouched); it waits for idle.
func TestV8BriefNeverOverActiveInput(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Check output @Builder\n")
	dump := "--session " + supTeam + " action dump-screen"
	box := func(s string) string {
		return "● Stopped\n────────────\n❯ " + s + "\n────────────\n  [Opus 5.5] 📁 app\n"
	}
	touched := func() (briefs, other int) {
		for _, c := range v.f.calls {
			j := strings.Join(c, " ")
			switch {
			case strings.Contains(j, " paste ") && strings.Contains(j, "source of truth"):
				briefs++
			case strings.Contains(j, " paste ") || strings.Contains(j, " send-keys ") || strings.Contains(j, " write-chars "):
				other++
			}
		}
		return
	}
	for _, st := range []string{"working", "asking"} {
		v.look(v.agent(st, v.now.Unix()))
	}
	v.f.answers[dump], v.f.answers[dump+" --ansi"] = box("half-typed owner text"), box("half-typed owner text")
	v.look(v.agent("waiting", v.now.Unix()))
	if b, o := touched(); b != 0 || o != 0 {
		t.Fatalf("typed over a busy agent or the owner's input: %d briefs, %d other keys", b, o)
	}
	v.f.answers[dump], v.f.answers[dump+" --ansi"] = box(""), box("")
	v.look(v.agent("waiting", v.now.Unix()))
	if b, _ := touched(); b != 1 {
		t.Fatalf("the brief once idle and empty: %d", b)
	}
	waitIdle(t, v.a, supTeam)
}

// Dependencies come only from explicit after: lines; a bad one is kept with why (never dropped, never "ready").
func TestV8AfterParsed(t *testing.T) {
	a, b, c := featStepKey("M", "A"), featStepKey("M", "B"), featStepKey("M", "C")
	ms := parseMilestones("## M\n- [ ] A\n  - after: " + c + "\n- [ ] B\n  - after: " + a + ", nope, " + b + "\n- [ ] C\n  - after: " + a + "\n- [ ] D\n")
	st := map[string]featPlanStep{}
	for _, s := range ms[0].Steps {
		st[s.Text] = s
	}
	if fmt.Sprint(st["B"].After) != "["+a+"]" || toJSON(st["B"].AfterInvalid) != `[{"ref":"nope","why":"unknown step"},{"ref":"`+b+`","why":"self"}]` {
		t.Fatalf("B: %v %v", st["B"].After, toJSON(st["B"].AfterInvalid))
	}
	if len(st["A"].After) != 0 || toJSON(st["A"].AfterInvalid) != `[{"ref":"`+c+`","why":"cycle"}]` || toJSON(st["C"].AfterInvalid) != `[{"ref":"`+a+`","why":"cycle"}]` {
		t.Fatalf("cycle A<->C: %v %v", toJSON(st["A"]), toJSON(st["C"]))
	}
	if st["D"].After != nil || st["D"].AfterInvalid != nil {
		t.Fatal("D has no dependency; none inferred")
	}
}

// A scratch instance (QA/UI harness) can switch the background model jobs off: the chapters/digests loop doesn't run.
func TestV8BackgroundModelsOff(t *testing.T) {
	t.Setenv("CP_NO_BACKGROUND_MODELS", "1")
	s := newTestServer(t).s
	done := make(chan struct{})
	go func() { s.chaptersLoop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the chapters/digests loop runs with CP_NO_BACKGROUND_MODELS=1")
	}
}
