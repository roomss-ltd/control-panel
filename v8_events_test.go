package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func v8Keys(v *supEnv) []string {
	var ks []string
	for _, m := range v.e.s.featDossier(supTeam).Milestones {
		for _, st := range m.Steps {
			ks = append(ks, st.Key)
		}
	}
	return ks
}

// An assignment is an explicit event: validated, persisted, read back by the session ledger; nothing is typed into a pane.
func TestV8AssignEvent(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n- [ ] Review docs @Reviewer\n")
	do := agentDo(t, v)
	k := v8Keys(v)
	for name, body := range map[string]string{
		"unknown step":          `{"session":"demo-feature","step":"000000000000","tab":"Builder"}`,
		"relation to itself":    `{"session":"demo-feature","step":"` + k[0] + `","tab":"Builder","relation":{"kind":"pair","with":"` + k[0] + `"}}`,
		"relation to a sid":     `{"session":"demo-feature","step":"` + k[0] + `","tab":"Builder","relation":{"kind":"pair","with":"abc-sid"}}`,
		"unknown relation kind": `{"session":"demo-feature","step":"` + k[0] + `","tab":"Builder","relation":{"kind":"likes","with":"` + k[1] + `"}}`,
		"no tab":                `{"session":"demo-feature","step":"` + k[0] + `"}`,
	} {
		if c, out := do("POST", "/agent/feature/assign", body); c != 400 {
			t.Fatalf("%s: %d %v", name, c, out)
		}
	}
	if c, _ := do("POST", "/agent/feature/assign", `{"session":"demo-feature","step":"`+k[0]+`","tab":"Builder","thread":"other-thread"}`); c != 409 {
		t.Fatalf("wrong thread: %d", c)
	}
	if n := len(v.e.s.featAssignments(supTeam)); n != 0 {
		t.Fatalf("refused assignments recorded: %d", n)
	}
	c, out := do("POST", "/agent/feature/assign", `{"session":"demo-feature","step":"`+k[1]+`","tab":"Reviewer","pane":12,"sid":"s-2","relation":{"kind":"handoff","with":"`+k[0]+`","why":"reviews the docs"}}`)
	if c != 200 || out["id"] != "ev-1" {
		t.Fatalf("assign: %d %v", c, out)
	}
	v.e.s.sup = newFeatSup(v.e.s.sup.file) // a restart
	got := v.e.s.featAssignments(supTeam)
	if len(got) != 1 || got[0].ID != "ev-1" || got[0].Seq != 1 || got[0].Step != k[1] || got[0].Pane != 12 || got[0].Sid != "s-2" ||
		got[0].Rel == nil || got[0].Rel.With != k[0] || got[0].Rel.Kind != "handoff" {
		t.Fatalf("after a restart: %+v", got)
	}
	if len(v.typed()) != 0 {
		t.Fatalf("an assignment typed into a pane: %v", v.typed())
	}
}

// Paging by id while events arrive loses none and repeats none; reading changes no state.
func TestV8EventsPaging(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	add := func(n int) {
		for i := 0; i < n; i++ {
			if _, err := v.e.s.featEvent(supTeam, featEv{Kind: "message", Text: fmt.Sprint(i)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(250)
	h := v.e.authed(v.e.login(t))
	before, _ := os.ReadFile(v.e.s.sup.file)
	seen, after := map[string]bool{}, "ev-0"
	last := int64(0)
	for page := 0; page < 10; page++ {
		var out struct {
			Events []featEv `json:"events"`
		}
		r := v.e.do(t, "GET", "/api/term/feature/events?session="+supTeam+"&after="+after+"&limit=200", "", h)
		if r.StatusCode != 200 {
			t.Fatalf("page %d: %d", page, r.StatusCode)
		}
		decodeBody(t, r, &out)
		if page == 0 {
			if len(out.Events) != 200 {
				t.Fatalf("limit: %d", len(out.Events))
			}
			now, _ := os.ReadFile(v.e.s.sup.file)
			if string(now) != string(before) {
				t.Fatal("a GET changed featsup.json")
			}
			add(30) // arrive mid-paging
		}
		if len(out.Events) == 0 {
			break
		}
		for _, e := range out.Events {
			if seen[e.ID] || e.Seq <= last {
				t.Fatalf("dup or out of order: %s after %d", e.ID, last)
			}
			seen[e.ID], last = true, e.Seq
		}
		after = out.Events[len(out.Events)-1].ID
	}
	if len(seen) != 280 {
		t.Fatalf("lost events: %d of 280", len(seen))
	}
	if r := v.e.do(t, "GET", "/api/term/feature/events?session="+supTeam+"&after=nope", "", h); r.StatusCode != 400 {
		t.Fatalf("bad cursor: %d", r.StatusCode)
	}
	do := agentDo(t, v)
	if c, out := do("GET", "/agent/feature/events?session=demo-feature&after=ev-279&limit=5", ""); c != 200 || len(out["events"].([]any)) != 1 {
		t.Fatalf("bearer read: %d %v", c, out)
	}
}

func decodeBody(t *testing.T, r *http.Response, v any) {
	t.Helper()
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

// An event whose save failed doesn't exist, and its id isn't spent.
func TestV8EventSaveFailure(t *testing.T) {
	v := newSupEnv(t, "")
	file := v.e.s.sup.file
	v.e.s.sup.file = t.TempDir() // a folder: the save fails
	if _, err := v.e.s.featEvent(supTeam, featEv{Kind: "message"}); err == nil {
		t.Fatal("no error")
	}
	v.e.s.sup.file = file
	if len(v.e.s.sup.Events[supTeam]) != 0 {
		t.Fatal("a failed event stayed")
	}
	if e, err := v.e.s.featEvent(supTeam, featEv{Kind: "message"}); err != nil || e.ID != "ev-1" {
		t.Fatalf("%v %v", e.ID, err)
	}
}

// A manager rebind keeps the feature's pinned record (its steps, keys and events); only who manages it changes. A
// second record in the new manager's folder is a conflict, never a silent shadow.
func TestV8RebindKeepsTheRecord(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	d0 := v.e.s.featDossier(supTeam)
	v.e.s.featEvent(supTeam, featEv{Kind: "message"})
	v.a.mu.Lock()
	v.a.thread(supTeam).Status = "archived"
	nt := v.a.newThread("Second lead", "")
	nt.Team, nt.Status = supTeam, "working"
	v.a.mu.Unlock()
	d := v.e.s.featDossier(supTeam)
	if d.Thread != nt.ID || d.Dir != d0.Dir || len(d.Milestones) != 1 || d.Milestones[0].Steps[0].Key != d0.Milestones[0].Steps[0].Key || len(d.Conflicts) != 0 {
		t.Fatalf("rebind moved the record: %s → %s (%s) %+v", d0.Dir, d.Dir, d.Thread, d.Conflicts)
	}
	if len(v.e.s.sup.Events[supTeam]) != 1 {
		t.Fatal("events lost on rebind")
	}
	writeFile(t, filepath.Join(v.e.s.cfg.home, "jarvis-files", "threads", nt.ID, "PLAN.md"), "## Other\n- [ ] Something else\n")
	if _, conflict := v.e.s.featHome(supTeam, nt.ID); conflict == "" {
		t.Fatal("a second record in the new manager's folder shadowed nothing and said nothing")
	}
}
