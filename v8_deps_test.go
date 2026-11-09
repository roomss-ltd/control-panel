package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const depPlan = "## Ship\n- [ ] Build it @Builder\n- [ ] Docs for it @Writer\n  - after: %A\n- [ ] Misc @Writer\n  - after: nope\n"

// depEnv is a supervisor test environment whose plan has dependencies: Docs waits for Build (returned key), Misc
// names a step that doesn't exist.
func depEnv(t *testing.T) (*supEnv, string) {
	a := featStepKey("Ship", "Build it")
	return newSupEnv(t, strings.ReplaceAll(depPlan, "%A", a)), a
}

// A step waiting on an unfinished step, or with an invalid dependency, isn't dispatched; its prerequisite is.
func TestV8DepsBlockAssign(t *testing.T) {
	v, a := depEnv(t)
	do := agentDo(t, v)
	k := v8Keys(v)
	if c, out := do("POST", "/agent/feature/assign", `{"session":"demo-feature","step":"`+k[1]+`","tab":"Writer"}`); c != 409 || !strings.Contains(toJSON(out), a) {
		t.Fatalf("unmet prerequisite: %d %v", c, out)
	}
	if c, out := do("POST", "/agent/feature/assign", `{"session":"demo-feature","step":"`+k[2]+`","tab":"Writer"}`); c != 409 || !strings.Contains(toJSON(out), "unknown step") {
		t.Fatalf("invalid dependency: %d %v", c, out)
	}
	if c, out := do("POST", "/agent/feature/assign", `{"session":"demo-feature","step":"`+k[0]+`","tab":"Builder"}`); c != 200 {
		t.Fatalf("the prerequisite: %d %v", c, out)
	}
}

// The reconcile wake names only steps that can move: not one waiting on another, nor one with a bad dependency.
func TestV8DepsWake(t *testing.T) {
	v, _ := depEnv(t)
	v.look(v.agent("waiting", v.now.Unix())) // the brief
	waitIdle(t, v.a, supTeam)
	old := v.now.Add(-time.Hour)
	for _, n := range []string{"PLAN.md", "LOG.md", "TESTS.md"} {
		os.Chtimes(filepath.Join(filepath.Dir(v.planFile()), n), old, old)
	}
	p := v.agent("waiting", old.Unix())
	p.Agent.LastAt = old.Unix()
	v.look(p)
	var body string
	for _, x := range v.notes("feature") {
		if strings.Contains(x.Title, "can move") {
			body += x.Body
		}
	}
	if !strings.Contains(body, `"Build it"`) || strings.Contains(body, "Docs for it") || strings.Contains(body, "Misc") {
		t.Fatalf("the wake: %q", body)
	}
}

// Done is refused while a step's dependency is invalid, even with every step verified.
func TestV8DepsGate(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [x] Read a log\n  - after: nope\n")
	attestStep(t, v, 0)
	if why := v.e.s.featGate(supTeam, supTeam, nil, false); !strings.Contains(why, "dependenc") {
		t.Fatalf("gate: %q", why)
	}
}
