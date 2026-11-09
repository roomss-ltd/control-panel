package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type v8PageOut struct {
	Page struct {
		Log []struct {
			ID, Kind, Title, Who, Step, StepClaimed, Rev string
			Seq                                          int64
			At                                           int64
			Considered                                   []struct{ Option string }
			Chose                                        *struct{ Option string }
			Review                                       bool
		} `json:"log"`
		Events  []featEv `json:"events"`
		Tickets *[]any   `json:"tickets"`
		Seen    struct {
			Frontier int64
			IDs      []string
		} `json:"seen"`
		Manager *struct {
			Thread string
			Bound  bool
		} `json:"manager"`
	} `json:"page"`
}

// The page is assembled from the record and the event log: typed log lines with event ids the owner can mark seen,
// the manager, events; what isn't tracked yet is absent, never a placeholder; reading it changes nothing.
func TestV8PageLog(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	k := v8Keys(v)[0]
	do := agentDo(t, v)
	for _, l := range []string{
		"DECISION: use sqlite | considered: sqlite; postgres | chose: sqlite | step: " + k + " | by: @Builder",
		"ACTION: wrote the schema | step: 000000000000",
		"hello, untyped",
	} {
		if c, out := do("POST", "/agent/feature/log", `{"session":"demo-feature","line":`+toJSON(l)+`}`); c != 200 {
			t.Fatalf("log: %d %v", c, out)
		}
	}
	tm := Terminals{Sessions: []TSession{{Name: supTeam, Tabs: []TTab{{ID: 1, Name: "Builder"}}}}}
	v.e.s.term.mu.Lock()
	v.e.s.term.last, v.e.s.term.list = tm, tm
	v.e.s.term.mu.Unlock()
	h := v.e.authed(v.e.login(t))
	files := []string{v.e.s.sup.file, v.e.s.notes.file, v.a.threadsFile}
	snap := func() (s string) {
		for _, f := range files {
			b, _ := os.ReadFile(f)
			s += string(b) + "\x00"
		}
		return
	}
	before := snap()
	var out v8PageOut
	for i := 0; i < 5; i++ {
		r := v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", h)
		decodeBody(t, r, &out)
	}
	if snap() != before {
		t.Fatal("reading the page changed state")
	}
	p := out.Page
	if len(p.Log) != 2 {
		t.Fatalf("page.log is the typed lines only: %+v", p.Log)
	}
	d, a := p.Log[0], p.Log[1]
	if d.Kind != "DECISION" || d.Title != "use sqlite" || d.Who != "Builder" || d.Step != k || len(d.Considered) != 2 || d.Chose == nil || d.Chose.Option != "sqlite" || !d.Review || d.ID != "ev-1" || d.At == 0 {
		t.Fatalf("decision: %+v", d)
	}
	if a.Kind != "ACTION" || a.Step != "" || a.StepClaimed != "000000000000" || a.Review || a.Chose != nil || a.Considered != nil {
		t.Fatalf("action with an unknown step: %+v", a)
	}
	if p.Manager == nil || p.Manager.Thread != supTeam || !p.Manager.Bound {
		t.Fatalf("manager: %+v", p.Manager)
	}
	if p.Tickets == nil || len(*p.Tickets) != 0 {
		t.Fatalf("tickets: none open: %v", p.Tickets)
	}
	if len(p.Events) != 3 {
		t.Fatalf("events: %d", len(p.Events))
	}
	if c, out := v8Post(t, v, h, "/api/term/feature/seen", `{"session":"`+supTeam+`","ids":["`+d.ID+`"]}`); c != 200 {
		t.Fatalf("a log line's id can be marked seen: %d %v", c, out)
	}
	if c, _ := v8Post(t, v, h, "/api/term/feature/reviewed", `{"session":"`+supTeam+`","id":"`+d.ID+`","rev":"`+d.Rev+`"}`); c != 200 {
		t.Fatalf("a decision can be reviewed: %d", c)
	}
	decodeBody(t, v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", h), &out)
	if out.Page.Seen.Frontier != 1 {
		t.Fatalf("seen on the page: %+v", out.Page.Seen)
	}
}

// An event carries the plan revision the panel read when it was made, never the writer's word for it (that is kept
// apart as claimedRev); review is against that revision, and the page says when the plan has moved since.
func TestV8EventRevIsTheRecords(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	r1 := v.e.s.featDossier(supTeam).Rev
	do := agentDo(t, v)
	if c, _ := do("POST", "/agent/feature/log", `{"session":"demo-feature","line":"DECISION: use sqlite | rev: zzz"}`); c != 200 {
		t.Fatal(c)
	}
	e := v.e.s.sup.Events[supTeam][0]
	if e.Rev != r1 {
		t.Fatalf("event rev %q, the plan's %q", e.Rev, r1)
	}
	writeFile(t, v.planFile(), "## Ship\n- [ ] Write the docs @Builder\n")
	tm := Terminals{Sessions: []TSession{{Name: supTeam}}}
	v.e.s.term.mu.Lock()
	v.e.s.term.last, v.e.s.term.list = tm, tm
	v.e.s.term.mu.Unlock()
	h := v.e.authed(v.e.login(t))
	var out struct {
		Page struct {
			Log []struct {
				Rev, ClaimedRev string
				Stale           bool
			}
		}
	}
	decodeBody(t, v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", h), &out)
	if l := out.Page.Log; len(l) != 1 || l[0].Rev != r1 || l[0].ClaimedRev != "zzz" || !l[0].Stale {
		t.Fatalf("page log: %+v", l)
	}
	if c, _ := v8Post(t, v, h, "/api/term/feature/reviewed", `{"session":"`+supTeam+`","id":"`+e.ID+`","rev":"zzz"}`); c != 409 {
		t.Fatalf("reviewed at the claimed rev: %d", c)
	}
	if c, _ := v8Post(t, v, h, "/api/term/feature/reviewed", `{"session":"`+supTeam+`","id":"`+e.ID+`","rev":"`+r1+`"}`); c != 200 {
		t.Fatalf("reviewed at the event's rev: %d", c)
	}
	if c, out := do("GET", "/agent/feature?session=demo-feature", ""); c != 200 || toJSON(out["reviewed"]) != `{"`+e.ID+`":"`+r1+`"}` {
		t.Fatalf("the lead reads reviews: %d %v", c, out["reviewed"])
	}
}

// Straight after a restart, before the first terminal sample, the record is served (never an empty or missing
// feature), and the page says the team isn't sampled yet.
func TestV8RecordBeforeFirstSample(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	h := v.e.authed(v.e.login(t))
	r := v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", h)
	var out struct {
		Dossier featDossier
		Page    map[string]any
	}
	if r.StatusCode != 200 {
		t.Fatalf("before the first sample: %d", r.StatusCode)
	}
	decodeBody(t, r, &out)
	if len(out.Dossier.Milestones) != 1 || out.Page["teamUnsampled"] != true {
		t.Fatalf("record / loading: %+v %v", out.Dossier.Milestones, out.Page["teamUnsampled"])
	}
}

// A record with no managing thread, after the panel restarted (fresh supervisor state from disk, no terminal sample
// yet): the first GET already has the plan.
func TestV8UnboundRecordAfterRestart(t *testing.T) {
	v := newSupEnv(t, "")
	dir := filepath.Join(v.e.s.cfg.home, "jarvis-files", "features", "solo-feature")
	writeFile(t, filepath.Join(dir, "PLAN.md"), "## Ship\n- [ ] Write docs @Builder\n")
	writeFile(t, filepath.Join(dir, "FEATURE.md"), "- [ ] D1: Docs exist\n")
	v.e.s.featDossier("solo-feature") // pinned once seen
	v.e.s.sup = newFeatSup(v.e.s.sup.file)
	h := v.e.authed(v.e.login(t))
	r := v.e.do(t, "GET", "/api/term/feature?session=solo-feature", "", h)
	var out struct{ Dossier featDossier }
	if r.StatusCode != 200 {
		t.Fatalf("%d", r.StatusCode)
	}
	decodeBody(t, r, &out)
	if len(out.Dossier.Milestones) != 1 || out.Dossier.Rev == "" {
		t.Fatalf("empty after restart: %+v", out.Dossier)
	}
}

// The page's remaining fields: observedAt is always present (0 = not sampled), objective status, the owner's
// attestation with its time and rev, and an assessment's writer claimed from the agent writer's X-Agent-Sid (its
// word only: author stays null).
func TestV8PageFinalFields(t *testing.T) {
	plan := "## Ship\n- [x] Export @Builder\n  - covers: D1\n  - evidence: file out.csv\n"
	v := newSupEnv(t, plan)
	writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "FEATURE.md"), "- [ ] D1: Export runs\n- [ ] D2: Docs\n")
	attestStep(t, v, 0)
	h := v.e.authed(v.e.login(t))
	get := func() map[string]any {
		var out struct{ Page map[string]any }
		decodeBody(t, v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", h), &out)
		return out.Page
	}
	p := get()
	if v, ok := p["observedAt"]; !ok || v != float64(0) {
		t.Fatalf("observedAt: %v %v", v, ok)
	}
	items := p["objectives"].(map[string]any)["items"].([]any)
	if items[0].(map[string]any)["status"] != "met" || items[1].(map[string]any)["status"] != "unmet" {
		t.Fatalf("status: %v", items)
	}
	k := v.e.s.featDossier(supTeam).Milestones[0].Steps[0].Key
	at := p["evidence"].(map[string]any)[k].(map[string]any)["attest"].(map[string]any)
	if at["rev"] == "" || at["rev"] == nil || at["at"] == nil {
		t.Fatalf("attest: %v", at)
	}
	// an agent adds an assessment through the writer, saying who it is in the header
	d := v.e.s.featDossier(supTeam)
	text := plan + "  - assessment: pass — it ran — cites out.csv\n"
	req := httptest.NewRequest("POST", "/agent/feature/doc", strings.NewReader(`{"session":"`+supTeam+`","doc":"plan","rev":"`+d.Rev+`","text":`+toJSON(text)+`}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Agent-Sid", "sid-rev-9")
	rec := httptest.NewRecorder()
	v.e.s.agentHandler("tok").ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("agent write: %d %s", rec.Code, rec.Body)
	}
	as := get()["evidence"].(map[string]any)[k].(map[string]any)["assessment"].(map[string]any)
	if as["claimedSid"] != "sid-rev-9" || as["author"] != nil {
		t.Fatalf("assessment claim: %v", as)
	}
}
