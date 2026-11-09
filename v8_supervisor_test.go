package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The feature page's "supervisor" block: the terminal sample the page shows (lastSample), the next supervisor check (nextCheckAt: the
// earliest, as the scheduler allows it; null with why before any pass), and the manager's next wake (nextWakeAt: from
// the wake backoff; null with why when none is pending); held work is named apart, never hiding other lanes' wakes (a lane is one team tab). Reads only.
func TestV8PageSupervisor(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	h := v.e.authed(v.e.login(t))
	sample := time.Unix(1_600_000_100, 0)
	v.e.s.term.mu.Lock()
	v.e.s.term.lastAt = sample
	v.e.s.term.mu.Unlock()
	sv := func() map[string]any {
		var out struct{ Page map[string]any }
		decodeBody(t, v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", h), &out)
		m, ok := out.Page["supervisor"].(map[string]any)
		if !ok {
			t.Fatalf("no page.supervisor: %v", out.Page["supervisor"])
		}
		return m
	}
	m := sv()
	if m["lastSample"] != float64(sample.Unix()) || m["nextCheckAt"] != nil || !strings.Contains(m["next"].(string), "no supervisor pass since the panel started") {
		t.Fatalf("before any pass: %v", m)
	}
	if m["nextWakeAt"] != nil || m["nextWakeWhy"] == "" {
		t.Fatalf("no wake pending: %v", m)
	}
	v.now = time.Unix(1_600_000_000, 0)
	v.look(v.agent("working", v.now.Unix()))
	v.e.s.term.mu.Lock()
	v.e.s.term.lastAt = sample
	v.e.s.term.mu.Unlock()
	m = sv()
	if m["nextCheckAt"] != float64(v.now.Add(supEvery).Unix()) || m["lastSample"] != float64(sample.Unix()) {
		t.Fatalf("after a pass at %d: %v", v.now.Unix(), m)
	}
	var th string
	for _, x := range v.e.s.ai.threads {
		if x.Team == supTeam {
			th = x.ID
		}
	}
	v.e.s.sup.mu.Lock()
	v.e.s.sup.Nudges[th] = featNudge{"fp", 1_600_000_500, 2}
	v.e.s.sup.mu.Unlock()
	if m = sv(); m["nextWakeAt"] != float64(1_600_000_500+int64(nudgeBackoff(2)/time.Second)) {
		t.Fatalf("a wake backing off: %v", m)
	}
	v.e.s.sup.mu.Lock()
	v.e.s.sup.Holds[supTeam+"/builder"] = featHold{Why: "owner check"}
	v.e.s.sup.mu.Unlock()
	// held work and idle lanes elsewhere: the held lane has no automatic wake, the others' repeat wake still stands
	if m = sv(); m["nextWakeAt"] != float64(1_600_000_500+int64(nudgeBackoff(2)/time.Second)) || !strings.Contains(fmt.Sprint(m["held"]), "waits for you") {
		t.Fatalf("held and idle: %v", m)
	}
	before, _ := os.ReadFile(v.e.s.sup.file)
	sv()
	if after, _ := os.ReadFile(v.e.s.sup.file); !bytes.Equal(before, after) {
		t.Fatal("a GET wrote the supervisor's state")
	}
}
