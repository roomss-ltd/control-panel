package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A Codex in a pane gets the same agent view as a Claude: its conversation (the rollout Codex appends to,
// ~/.codex/sessions/…/rollout-<time>-<id>.jsonl) is copied, entry by entry as it grows, into a file in Claude
// Code's transcript format, so the chat, the peek, "Load earlier" and the agent API read it like any other.

// codexRolloutRe matches the path of a Codex rollout (session log) file and captures its session id.
var codexRolloutRe = regexp.MustCompile(`/\.codex/sessions/.*/rollout-.*-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

// rollout is one Codex session log: its path and session id.
type rollout struct{ path, id string }

// codexRollouts: the rollouts a Codex process writes (it keeps them open), its own first: each subagent it runs
// has one too. The oldest is taken to be its own (subagents start after it); read session_meta instead if forks break that.
func codexRollouts(pid int) []rollout {
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	ents, _ := os.ReadDir(dir)
	var out []rollout
	seen := map[string]bool{}
	for _, e := range ents {
		if l, err := os.Readlink(dir + "/" + e.Name()); err == nil && !seen[l] {
			if m := codexRolloutRe.FindStringSubmatch(l); m != nil {
				seen[l] = true
				out = append(out, rollout{l, m[1]})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return filepath.Base(out[i].path) < filepath.Base(out[j].path) }) // rollout-<start time>-…
	return out
}

// codexMirrors translates Codex rollouts into Claude Code transcripts, so the chat view reads both the same way.
type codexMirrors struct {
	dir string // where the copies go
	mu  sync.Mutex
	m   map[string]*codexMirror // by rollout path
}

// codexMirror is the copy of one rollout: where it is written, how far it has been read, and the session's cwd, model and effort.
type codexMirror struct {
	out          string
	off          int64 // how far the rollout has been copied
	cwd, model   string
	effort       string
	used, window int64 // the latest request's input tokens, and the model's context window
	turnAt       int64 // when the turn under way started
	titled       bool
	subs         string          // where its subagents' copies go: <copy, less .jsonl>/subagents, as Claude Code keeps them
	wrapped      map[string]bool // exec calls: a script around tools whose work comes as items of its own
}

// sync copies what the rollout gained since the last call and returns the copy's state.
func (c *codexMirrors) sync(src, out string) codexMirror {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*codexMirror{}
	}
	m := c.m[src]
	if m == nil {
		m = &codexMirror{out: out, subs: filepath.Join(strings.TrimSuffix(out, ".jsonl"), "subagents"), wrapped: map[string]bool{}}
		c.m[src] = m
		os.MkdirAll(filepath.Dir(out), 0o700)
		// copied whole again after a restart, all of it before anyone reads it: a copy made a few MB a second
		// opened chats on an old turn, then brought hours of work as one batch
		tmp := out + ".tmp"
		os.WriteFile(tmp, nil, 0o600)
		for m.copy(src, tmp) {
		}
		os.Rename(tmp, out)
		return *m
	}
	m.copy(src, m.out)
	return *m
}

// copy translates what the rollout gained since m.off onto the end of dst. false: nothing more (or a failed write).
func (m *codexMirror) copy(src, dst string) bool {
	lines, off := readFrom(src, m.off)
	if off == m.off {
		return false
	}
	var buf []byte
	for _, l := range lines {
		for _, e := range m.translate(l) {
			b, _ := json.Marshal(e)
			buf = append(append(buf, b...), '\n')
		}
	}
	if len(buf) > 0 {
		f, err := os.OpenFile(dst, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return false
		}
		_, err = f.Write(buf)
		f.Close()
		if err != nil {
			return false
		}
	}
	m.off = off
	return true
}

// cxLine is one line of a Codex rollout.
type cxLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// cxPayload is the part of a rollout line's payload that translate reads.
type cxPayload struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Cwd     string          `json:"cwd"`
	Model   string          `json:"model"`
	Role    string          `json:"role"`
	Message string          `json:"message"`
	Content json.RawMessage `json:"content"`
	Summary json.RawMessage `json:"summary"` // reasoning: [{text}]; turn_context: a word
	Effort  string          `json:"effort"`
	Collab  struct {
		Settings struct {
			Effort string `json:"reasoning_effort"`
		} `json:"settings"`
	} `json:"collaboration_mode"`
	Name      string                 `json:"name"`
	Arguments string                 `json:"arguments"`
	Input     string                 `json:"input"`
	CallID    string                 `json:"call_id"`
	Output    json.RawMessage        `json:"output"`
	Item      json.RawMessage        `json:"item"` // item_completed: what one step did
	Action    struct{ Query string } `json:"action"`
	Info      *struct {
		Last struct {
			Input int64 `json:"input_tokens"`
		} `json:"last_token_usage"`
		Window int64 `json:"model_context_window"`
	} `json:"info"`
}

// obj is a JSON object.
type obj = map[string]any

// translate turns one rollout entry into Claude Code transcript entries (none for what the chat doesn't show).
func (m *codexMirror) translate(l []byte) []obj {
	var e cxLine
	var p cxPayload
	if json.Unmarshal(l, &e) != nil || json.Unmarshal(e.Payload, &p) != nil {
		return nil
	}
	t := unixOf(e.Timestamp)
	entry := func(typ string, msg obj) obj {
		return obj{"type": typ, "timestamp": e.Timestamp, "cwd": m.cwd, "effort": m.effort, "message": msg}
	}
	assistant := func(content ...obj) []obj {
		return []obj{entry("assistant", obj{"model": m.model, "content": content})}
	}
	switch e.Type {
	case "session_meta", "turn_context":
		if p.Cwd != "" {
			m.cwd = p.Cwd
		}
		if p.Model != "" {
			m.model = p.Model
		}
		if e.Type == "turn_context" {
			m.effort = cmp.Or(p.Effort, p.Collab.Settings.Effort) // none: the model's default, which the rollout doesn't say
		}
	case "compacted":
		return []obj{{"type": "system", "subtype": "compact_boundary", "timestamp": e.Timestamp, "content": "Compacted"}}
	case "event_msg":
		switch p.Type {
		case "task_started", "turn_started":
			m.turnAt = t
		case "task_complete", "turn_complete":
			out := []obj{entry("assistant", obj{"model": "<synthetic>", "content": []obj{}, "stop_reason": "end_turn"})}
			if m.turnAt > 0 && t >= m.turnAt {
				out = append(out, obj{"type": "system", "subtype": "turn_duration", "timestamp": e.Timestamp, "durationMs": (t - m.turnAt) * 1000})
			}
			m.turnAt = 0
			return out
		case "turn_aborted":
			m.turnAt = 0
			return []obj{entry("user", obj{"role": "user", "content": "[Request interrupted by user]"})}
		case "item_completed":
			return m.item(p.Item, assistant, entry)
		case "token_count":
			if p.Info != nil {
				m.used, m.window = p.Info.Last.Input, p.Info.Window
			}
		}
	case "response_item":
		switch p.Type {
		case "message":
			var cs []struct{ Type, Text string }
			json.Unmarshal(p.Content, &cs)
			var text []string
			for _, c := range cs {
				if c.Type == "output_text" || c.Type == "input_text" {
					text = append(text, c.Text)
				}
			}
			s := strings.Join(text, "\n")
			switch {
			case strings.TrimSpace(s) == "":
			case p.Role == "assistant":
				return assistant(obj{"type": "text", "text": s})
			case p.Role == "user" && !codexContext(s): // yours; Codex also files its context (AGENTS.md, the environment) as user messages
				out := []obj{entry("user", obj{"role": "user", "content": s})}
				if !m.titled {
					m.titled = true
					out = append(out, obj{"type": "ai-title", "aiTitle": trunc(strings.Join(strings.Fields(s), " "), 80)})
				}
				return out
			}
		case "reasoning":
			var sum []struct{ Text string }
			json.Unmarshal(p.Summary, &sum)
			var text []string
			for _, s := range sum {
				text = append(text, s.Text)
			}
			if len(text) > 0 { // none saved: nothing to show
				return assistant(obj{"type": "thinking", "thinking": strings.Join(text, "\n\n")})
			}
		case "function_call", "custom_tool_call", "local_shell_call":
			if p.Name == "exec" { // code mode: its commands and searches arrive as items
				m.wrapped[p.CallID] = true
				return nil
			}
			name, in := codexTool(p)
			return assistant(obj{"type": "tool_use", "id": p.CallID, "name": name, "input": in})
		case "web_search_call":
			return assistant(obj{"type": "tool_use", "id": "ws-" + e.Timestamp, "name": "WebSearch", "input": obj{"query": p.Action.Query}})
		case "function_call_output", "custom_tool_call_output":
			if m.wrapped[p.CallID] {
				delete(m.wrapped, p.CallID)
				return nil
			}
			out := codexOutput(p.Output)
			return []obj{entry("user", obj{"role": "user", "content": []obj{{"type": "tool_result", "tool_use_id": p.CallID, "content": out, "is_error": codexFailed(out)}}})}
		}
	}
	return nil
}

// cxItem is the item of an item_completed event: what one step did (a command, a file change, a web search).
type cxItem struct {
	Type      string          `json:"type"`
	Kind      string          `json:"kind"`
	ID        string          `json:"id"`
	Command   json.RawMessage `json:"command"` // ["/bin/bash", "-lc", "…"] or a string
	Output    string          `json:"aggregated_output"`
	Exit      *int            `json:"exit_code"`
	Query     string          `json:"query"`
	Thread    string          `json:"agent_thread_id"`
	AgentPath string          `json:"agent_path"`
	Changes   map[string]struct {
		Type    string `json:"type"` // add, update, delete
		Content string `json:"content"`
		Diff    string `json:"unified_diff"`
	} `json:"changes"` // FileChange: what an exec's edit did, by path
}

// item turns one finished step into a tool call and its result: a command as Bash, a web search as WebSearch.
// A subagent it starts gets the meta file Claude Code writes for one, so the panel lists and opens it.
func (m *codexMirror) item(raw json.RawMessage, assistant func(...obj) []obj, entry func(string, obj) obj) []obj {
	var it cxItem
	if json.Unmarshal(raw, &it) != nil {
		return nil
	}
	call := func(name string, in obj, out string, failed bool) []obj {
		return append(assistant(obj{"type": "tool_use", "id": it.ID, "name": name, "input": in}),
			entry("user", obj{"role": "user", "content": []obj{{"type": "tool_result", "tool_use_id": it.ID, "content": out, "is_error": failed}}}))
	}
	switch it.Type {
	case "CommandExecution":
		var cmd string
		var parts []string
		if json.Unmarshal(it.Command, &parts) == nil && len(parts) > 0 {
			cmd = parts[len(parts)-1]
		} else {
			json.Unmarshal(it.Command, &cmd)
		}
		return call("Bash", obj{"command": cmd}, trunc(it.Output, 30000), it.Exit != nil && *it.Exit != 0)
	case "FileChange": // an edit a script made (code mode): each file as a Write or an Edit, so it shows its diff
		paths := make([]string, 0, len(it.Changes))
		for p := range it.Changes {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		var out []obj
		for n, p := range paths {
			c := it.Changes[p]
			name, in := "Write", obj{"file_path": p, "content": c.Content}
			switch c.Type {
			case "update":
				var ok bool
				if name, in, ok = patchAsEdit("*** Update File: " + p + "\n" + c.Diff); !ok {
					continue
				}
			case "delete":
				name, in = "Edit", obj{"file_path": p, "old_string": c.Content, "new_string": ""}
			}
			id := it.ID + "-" + strconv.Itoa(n)
			out = append(append(out, assistant(obj{"type": "tool_use", "id": id, "name": name, "input": in})...),
				entry("user", obj{"role": "user", "content": []obj{{"type": "tool_result", "tool_use_id": id, "content": ""}}}))
		}
		return out
	case "Extension":
		if it.Kind == "web.search" {
			return call("WebSearch", obj{"query": it.Query}, "Searched the web", false)
		}
	case "SubAgentActivity":
		if it.Kind == "started" && subIDRe.MatchString(it.Thread) {
			name := strings.ReplaceAll(filepath.Base(it.AgentPath), "_", " ")
			b, _ := json.Marshal(obj{"agentType": "codex", "description": name, "toolUseId": it.ID, "requestShape": "background"})
			os.MkdirAll(m.subs, 0o700)
			os.WriteFile(filepath.Join(m.subs, "agent-"+it.Thread+".meta.json"), b, 0o600)
		}
	}
	return nil
}

// codexContext: a user message Codex wrote itself, the instructions and environment it hands the model.
func codexContext(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "<") || strings.HasPrefix(s, "# AGENTS.md instructions")
}

// codexTool names a Codex tool call the way the chat already draws Claude's: a command as Bash, the plan as
// TodoWrite, a one-file patch as Edit or Write (so it shows its diff); anything else under its own name.
func codexTool(p cxPayload) (string, obj) {
	var args obj
	json.Unmarshal([]byte(p.Arguments), &args)
	str := func(k string) string { s, _ := args[k].(string); return s }
	switch p.Name {
	case "exec_command", "shell_command", "shell", "container.exec", "local_shell":
		cmd := cmp.Or(str("cmd"), str("command"))
		if parts, ok := args["command"].([]any); ok && len(parts) > 0 { // ["bash", "-lc", "…"]
			s, _ := parts[len(parts)-1].(string)
			cmd = s
		}
		return "Bash", obj{"command": cmd}
	case "update_plan":
		steps, _ := args["plan"].([]any)
		var todos []obj
		for _, s := range steps {
			if st, ok := s.(map[string]any); ok {
				todos = append(todos, obj{"content": st["step"], "status": st["status"], "activeForm": st["step"]})
			}
		}
		return "TodoWrite", obj{"todos": todos}
	case "spawn_agent": // its task goes encrypted: the name is what there is
		return "Agent", obj{"description": strings.ReplaceAll(str("task_name"), "_", " "), "subagent_type": "codex"}
	case "followup_task", "send_message":
		return "SendMessage", obj{"to": str("target")}
	case "request_user_input_async":
		qs, _ := args["questions"].([]any)
		var out []obj
		for _, q := range qs {
			if qm, ok := q.(map[string]any); ok {
				out = append(out, obj{"question": qm["title"]})
			}
		}
		return "AskUserQuestion", obj{"questions": out}
	case "view_image":
		return "Read", obj{"file_path": str("path")}
	case "apply_patch":
		patch := p.Input
		if patch == "" {
			patch = str("input")
		}
		if name, in, ok := patchAsEdit(patch); ok {
			return name, in
		}
		return "apply_patch", obj{"patch": patch}
	}
	if args == nil && p.Input != "" {
		args = obj{"input": p.Input}
	}
	return p.Name, args
}

// patchAsEdit reads a one-file apply_patch: an added file is a Write, an updated one an Edit whose old and new
// text are its hunks' lines (context with the removed, context with the added).
func patchAsEdit(patch string) (string, obj, bool) {
	var file, kind string
	var old, nu []string
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "*** Add File: "), strings.HasPrefix(l, "*** Update File: "), strings.HasPrefix(l, "*** Delete File: "):
			if file != "" {
				return "", nil, false
			}
			kind, file, _ = strings.Cut(strings.TrimPrefix(l, "*** "), " File: ")
		case strings.HasPrefix(l, "***"), strings.HasPrefix(l, "@@"):
		case strings.HasPrefix(l, "+"):
			nu = append(nu, l[1:])
		case strings.HasPrefix(l, "-"):
			old = append(old, l[1:])
		case strings.HasPrefix(l, " "):
			old, nu = append(old, l[1:]), append(nu, l[1:])
		}
	}
	switch kind {
	case "Add":
		return "Write", obj{"file_path": file, "content": strings.Join(nu, "\n")}, true
	case "Update":
		return "Edit", obj{"file_path": file, "old_string": strings.Join(old, "\n"), "new_string": strings.Join(nu, "\n")}, true
	}
	return "", nil, false
}

// codexOutput: a tool's output, which is text, a list of content items, or (older Codex) {"output": …}.
func codexOutput(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		var old struct{ Output string }
		if strings.HasPrefix(s, "{") && json.Unmarshal([]byte(s), &old) == nil && old.Output != "" {
			return old.Output
		}
		return s
	}
	var items []struct{ Type, Text string }
	json.Unmarshal(raw, &items)
	var text []string
	for _, it := range items {
		if it.Text != "" {
			text = append(text, it.Text)
		} else if it.Type == "input_image" {
			text = append(text, "[image]")
		}
	}
	return strings.Join(text, "\n")
}

// codexExitRe finds the exit code in a command's output.
var codexExitRe = regexp.MustCompile(`(?:exited with code|Exit code:|"exit_code":)\s*(-?\d+)`)

// codexFailed reports whether a command's output ends in a non-zero exit code.
func codexFailed(out string) bool {
	m := codexExitRe.FindStringSubmatch(out)
	return m != nil && m[1] != "0"
}

// codexAgent is the tracker's view of the Codex in a pane: its status from the hook plugin's word (and from its
// rollout: a finished turn is over whatever a missed hook says), its conversation from the copy.
func (t *termTracker) codexAgent(in paneInfo, fb *Pane, now time.Time) *agentInfo {
	rs := codexRollouts(in.AgentPID)
	if len(rs) == 0 {
		return nil
	}
	id := rs[0].id
	m := t.codex.sync(rs[0].path, filepath.Join(t.codex.dir, id+".jsonl"))
	for _, r := range rs[1:] { // its subagents' rollouts, where the subagent list reads Claude Code's
		t.codex.sync(r.path, filepath.Join(m.subs, "agent-"+r.id+".jsonl"))
	}
	t.tr.mu.Lock()
	t.tr.paths[id] = m.out
	t.tr.mu.Unlock()
	cp := claudeProc{PID: in.AgentPID, SessionID: id, Cwd: m.cwd, Status: "idle"}
	if st, ok := t.startOf(in.AgentPID); ok {
		cp.State = st.State
	}
	ag := &agentInfo{cp: cp, kind: "codex", window: m.window}
	ag.tail, ag.path = t.tr.tail(id)
	ag.tail.ctx, ag.tail.model = m.used, m.model
	switch {
	case fbActivity(fb) == "Waiting":
		ag.cp.Status = "waiting"
	case !ag.tail.final && ag.tail.size > 0:
		ag.cp.Status = "busy"
	}
	ag.cp.Since = ag.tail.mtime.Unix()
	if ag.path != "" {
		ag.subs = t.subs.list(ag.path, ag.cp.Status != "idle", now)
	}
	return ag
}

// ---- model and effort: Codex's /model picker, driven like you would ----

// Codex takes no argument to /model (typed, it goes to the model as a message): it opens a list of models, then
// one of reasoning levels, Max and Ultra one deeper under "More reasoning…". The panel moves the highlight to the
// row it wants and takes it with "s", for this session only: Enter, or a row's digit, saves it as the default.
var codexRowRe = regexp.MustCompile(`^\s*(›)?\s*(\d+)\.\s+(.*?)(?:\s{2,}.*)?$`)

// codexMenu reads the menu under title on a screen: its rows' labels, and which row is highlighted.
func codexMenu(screen, title string) (rows []string, at int, ok bool) {
	i := strings.LastIndex(screen, title)
	if i < 0 {
		return nil, 0, false
	}
	at = -1
	for _, l := range strings.Split(screen[i+len(title):], "\n") {
		if m := codexRowRe.FindStringSubmatch(l); m != nil {
			if m[1] != "" {
				at = len(rows)
			}
			rows = append(rows, strings.TrimSpace(m[3]))
		}
	}
	return rows, at, len(rows) > 0 && at >= 0
}

// codexRow: the row whose label starts with want (case and the "(current)"/"(default)" notes aside).
func codexRow(rows []string, want string) int {
	for i, r := range rows {
		if strings.HasPrefix(strings.ToLower(r), strings.ToLower(want)) {
			return i
		}
	}
	return -1
}

// codexLevel maps a reasoning effort to the menu entries to pick in Codex's model picker.
var codexLevel = map[string][2]string{"low": {"Low"}, "medium": {"Medium"}, "high": {"High"}, "xhigh": {"Extra high"},
	"max": {"More reasoning", "Max"}, "ultra": {"More reasoning", "Ultra"}}

// codexModel switches a Codex pane to the named model and effort by driving its picker menu.
func (s *server) codexModel(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Session string
		Pane    int
		Model   string // its name as the picker lists it, e.g. GPT-6-Sol
		Effort  string
	}
	if err := readJSON(r, &b); err != nil || !sessionNameRe.MatchString(b.Session) || b.Model == "" || len(b.Model) > 60 {
		writeJSON(w, 400, map[string]any{"error": "bad request"})
		return
	}
	lv, ok := codexLevel[b.Effort]
	if !ok {
		writeJSON(w, 400, map[string]any{"error": "unknown effort"})
		return
	}
	if p, ok := s.term.pane(b.Session, b.Pane); !ok || p.Kind != "codex" {
		writeJSON(w, 404, map[string]any{"error": "no Codex in this pane"})
		return
	}
	what := fmt.Sprintf("%s:%d codex model %s %s", b.Session, b.Pane, b.Model, b.Effort)
	err := s.codexPick(b.Session, b.Pane, b.Model, lv)
	if err != nil {
		s.z.keys(b.Session, b.Pane, "Esc", "Esc", "Esc") // leave no menu open
		s.audit.add("input-fail", what+": "+err.Error(), clientIP(r))
		writeJSON(w, 502, map[string]any{"error": err.Error()})
		return
	}
	s.audit.add("input", what, clientIP(r))
	writeJSON(w, 200, map[string]any{"ok": true})
}

// codexPick walks Codex's model menus to model and lv, failing if a menu is already open.
func (s *server) codexPick(session string, pane int, model string, lv [2]string) error {
	screen := func() string { sc, _ := s.z.dump(session, pane); return sc }
	if box := screen(); strings.Contains(box, "Select Model and Effort") || strings.Contains(box, "Select Reasoning Level") {
		return errors.New("its model menu is already open: see the Terminal tab")
	}
	// move to the row starting with want in the menu under title, then take it with key
	choose := func(title, want string, key func(row int) error) error {
		var rows []string
		var at int
		if !waitUntil(5*time.Second, func() bool { var ok bool; rows, at, ok = codexMenu(screen(), title); return ok }) {
			return errors.New("Codex didn't show " + strconv.Quote(title))
		}
		i := codexRow(rows, want)
		if i < 0 {
			return errors.New("Codex doesn't offer " + want + " here")
		}
		for ; at < i; at++ {
			if err := s.z.keys(session, pane, "Down"); err != nil {
				return err
			}
		}
		for ; at > i; at-- {
			if err := s.z.keys(session, pane, "Up"); err != nil {
				return err
			}
		}
		return key(i)
	}
	enter := func(int) error { return s.z.keys(session, pane, "Enter") }
	take := func(int) error { return s.z.write(session, pane, "s") }                     // this session only
	open := func(row int) error { return s.z.write(session, pane, strconv.Itoa(row+1)) } // a submenu opens on its digit
	if err := s.z.write(session, pane, "/model"); err != nil {
		return err
	}
	if err := enter(0); err != nil {
		return err
	}
	if err := choose("Select Model and Effort", model, enter); err != nil {
		return err
	}
	if lv[1] == "" {
		return choose("Select Reasoning Level", lv[0], take)
	}
	if err := choose("Select Reasoning Level", lv[0], open); err != nil {
		return err
	}
	return choose("Advanced Reasoning", lv[1], take)
}
