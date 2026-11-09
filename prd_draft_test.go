package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FEATURE.draft.md: agents author a requirements draft; only the owner turns it into FEATURE.md (promote), as one
// committed change that voids any earlier acceptance.

const (
	prdOld   = "# Outcome\n- [ ] It works\n"
	prdDraft = "# Outcome (reworded by the lead)\n- [ ] It works\n" // same outcome set: only the explicit clear voids acceptance
)

func prdPaths(v *supEnv) (feat, draft, intent string) {
	dir := filepath.Dir(v.planFile())
	return filepath.Join(dir, "FEATURE.md"), filepath.Join(dir, "FEATURE.draft.md"), filepath.Join(dir, ".promote-txn")
}

func readOr(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "<absent>"
	}
	return string(b)
}

// prdSetup: a verified plan, accepted outcomes, done, and a draft the agent wrote
func prdSetup(t *testing.T) *supEnv {
	t.Helper()
	v := newSupEnv(t, "## Ship\n- [x] Check output\n")
	attestStep(t, v, 0)
	acceptOutcomes(t, v)
	if c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done"}`); c != 200 {
		t.Fatalf("done: %d %v", c, out)
	}
	do := agentDo(t, v)
	if c, out := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"draft","text":`+jsonQ(prdDraft)+`,"rev":"`+featRev(nil)+`"}`); c != 200 {
		t.Fatalf("draft: %d %v", c, out)
	}
	return v
}

func jsonQ(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func prdPromote(v *supEnv, draftRev, featureRev string) (int, map[string]any) {
	return call(v.e.s.termFeaturePromote, `{"session":"`+supTeam+`","draftRev":"`+draftRev+`","featureRev":"`+featureRev+`"}`)
}

func prdCur(v *supEnv) (int, map[string]any) {
	d := v.e.s.featDossier(supTeam)
	return prdPromote(v, d.Revs["draft"], d.Revs["feature"])
}

// prdState: "old" (FEATURE.md, acceptance and draft as they were) or "new" (the draft adopted, acceptance cleared,
// no draft), each with no intent left; anything else is a mixed state
func prdState(v *supEnv, oldAcc string) string {
	feat, draft, intent := prdPaths(v)
	v.e.s.sup.mu.Lock()
	acc, had := v.e.s.sup.Accepts[supTeam]
	v.e.s.sup.mu.Unlock()
	f, dr, in := readOr(feat), readOr(draft), readOr(intent)
	switch {
	case in != "<absent>":
		return "intent left: " + in
	case f == prdOld && dr == prdDraft && had == (oldAcc != "") && acc == oldAcc:
		return "old"
	case f == prdDraft && dr == "<absent>" && !had:
		return "new"
	}
	return "mixed: feature=" + f + " draft=" + dr + " accepted=" + acc
}

func TestPrdAgentWritesDraftNeverFeature(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] one\n")
	feat, draft, _ := prdPaths(v)
	writeFile(t, feat, prdOld)
	do := agentDo(t, v)
	post := func(doc, text, rev string) int {
		body := `{"session":"demo-feature","doc":"` + doc + `","text":` + jsonQ(text)
		if rev != "-" {
			body += `,"rev":"` + rev + `"`
		}
		c, _ := do("POST", "/agent/feature/doc", body+"}")
		return c
	}
	if c := post("draft", "x", "-"); c != 400 && c != 409 {
		t.Fatalf("no rev: %d", c)
	}
	if c := post("draft", "x", "stale"); c != 409 {
		t.Fatalf("stale rev on create: %d", c)
	}
	if c := post("draft", "v1\n", featRev(nil)); c != 200 {
		t.Fatalf("create: %d", c)
	}
	if c := post("draft", "v2\n", featRev(nil)); c != 409 {
		t.Fatalf("update at the old rev: %d", c)
	}
	c, out := do("GET", "/agent/feature?session=demo-feature", "")
	d := out["dossier"].(map[string]any)
	rev, _ := d["revs"].(map[string]any)["draft"].(string)
	if c != 200 || d["draft"] != "v1\n" || rev != featRev([]byte("v1\n")) {
		t.Fatalf("read: %d draft=%v rev=%v", c, d["draft"], rev)
	}
	if c := post("draft", "v2\n", rev); c != 200 {
		t.Fatalf("update: %d", c)
	}
	if readOr(draft) != "v2\n" {
		t.Fatal("the draft wasn't written")
	}
	if c := post("feature", "pwned\n", featRev([]byte(prdOld))); c != 403 {
		t.Fatalf("doc=feature: %d", c)
	}
	if readOr(feat) != prdOld {
		t.Fatal("FEATURE.md changed through agent draft writes")
	}
}

func TestPrdPromoteStaleRevs(t *testing.T) {
	v := prdSetup(t)
	acc := v.e.s.sup.accepted(supTeam)
	d := v.e.s.featDossier(supTeam)
	if c, _ := prdPromote(v, "stale", d.Revs["feature"]); c != 409 {
		t.Fatalf("stale draftRev: %d", c)
	}
	if c, _ := prdPromote(v, d.Revs["draft"], "stale"); c != 409 {
		t.Fatalf("stale featureRev: %d", c)
	}
	if s := prdState(v, acc); s != "old" {
		t.Fatal(s)
	}
	_, draft, _ := prdPaths(v)
	os.Remove(draft)
	if c, _ := prdPromote(v, featRev(nil), d.Revs["feature"]); c != 409 {
		t.Fatalf("absent draft: %d", c)
	}
	waitIdle(t, v.a, supTeam)
}

func TestPrdPromoteAdoptsAndVoidsAcceptance(t *testing.T) {
	v := prdSetup(t)
	if c, out := prdCur(v); c != 200 || out["warning"] != nil {
		t.Fatalf("promote: %d %v", c, out)
	}
	if s := prdState(v, ""); s != "new" {
		t.Fatal(s)
	}
	if d := v.e.s.featDossier(supTeam); d.Accepted || d.Draft != "" {
		t.Fatalf("after promote: accepted=%v draft=%q", d.Accepted, d.Draft)
	}
	v.a.mu.Lock()
	st := v.a.thread(supTeam).Status
	v.a.mu.Unlock()
	if st != "working" {
		t.Fatalf("a done thread didn't reopen: %s", st)
	}
	if c, _ := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done"}`); c != 409 {
		t.Fatalf("done on a voided acceptance: %d", c)
	}
	d := v.e.s.featDossier(supTeam)
	if c, out := call(v.e.s.termFeatureAccept, `{"session":"`+supTeam+`","rev":"`+d.Revs["feature"]+`"}`); c != 200 {
		t.Fatalf("fresh accept: %d %v", c, out)
	}
	if c, out := call(v.e.s.agentThreadUpdate, `{"id":"demo-feature","status":"done"}`); c != 200 {
		t.Fatalf("done after a fresh accept: %d %v", c, out)
	}
	if !strings.Contains(readOr(filepath.Join(filepath.Dir(v.planFile()), "LOG.md")), "owner promoted the requirements draft") {
		t.Fatal("no LOG line")
	}
	waitIdle(t, v.a, supTeam)
}

