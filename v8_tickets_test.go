package main

import (
	"strings"
	"testing"
	"time"
)

// ticketPlan is a plan with an unapproved check and a step asking the owner a question.
const ticketPlan = "## Ship\n- [ ] Run the suite @Builder\n  - check: go test ./...\n  - cwd: /tmp\n- [?] Pick a region @Writer\n  - ask: eu or us?\n"

// v8Tickets returns the feature's current tickets keyed by kind.
func v8Tickets(t *testing.T, v *supEnv) map[string]featTicket {
	t.Helper()
	out := map[string]featTicket{}
	for _, tk := range v.e.s.featTickets(supTeam) {
		out[tk.Kind] = tk
	}
	return out
}

// Tickets come from the record and the supervisor; a reply's option is one the server offers for that ticket now, and
// does exactly what the owner route does; anything else is refused.
func TestV8Tickets(t *testing.T) {
	v := newSupEnv(t, ticketPlan)
	v.e.s.sup.mu.Lock()
	v.e.s.sup.Holds[supTeam+"/builder"] = featHold{Why: "exited mid-step", Steps: []string{"Run the suite"}, At: 1}
	v.e.s.sup.mu.Unlock()
	tm := Terminals{Sessions: []TSession{{Name: supTeam, Tabs: []TTab{{ID: 1, Name: "Writer", Panes: []TPane{{ID: 3, Agent: &TAgent{sid: "sid-w1"}}}}}}}}
	v.e.s.term.mu.Lock()
	v.e.s.term.last, v.e.s.term.list = tm, tm
	v.e.s.term.mu.Unlock()
	tk := v8Tickets(t, v)
	for _, k := range []string{"approve", "ask", "hold"} {
		if tk[k].ID == "" || len(tk[k].Options) == 0 {
			t.Fatalf("no %s ticket: %+v", k, tk)
		}
	}
	if tk["ask"].Sid != "sid-w1" {
		t.Fatalf("ask ticket's agent: %+v", tk["ask"])
	}
	h := v.e.authed(v.e.login(t))
	reply := func(body string) (int, map[string]any) {
		return v8Post(t, v, h, "/api/term/feature/ticket/reply", body)
	}
	ap := tk["approve"]
	if c, out := reply(`{"session":"` + supTeam + `","ticket":"` + ap.ID + `","option":"accept","rev":"` + ap.Rev + `"}`); c != 400 {
		t.Fatalf("an option not offered: %d %v", c, out)
	}
	if c, out := reply(`{"session":"` + supTeam + `","ticket":"` + ap.ID + `","option":"/api/term/feature/promote","rev":"` + ap.Rev + `"}`); c != 400 {
		t.Fatalf("a client-named route: %d %v", c, out)
	}
	if c, _ := reply(`{"session":"` + supTeam + `","ticket":"` + ap.ID + `","option":"approve","rev":"stale"}`); c != 409 {
		t.Fatalf("stale rev: %d", c)
	}
	if c, out := reply(`{"session":"` + supTeam + `","ticket":"` + ap.ID + `","option":"approve","rev":"` + ap.Rev + `"}`); c != 200 {
		t.Fatalf("approve: %d %v", c, out)
	}
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; !st.Approval.Check {
		t.Fatalf("not approved as the route would: %+v", st.Approval)
	}
	if evs := v.e.s.sup.Events[supTeam]; len(evs) != 1 || evs[0].Kind != "ticket" || evs[0].Text != ap.ID+" → approve" || evs[0].Rev != ap.Rev || evs[0].Tab != ap.Tab {
		t.Fatalf("the reply's audit event: %+v", evs)
	}
	if c, _ := reply(`{"session":"` + supTeam + `","ticket":"` + ap.ID + `","option":"approve","rev":"` + ap.Rev + `"}`); c != 409 {
		t.Fatalf("a closed ticket: %d", c)
	}
	ask := tk["ask"]
	if c, _ := reply(`{"session":"` + supTeam + `","ticket":"` + ask.ID + `","option":"answer","text":"eu","rev":"` + v.e.s.featDossier(supTeam).Rev + `","sid":"sid-old"}`); c != 409 {
		t.Fatalf("a replaced agent: %d", c)
	}
	ask = v8Tickets(t, v)["ask"]
	if c, out := reply(`{"session":"` + supTeam + `","ticket":"` + ask.ID + `","option":"answer","text":"eu","rev":"` + ask.Rev + `","sid":"sid-w1"}`); c != 200 {
		t.Fatalf("answer: %d %v", c, out)
	}
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[1]; !strings.Contains(st.Answer, "eu") {
		t.Fatalf("not answered: %+v", st)
	}
	hd := v8Tickets(t, v)["hold"]
	if c, out := reply(`{"session":"` + supTeam + `","ticket":"` + hd.ID + `","option":"release","rev":"` + hd.Rev + `"}`); c != 200 || len(v.e.s.sup.Holds) != 0 {
		t.Fatalf("release: %d %v", c, out)
	}
	if c, _ := v8Post(t, v, map[string]string{}, "/api/term/feature/ticket/reply", `{}`); c == 200 {
		t.Fatal("reply without the owner login")
	}
}

