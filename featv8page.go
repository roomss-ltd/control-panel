package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// featPage assembles the feature page's data for the owner's GET: from the record (the dossier in f), the event log
// and the cached views of the other parts (session ledger, code delivery, artifacts). The keys are events, seen,
// reviewed, log, tickets, objectives, evidence, manager, supervisor, reconciledAt, arts, observedAt, sessions,
// relations, branches, releases and prx. A key that is absent means "not tracked yet", never a placeholder. It only
// reads: no sampling, no fetching, and nothing is marked seen. Call it with no lock held.
func (s *server) featPage(session string, f featFacts) map[string]any {
	keys := map[string]bool{}
	for _, m := range f.Dossier.Milestones {
		for _, st := range m.Steps {
			keys[st.Key] = true
		}
	}
	p := map[string]any{}
	s.sup.mu.Lock()
	evs := s.sup.Events[session]
	tail := evs[max(len(evs)-200, 0):]
	p["events"], p["eventsMore"] = append([]featEv{}, tail...), len(evs) > len(tail)
	sn := s.sup.Seen[session]
	ids := []string{}
	for _, n := range sn.Above {
		ids = append(ids, "ev-"+itoa64(n))
	}
	p["seen"] = map[string]any{"frontier": sn.Frontier, "ids": ids, "unseen": s.sup.unseenLocked(session), "retainedFrom": s.sup.retainedFromLocked(session)}
	rv := map[string]string{}
	for k, v := range s.sup.Reviewed[session] {
		rv[k] = v
	}
	p["reviewed"] = rv
	logs, objIDs, prs := []featLogEntry{}, map[string]bool{}, map[int]bool{}
	for _, it := range featObjectives(f.Dossier).Items {
		objIDs[it.ID] = true
	}
	for _, pr := range f.PRs {
		prs[pr.Number] = true
	}
	for _, e := range evs {
		if e.Kind == "log" {
			if l, ok := featParseLog(e, keys, objIDs, prs); ok {
				l.Stale = e.Rev != "" && e.Rev != f.Dossier.Rev
				logs = append(logs, l)
			}
		}
	}
	s.sup.mu.Unlock()
	p["log"] = logs
	p["tickets"] = s.featTickets(session)
	obj := featObjectives(f.Dossier)
	// evidence is shown whether or not objectives exist
	x := featEvCtx{Attests: map[string]featAttest{}, Claims: map[string]featAssessBy{}}
	s.sup.mu.Lock()
	for k, a := range s.sup.Attests {
		if key, ok := strings.CutPrefix(k, session+"/"); ok {
			x.Attests[key] = a
		}
	}
	for k, a := range s.sup.AssessBy {
		if key, ok := strings.CutPrefix(k, session+"/"); ok {
			x.Claims[key] = a
		}
	}
	s.sup.mu.Unlock()
	x.Items = s.featR1Items(session, f.Dossier)
	p["objectives"], p["evidence"] = obj, featEvidence(f.Dossier, obj, x)
	if f.Thread != nil {
		p["manager"] = map[string]any{"thread": f.Thread.ID, "title": f.Thread.Title, "bound": true}
	}
	_, sampled := s.term.sample()
	sv := map[string]any{"lastSample": unixOr0(sampled), "nextCheckAt": nil, "nextWakeAt": nil}
	s.sup.mu.Lock()
	p["reconciledAt"] = s.sup.ReconciledAt[session] // the last completed supervisor pass (persisted); 0: never
	// The next supervisor check runs on the first panel sample at or after the last pass + supEvery (the earliest it
	// can happen, not a promise).
	if s.sup.at.IsZero() {
		sv["next"] = "on the next panel sample (no supervisor pass since the panel started)"
	} else {
		next := s.sup.at.Add(supEvery)
		sv["nextCheckAt"], sv["next"] = next.Unix(), "on the first panel sample at or after "+next.UTC().Format("15:04:05")+" UTC"
	}
	// The manager's next wake. A lane is a tab of the team's session. Only a supervisor pass that finds idle lanes (held
	// ones excluded) sends a wake to the thread's lead.
	held := false
	for k := range s.sup.Holds {
		held = held || strings.HasPrefix(k, session+"/")
	}
	var w featNudge
	if f.Thread != nil {
		w = s.sup.Nudges[f.Thread.ID]
	}
	if held { // held work is left out of wakes; other lanes' idle work still wakes the manager (see supThread)
		sv["held"] = "held work waits for you: no automatic wake for it; other lanes' idle work still wakes the manager"
	}
	switch {
	case w.N > 0:
		sv["nextWakeAt"] = w.At + int64(nudgeBackoff(w.N)/time.Second)
		sv["nextWakeWhy"] = "the earliest repeat wake, if the same lanes are still idle then"
	default:
		sv["nextWakeWhy"] = "none pending: a wake is sent only when a supervisor pass finds an idle lane"
	}
	s.sup.mu.Unlock()
	p["supervisor"] = sv
	if arts := s.featArts(f.Dossier, f.Team, nil); len(arts) > 0 { // the plan's explicit artifact lines only
		p["arts"] = arts
	}
	if at := sampled; !at.IsZero() { // the same sample as supervisor.lastSample
		p["observedAt"] = at.Unix()
	} else {
		p["observedAt"] = 0
		p["teamUnsampled"] = true // the panel hasn't sampled the terminals since it started: team facts unknown, not empty
	}
	if sess, rels, at := s.featSessionsPage(session, f.Dossier); sess != nil || rels != nil {
		p["sessions"], p["relations"] = sess, rels
		if at > 0 {
			p["observedAt"] = at
		}
	}
	if br, rel, prx := s.featCodePage(session); br != nil || rel != nil || prx != nil {
		if br != nil {
			p["branches"] = br
		}
		if rel != nil {
			p["releases"] = rel
		}
		if prx != nil {
			p["prx"] = prx
		}
	}
	return p
}

