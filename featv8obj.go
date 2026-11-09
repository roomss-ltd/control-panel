package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Objectives and proof. Objectives are FEATURE.md's (the owner's) "- [ ] D1: … — done when: …" lines, with an optional
// "  - proof: …" line naming proof the owner requires; plan steps say which objectives they serve ("covers: D1"). An
// objective is met only when it has covering steps and every one is verified (receipt or owner attestation). Three
// results stay apart: the mechanical check, a model's assessment (a judgement, never verification) and the owner's
// attestation.

// featObj is the feature's objectives as the page shows them: FEATURE.md's revision, its "## Objective" text, the
// objectives, and the "covers:" ids that match no objective.
type featObj struct {
	Rev     string        `json:"rev"`
	Text    string        `json:"text,omitempty"` // the "## Objective" section
	Items   []featObjItem `json:"items"`
	Unknown []string      `json:"unknown,omitempty"` // covers: ids no objective has
}

// featObjItem is one objective (a D<n> line) with the steps that cover it and whether it is met.
type featObjItem struct {
	ID       string   `json:"id"` // D<n>, or "D?" for an outcome line without an id
	Text     string   `json:"text"`
	DoneWhen string   `json:"doneWhen,omitempty"`
	Proof    string   `json:"proof,omitempty"` // required by the owner
	Steps    []string `json:"steps"`           // covering step keys
	Met      bool     `json:"met"`
	Status   string   `json:"status"`            // met, unmet, or unknown (an outcome without an id: not tracked as an objective)
	Missing  []string `json:"missing,omitempty"` // why not met
}

// Patterns for the objective lines in FEATURE.md: the checklist line, its D<n> id, the "done when" clause and the
// indented "proof:" line.
var (
	featDodRe      = regexp.MustCompile(`^[-*] \[[ xX~?]\]\s+(.+?)\s*$`)
	featDodIDRe    = regexp.MustCompile(`^(D\d{1,4})\s*:\s*(.*)$`)
	featDoneWhenRe = regexp.MustCompile(`(?i)\s+(?:—|-|–)\s*done when\s*:\s*`)
	featObjProofRe = regexp.MustCompile(`^\s+[-*]\s*proof\s*:\s*(.+?)\s*$`)
)

// featObjectives reads the objectives from FEATURE.md and marks each as met or unmet from the plan's steps: met needs at
// least one covering step, every covering step verified done, and (when the owner named a proof) the owner's
// attestation of a covering step. Outcome lines without a D<n> id are listed but have status "unknown".
func featObjectives(d featDossier) featObj {
	o := featObj{Rev: d.Revs["feature"], Items: []featObjItem{}}
	var sec []string
	in := false
	for _, l := range strings.Split(d.Feature, "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(l), "## "); ok {
			in = strings.EqualFold(strings.TrimSpace(h), "objective")
			continue
		}
		if in {
			sec = append(sec, l)
		}
		if m := featDodRe.FindStringSubmatch(l); m != nil {
			it := featObjItem{ID: "D?", Text: m[1], Steps: []string{}}
			if x := featDodIDRe.FindStringSubmatch(m[1]); x != nil {
				it.ID, it.Text = x[1], x[2]
			}
			if p := featDoneWhenRe.FindStringIndex(it.Text); p != nil {
				it.Text, it.DoneWhen = it.Text[:p[0]], it.Text[p[1]:]
			}
			o.Items = append(o.Items, it)
		} else if m := featObjProofRe.FindStringSubmatch(l); m != nil && len(o.Items) > 0 {
			o.Items[len(o.Items)-1].Proof = trunc(m[1], 300)
		}
	}
	o.Text = strings.TrimSpace(strings.Join(sec, "\n"))
	ids := map[string]int{}
	for i, it := range o.Items {
		if it.ID != "D?" {
			ids[it.ID] = i
		}
	}
	state, label := map[string]string{}, map[string]string{}
	for _, m := range d.Milestones {
		for _, st := range m.Steps {
			state[st.Key], label[st.Key] = st.State, st.Label
			for _, c := range st.Covers {
				if i, ok := ids[c]; ok {
					o.Items[i].Steps = append(o.Items[i].Steps, st.Key)
				} else if !strings.Contains(strings.Join(o.Unknown, ","), c) {
					o.Unknown = append(o.Unknown, c)
				}
			}
		}
	}
	for i := range o.Items {
		it := &o.Items[i]
		if it.ID == "D?" {
			it.Status = "unknown"
			continue
		}
		if len(it.Steps) == 0 {
			it.Missing = append(it.Missing, "no step covers it")
		}
		attested := false
		for _, k := range it.Steps {
			if state[k] != "done" {
				it.Missing = append(it.Missing, k+" isn't verified ("+state[k]+")")
			}
			attested = attested || label[k] == "owner verified"
		}
		if it.Proof != "" && !attested { // the owner's named proof: only the owner says a covering step shows it
			it.Missing = append(it.Missing, "the proof you named ("+trunc(it.Proof, 60)+") needs your attestation of a covering step")
		}
		it.Met = len(it.Missing) == 0
		it.Status = map[bool]string{true: "met", false: "unmet"}[it.Met]
	}
	return o
}