// The replaced-agent refusal holds at the effect itself: an agent replaced after the reply's first look, before the
// owner route runs, still gets 409 and nothing changes.
func TestV8TicketReplacedAtEffect(t *testing.T) {
	v := newSupEnv(t, ticketPlan)
	set := func(sid string) {
		tm := Terminals{Sessions: []TSession{{Name: supTeam, Tabs: []TTab{{ID: 1, Name: "Builder", Panes: []TPane{{ID: 3, Agent: &TAgent{sid: sid}}}}}}}}
		v.e.s.term.mu.Lock()
		v.e.s.term.last, v.e.s.term.list = tm, tm
		v.e.s.term.mu.Unlock()
	}
	set("sid-1")
	ap := v8Tickets(t, v)["approve"]
	featTicketBeforeEffect = func() { set("sid-2") }
	t.Cleanup(func() { featTicketBeforeEffect = nil })
	h := v.e.authed(v.e.login(t))
	if c, out := v8Post(t, v, h, "/api/term/feature/ticket/reply", `{"session":"`+supTeam+`","ticket":"`+ap.ID+`","option":"approve","rev":"`+ap.Rev+`","sid":"sid-1"}`); c != 409 {
		t.Fatalf("replaced before the effect: %d %v", c, out)
	}
	if v.e.s.featDossier(supTeam).Milestones[0].Steps[0].Approval.Check || len(v.e.s.sup.Events[supTeam]) != 0 {
		t.Fatal("approved (or recorded as done) for a replaced agent")
	}
	set("sid-2")
	v.e.s.sup.mu.Lock()
	v.e.s.sup.Holds[supTeam+"/builder"] = featHold{Why: "exited", At: 1}
	v.e.s.sup.mu.Unlock()
	hd := v8Tickets(t, v)["hold"]
	featTicketBeforeEffect = func() { set("sid-3") }
	if c, _ := v8Post(t, v, h, "/api/term/feature/ticket/reply", `{"session":"`+supTeam+`","ticket":"`+hd.ID+`","option":"release","rev":"`+hd.Rev+`","sid":"sid-2"}`); c != 409 || len(v.e.s.sup.Holds) != 1 {
		t.Fatalf("release for a replaced agent: %d %v", c, v.e.s.sup.Holds)
	}
}

// A listing published while a ticket's effect runs (after its agent was checked) waits for the effect: the effect is
// for the agent checked, and the new observation comes after it.
func TestV8TicketEffectHoldsTheListing(t *testing.T) {
	v := newSupEnv(t, ticketPlan)
	tm := func(sid string) Terminals {
		return Terminals{Sessions: []TSession{{Name: supTeam, Tabs: []TTab{{ID: 1, Name: "Builder", Panes: []TPane{{ID: 3, Agent: &TAgent{sid: sid}}}}}}}}
	}
	v.e.s.term.publish(tm("sid-1"), nil, true)
	ap := v8Tickets(t, v)["approve"]
	published := make(chan struct{})
	featTicketAtEffect = func() {
		go func() { v.e.s.term.publish(tm("sid-2"), nil, true); close(published) }()
		select {
		case <-published:
			t.Error("a new listing landed between the check and the effect")
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Cleanup(func() { featTicketAtEffect = nil })
	h := v.e.authed(v.e.login(t))
	if c, out := v8Post(t, v, h, "/api/term/feature/ticket/reply", `{"session":"`+supTeam+`","ticket":"`+ap.ID+`","option":"approve","rev":"`+ap.Rev+`","sid":"sid-1"}`); c != 200 {
		t.Fatalf("approve: %d %v", c, out)
	}
	<-published
	if v.e.s.featTabSid(supTeam, "Builder") != "sid-2" {
		t.Fatal("the new listing was lost")
	}
}

// A check without an absolute folder isn't offered as approvable (the route would refuse it): the ticket says what to fix.
func TestV8ApproveTicketNeedsAbsoluteFolder(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Run the suite @Builder\n  - check: go test ./...\n  - cwd: repo\n")
	ap := v8Tickets(t, v)["approve"]
	if ap.ID == "" || len(ap.Options) != 0 || !strings.Contains(ap.Reason, "absolute") {
		t.Fatalf("a dead approve option: %+v", ap)
	}
}