// itoa64 formats n in decimal.
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// featLogEntry is one typed LOG.md line (DECISION, ACTION, EVIDENCE, BLOCKER or RESEARCH) parsed for the feature page.
// Everything beyond the kind and title is the author's own account, taken only when the line carries that segment;
// ids the panel can check (steps, objectives, PRs) are split into verified fields and "...Claimed" ones.
type featLogEntry struct {
	ID          string           `json:"id"`
	Seq         int64            `json:"seq"`
	At          int64            `json:"at"` // unix s
	Who         string           `json:"who"`
	Kind        string           `json:"kind"`
	Title       string           `json:"title"`
	Step        string           `json:"step,omitempty"`
	StepClaimed string           `json:"stepClaimed,omitempty"` // a step: that names no step of this plan
	Rev         string           `json:"rev,omitempty"`         // the plan revision the panel read when the line was written
	ClaimedRev  string           `json:"claimedRev,omitempty"`  // the writer's own "rev:" (its word only)
	Stale       bool             `json:"stale,omitempty"`       // the plan has changed since
	Considered  []map[string]any `json:"considered,omitempty"`
	Chose       map[string]any   `json:"chose,omitempty"`
	Review      bool             `json:"review,omitempty"`
	// The line's optional segments, present only when written: the author's account, never a receipt.
	Ran               []featRan `json:"ran,omitempty"`
	RanUnparsed       []string  `json:"ranUnparsed,omitempty"` // items not in the ok=,cmd=,result= form, as written
	Next              string    `json:"next,omitempty"`
	Objectives        []string  `json:"objectives,omitempty"`
	ObjectivesClaimed []string  `json:"objectivesClaimed,omitempty"` // ids FEATURE.md doesn't have
	PR                int       `json:"pr,omitempty"`
	PRKnown           *bool     `json:"prKnown,omitempty"` // whether it's one of the feature's PRs
	ProofStep         string    `json:"proofStep,omitempty"`
	ProofStepClaimed  string    `json:"proofStepClaimed,omitempty"` // a proof: naming no step of this plan
	Session           string    `json:"session,omitempty"`          // the writer's X-Agent-Sid
	SessionClaimed    bool      `json:"sessionClaimed,omitempty"`   // always true with session: its word, never authority
}

// featRan is one command a LOG.md line says its author ran, with the result they report.
type featRan struct {
	Cmd    string `json:"cmd"`
	Result string `json:"result"`
	Ok     *bool  `json:"ok"` // null: "?"
}

