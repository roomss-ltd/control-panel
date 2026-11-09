package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Evidence files resolve only inside the feature's folders, as regular files under the size limit, and seal as their
// sha256; anything else is refused with why.
func TestR1SealFile(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, "work/ledger.txt"), "ok\n")
	writeFile(t, filepath.Join(out, "secret.txt"), "x")
	os.Symlink(filepath.Join(out, "secret.txt"), filepath.Join(root, "escape.txt"))
	os.MkdirAll(filepath.Join(root, "adir"), 0o700)
	a, why := featSealFile([]string{root}, nil, "work/ledger.txt")
	if why != "" || a.Path != filepath.Join(root, "work/ledger.txt") || a.Size != 3 || a.Sha256 != sha256hex([]byte("ok\n")) {
		t.Fatalf("seal: %+v %q", a, why)
	}
	if b, _ := featSealFile([]string{root}, nil, filepath.Join(root, "work/ledger.txt")); b.Path != a.Path {
		t.Fatal("an absolute path inside the root")
	}
	for ref, want := range map[string]string{
		"../" + filepath.Base(out) + "/secret.txt": "outside",
		filepath.Join(out, "secret.txt"):           "outside",
		"escape.txt":                               "outside",
		"adir":                                     "not a regular file",
		"nope.txt":                                 "missing",
	} {
		if _, why := featSealFile([]string{root}, nil, ref); !strings.Contains(why, want) {
			t.Fatalf("%s: %q, want %q", ref, why, want)
		}
	}
	writeFile(t, filepath.Join(root, ".ssh/id"), "k")
	if a, why := featSealFile([]string{root}, []string{filepath.Join(root, ".ssh")}, ".ssh/id"); why != "outside the feature's folders" || a.Path != "" {
		t.Fatalf("a secret inside a root: %+v %q", a, why)
	}
	writeFile(t, filepath.Join(root, "repo/.git/config"), "x")
	if _, why := featSealFile([]string{root}, nil, "repo/.git/config"); why != "outside the feature's folders" {
		t.Fatalf("a VCS path: %q", why)
	}
	old := maxEvidenceBytes
	t.Cleanup(func() { maxEvidenceBytes = old })
	maxEvidenceBytes = 2
	if _, why := featSealFile([]string{root}, nil, "work/ledger.txt"); !strings.Contains(why, "too large") {
		t.Fatalf("size limit: %q", why)
	}
}