func TestPrdDiscard(t *testing.T) {
	v := prdSetup(t)
	_, draft, _ := prdPaths(v)
	disc := func(rev string) int {
		c, _ := call(v.e.s.termFeatureDiscard, `{"session":"`+supTeam+`","draftRev":"`+rev+`"}`)
		return c
	}
	if c := disc("stale"); c != 409 || readOr(draft) != prdDraft {
		t.Fatalf("stale discard: %d", c)
	}
	if c := disc(featRev([]byte(prdDraft))); c != 200 || readOr(draft) != "<absent>" {
		t.Fatalf("discard: %d", c)
	}
	waitIdle(t, v.a, supTeam)
}

func TestPrdPromoteNotOnAgentRoute(t *testing.T) {
	v := prdSetup(t)
	do := agentDo(t, v)
	for _, p := range []string{"/agent/feature/promote", "/agent/feature/discard", "/api/term/feature/promote", "/api/term/feature/discard"} {
		if c, _ := do("POST", p, `{"session":"demo-feature"}`); c != 404 && c != 405 {
			t.Fatalf("%s reachable with the bearer: %d", p, c)
		}
	}
	waitIdle(t, v.a, supTeam)
}

// Failure at each boundary, and a crash after each step: always exactly the old record or exactly the new one.
func TestPrdPromoteFailureInjection(t *testing.T) {
	boom := errors.New("injected")
	for _, accepted := range []bool{true, false} { // with an earlier acceptance (and done), and without one
		for _, tc := range []struct {
			stage, want string
			code        int
			crash       bool
		}{
			{"intent", "old", 500, false},
			{"feature", "old", 500, false},
			{"commit", "old", 500, false},
			{"draft", "new", 200, false},
			{"after-intent", "old", 500, true},
			{"after-feature", "old", 500, true},
			{"after-commit", "new", 500, true},
		} {
			t.Run(fmt.Sprintf("accepted=%v/%s", accepted, tc.stage), func(t *testing.T) {
				v := prdSetup(t)
				if !accepted { // no acceptance and not done: before and after the commit can't be told apart by the files
					v.e.s.sup.mu.Lock()
					delete(v.e.s.sup.Accepts, supTeam)
					v.e.s.sup.save()
					v.e.s.sup.mu.Unlock()
					v.a.mu.Lock()
					v.a.thread(supTeam).Status = "working"
					v.a.saveThreads()
					v.a.mu.Unlock()
				}
				acc := v.e.s.sup.accepted(supTeam)
				promoteHook = func(stage string) error {
					if stage == tc.stage {
						return boom
					}
					return nil
				}
				c, out := prdCur(v)
				promoteHook = nil
				if c != tc.code {
					t.Fatalf("%d %v", c, out)
				}
				if tc.stage == "draft" && out["warning"] == nil {
					t.Fatalf("a draft left after the commit went unsaid: %v", out)
				}
				if tc.crash { // a restart: memory from disk, then the startup recovery
					v.e.s.sup = newFeatSup(v.e.s.sup.file)
					v.e.s.recoverPromoteTxn(filepath.Dir(v.planFile()))
				}
				v.e.s.reopenStale()
				if s := prdState(v, acc); s != tc.want {
					t.Fatalf("want %s, got %s", tc.want, s)
				}
				v.a.mu.Lock()
				st := v.a.thread(supTeam).Status
				v.a.mu.Unlock()
				if want := map[bool]string{true: "done", false: "working"}[accepted && tc.want == "old"]; st != want {
					t.Fatalf("thread %s after %s, want %s", st, tc.want, want)
				}
				if len(v.e.s.featDossier(supTeam).Conflicts) != 0 {
					t.Fatalf("settled, still shown pending: %v", v.e.s.featDossier(supTeam).Conflicts)
				}
				waitIdle(t, v.a, supTeam)
			})
		}
	}
}