// featLogDec decodes the escapes a client uses inside LOG.md segments (%25 %7C %3B %2C %3D %0A %09 %0D %20) in one
// pass, so "%257C" becomes "%7C"; any other "%" stays as written.
func featLogDec(v string) string {
	codes := map[string]string{"25": "%", "7C": "|", "3B": ";", "2C": ",", "3D": "=", "0A": "\n", "09": "\t", "0D": "\r", "20": " "}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '%' && i+3 <= len(v) {
			if c, ok := codes[strings.ToUpper(v[i+1:i+3])]; ok {
				b.WriteString(c)
				i += 2
				continue
			}
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// featLogKindRe matches the kind prefix of a typed LOG.md line.
var featLogKindRe = regexp.MustCompile(`^(DECISION|ACTION|EVIDENCE|BLOCKER|RESEARCH)\s*:\s*(.*)$`)

// featParseLog parses a typed LOG.md event: "KIND: summary | considered: a; b | chose: x | step: <key> | rev: <rev> |
// by: @tab", where ran:, next:, objectives:, pr: and proof: segments are also understood. Only typed lines are
// parsed (ok is false for others); each known segment counts once; anything else stays in the title as written.
// keys, objs and prs are the plan's step keys, the objective ids and the PR numbers that exist, so claims about them
// can be told apart from facts.
func featParseLog(e featEv, keys, objs map[string]bool, prs map[int]bool) (featLogEntry, bool) {
	m := featLogKindRe.FindStringSubmatch(e.Text)
	if m == nil {
		return featLogEntry{}, false
	}
	l := featLogEntry{ID: e.ID, Seq: e.Seq, At: e.At.Unix(), Who: "unknown", Kind: m[1], Review: m[1] == "DECISION", Rev: e.Rev,
		Session: e.Sid, SessionClaimed: e.Sid != ""}
	segs := strings.Split(m[2], " | ")
	title, done := []string{segs[0]}, map[string]bool{}
	for _, sg := range segs[1:] {
		k, v, ok := strings.Cut(sg, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || done[k] || !map[string]bool{"considered": true, "chose": true, "step": true, "rev": true, "by": true, "ran": true, "next": true, "objectives": true, "pr": true, "proof": true}[k] {
			title = append(title, sg)
			continue
		}
		done[k] = true
		switch k {
		case "considered":
			for _, o := range strings.Split(v, ";") {
				if o = strings.TrimSpace(o); o != "" {
					l.Considered = append(l.Considered, map[string]any{"option": o})
				}
			}
		case "chose":
			l.Chose = map[string]any{"option": v}
		case "step":
			if keys[v] {
				l.Step = v
			} else {
				l.StepClaimed = v
			}
		case "rev":
			l.ClaimedRev = v
		case "by":
			l.Who = strings.TrimPrefix(v, "@")
		case "ran": // "ok=1|0|?,cmd=<e>,result=<e>; …"
			for _, item := range strings.Split(v, ";") {
				item = strings.TrimSpace(item)
				x, okv, good := featRan{}, "", item != ""
				seen := map[string]bool{}
				for _, kv := range strings.Split(item, ",") {
					k2, v2, cut := strings.Cut(strings.TrimSpace(kv), "=")
					if !cut || seen[k2] {
						good = false
						break
					}
					seen[k2] = true
					switch k2 {
					case "ok":
						okv = v2
					case "cmd":
						x.Cmd = featLogDec(v2)
					case "result":
						x.Result = featLogDec(v2)
					default:
						good = false
					}
				}
				switch okv {
				case "1", "0":
					b := okv == "1"
					x.Ok = &b
				case "?":
				default:
					good = false
				}
				if good && seen["cmd"] {
					l.Ran = append(l.Ran, x)
				} else if item != "" {
					l.RanUnparsed = append(l.RanUnparsed, item)
				}
			}
		case "next":
			l.Next = featLogDec(v)
		case "objectives":
			for _, id := range strings.Split(v, ",") {
				if id = strings.TrimSpace(id); objs[id] {
					l.Objectives = append(l.Objectives, id)
				} else if id != "" {
					l.ObjectivesClaimed = append(l.ObjectivesClaimed, id)
				}
			}
		case "pr":
			if n, err := strconv.Atoi(strings.TrimPrefix(v, "#")); err == nil && n > 0 {
				known := prs[n]
				l.PR, l.PRKnown = n, &known
			} else {
				title = append(title, sg)
			}
		case "proof":
			if keys[v] {
				l.ProofStep = v
			} else {
				l.ProofStepClaimed = v
			}
		}
	}
	l.Title = strings.TrimSpace(strings.Join(title, " | "))
	return l, true
}