// featEvid is the evidence picture of one plan step: the proof required of it, what the panel checked mechanically, the
// model's assessment, the owner's attestation, and the agent's own evidence line (a claim only).
type featEvid struct {
	Required   *featReq    `json:"required,omitempty"`
	Check      *featCheck  `json:"check,omitempty"`      // what the panel verified mechanically (a receipt)
	Assessment *featAssess `json:"assessment,omitempty"` // a model's judgement: never a verification
	Attest     *featCheck  `json:"attest,omitempty"`     // the owner's own statement
	Claim      string      `json:"claim,omitempty"`      // the evidence line as written: claimed only
	StaleWhy   string      `json:"staleWhy,omitempty"`
	Items      []any       `json:"items,omitempty"`
	Criterion  *featCrit   `json:"criterion,omitempty"` // whether the required proof's criterion is met, and how
}

// featReq is the proof a step must show: the owner's (from FEATURE.md) or the plan's own (chosen by the model).
type featReq struct {
	By   string `json:"by"` // owner (FEATURE.md) or model (the plan)
	Text string `json:"text"`
	// For a model's proof "<kind> — <criterion>": Mechanical lists the only clauses the panel checks by itself (for a
	// run: exit status 0, and each `literal` quoted in the criterion appearing in the output); null: nothing is
	// checked mechanically, so only the owner's attestation meets it
	Kind       string   `json:"kind,omitempty"`
	Criterion  string   `json:"criterion,omitempty"`
	Mechanical []string `json:"mechanical"`
}

// featCrit says whether a required proof's criterion is met: mechanically by a receipt, or by the owner's attestation.
type featCrit struct {
	Met     *bool    `json:"met"` // null: not decidable mechanically
	How     string   `json:"how"` // mechanical check | needs your attestation | owner attested
	Missing []string `json:"missing,omitempty"`
}

// featCheck is a statement about a step: a check's receipt, or the owner's attestation (with when and at which plan
// revision it was made).
type featCheck struct {
	Source string `json:"source,omitempty"` // receipt
	Text   string `json:"text"`
	At     int64  `json:"at,omitempty"`  // the owner's attestation: when
	Rev    string `json:"rev,omitempty"` // and at which plan revision
}

// featEvCtx is what the panel recorded beside the record, by step key: owner attestations, the claimed writers of
// assessments, and the file evidence found for each step.
type featEvCtx struct {
	Attests map[string]featAttest
	Claims  map[string]featAssessBy
	Items   map[string][]any // file evidence by step key (featR1Items)
}

// featAssessBy records the X-Agent-Sid of the agent write that put a step's assessment line in (the writer's own
// word), with that line's hash.
type featAssessBy struct {
	Sid string `json:"sid"`
	Sum string `json:"sum"`
}

// featAssess is a step's assessment line ("pass|fail|insufficient — why — cites …") parsed. It is a judgement, never a
// verification.
type featAssess struct {
	Verdict string   `json:"verdict"`
	Why     string   `json:"why,omitempty"`
	Cites   []string `json:"cites,omitempty"`
	Author  *bool    `json:"author"` // null: who wrote it isn't bound, so independence is never claimed
	// ClaimedSid: the session the line says made it ("by <sid>"): its word only, never checked against the panel
	ClaimedSid string `json:"claimedSid,omitempty"`
}

// featItem is one piece of evidence: a kind (run, file, image, doc, db, link, ui) and its reference.
type featItem struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// featEvKinds lists the evidence kinds an "evidence:" line may start with.
var featEvKinds = map[string]bool{"run": true, "file": true, "image": true, "doc": true, "db": true, "link": true, "ui": true, "log": true} // log: older records

