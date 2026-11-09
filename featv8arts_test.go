package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// page.arts from explicit refs: the feature's evidence roots decide what may be shown, the existing preview opens it,
// nothing is fetched, and a refused or missing ref never echoes a path.
func TestFeatArts(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] Design the dialog @Designer\n")
	s := v.e.s
	home := s.cfg.home
	s.cfg.pinFile = filepath.Join(home, "cp-pin", "pin")
	d := s.featDossier(supTeam)
	rec := d.Dir
	key := d.Milestones[0].Steps[0].Key
	writeFile(t, filepath.Join(rec, "ui", "dialog.html"), "<html>v2</html>")
	writeFile(t, filepath.Join(rec, "ui", "dialog.html.comments.json"), `{"comments":[{"x":1},{"x":2}]}`)
	writeFile(t, filepath.Join(rec, "shot.png"), "\x89PNG")
	writeFile(t, filepath.Join(rec, ".env"), "TOKEN=x")
	writeFile(t, filepath.Join(home, ".ssh", "id_rsa"), "KEY")
	outside := filepath.Join(t.TempDir(), "elsewhere.png")
	writeFile(t, outside, "\x89PNG")
	os.Symlink(outside, filepath.Join(rec, "link.png"))
	agentFile := filepath.Join(home, "work", "made.png") // an agent wrote it, but it's outside the feature's roots
	writeFile(t, agentFile, "\x89PNG")
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	writeFile(t, tr, fmt.Sprintf(`{"type":"assistant","timestamp":"2026-11-08T10:00:00Z","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":%q,"content":"x"}},{"type":"tool_use","name":"Write","input":{"file_path":%q,"content":"x"}}]}}`+"\n", agentFile, filepath.Join(rec, "shot.png")))
	team := []featAgent{{Key: "s:4", Tab: "Designer", path: tr}}

	ref := func(raw string) featArtRef {
		return featArtRef{Raw: raw, Step: key, Milestone: "Ship", Source: "PLAN.md"}
	}
	refs := []featArtRef{
		ref("ui/dialog.html | ui | Export dialog v2"),
		ref(filepath.Join(rec, "shot.png") + " | issue | Repro"),
		ref("https://example.com/spec | ui | Spec"),
		ref("http://example.com/x | ui | Plain http"),
		ref("file:///etc/passwd | ui"),
		ref("https://user:pw@example.com/x"),
		ref("~/.ssh/id_rsa | ui | key"),
		ref("../../../etc/passwd | ui"),
		ref("nope.png | ui | Not yet"),
		ref(".env | ui"),
		ref("link.png | ui"),
		ref(agentFile + " | ui | written by an agent, outside the roots"),
		ref(" | ui | empty"),
		{Raw: "shot.png | issue | from the log", Step: key, Source: "LOG ev-3", By: "s:9", ByClaimed: ""},
		{Raw: "shot.png | issue | claimed", Step: key, Source: "LOG ev-4", ByClaimed: "abc-sid"},
		ref("shot.png | issue | Repro\x1b[31m red"),        // 15: control characters: refused, not stripped
		ref("shot.png | ui | " + strings.Repeat("é", 201)), // 16: 201 characters: refused, not cut
		ref("shot.png | ui | " + strings.Repeat("é", 200)), // 17: 200: fine
		ref("shot.png | design | x"),                       // 18: not ui|issue
		ref("shot.png | UI | x"),                           // 19: as written: refused
	}
	calls := gitShim(t)
	arts := s.featArts(d, team, refs)
	if len(arts) != len(refs) {
		t.Fatalf("arts: %d, want %d", len(arts), len(refs))
	}
	want := []struct{ state, kind string }{
		{"ok", "html"}, {"ok", "image"}, {"link", "link"}, {"refused", "link"}, {"refused", "link"}, {"refused", "link"},
		{"refused", ""}, {"refused", ""}, {"missing", ""}, {"refused", ""}, {"refused", ""}, {"refused", ""}, {"refused", ""},
		{"ok", "image"}, {"ok", "image"},
		{"refused", ""}, {"refused", ""}, {"ok", "image"}, {"refused", ""}, {"refused", ""},
	}
	for i, w := range want {
		if a := arts[i]; a.State != w.state || a.Kind != w.kind || a.Step != key || a.ID == "" {
			t.Errorf("%d %q: state %q kind %q step %q, want %s %s", i, refs[i].Raw, a.State, a.Kind, a.Step, w.state, w.kind)
		}
	}
	html := arts[0]
	if html.Purpose != "ui" || html.Title != "Export dialog v2" || html.Comments != 2 || !strings.HasSuffix(html.URL, "&review=1") ||
		!strings.HasPrefix(html.URL, "/api/fs/raw?path=") || html.Path != filepath.Join(rec, "ui", "dialog.html") || html.At == 0 || html.Milestone != "Ship" {
		t.Fatalf("html artifact: %+v", html)
	}
	for i, why := range map[int]string{15: "not '<path> | ui|issue | <title>'", 16: "title longer than 200 characters", 18: "purpose must be ui or issue", 19: "purpose must be ui or issue"} {
		if a := arts[i]; a.Why != why || a.Title != "" || a.Path != "" || a.URL != "" {
			t.Errorf("%d: an invalid authored line must be refused as written: %+v", i, a)
		}
	}
	if a := arts[17]; utf8.RuneCountInString(a.Title) != 200 {
		t.Fatalf("a 200-character title: %d", utf8.RuneCountInString(a.Title))
	}
	if png := arts[1]; png.Title != "Repro" || png.By != "s:4" || !strings.HasPrefix(png.BySource, "observed: its transcript") || png.Comments != 0 || strings.Contains(png.URL, "review") {
		t.Fatalf("png (control chars stripped, observed writer): %+v", png)
	}
	if l := arts[2]; l.URL != "https://example.com/spec" || l.Path != "" || l.Size != 0 {
		t.Fatalf("https link: %+v", l)
	}
	if l := arts[5]; l.URL != "" {
		t.Fatalf("a link with credentials kept: %+v", l)
	}
	for i := 3; i < 13; i++ {
		if a := arts[i]; a.Path != "" || a.State != "link" && a.Why == "" || a.By != "" {
			t.Errorf("%d refused/missing must give why and no path: %+v", i, a)
		}
	}
	if a := arts[11]; a.State != "refused" {
		t.Fatalf("an agent's own file outside the feature's roots was allowed: %+v", a)
	}
	if a := arts[13]; a.By != "s:9" || !strings.HasPrefix(a.BySource, "observed: the session that wrote the line") || a.Source != "LOG ev-3" {
		t.Fatalf("log ref, observed: %+v", a)
	}
	if a := arts[14]; a.ByClaimed != "abc-sid" || a.By != "s:4" || !strings.Contains(a.BySource, "transcript") {
		t.Fatalf("log ref, claimed: the claim stays separate: %+v", a)
	}
	b, _ := json.Marshal(arts)
	for _, leak := range []string{".ssh", "id_rsa", "passwd", outside, agentFile, ".env\"", "TOKEN"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("payload leaks %q", leak)
		}
	}
	if again := s.featArts(d, team, refs); again[0].ID != html.ID || again[13].ID == arts[14].ID {
		t.Fatal("ids aren't stable per ref, or two refs share one")
	}
	if c := calls(); len(c) != 0 {
		t.Fatalf("git ran: %q", c)
	}
	if s.featArts(d, team, nil) != nil {
		t.Fatal("arts with no ref")
	}
}

