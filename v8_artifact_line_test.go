package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The canonical "- artifact: <path> | ui|issue | <title>" lines: a job's (indented under it) and a milestone's own
// (under its heading, before any job), kept raw as written and in order, for featArts to validate.
func TestV8ArtifactLines(t *testing.T) {
	ms := parseMilestones("## Export\n- artifact: shots/overview.png | ui | the milestone overview\n- [ ] Export @A\n  - artifact: shots/export.png | ui | the export dialog\n  - artifact: notes/bug.md | issue | crash on save\n")
	if a := ms[0].Artifacts; strings.Join(a, "\n") != "shots/overview.png | ui | the milestone overview" {
		t.Fatalf("milestone artifacts: %q", a)
	}
	if a := ms[0].Steps[0].Artifacts; strings.Join(a, "\n") != "shots/export.png | ui | the export dialog\nnotes/bug.md | issue | crash on save" {
		t.Fatalf("job artifacts: %q", a)
	}
}

// The plan's artifact lines reach page.arts through featArts, milestone's own first; a write with too many
// (or too long) artifact lines is refused whole, PLAN.md unchanged; nothing is cut or dropped.
func TestV8ArtifactRefsLimitsAndPage(t *testing.T) {
	ms := parseMilestones("## Export\n- artifact: a.png | ui | overview\n- [ ] Export @A\n  - artifact: b.md | issue | crash\n")
	refs := featPlanArtRefs(ms)
	if len(refs) != 2 || refs[0].Step != "" || refs[0].Milestone != "Export" || refs[1].Step != ms[0].Steps[0].Key || refs[1].Raw != "b.md | issue | crash" || refs[1].Source != "PLAN.md" {
		t.Fatalf("refs: %+v", refs)
	}
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	v.e.s.cfg.pinFile = filepath.Join(v.e.s.cfg.home, "cp-pin", "pin")
	do := agentDo(t, v)
	before, _ := os.ReadFile(v.planFile())
	many := "## Ship\n- [ ] Write docs @Builder\n" + strings.Repeat("  - artifact: x.png | ui | shot\n", 21)
	if c, out := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"plan","rev":"`+v.e.s.featDossier(supTeam).Rev+`","text":`+toJSON(many)+`}`); c != 400 || !strings.Contains(fmt.Sprint(out), "at most 20") {
		t.Fatalf("21 artifact lines: %d %v", c, out)
	}
	if after, _ := os.ReadFile(v.planFile()); string(after) != string(before) {
		t.Fatal("a refused write changed PLAN.md")
	}
	writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "shot.png"), "png")
	one := "## Ship\n- [ ] Write docs @Builder\n  - artifact: shot.png | ui | the docs page\n"
	if c, out := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"plan","rev":"`+v.e.s.featDossier(supTeam).Rev+`","text":`+toJSON(one)+`}`); c != 200 {
		t.Fatalf("one artifact line: %d %v", c, out)
	}
	var out struct{ Page map[string]any }
	decodeBody(t, v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", v.e.authed(v.e.login(t))), &out)
	arts, _ := out.Page["arts"].([]any)
	if len(arts) != 1 || !strings.Contains(fmt.Sprint(arts[0]), "the docs page") {
		t.Fatalf("page.arts: %v", out.Page["arts"])
	}
}

// Each artifact line must be '<path> | ui|issue | <title>' with a title of 1–200 characters: anything else is refused
// whole (400, PLAN.md unchanged), never shortened.
func TestV8ArtifactLineValidated(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	do := agentDo(t, v)
	before, _ := os.ReadFile(v.planFile())
	for _, bad := range []string{
		"shot.png | ui | " + strings.Repeat("t", 201),
		"shot.png | poster | a title",
		"shot.png | ui |  ",
		"shot.png",
		" | ui | no path",
		"shot.png | ui | tab\tinside",
	} {
		text := "## Ship\n- [ ] Write docs @Builder\n  - artifact: " + bad + "\n"
		if c, out := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"plan","rev":"`+v.e.s.featDossier(supTeam).Rev+`","text":`+toJSON(text)+`}`); c != 400 {
			t.Fatalf("%q: %d %v (want 400)", trunc(bad, 40), c, out)
		}
	}
	if after, _ := os.ReadFile(v.planFile()); string(after) != string(before) {
		t.Fatal("a refused write changed PLAN.md")
	}
	ok := "## Ship\n- [ ] Write docs @Builder\n  - artifact: shot.png | Issue | " + strings.Repeat("t", 200) + "\n"
	if c, out := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"plan","rev":"`+v.e.s.featDossier(supTeam).Rev+`","text":`+toJSON(ok)+`}`); c != 200 {
		t.Fatalf("a 200-character title: %d %v", c, out)
	}
}

// A line already in PLAN.md (edited by hand) that breaks the format shows as refused with why, never shortened.
func TestV8ArtifactLegacyLineRefused(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n  - artifact: shot.png | ui | "+strings.Repeat("t", 250)+"\n")
	v.e.s.cfg.pinFile = filepath.Join(v.e.s.cfg.home, "cp-pin", "pin")
	writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "shot.png"), "png")
	var out struct{ Page map[string]any }
	decodeBody(t, v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", v.e.authed(v.e.login(t))), &out)
	arts, _ := out.Page["arts"].([]any)
	a, _ := arts[0].(map[string]any)
	if len(arts) != 1 || a["state"] != "refused" || a["why"] == nil || a["title"] != nil && a["title"] != "" {
		t.Fatalf("a 250-character title already in the file: %v", arts)
	}
}
