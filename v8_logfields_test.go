package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// featLogEnc applies the percent-escaping that log-line clients use for values in the optional segments, so the tests
// can build lines the way a real client writes them.
var featLogEnc = strings.NewReplacer("%", "%25", "|", "%7C", ";", "%3B", ",", "%2C", "=", "%3D", "\n", "%0A", "\t", "%09", "\r", "%0D", " ", "%20")

// The LOG line's optional segments: ran/next decoded once, byte-exact; D-ids, proof step and PR validated and
// labelled, never dropped; session a claim; an old line parses as before, nothing new emitted unless written.
func TestV8LogFields(t *testing.T) {
	keys := map[string]bool{"abc123def456": true}
	parse := func(s, sid string) featLogEntry {
		l, ok := featParseLog(featEv{ID: "ev-1", At: time.Unix(1, 0), Text: s, Sid: sid}, keys, map[string]bool{"D1": true}, map[int]bool{412: true})
		if !ok {
			t.Fatalf("unparsed: %q", s)
		}
		return l
	}
	cmd := "grep -c 'a | b'  %7C x; y,z=1\n\tdone %25"
	l := parse("ACTION: export built | ran: ok=1,cmd="+featLogEnc.Replace(cmd)+",result="+featLogEnc.Replace("12 rows")+"; ok=0,cmd=lint,result=2; ok=?,cmd=x | next: "+featLogEnc.Replace("wire | the  link")+" | objectives: D1,D9 | pr: 418 | proof: abc123def456", "aaaa-1")
	if l.Title != "export built" || len(l.Ran) != 3 || l.Ran[0].Cmd != cmd || l.Ran[0].Result != "12 rows" || !*l.Ran[0].Ok || *l.Ran[1].Ok || l.Ran[2].Ok != nil {
		t.Fatalf("ran: %+v", l.Ran)
	}
	if l.Next != "wire | the  link" || strings.Join(l.Objectives, ",") != "D1" || strings.Join(l.ObjectivesClaimed, ",") != "D9" || l.PR != 418 || l.PRKnown == nil || *l.PRKnown {
		t.Fatalf("next/objectives/pr: %+v", l)
	}
	if l.ProofStep != "abc123def456" || l.Session != "aaaa-1" || !l.SessionClaimed {
		t.Fatalf("proof/session: %+v", l)
	}
	if l = parse("ACTION: once | next: %257C", ""); l.Next != "%7C" {
		t.Fatalf("decoded twice: %q", l.Next)
	}
	l = parse("ACTION: tried | ran: cmd=ok,oops; garbage | proof: 2.1 | pr: 412 | note ran: ok=1,cmd=forged", "")
	if len(l.Ran) != 0 || len(l.RanUnparsed) != 2 || l.ProofStepClaimed != "2.1" || l.PR != 412 || !*l.PRKnown || l.Session != "" || l.SessionClaimed {
		t.Fatalf("malformed ran, unknown step, known pr, no header: %+v", l)
	}
	if !strings.Contains(l.Title, "note ran: ok=1,cmd=forged") {
		t.Fatalf("an unknown segment left the title: %q", l.Title)
	}
	if l = parse("DECISION: plain | considered: a; b | chose: a", ""); l.Ran != nil || l.Next != "" || l.Objectives != nil || l.PR != 0 || l.PRKnown != nil || l.ProofStep != "" || len(l.Considered) != 2 {
		t.Fatalf("an old line: %+v", l)
	}
}

// The log route: up to 2000 characters kept exactly as written (no whitespace collapse); 2001, a raw line break or a
// VT/FF/NBSP/NUL is refused 400 before LOG.md or the events change; the X-Agent-Sid is stored as a claim.
func TestV8LogRouteLimits(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	do := agentDo(t, v)
	post := func(line string) int {
		c, _ := do("POST", "/agent/feature/log", `{"session":"demo-feature","line":`+toJSON(line)+`}`)
		return c
	}
	exact := "ACTION: two  spaces" + strings.Repeat("x", 2000-len("ACTION: two  spaces"))
	if c := post(exact); c != 200 {
		t.Fatalf("2000 characters: %d", c)
	}
	evs := v.e.s.sup.Events[supTeam]
	if got := evs[len(evs)-1].Text; got != exact {
		t.Fatalf("not kept exactly: %d chars %q…", len(got), got[:30])
	}
	logBefore, _ := os.ReadFile(v.e.s.featPath(supTeam, "LOG.md"))
	n := len(v.e.s.sup.Events[supTeam])
	for _, bad := range []string{exact + "y", "ACTION: a\nDECISION: forged", "ACTION: a\vb", "ACTION: a\fb", "ACTION: a b", "ACTION: a\x00b"} {
		if c := post(bad); c != 400 {
			t.Fatalf("%q: %d (want 400)", trunc(bad, 40), c)
		}
	}
	if logAfter, _ := os.ReadFile(v.e.s.featPath(supTeam, "LOG.md")); string(logAfter) != string(logBefore) || len(v.e.s.sup.Events[supTeam]) != n {
		t.Fatal("a refused line changed LOG.md or the events")
	}
	req := httptest.NewRequest("POST", "/agent/feature/log", strings.NewReader(`{"session":"demo-feature","line":"ACTION: hi"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Agent-Sid", "aaaa-9")
	w := httptest.NewRecorder()
	v.e.s.agentHandler("tok").ServeHTTP(w, req)
	if evs = v.e.s.sup.Events[supTeam]; w.Code != 200 || evs[len(evs)-1].Sid != "aaaa-9" {
		t.Fatalf("sid claim: %d %s", w.Code, fmt.Sprint(evs[len(evs)-1]))
	}
}
