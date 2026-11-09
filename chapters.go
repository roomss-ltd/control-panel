package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// chapters: one line on what each stretch of a chat is about (the owner's message and what followed, up to their
// next), written in the background by a small model through `claude -p`, so the history rail can say what the chat
// was about there. Progressive checkpoints: a stretch is summed up once it's over (the owner wrote again, or it has
// been quiet a few minutes), newest first, and kept in chapters.json by the owner message's key, never redone.
type chapters struct {
	mu   sync.Mutex
	file string
	M    map[string]string
	busy bool
}

// Limits of the background summaries.
const (
	chapterBatch = 25              // stretches per model call
	chapterQuiet = 3 * time.Minute // the last stretch counts as over after this
)

// newChapters loads the saved one-line summaries from file (a missing file starts empty).
func newChapters(file string) *chapters {
	c := &chapters{file: file, M: map[string]string{}}
	if b, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(b, &c.M)
	}
	return c
}

// snapshot returns a copy of the summaries, by owner message key.
func (c *chapters) snapshot() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := make(map[string]string, len(c.M))
	for k, v := range c.M {
		m[k] = v
	}
	return m
}

// stretch is an owner message and the answers after it, as the model reads it.
type stretch struct {
	key, text string
}

// notOwner matches the turns that aren't the owner talking: agents and the team writing in, Main briefing a thread
var notOwner = regexp.MustCompile(`^\[(?:From the Claude agent|From the Codex agent|Team update|From Main JARVIS|Brief from Main JARVIS|Thread context)`)

// stretches returns a chat's finished, not yet summed-up stretches, newest first.
func stretches(items []aiItem, done map[string]string, live bool, now time.Time) []stretch {
	var out []stretch
	var cur *stretch
	var ans strings.Builder
	var last float64
	flush := func(over bool) {
		if cur != nil && over && done[cur.key] == "" {
			cur.text += "\nJARVIS: " + trunc(strings.TrimSpace(ans.String()), 500)
			out = append(out, *cur)
		}
		cur = nil
		ans.Reset()
	}
	for _, it := range items {
		switch it.Role {
		case "user":
			if notOwner.MatchString(it.Text) {
				if cur != nil {
					fmt.Fprintf(&ans, " [%s]", trunc(it.Text, 120))
				}
				continue
			}
			flush(true)
			cur = &stretch{key: it.K, text: "Owner: " + trunc(strings.TrimSpace(it.Text), 500)}
		case "assistant":
			if cur != nil && ans.Len() < 600 {
				ans.WriteString(" " + it.Text)
			}
		}
		last = it.T
	}
	flush(!live && now.Sub(time.Unix(int64(last), 0)) > chapterQuiet)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 { // newest first
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// chaptersFill sums up what's due in every chat: Main, then each thread. One pass at a time.
func (s *server) chaptersFill(ctx context.Context) {
	c := s.chapters
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		return
	}
	c.busy = true
	done := make(map[string]string, len(c.M))
	for k, v := range c.M {
		done[k] = v
	}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.busy = false; c.mu.Unlock() }()

	a := s.ai
	a.mu.Lock()
	var due []stretch
	due = append(due, stretches(a.items, done, a.conv("").live != nil, time.Now())...)
	for id, its := range a.thItems {
		due = append(due, stretches(its, done, a.conv(id).live != nil, time.Now())...)
	}
	a.mu.Unlock()

	for len(due) > 0 && ctx.Err() == nil {
		n := min(chapterBatch, len(due))
		lines, err := summarize(ctx, due[:n])
		if err != nil {
			log.Printf("chapters: %v", err)
			return
		}
		c.mu.Lock()
		for i, st := range due[:n] {
			if l := strings.TrimSpace(lines[fmt.Sprint(i+1)]); l != "" {
				c.M[st.key] = l
			}
		}
		b, _ := json.MarshalIndent(c.M, "", " ")
		_ = os.WriteFile(c.file+".tmp", b, 0o600)
		_ = os.Rename(c.file+".tmp", c.file)
		c.mu.Unlock()
		due = due[n:]
	}
}

// summarize asks a small model for one line per stretch: {"1": "…", "2": "…"}. No tools, no MCP servers, no settings,
// no saved session: a plain completion through the owner's Claude Code.
func summarize(ctx context.Context, sts []stretch) (map[string]string, error) {
	var b strings.Builder
	b.WriteString("Below are numbered stretches of a chat between the owner and JARVIS, their assistant on a VM. " +
		"For each, write one line (at most 14 words) saying what that part of the chat is about: the topic and where it " +
		"got to. Name concrete things (projects, PRs, services). No \"the owner asked\" phrasing. " +
		"Reply with JSON only: {\"1\": \"...\", \"2\": \"...\"}.\n\n")
	for i, st := range sts {
		fmt.Fprintf(&b, "%d.\n%s\n\n", i+1, st.text)
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	dir := filepath.Join(os.TempDir(), "cp-chapters")
	_ = os.MkdirAll(dir, 0o700)
	cmd := exec.CommandContext(cctx, "claude", "-p", "--model", "haiku", "--tools", "", "--strict-mcp-config",
		"--setting-sources", "", "--no-session-persistence", "--output-format", "text")
	cmd.Dir = dir // no project here: no CLAUDE.md, no hooks
	cmd.Stdin = strings.NewReader(b.String())
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude -p: %w", err)
	}
	s := string(out)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return nil, fmt.Errorf("claude -p: no JSON in %q", trunc(s, 200))
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s[i:j+1]), &m); err != nil {
		return nil, fmt.Errorf("claude -p: %w", err)
	}
	return m, nil
}

// chaptersLoop runs a pass a minute after start, then every few minutes.
func (s *server) chaptersLoop() {
	if os.Getenv("CP_NO_BACKGROUND_MODELS") == "1" { // a scratch instance's harness: no paid model runs in the background
		log.Print("chapters and digests: off (CP_NO_BACKGROUND_MODELS=1)")
		return
	}
	ctx := context.Background()
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.chaptersFill(ctx)
			s.digestsFill(ctx) // and the agents' messages, made readable (digest.go)
			t.Reset(4 * time.Minute)
		}
	}
}

// aiChapters serves GET /api/ai/chapters: {owner message key: its stretch's line}.
func (s *server) aiChapters(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.chapters.snapshot())
}
