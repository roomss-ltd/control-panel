package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexRolloutReadsAsAChat(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "rollout.jsonl")
	rollout := []string{
		`{"timestamp":"2026-10-30T10:00:00.000Z","type":"session_meta","payload":{"id":"0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b","cwd":"/work/app","cli_version":"0.156.1"}}`,
		`{"timestamp":"2026-10-30T10:00:00.100Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd</environment_context>"}]}}`,
		`{"timestamp":"2026-10-30T10:00:01.000Z","type":"turn_context","payload":{"cwd":"/work/app","model":"gpt-6-astra","summary":"none","collaboration_mode":{"mode":"default","settings":{"model":"gpt-6-astra","reasoning_effort":"high"}}}}`,
		`{"timestamp":"2026-10-30T10:00:01.000Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-10-30T10:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /work/app\n\nbe nice"}]}}`,
		`{"timestamp":"2026-10-30T10:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the failing test"}]}}`,
		`{"timestamp":"2026-10-30T10:00:02.000Z","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"x"}}`,
		`{"timestamp":"2026-10-30T10:00:03.000Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"**Looking at tests**"}],"encrypted_content":"x"}}`,
		`{"timestamp":"2026-10-30T10:00:04.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"go test ./...\",\"workdir\":\"/work/app\"}","call_id":"call_1"}}`,
		`{"timestamp":"2026-10-30T10:00:09.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"Process exited with code 1\nOutput:\nFAIL x"}}`,
		`{"timestamp":"2026-10-30T10:00:10.000Z","type":"response_item","payload":{"type":"custom_tool_call","status":"completed","call_id":"call_2","name":"apply_patch","input":"*** Begin Patch\n*** Update File: x.go\n@@\n a := 1\n-b := 2\n+b := 3\n*** End Patch"}}`,
		`{"timestamp":"2026-10-30T10:00:10.500Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_2","output":"Success. Updated the following files:\nM x.go"}}`,
		`{"timestamp":"2026-10-30T10:00:11.000Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":41000},"model_context_window":272000}}}`,
		`{"timestamp":"2026-10-30T10:00:12.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Fixed: b is 3 now."}]}}`,
		`{"timestamp":"2026-10-30T10:00:12.000Z","type":"event_msg","payload":{"type":"agent_message","message":"Fixed: b is 3 now."}}`,
	}
	os.WriteFile(src, []byte(strings.Join(rollout, "\n")+"\n"), 0o600)
	c := codexMirrors{dir: filepath.Join(dir, "copies")}
	m := c.sync(src, filepath.Join(dir, "copies", "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b.jsonl"))
	if m.cwd != "/work/app" || m.model != "gpt-6-astra" || m.used != 41000 || m.window != 272000 {
		t.Fatalf("state: %+v", m)
	}
	lines, _ := readFrom(m.out, 0)
	if _, final := lastTurn(lines); final {
		t.Fatal("the turn is still going")
	}
	items, title := parseChat(lines)
	if title != "fix the failing test" {
		t.Errorf("title %q", title)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.K+":"+it.Name+":"+it.Text)
	}
	want := []string{"user::fix the failing test", "model:high:gpt-6-astra", "think::**Looking at tests**", "tool:Bash:go test ./...",
		"result::Process exited with code 1\nOutput:\nFAIL x", "tool:Edit:x.go", "result::Success. Updated the following files:\nM x.go", "text::Fixed: b is 3 now."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("items:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !items[4].Err || items[6].Err {
		t.Error("only the failed command is an error")
	}
	if items[5].Add != 1 || items[5].Del != 1 {
		t.Errorf("the patch's diff: %+v", items[5])
	}

	// the turn ends: only the new entries are copied, and the chat says it's over
	f, _ := os.OpenFile(src, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"timestamp":"2026-10-30T10:00:13.000Z","type":"event_msg","payload":{"type":"task_complete","last_agent_message":"Fixed"}}` + "\n")
	f.Close()
	c.sync(src, filepath.Join(dir, "copies", "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b.jsonl"))
	lines, _ = readFrom(m.out, 0)
	if _, final := lastTurn(lines); !final {
		t.Fatal("the turn ended")
	}
	items, _ = parseChat(lines)
	if n := len(items); n != len(want)+1 || items[n-1].Text != "Worked for 12s" {
		t.Fatalf("after the turn: %d items, last %+v", n, items[n-1])
	}
}

func TestCodexMenusReadOffItsScreen(t *testing.T) {
	models := "› /model\n  Select Model and Effort\n› 1. GPT-6-Astra (current)\n  2. GPT-6-Sol\n  3. GPT-6-Luna\n  enter select · esc back\n"
	rows, at, ok := codexMenu(models, "Select Model and Effort")
	if !ok || at != 0 || len(rows) != 3 || codexRow(rows, "GPT-6-Sol") != 1 || codexRow(rows, "GPT-6") != 0 {
		t.Fatalf("models: %q at %d", rows, at)
	}
	levels := "  Select Reasoning Level for GPT-6-Astra\n  1. Low               Fast responses with\n                       lighter reasoning\n› 2. Medium (default)  Balances speed and\n  3. High              Greater reasoning depth\n  4. Extra high        Extra high reasoning\n  5. More reasoning…   Max and Ultra consume\n  enter default · s session · esc back\n"
	rows, at, ok = codexMenu(levels, "Select Reasoning Level")
	if !ok || at != 1 || codexRow(rows, codexLevel["xhigh"][0]) != 3 || codexRow(rows, codexLevel["ultra"][0]) != 4 {
		t.Fatalf("levels: %q at %d", rows, at)
	}
	if _, _, ok := codexMenu("› Ask Codex to do anything", "Select Reasoning Level"); ok {
		t.Fatal("no menu on screen")
	}
}

func TestAFileACommandWroteIsTheSessionsFile(t *testing.T) {
	dir := t.TempDir()
	made, read := filepath.Join(dir, "plan.html"), filepath.Join(dir, "old.md")
	os.WriteFile(made, []byte("<h1>plan</h1>"), 0o600)
	os.WriteFile(read, []byte("# old"), 0o600)
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(read, old, old)
	ts := time.Now().UTC().Format(time.RFC3339)
	tr := filepath.Join(dir, "s.jsonl")
	os.WriteFile(tr, []byte(`{"type":"assistant","timestamp":"`+ts+`","cwd":"`+dir+`","message":{"content":[{"type":"tool_use","id":"x","name":"Bash","input":{"command":"cat old.md > /dev/null; cat > plan.html <<'EOF'\n<h1>plan</h1>\nEOF"}}]}}`+"\n"), 0o600)
	c := artifactCache{scans: map[string]*artScan{}}
	files := c.scan(tr)
	if a := files[made]; a == nil || !a.Created || a.Mentioned {
		t.Fatalf("the file it wrote: %+v", a)
	}
	if files[read] != nil {
		t.Fatal("a file it only read is not its file")
	}
}

// An edit a code-mode script made comes as a FileChange: each file shows as a Write or an Edit with its diff.
func TestCodexFileChangeShowsItsDiff(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "rollout.jsonl")
	rollout := []string{
		`{"timestamp":"2026-10-30T10:00:00.000Z","type":"session_meta","payload":{"id":"0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c","cwd":"/work/app"}}`,
		`{"timestamp":"2026-10-30T10:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"edit them"}]}}`,
		`{"timestamp":"2026-10-30T10:00:02.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"FileChange","id":"exec-1","changes":{"/w/b.go":{"type":"update","unified_diff":"@@ -1,2 +1,2 @@\n a := 1\n-b := 2\n+b := 3\n"},"/w/a.txt":{"type":"add","content":"hi\n"}}}}}`,
	}
	os.WriteFile(src, []byte(strings.Join(rollout, "\n")+"\n"), 0o600)
	c := codexMirrors{dir: filepath.Join(dir, "copies")}
	m := c.sync(src, filepath.Join(dir, "copies", "x.jsonl"))
	lines, _ := readFrom(m.out, 0)
	items, _ := parseChat(lines)
	var got []string
	for _, it := range items {
		if it.K == "tool" {
			got = append(got, it.Name+":"+it.Path)
		}
	}
	if strings.Join(got, "|") != "Write:/w/a.txt|Edit:/w/b.go" {
		t.Fatalf("tools: %v", got)
	}
}
