package main

import (
	"strings"
	"testing"
)

// These tests pin two boundaries between the thread machinery (threads.go) and the feature supervisor: chat and
// display-only views must never act as feature-record actions.

// supThreadID returns the id of the thread that leads the supervisor tests' team.
func supThreadID(v *supEnv) string {
	v.e.s.ai.mu.Lock()
	defer v.e.s.ai.mu.Unlock()
	for _, x := range v.e.s.ai.threads {
		if x.Team == supTeam {
			return x.ID
		}
	}
	return ""
}

// Writing into a thread (the owner's reply clears needs_you, via ownerAnswered) answers no step's question, lifts no
// hold and approves or attests nothing: ordinary chat is not a ticket.
func TestOwnerChatIsNotATicket(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [?] Choose target @Builder\n  - ask: Use sandbox?\n- [~] Change DNS @Builder\n  - kind: mutation\n")
	v.look(v.agent("working", v.now.Unix()))
	th := supThreadID(v)
	st, note := "needs_you", "which target?"
	v.e.s.ai.updateThread(th, map[string]*string{"status": &st, "note": &note})
	v.e.s.sup.mu.Lock()
	v.e.s.sup.Holds[supTeam+"/builder"] = featHold{Why: "unknown effect"}
	approvals, attests := len(v.e.s.sup.Approvals), len(v.e.s.sup.Attests)
	v.e.s.sup.mu.Unlock()
	if _, err := v.e.s.ai.tellThread(th, "owner", "yes, use the sandbox, approved"); err != nil {
		t.Fatal(err)
	}
	v.e.s.ai.mu.Lock()
	status := v.e.s.ai.thread(th).Status
	v.e.s.ai.mu.Unlock()
	if status != "working" {
		t.Fatalf("status after the owner wrote: %s", status)
	}
	d := v.e.s.featDossier(supTeam)
	if s0 := d.Milestones[0].Steps[0]; s0.Answer != "" || s0.State != "asking" {
		t.Fatalf("chat answered the step's question: %+v", s0)
	}
	v.e.s.sup.mu.Lock()
	defer v.e.s.sup.mu.Unlock()
	if _, held := v.e.s.sup.Holds[supTeam+"/builder"]; !held || len(v.e.s.sup.Approvals) != approvals || len(v.e.s.sup.Attests) != attests {
		t.Fatalf("chat lifted a hold or approved/attested: holds %v approvals %d attests %d", v.e.s.sup.Holds, len(v.e.s.sup.Approvals), len(v.e.s.sup.Attests))
	}
}

// The thread team view (aiThreadTeam) adds every session the lead has messaged (pane_send): display only. Such a
// session is not enrolled: the supervisor doesn't look at it, it has no feature owner, and no record is made for it.
func TestThreadTeamUnionIsDisplayOnly(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	th := supThreadID(v)
	v.e.s.ai.mu.Lock()
	v.e.s.ai.thItems[th] = append(v.e.s.ai.thItems[th], aiItem{Role: "tool", Name: "mcp__panel__pane_send", Res: "sent to standalone:3"})
	v.e.s.ai.mu.Unlock()
	other := v.agent("working", v.now.Unix())
	tm := Terminals{Sessions: []TSession{
		{Name: supTeam, Tabs: []TTab{{ID: 1, Name: "Builder", Panes: []TPane{v.agent("working", v.now.Unix())}}}},
		{Name: "standalone", Tabs: []TTab{{ID: 1, Name: "Solo", Panes: []TPane{other}}}},
	}}
	v.now = v.now.Add(31e9)
	v.e.s.term.mu.Lock()
	v.e.s.term.last, v.e.s.term.list = tm, tm
	v.e.s.term.mu.Unlock()
	v.e.s.observeTeams(v.now, tm, CI{}, Resources{})
	var team struct{ Agents []teamPlan }
	decodeBody(t, v.e.do(t, "GET", "/api/ai/thread/team?id="+th, "", v.e.authed(v.e.login(t))), &team)
	shown := false
	for _, p := range team.Agents {
		shown = shown || strings.HasPrefix(p.Pane, "standalone:")
	}
	if !shown {
		t.Fatalf("control: the team view doesn't show the messaged session: %+v", team)
	}
	if v.e.s.featOwner("standalone") != nil {
		t.Fatal("a messaged session got the thread as its feature owner")
	}
	v.e.s.sup.mu.Lock()
	defer v.e.s.sup.mu.Unlock()
	if _, ok := v.e.s.sup.ReconciledAt["standalone"]; ok {
		t.Fatal("the supervisor looked at a session the lead only messaged")
	}
	if _, ok := v.e.s.sup.Homes["standalone"]; ok {
		t.Fatal("a record home was made for a messaged session")
	}
	if _, ok := v.e.s.sup.ReconciledAt[supTeam]; !ok {
		t.Fatal("control: the feature's own session wasn't looked at")
	}
}