// Provenance: a successful file-writing tool call on exactly that path after the criterion time; a Bash run naming it
// is weaker and said so; a failed call, another path, or one before the criterion doesn't count.
func TestR1Provenance(t *testing.T) {
	dir := t.TempDir()
	tr := filepath.Join(dir, "t.jsonl")
	p := "/work/ledger.txt"
	call := func(id, ts, tool, input string) string {
		return `{"type":"assistant","timestamp":"` + ts + `","message":{"content":[{"type":"tool_use","id":"` + id + `","name":"` + tool + `","input":` + input + `}]}}` + "\n"
	}
	res := func(id, ts string, isErr bool) string {
		e := "false"
		if isErr {
			e = "true"
		}
		return `{"type":"user","timestamp":"` + ts + `","message":{"content":[{"type":"tool_result","tool_use_id":"` + id + `","is_error":` + e + `}]}}` + "\n"
	}
	crit := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	os.WriteFile(tr, []byte(
		call("early", "2026-11-08T11:00:00Z", "Write", `{"file_path":"`+p+`"}`)+res("early", "2026-11-08T11:00:01Z", false)+
			call("other", "2026-11-08T12:10:00Z", "Write", `{"file_path":"/work/other.txt"}`)+res("other", "2026-11-08T12:10:01Z", false)+
			call("failed", "2026-11-08T12:20:00Z", "Edit", `{"file_path":"`+p+`"}`)+res("failed", "2026-11-08T12:20:01Z", true)+
			call("bash", "2026-11-08T12:30:00Z", "Bash", `{"command":"echo ok > `+p+`"}`)+res("bash", "2026-11-08T12:30:01Z", false)), 0o600)
	if pv, mn := featProvenance([]string{tr}, []string{p}, crit, ""); pv != nil || mn == nil || mn.Tool != "Bash" || mn.ToolUseID != "bash" {
		t.Fatalf("a bash run naming it is a mention, not provenance: %+v %+v", pv, mn)
	}
	f, _ := os.OpenFile(tr, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(call("w2", "2026-11-08T12:40:00Z", "Write", `{"file_path":"`+p+`"}`) + res("w2", "2026-11-08T12:40:01Z", false))
	f.Close()
	if pv, _ := featProvenance([]string{tr}, []string{p}, crit, ""); pv == nil || pv.Tool != "Write" || pv.ToolUseID != "w2" || pv.Transcript != tr {
		t.Fatalf("a write after the criterion: %+v", pv)
	}
	if pv, mn := featProvenance([]string{tr}, []string{p}, crit.Add(time.Hour), ""); pv != nil || mn != nil {
		t.Fatalf("nothing after a later criterion: %+v %+v", pv, mn)
	}
	if pv, _ := featProvenance([]string{tr}, []string{"/work/ledger.txt.bak"}, crit, ""); pv != nil {
		t.Fatalf("a longer path containing it isn't it: %+v", pv)
	}
	tr2 := filepath.Join(dir, "t2.jsonl")
	os.WriteFile(tr2, []byte(call("b2", "2026-11-08T12:30:00Z", "Bash", `{"command":"cp x /work/ledger.txt.bak"}`)+res("b2", "2026-11-08T12:30:01Z", false)+
		call("w3", "2026-11-08T12:31:00Z", "Write", `{"file_path":"`+p+`","content":"ok\n"}`)+res("w3", "2026-11-08T12:31:01Z", false)), 0o600)
	if _, mn := featProvenance([]string{tr2}, []string{p}, crit, ""); mn != nil {
		t.Fatalf("a command naming a longer path doesn't mention it: %+v", mn)
	}
	if pv, _ := featProvenance([]string{tr2}, []string{p}, crit, sha256hex([]byte("ok\n"))); pv == nil || pv.BytesMatch == nil || !*pv.BytesMatch {
		t.Fatalf("Write content matching the seal: %+v", pv)
	}
	if pv, _ := featProvenance([]string{tr2}, []string{p}, crit, sha256hex([]byte("changed"))); pv == nil || pv.BytesMatch == nil || *pv.BytesMatch {
		t.Fatalf("Write content not matching the seal: %+v", pv)
	}
}

func sha256hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// r1Env: newSupEnv with the PIN in its own folder, as on a real instance (the test server keeps it in the home's root,
// which would make the whole home a secret path)
func r1Env(t *testing.T, plan string) *supEnv {
	v := newSupEnv(t, plan)
	v.e.s.cfg.pinFile = filepath.Join(v.e.s.cfg.home, "cp-pin", "pin")
	return v
}

func r1Items(t *testing.T, v *supEnv, h map[string]string) []map[string]any {
	t.Helper()
	var out struct{ Page map[string]any }
	decodeBody(t, v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", h), &out)
	k := v.e.s.featDossier(supTeam).Milestones[0].Steps[0].Key
	ev, _ := out.Page["evidence"].(map[string]any)[k].(map[string]any)
	var items []map[string]any
	for _, x := range ev["items"].([]any) {
		items = append(items, x.(map[string]any))
	}
	return items
}

// A seal is taken on a write path at first sight (never on a GET, never replaced); a GET compares the bytes and says
// stale when they changed; seal and provenance never verify anything.
func TestR1SealOnWritePathsOnly(t *testing.T) {
	plan := "## Ship\n- [x] Export @Builder\n  - evidence: file out.csv\n"
	v := r1Env(t, plan)
	csv := filepath.Join(filepath.Dir(v.planFile()), "out.csv")
	writeFile(t, csv, "a,b\n")
	h := v.e.authed(v.e.login(t))
	if it := r1Items(t, v, h); len(it) != 1 || it[0]["seal"] != nil || len(v.e.s.sup.Sealed) != 0 {
		t.Fatalf("a GET sealed: %v %v", it, v.e.s.sup.Sealed)
	}
	d := v.e.s.featDossier(supTeam)
	do := agentDo(t, v)
	if c, out := do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"plan","rev":"`+d.Rev+`","text":`+toJSON(plan+"\n")+`}`); c != 200 {
		t.Fatalf("writer: %d %v", c, out)
	}
	it := r1Items(t, v, h)
	seal, _ := it[0]["seal"].(map[string]any)
	if seal == nil || seal["sha256"] != sha256hex([]byte("a,b\n")) || seal["submittedBy"] != "writer" || it[0]["stale"] == true || it[0]["provenanceNone"] != true {
		t.Fatalf("sealed by the writer: %v", it)
	}
	writeFile(t, csv, "changed\n")
	d = v.e.s.featDossier(supTeam)
	do("POST", "/agent/feature/doc", `{"session":"demo-feature","doc":"plan","rev":"`+d.Rev+`","text":`+toJSON(plan)+`}`)
	v.e.s.sup = newFeatSup(v.e.s.sup.file) // a restart
	it = r1Items(t, v, h)
	if s2, _ := it[0]["seal"].(map[string]any); s2 == nil || s2["sha256"] != sha256hex([]byte("a,b\n")) || it[0]["stale"] != true {
		t.Fatalf("resealed or not stale: %v", it)
	}
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.State == "done" {
		t.Fatal("a seal verified a step")
	}
}

// The owner's attestation binds its step's evidence bytes: changed afterwards, the attestation is stale (kept) and the
// step is claimed again, so its objective isn't met.
func TestR1AttestBindsEvidenceBytes(t *testing.T) {
	v := r1Env(t, "## Ship\n- [x] Export @Builder\n  - covers: D1\n  - evidence: file out.csv\n")
	writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "FEATURE.md"), "- [ ] D1: Export runs\n")
	csv := filepath.Join(filepath.Dir(v.planFile()), "out.csv")
	writeFile(t, csv, "a,b\n")
	attestStep(t, v, 0)
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.State != "done" {
		t.Fatalf("attested: %+v", st)
	}
	writeFile(t, csv, "a,b,c\n")
	d := v.e.s.featDossier(supTeam)
	st := d.Milestones[0].Steps[0]
	if st.State == "done" || !strings.Contains(st.Why, "changed since you attested") {
		t.Fatalf("changed evidence kept the attestation: %+v", st)
	}
	if featObjectives(d).Items[0].Met {
		t.Fatal("objective met on changed evidence")
	}
	if _, ok := v.e.s.sup.Attests[supTeam+"/"+st.Key]; !ok {
		t.Fatal("the attestation record was dropped (keep it, labelled stale)")
	}
}

// Sensitive files inside an allowed folder are refused like outside; an interrupted Write isn't provenance; a step's
// several evidence lines are each an item (none lost), with stable ids; a same-size edit with its mtime put back is
// re-hashed (ctime moved), never shown as unchanged.
func TestR1Review(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{".env", ".env.prod", "certs/server.pem", "keys/id_rsa", "x.key", ".netrc", "aws/credentials"} {
		writeFile(t, filepath.Join(root, n), "TOPSECRET")
		if a, why := featSealFile([]string{root}, nil, n); why != "outside the feature's folders" || a.Path != "" {
			t.Fatalf("%s: %+v %q", n, a, why)
		}
	}
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	p := filepath.Join(root, "doc.md")
	os.WriteFile(tr, []byte(`{"type":"assistant","timestamp":"2026-11-08T12:00:00Z","message":{"content":[{"type":"tool_use","id":"w","name":"Write","input":{"file_path":"`+p+`","content":"x"}}]}}`+"\n"+
		`{"type":"user","timestamp":"2026-11-08T12:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"w","content":"ok"}]},"toolUseResult":{"interrupted":true}}`+"\n"), 0o600)
	if pv, _ := featProvenance([]string{tr}, []string{p}, time.Time{}, ""); pv != nil {
		t.Fatalf("an interrupted Write: %+v", pv)
	}
	ms := parseMilestones("## S\n- [x] Both @A\n  - evidence: image shot.png\n  - evidence: file run.log\n")
	if st := ms[0].Steps[0]; len(st.Evidences) != 2 || st.Evidence != "file run.log" {
		t.Fatalf("several evidence lines: %+v", st)
	}
	v := r1Env(t, "## Ship\n- [x] Export @Builder\n  - evidence: image shot.png\n  - evidence: file run.log\n")
	dir := filepath.Dir(v.planFile())
	writeFile(t, filepath.Join(dir, "shot.png"), "png")
	writeFile(t, filepath.Join(dir, "run.log"), "rows: 12\n")
	v.e.s.featSealSteps(supTeam, "writer")
	h := v.e.authed(v.e.login(t))
	it := r1Items(t, v, h)
	if len(it) != 2 || it[0]["id"] == it[1]["id"] || it[0]["id"] == nil || it[0]["seal"] == nil || it[1]["seal"] == nil {
		t.Fatalf("two items, sealed, distinct ids: %v", it)
	}
	log := filepath.Join(dir, "run.log")
	fi, _ := os.Stat(log)
	writeFile(t, log, "rows: 99\n")
	os.Chtimes(log, fi.ModTime(), fi.ModTime())
	it = r1Items(t, v, h)
	if it[1]["stale"] != true || it[1]["current"].(map[string]any)["how"] != "hashed" {
		t.Fatalf("a same-size edit with the mtime put back: %v", it[1])
	}
}

// A step already in the plan when the panel first saw the feature has no known criterion time; its
// seal takes the first-sight time instead, so a Write from before it (48 h ago) is no provenance, while a Write after
// it is. A seal with no criterion time at all (legacy) never gives provenance.
func TestR1ProvenanceNeedsAKnownCriterion(t *testing.T) {
	v := r1Env(t, "## Ship\n- [x] Export @Builder\n  - evidence: file out.csv\n")
	p := filepath.Join(filepath.Dir(v.planFile()), "out.csv")
	writeFile(t, p, "a,b\n")
	write := func(id string, at time.Time) {
		f, _ := os.OpenFile(v.transcript(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fmt.Fprintf(f, `{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":"Write","input":{"file_path":%q,"content":"a,b\n"}}]}}`+"\n", at.Add(-time.Second).Format(time.RFC3339Nano), id, p)
		fmt.Fprintf(f, `{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}}`+"\n", at.Format(time.RFC3339Nano), id)
		f.Close()
	}
	write("early", time.Now().Add(-48*time.Hour))
	v.look(v.agent("working", v.now.Unix()))
	h := v.e.authed(v.e.login(t))
	it := r1Items(t, v, h)
	seal, _ := it[0]["seal"].(map[string]any)
	if seal == nil || seal["criterionAt"] == nil || it[0]["provenance"] != nil || it[0]["provenanceNone"] != true {
		t.Fatalf("a Write from before the panel first saw the step: %v", it)
	}
	write("late", time.Now().Add(2*time.Second))
	if it = r1Items(t, v, h); it[0]["provenance"] == nil {
		t.Fatalf("a Write after first sight: %v", it)
	}
	for k, r := range v.e.s.sup.Sealed {
		r.CriterionAt = 0
		v.e.s.sup.Sealed[k] = r
	}
	if it = r1Items(t, v, h); it[0]["provenance"] != nil || it[0]["provenanceNone"] != true {
		t.Fatalf("a seal with no criterion time gave provenance: %v", it)
	}
}

// An attestation given while its evidence file was missing binds that absence: the file appearing later is a change, so
// the attestation goes stale (kept) and the step is claimed again.
func TestR1AttestBindsAMissingFile(t *testing.T) {
	v := r1Env(t, "## Ship\n- [x] Export @Builder\n  - evidence: file out.csv\n")
	attestStep(t, v, 0)
	if st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]; st.State != "done" {
		t.Fatalf("attested: %+v", st)
	}
	writeFile(t, filepath.Join(filepath.Dir(v.planFile()), "out.csv"), "a,b\n")
	st := v.e.s.featDossier(supTeam).Milestones[0].Steps[0]
	if st.State == "done" || !strings.Contains(st.Why, "changed since you attested") {
		t.Fatalf("a file that appeared after the attestation kept it: %+v", st)
	}
	if _, ok := v.e.s.sup.Attests[supTeam+"/"+st.Key]; !ok {
		t.Fatal("the attestation record was dropped")
	}
}

// A legacy 'evidence: log <ref>' line (before clients wrote 'file') is a file evidence item.
func TestR1LegacyLogKind(t *testing.T) {
	if k, r, ok := featEvRef("log run.log"); !ok || k != "log" || r != "run.log" {
		t.Fatalf("legacy log line: %q %q %v", k, r, ok)
	}
}

// A step's run line still gives its receipt when another evidence line (a file) follows it: several lines are kept,
// and the receipt reads the run line, not the last one.
func TestR1ReceiptWithSeveralEvidenceLines(t *testing.T) {
	v, st := checkEnv(t)
	out, failed := bash(t, st.RunAs)
	record(t, v.transcript(), "toolu_ok1", st.RunAs, out, failed, false, time.Now())
	if got := v.tick(t, st, "run "+st.Attempt+"\n  - evidence: file out.csv"); got.State != "done" || got.Label != "check ran (receipt)" {
		t.Fatalf("run line then a file line: %+v", got)
	}
}

// The attestation's stale check reads nothing outside the safe resolution: a missing file that appears as a symlink
// out of the folders, or an attested file replaced by one, is stale without being opened (a FIFO outside would block
// any read), and never verified.
func TestR1AttestStaleCheckIsGuarded(t *testing.T) {
	for _, attested := range []bool{false, true} {
		v := r1Env(t, "## Ship\n- [x] Export @Builder\n  - evidence: file out.csv\n")
		p := filepath.Join(filepath.Dir(v.planFile()), "out.csv")
		if attested {
			writeFile(t, p, "a,b\n")
		}
		attestStep(t, v, 0)
		fifo := filepath.Join(t.TempDir(), "outside")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Skip("no fifo")
		}
		os.Remove(p)
		os.Symlink(fifo, p)
		done := make(chan featPlanStep, 1)
		go func() { done <- v.e.s.featDossier(supTeam).Milestones[0].Steps[0] }()
		select {
		case st := <-done:
			if st.State == "done" || !strings.Contains(st.Why, "changed since you attested") {
				t.Fatalf("attested=%v: a symlink out: %+v", attested, st)
			}
		case <-time.After(5 * time.Second):
			os.WriteFile(fifo, nil, 0) // unblock the reader
			t.Fatalf("attested=%v: the stale check opened a path outside the feature's folders", attested)
		}
	}
}