// featEvidence builds the evidence picture for every step that has any: its required proof (the owner's first, else the
// plan's), whether the criterion is met, the receipt or attestation behind a verified step, the agent's claim and the
// model's assessment.
func featEvidence(d featDossier, o featObj, x featEvCtx) map[string]featEvid {
	owner := map[string]string{}
	for _, it := range o.Items {
		for _, k := range it.Steps {
			if it.Proof != "" && owner[k] == "" {
				owner[k] = it.Proof
			}
		}
	}
	out := map[string]featEvid{}
	for _, m := range d.Milestones {
		for _, st := range m.Steps {
			var e featEvid
			switch {
			case owner[st.Key] != "":
				e.Required = &featReq{By: "owner", Text: owner[st.Key]}
			case st.PlanProof != "":
				e.Required = &featReq{By: "model", Text: st.PlanProof}
				k, c, _ := strings.Cut(st.PlanProof, " ")
				e.Required.Kind, e.Required.Criterion = k, strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(c), "—–-"))
				if k == "run" {
					e.Required.Mechanical = []string{"exit 0"}
					for _, l := range featLiterals(c) {
						e.Required.Mechanical = append(e.Required.Mechanical, "output contains `"+l+"`")
					}
				}
			}
			if r := e.Required; r != nil {
				yes, no := true, false
				switch {
				case st.Label == "owner verified":
					e.Criterion = &featCrit{Met: &yes, How: "owner attested"}
				case r.Mechanical == nil: // an owner's proof or a non-run kind: never met by a receipt, a seal or an assessment
					e.Criterion = &featCrit{How: "needs your attestation"}
				case st.Label == "check ran (receipt)":
					e.Criterion = &featCrit{Met: &yes, How: "mechanical check"}
				default:
					e.Criterion = &featCrit{Met: &no, How: "mechanical check"}
					for _, l := range featLiterals(r.Criterion) {
						if strings.Contains(st.Why, strconv.Quote(trunc(l, 80))) {
							e.Criterion.Missing = append(e.Criterion.Missing, "output contains `"+l+"`")
						}
					}
				}
			}
			switch st.Label {
			case "check ran (receipt)":
				e.Check = &featCheck{Source: "receipt", Text: st.Proof}
			case "owner verified":
				e.Attest = &featCheck{Text: st.Proof}
				if a, ok := x.Attests[st.Key]; ok {
					e.Attest.At, e.Attest.Rev = a.At, a.Rev
				}
			}
			if st.Evidence != "" {
				e.Claim = st.Evidence
				if it, ok := x.Items[st.Key]; ok {
					e.Items = it
				} else if k, ref, ok := strings.Cut(st.Evidence, " "); ok && featEvKinds[k] {
					e.Items = []any{featItem{k, strings.TrimSpace(ref)}}
				}
			}
			if st.State != "done" && st.Why != "" {
				e.StaleWhy = st.Why
			}
			if st.Assessment != "" {
				parts := regexp.MustCompile(`\s+(?:—|–|-)\s+`).Split(st.Assessment, -1)
				a := &featAssess{Verdict: strings.ToLower(strings.TrimSpace(parts[0]))}
				var why []string
				for _, p := range parts[1:] {
					p = strings.TrimSpace(p)
					switch {
					case strings.HasPrefix(p, "by "):
						a.ClaimedSid = strings.TrimSpace(strings.TrimPrefix(p, "by "))
					case strings.HasPrefix(p, "cites "):
						for _, c := range strings.Split(strings.TrimPrefix(p, "cites "), ",") {
							if c = strings.TrimSpace(c); c != "" {
								a.Cites = append(a.Cites, c)
							}
						}
					default:
						why = append(why, p)
					}
				}
				a.Why = strings.Join(why, " — ")
				if c, ok := x.Claims[st.Key]; ok && a.ClaimedSid == "" && c.Sum == featRev([]byte(st.Assessment)) {
					a.ClaimedSid = c.Sid
				}
				e.Assessment = a
			}
			if e.Required != nil || e.Check != nil || e.Attest != nil || e.Claim != "" || e.Assessment != nil {
				out[st.Key] = e
			}
		}
	}
	return out
}

// featCoverageEvents logs an event when a plan write changes what a step covers, so the owner can review it (the
// step's attestation and check approval no longer hold).
func (s *server) featCoverageEvents(session, before, after string) {
	old := map[string]string{}
	for _, m := range parseMilestones(before) {
		for _, st := range m.Steps {
			old[st.Key] = strings.Join(st.Covers, ", ")
		}
	}
	rev := featRev([]byte(after))
	for _, m := range parseMilestones(after) {
		for _, st := range m.Steps {
			was, ok := old[st.Key]
			if now := strings.Join(st.Covers, ", "); ok && was != now {
				s.featEvent(session, featEv{Kind: "coverage", Step: st.Key, Rev: rev,
					Text: "covers: " + cmpOr(was, "nothing") + " → " + cmpOr(now, "nothing") + " (its attestation and check approval no longer hold)"})
			}
		}
	}
}

// featAssessClaims records the writer's claimed session id when an agent write adds or changes a step's assessment
// line.
func (s *server) featAssessClaims(session, before, after, sid string) {
	old := map[string]string{}
	for _, m := range parseMilestones(before) {
		for _, st := range m.Steps {
			old[st.Key] = st.Assessment
		}
	}
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	changed := false
	for _, m := range parseMilestones(after) {
		for _, st := range m.Steps {
			if st.Assessment != "" && st.Assessment != old[st.Key] {
				s.sup.AssessBy[session+"/"+st.Key] = featAssessBy{Sid: sid, Sum: featRev([]byte(st.Assessment))}
				changed = true
			}
		}
	}
	if changed {
		s.sup.save()
	}
}
