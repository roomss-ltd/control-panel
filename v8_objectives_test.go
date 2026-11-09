package main

import (
	"path/filepath"
	"strings"
	"testing"
)

const objFeature = "## Objective\nExports work.\n\n## Done\n- [ ] D1: Export runs — done when: a CSV lands\n  - proof: the CSV file\n- [ ] D2: Docs exist\n- [ ] Untagged outcome\n"

// Objectives come from FEATURE.md (the owner's); coverage from explicit covers: lines; an objective is met only when
// every covering step is verified; proof the owner names in FEATURE.md is required.by owner, a plan's is the model's;
// an assessment never verifies.
func TestV8Objectives(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [x] Export @Builder\n  - covers: D1\n  - evidence: file out.csv\n  - assessment: pass — it ran — cites out.csv\n  - proof: a screenshot\n- [ ] Write docs @Writer\n  - covers: D2, D9\n")
	writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "FEATURE.md"), objFeature)
	d := v.e.s.featDossier(supTeam)
	o := featObjectives(d)
	if o.Text != "Exports work." || len(o.Items) != 3 {
		t.Fatalf("objectives: %+v", o)
	}
	d1, d2, dq := o.Items[0], o.Items[1], o.Items[2]
	k0, k1 := d.Milestones[0].Steps[0].Key, d.Milestones[0].Steps[1].Key
	if d1.ID != "D1" || d1.DoneWhen != "a CSV lands" || d1.Proof != "the CSV file" || strings.Join(d1.Steps, ",") != k0 || d1.Met {
		t.Fatalf("D1 (its step is only claimed, assessed pass): %+v", d1)
	}
	if d2.ID != "D2" || strings.Join(d2.Steps, ",") != k1 || d2.Met || dq.ID != "D?" {
		t.Fatalf("D2 / untagged: %+v %+v", d2, dq)
	}
	if strings.Join(o.Unknown, ",") != "D9" {
		t.Fatalf("a covers: naming no objective: %v", o.Unknown)
	}
	ev := featEvidence(d, o, featEvCtx{})[k0]
	if ev.Required == nil || ev.Required.By != "owner" || ev.Required.Text != "the CSV file" || ev.Claim == "" || ev.Check != nil || ev.Attest != nil || ev.Assessment == nil || ev.Assessment.Verdict != "pass" || ev.Assessment.Author != nil {
		t.Fatalf("evidence: %+v %+v %+v", ev, ev.Required, ev.Assessment)
	}
	attestStep(t, v, 0)
	d = v.e.s.featDossier(supTeam)
	o = featObjectives(d)
	if !o.Items[0].Met || o.Items[1].Met {
		t.Fatalf("met only when every covering step is verified: %+v", o.Items)
	}
	if ev := featEvidence(d, o, featEvCtx{})[k0]; ev.Attest == nil || ev.Check != nil {
		t.Fatalf("owner attestation: %+v", ev)
	}
	if why := v.e.s.featGate(supTeam, supTeam, nil, false); why == "" {
		t.Fatalf("gate: %q", why)
	}
}

// With every step verified, an objective no step covers still refuses done.
func TestV8UncoveredObjectiveBlocksDone(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [x] Export @Builder\n  - covers: D1\n")
	writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "FEATURE.md"), "## Done\n- [ ] D1: Export runs\n- [ ] D2: Docs exist\n")
	attestStep(t, v, 0)
	if why := v.e.s.featGate(supTeam, supTeam, nil, false); !strings.Contains(why, "D2") {
		t.Fatalf("an uncovered objective: %q", why)
	}
}

// Proof the owner named is met only by the owner's attestation of a covering step (bound to its evidence and result);
// a receipt verifies the step's check, not the owner's named proof.
func TestV8OwnerProofNeedsAttestation(t *testing.T) {
	st := featPlanStep{Key: "k1", State: "done", Label: "check ran (receipt)", Covers: []string{"D1"}, Evidence: "image shot.png"}
	d := featDossier{Feature: "- [ ] D1: The page looks right\n  - proof: a screenshot of the page\n", Milestones: []featMilestone{{Steps: []featPlanStep{st}}}}
	if it := featObjectives(d).Items[0]; it.Met || !strings.Contains(strings.Join(it.Missing, ";"), "attest") {
		t.Fatalf("a receipt met the owner's named proof: %+v", it)
	}
	d.Milestones[0].Steps[0].Label = "owner verified"
	if it := featObjectives(d).Items[0]; !it.Met {
		t.Fatalf("attested: %+v", it)
	}
}