// While a promote is unsettled the record says so, and done is refused; a write first settles it, and a newer write
// is never rolled back by a later recovery.
func TestPrdPromotePendingAndNewerWrites(t *testing.T) {
	v := prdSetup(t)
	feat, _, intent := prdPaths(v)
	promoteHook = func(stage string) error {
		if stage == "after-feature" {
			return errors.New("crash")
		}
		return nil
	}
	d0 := v.e.s.featDossier(supTeam)
	v.e.s.featPromote(supTeam, d0.Revs["draft"], d0.Revs["feature"]) // the process dies here: no handler continues
	promoteHook = nil
	featPlanMu.Lock() // as if a promote were in progress: no lazy recovery
	d := v.e.s.featDossier(supTeam)
	why := v.e.s.featGate(supTeam, supTeam, nil, true)
	featPlanMu.Unlock()
	if len(d.Conflicts) == 0 || !strings.Contains(why, "promote") {
		t.Fatalf("an unsettled promote shown as current: %v / %q", d.Conflicts, why)
	}
	// the owner writes FEATURE.md at the revision they see after settling (the old one): it stands
	v.e.s.featDossier(supTeam) // settles (old: not committed)
	cur, _ := os.ReadFile(feat)
	if string(cur) != prdOld {
		t.Fatalf("not settled to old: %q", cur)
	}
	newer := "# Outcome\n- [ ] It works\n- [ ] Owner's newer line\n"
	if c, out := call(v.e.s.termFeatureDoc, `{"session":"`+supTeam+`","doc":"feature","text":`+jsonQ(newer)+`,"rev":"`+featRev(cur)+`"}`); c != 200 {
		t.Fatalf("newer write: %d %v", c, out)
	}
	stale, _ := json.Marshal(promoteTxn{ID: "x", Session: supTeam, Draft: []byte(prdDraft)}) // a journal left over from the cut-short promote
	writeFile(t, intent, string(stale))
	v.e.s.recoverPromoteTxn(filepath.Dir(v.planFile()))
	if b, _ := os.ReadFile(feat); string(b) != newer {
		t.Fatalf("recovery rolled back a newer write: %q", b)
	}
	waitIdle(t, v.a, supTeam)
}
