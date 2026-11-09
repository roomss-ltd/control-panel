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
	"strings"
	"sync"
	"time"
)

// digests holds readable versions of the agents' messages. The messages agents send each other (Main briefing a lead,
// a team update, a Claude agent writing in) are written for agents: long, dense, full of IDs. The owner reads them in
// the panel, so each one gets a readable version, written once in the background by a small model through `claude -p`
// (as chapters.go): the point in a headline, then short labelled bullets in plain words. Kept in digests.json by the
// message's key; the page shows it in place of the original, which stays one click away. The agents are told to write
// this way too (SOUL.md, LEAD.md); this covers what they wrote before, and whatever they still get wrong.
type digests struct {
	mu   sync.Mutex
	file string
	M    map[string]string
	busy bool
}

// Limits of the background rewrites.
const (
	digestBatch = 8   // messages per model call
	digestMin   = 280 // shorter ones read fine as they are
)

// newDigests loads the saved readable versions from file (a missing file starts empty).
func newDigests(file string) *digests {
	d := &digests{file: file, M: map[string]string{}}
	if b, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(b, &d.M)
	}
	return d
}

// snapshot returns a copy of the readable versions, by message key.
func (d *digests) snapshot() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := make(map[string]string, len(d.M))
	for k, v := range d.M {
		m[k] = v
	}
	return m
}

// digestDue returns the agent-to-agent messages in these items that have no readable version yet, newest first
func digestDue(its []aiItem, done map[string]string) []stretch {
	var out []stretch
	for i := len(its) - 1; i >= 0; i-- {
		it := its[i]
		if it.Role != "user" || it.K == "" || done[it.K] != "" || !notOwner.MatchString(it.Text) || strings.HasPrefix(it.Text, "[Thread context]") {
			continue
		}
		body := it.Text
		if j := strings.Index(body, "]\n"); j > 0 && j < 160 {
			body = body[j+2:] // its "[From …]" line: the page says who it's from
		}
		if len(body) < digestMin {
			continue
		}
		out = append(out, stretch{it.K, trunc(body, 6000)})
	}
	return out
}

// digestsFill writes the readable versions that are due for every chat: Main, then each thread. One pass at a time.
func (s *server) digestsFill(ctx context.Context) {
	d := s.digests
	d.mu.Lock()
	if d.busy {
		d.mu.Unlock()
		return
	}
	d.busy = true
	done := make(map[string]string, len(d.M))
	for k, v := range d.M {
		done[k] = v
	}
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.busy = false; d.mu.Unlock() }()

	a := s.ai
	a.mu.Lock()
	due := digestDue(a.items, done)
	for _, its := range a.thItems {
		due = append(due, digestDue(its, done)...)
	}
	a.mu.Unlock()

	for len(due) > 0 && ctx.Err() == nil {
		n := min(digestBatch, len(due))
		got, err := digestWrite(ctx, due[:n])
		if err != nil {
			log.Printf("digests: %v", err)
			return
		}
		d.mu.Lock()
		for i, st := range due[:n] {
			if v := strings.TrimSpace(got[fmt.Sprint(i+1)]); v != "" {
				d.M[st.key] = v
			}
		}
		b, _ := json.MarshalIndent(d.M, "", " ")
		_ = os.WriteFile(d.file+".tmp", b, 0o600)
		_ = os.Rename(d.file+".tmp", d.file)
		d.mu.Unlock()
		due = due[n:]
	}
}

// digestPrompt is how a message is made readable (the same rules the agents are given in SOUL.md and LEAD.md)
const digestPrompt = `Below are numbered messages that AI agents sent each other about the owner's software work. The owner reads them in a chat feed and finds them unreadable: dense, full of IDs, hashes, file paths and jargon. Rewrite each one for a busy person scrolling a feed. Keep every fact that matters; drop nothing the owner would need, invent nothing.

Format each as Markdown:
- First line: the point in plain words, in bold, under 15 words (what changed, or what is being asked).
- Then short bullets, one fact each, under bold labels where they help (**Done**, **Still open**, **Next**, **Needs you**, **Asked of the lead**). Usually 3-7 bullets.
- Plain words: name the thing ("the old API keys are deleted"), not its ID or path; keep a PR number only as "PR #81". No timestamps to the second, no commit hashes.
- If the message says what is proven and what is not, say each once.
- Last bullet: whether the owner needs to do anything ("Nothing needed from you" when not).

Reply with JSON only: {"1": "<markdown>", "2": "<markdown>"}.

`

// digestWrite asks a small model through `claude -p` for a readable version of each message: {"1": "…", "2": "…"}.
func digestWrite(ctx context.Context, sts []stretch) (map[string]string, error) {
	var b strings.Builder
	b.WriteString(digestPrompt)
	for i, st := range sts {
		fmt.Fprintf(&b, "%d.\n%s\n\n", i+1, st.text)
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
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

// aiDigests serves GET /api/ai/digests: {message key: its readable version}.
func (s *server) aiDigests(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.digests.snapshot())
}