// Changing what a step covers voids its owner attestation and its check approval (a verified step can't be re-used for
// another objective unseen), and the change is an event for the owner to review.
func TestV8CoversChangeVoids(t *testing.T) {
	plan := "## Ship\n- [x] Export @Builder\n  - covers: D1\n  - evidence: file out.csv\n  - check: true\n  - cwd: /tmp\n"
	v := newSupEnv(t, plan)
	writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "FEATURE.md"), "- [ ] D1: Export runs\n- [ ] D2: Import runs\n")
	attestStep(t, v, 0)
	d := v.e.s.featDossier(supTeam)
	if c := v.approve(t, map[string]string{"key": d.Milestones[0].Steps[0].Key, "check": "true", "cwd": "/tmp"}); c != 200 {
		t.Fatal(c)
	}
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.State != "done" || !st.Approval.Check {
		t.Fatalf("before: %+v", st)
	}
	h := v.e.authed(v.e.login(t))
	body := `{"session":"` + supTeam + `","doc":"plan","rev":"` + d.Rev + `","text":` + toJSON(strings.Replace(plan, "covers: D1", "covers: D1, D2", 1)) + `}`
	if r := v.e.do(t, "POST", "/api/term/feature/doc", body, h); r.StatusCode != 200 {
		t.Fatalf("edit: %d", r.StatusCode)
	}
	st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]
	if st.State == "done" || st.Approval.Check {
		t.Fatalf("a covers change kept the attestation or approval: %+v", st)
	}
	var cov []featEv
	for _, e := range v.e.s.sup.Events[supTeam] {
		if e.Kind == "coverage" {
			cov = append(cov, e)
		}
	}
	if len(cov) != 1 || cov[0].Step != st.Key || !strings.Contains(cov[0].Text, "D1 → D1, D2") {
		t.Fatalf("coverage event: %+v", cov)
	}
}

// Untagged outcomes don't gate only for a feature the panel knew before objectives existed (recorded once, at the
// upgrade); a new feature's outcome without an id refuses done.
func TestV8UntaggedOutcomeNeedsProvenance(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [x] Export @Builder\n")
	writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "FEATURE.md"), "- [ ] It works\n")
	attestStep(t, v, 0)
	if why := v.e.s.featGate(supTeam, supTeam, nil, false); !strings.Contains(why, "id") {
		t.Fatalf("a new feature's untagged outcome: %q", why)
	}
	dir := filepath.Dir(v.planFile())
	writeFile(t, v.e.s.sup.file, `{"homes":{"`+supTeam+`":"`+dir+`"}}`) // featsup.json from before the upgrade
	v.e.s.sup = newFeatSup(v.e.s.sup.file)
	if !v.e.s.sup.ObjLegacy[supTeam] {
		t.Fatal("not recorded as known before the upgrade")
	}
	if why := v.e.s.featGate(supTeam, supTeam, nil, false); strings.Contains(why, " id") {
		t.Fatalf("a pre-upgrade feature: %q", why)
	}
	v.e.s.sup = newFeatSup(v.e.s.sup.file)
	v.e.s.sup.mu.Lock()
	v.e.s.sup.Homes["brand-new"] = "/x"
	v.e.s.sup.save()
	v.e.s.sup.mu.Unlock()
	if v.e.s.sup = newFeatSup(v.e.s.sup.file); v.e.s.sup.ObjLegacy["brand-new"] {
		t.Fatal("a feature seen after the upgrade was grandfathered")
	}
}

// An assessment naming who made it is a claim: claimedSid, author still null (independence never claimed).
func TestV8AssessmentClaimedSid(t *testing.T) {
	st := featPlanStep{Key: "k1", State: "claimed", Assessment: "pass — by sid-review-2 — it ran clean — cites out.csv, run.log"}
	a := featEvidence(featDossier{Milestones: []featMilestone{{Steps: []featPlanStep{st}}}}, featObj{}, featEvCtx{})["k1"].Assessment
	if a == nil || a.Verdict != "pass" || a.ClaimedSid != "sid-review-2" || a.Author != nil || a.Why != "it ran clean" || strings.Join(a.Cites, ",") != "out.csv,run.log" {
		t.Fatalf("%+v", a)
	}
}
