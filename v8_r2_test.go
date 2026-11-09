package main

import (
	"strings"
	"testing"
	"time"
)

// Run proofs: a model-selected 'proof: run — …' is met mechanically only when the receipt's output shows every
// backtick literal of the criterion; the text outside backticks is description.
func TestR2RunProofLiterals(t *testing.T) {
	v, st := checkEnv(t)
	out, failed := bash(t, st.RunAs)
	record(t, v.transcript(), "toolu_ok1", st.RunAs, out, failed, false, time.Now())
	got := v.tick(t, st, "run "+st.Attempt+"\n  - proof: run — exit 0 and output names `Hello from` and `Goodbye`")
	if got.State == "done" || !strings.Contains(got.Why, `doesn't show "Goodbye"`) {
		t.Fatalf("a literal the output lacks: %+v", got)
	}
	got = v.tick(t, st, "run "+st.Attempt+"\n  - proof: run — exit 0 and output names `Hello from`")
	if got.State != "done" || got.Label != "check ran (receipt)" {
		t.Fatalf("every literal shown: %+v", got)
	}
}

// Proof wire format: required carries the proof's kind, criterion and the mechanical clauses (null: none, attestation only);
// criterion says whether it's met, how, and what's missing.
func TestR2ProofWire(t *testing.T) {
	ev := func(st featPlanStep) featEvid {
		st.Key = "k"
		return featEvidence(featDossier{Milestones: []featMilestone{{Steps: []featPlanStep{st}}}}, featObj{}, featEvCtx{})["k"]
	}
	e := ev(featPlanStep{PlanProof: "run — exit 0 and output names `12 rows`", State: "claimed", Why: `the run's output doesn't show "12 rows" (the proof's criterion)`})
	if r := e.Required; r == nil || r.Kind != "run" || r.Criterion != "exit 0 and output names `12 rows`" || strings.Join(r.Mechanical, "|") != "exit 0|output contains `12 rows`" {
		t.Fatalf("run proof required: %+v", r)
	}
	if c := e.Criterion; c == nil || c.Met == nil || *c.Met || c.How != "mechanical check" || strings.Join(c.Missing, "|") != "output contains `12 rows`" {
		t.Fatalf("run proof unmet: %+v", c)
	}
	e = ev(featPlanStep{PlanProof: "run — exit 0 and output names `12 rows`", State: "done", Label: "check ran (receipt)"})
	if c := e.Criterion; c == nil || c.Met == nil || !*c.Met || c.How != "mechanical check" {
		t.Fatalf("run proof met: %+v", c)
	}
	e = ev(featPlanStep{PlanProof: "image — the export opens in a spreadsheet", State: "done", Label: "check ran (receipt)", Assessment: "pass — looks right"})
	if r, c := e.Required, e.Criterion; r.Mechanical != nil || c == nil || c.Met != nil || c.How != "needs your attestation" {
		t.Fatalf("a non-run proof is never met mechanically, nor by an assessment: %+v %+v", r, c)
	}
	e = ev(featPlanStep{PlanProof: "image — the export opens", State: "done", Label: "owner verified"})
	if c := e.Criterion; c == nil || c.Met == nil || !*c.Met || c.How != "owner attested" {
		t.Fatalf("attested: %+v", c)
	}
}

// Non-run proofs: a model proof of a non-run kind isn't met by a check's receipt, only by the owner's attestation. A
// receipt that verified the step before a proof line was added (or its criterion changed) doesn't carry over: every
// run is judged against the step's current proof, with no exemption by age.
func TestR2NonRunProofNeedsAttestation(t *testing.T) {
	v, st := checkEnv(t)
	out, failed := bash(t, st.RunAs)
	record(t, v.transcript(), "toolu_ok1", st.RunAs, out, failed, false, time.Now())
	if got := v.tick(t, st, "run "+st.Attempt); got.State != "done" {
		t.Fatalf("receipt with no proof line: %+v", got)
	}
	got := v.tick(t, st, "run "+st.Attempt+"\n  - proof: image — the greeting shows on the page")
	if got.State == "done" || !strings.Contains(got.Why, "your attestation meets it") {
		t.Fatalf("an old receipt met a non-run proof added later: %+v", got)
	}
	got = v.tick(t, st, "run "+st.Attempt+"\n  - proof: run — output names `Goodbye`")
	if got.State == "done" || !strings.Contains(got.Why, "Goodbye") {
		t.Fatalf("an old receipt met a changed run criterion: %+v", got)
	}
}
