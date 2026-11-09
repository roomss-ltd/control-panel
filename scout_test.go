package main

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScoutPick(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ago := func(h int) int64 { return now.Add(-time.Duration(h) * time.Hour).Unix() }
	ag := func(tab string, h, add int, state string, prs ...int) featAgent {
		return featAgent{Tab: tab, LastAt: ago(h), Add: add, State: state, PRs: prs}
	}
	facts := map[string]featFacts{
		// one session: a busy tab beside a forgotten one; the busy one mustn't hide it
		"box-dev": {Team: []featAgent{ag("Control Panel", 0, 900, "working"), ag("Telemetry V1", 30, 40, "idle")}},
		"acme": {Team: []featAgent{
			ag("Fresh", 2, 50, "idle"),          // worked on today
			ag("Clean", 72, 0, "idle"),          // stalled, nothing to ship
			ag("KPI Report", 60, 0, "idle", 81), // stalled longer, a PR open
			ag("Snoozed", 90, 9, "idle"),        // "Not now"
		}, PRs: []*PRStatus{{Number: 81, State: "OPEN"}}},
		"owned": {Team: []featAgent{ag("Mine", 90, 9, "idle")}}, // a thread has it
		"infra": {},                                             // no agents
	}
	names := []string{"box-dev", "acme", "owned", "infra"}
	get := func(s string) (featFacts, bool) { f, ok := facts[s]; return f, ok }
	owned := func(s string) bool { return s == "owned" }
	marks := map[string]*scoutMark{"acme/Snoozed": {Until: now.Add(time.Hour).Unix()}}
	f := scoutPick(now, scoutStall, names, get, owned, marks)
	if f == nil || f.key() != "acme/KPI Report" || f.Open != 1 {
		t.Fatalf("got %+v, want acme/KPI Report", f)
	}
	marks["acme/KPI Report"] = &scoutMark{Never: true}
	if f = scoutPick(now, scoutStall, names, get, owned, marks); f == nil || f.key() != "box-dev/Telemetry V1" {
		t.Fatalf("got %+v, want box-dev/Telemetry V1", f)
	}
	marks["box-dev/Telemetry V1"] = &scoutMark{Taken: "t1"}
	if f = scoutPick(now, scoutStall, names, get, owned, marks); f != nil {
		t.Fatalf("got %+v, want none", f)
	}
}

func TestScoutTake(t *testing.T) {
	e := newTestServer(t)
	a, turns := threadAssistant(t)
	e.s.ai = a
	e.s.scout.file = filepath.Join(t.TempDir(), "scout.json")
	e.s.scout.load()
	w := httptest.NewRecorder()
	e.s.aiScout(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"session":"box-dev/Telemetry V1","act":"take","title":"Ship Telemetry V1","plan":{"what":"Tracing for the API.","plan":["Finish the exporter","Open the PR"],"proof":["CI green"]}}`)))
	if w.Code != 200 {
		t.Fatalf("take: %d %s", w.Code, w.Body)
	}
	a.mu.Lock()
	th := a.thread("ship-telemetry-v1")
	a.mu.Unlock()
	if th == nil || th.Team != "box-dev" {
		t.Fatalf("thread %+v: want one adopting box-dev", th)
	}
	brief := turns("thread-ship-telemetry-v1")[0]["message"].(string)
	for _, want := range []string{"box-dev/Telemetry V1", "Finish the exporter", "CI green", "Tracing for the API."} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief lacks %q:\n%s", want, brief)
		}
	}
	if m := e.s.scout.Marks["box-dev/Telemetry V1"]; m == nil || m.Taken != "ship-telemetry-v1" {
		t.Fatalf("mark %+v", m)
	}
	waitIdle(t, a, "ship-telemetry-v1")
}