func TestFeatArtsCapAndCommentsGuard(t *testing.T) {
	v := newSupEnv(t, "## Ship\n- [ ] A\n")
	s := v.e.s
	s.cfg.pinFile = filepath.Join(s.cfg.home, "cp-pin", "pin")
	d := s.featDossier(supTeam)
	writeFile(t, filepath.Join(d.Dir, "p.html"), "<html>")
	secret := filepath.Join(s.cfg.home, "cp-pin", "pin.json")
	writeFile(t, secret, `{"comments":[1,2,3,4,5,6,7]}`)
	os.Symlink(secret, filepath.Join(d.Dir, "p.html.comments.json")) // a planted link into a secret path: not read
	var refs []featArtRef
	for i := range 150 {
		refs = append(refs, featArtRef{Raw: "p.html | ui | " + fmt.Sprint(i), Source: "LOG ev-" + fmt.Sprint(i)})
	}
	arts := s.featArts(d, nil, refs)
	if len(arts) != maxFeatArts || arts[0].State != "ok" || arts[0].Comments != 0 {
		t.Fatalf("cap %d, first %+v", len(arts), arts[0])
	}
}

// Through the real plan parser (parseMilestones keeps artifact: lines raw): a 201-character title is refused, never
// cut into a valid-looking one; a milestone's own line has no step.
func TestFeatArtsFromPlanParser(t *testing.T) {
	long, ok := strings.Repeat("é", 201), strings.Repeat("é", 200)
	v := newSupEnv(t, "## Ship\n- artifact: shot.png | issue | Milestone repro\n- [ ] Design the dialog @Designer\n  - artifact: shot.png | ui | "+long+"\n  - artifact: shot.png | ui | "+ok+"\n  - artifact: ~/.ssh/id_rsa | ui | key\n  - artifact: shot.png | ui | "+strings.Repeat("x", 250)+"\n  - artifact: shot.png | | no purpose\n")
	s := v.e.s
	s.cfg.pinFile = filepath.Join(s.cfg.home, "cp-pin", "pin")
	d := s.featDossier(supTeam)
	writeFile(t, filepath.Join(d.Dir, "shot.png"), "\x89PNG")
	key := d.Milestones[0].Steps[0].Key
	arts := s.featArts(d, nil, nil)
	if len(arts) != 6 {
		t.Fatalf("arts: %+v", arts)
	}
	if a := arts[0]; a.Step != "" || a.Milestone != "Ship" || a.State != "ok" || a.Title != "Milestone repro" || a.Source != "PLAN.md" {
		t.Fatalf("milestone line: %+v", a)
	}
	if a := arts[1]; a.Step != key || a.State != "refused" || a.Why != "title longer than 200 characters" || a.Title != "" {
		t.Fatalf("201 characters: %+v", a)
	}
	if a := arts[2]; a.State != "ok" || utf8.RuneCountInString(a.Title) != 200 {
		t.Fatalf("200 characters: %+v", a)
	}
	if a := arts[3]; a.State != "refused" || a.Path != "" || strings.Contains(fmt.Sprint(a), ".ssh") {
		t.Fatalf("secret ref: %+v", a)
	}
	if a := arts[4]; a.State != "refused" || a.Why != "title longer than 200 characters" || a.Title != "" { // 250 characters, as a legacy direct edit could leave it
		t.Fatalf("250 characters: %+v", a)
	}
	if a := arts[5]; a.State != "ok" || a.Purpose != "" { // no purpose written: none invented
		t.Fatalf("no purpose: %+v", a)
	}
}
