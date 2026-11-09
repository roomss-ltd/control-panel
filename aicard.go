package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// A ```card block in a JARVIS reply: one of vm-agent's message-ui cards (attachments/cards/*.tsx) drawn live in
// the chat as HTML, the same React component Telegram gets as a PNG. vm-agent's render.tsx renders it.

// cardNameRe is what a card's name may look like; it becomes a file name under the card directory.
var cardNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

// cardCache keeps rendered card HTML by a hash of the card, its file's mtime and its props.
type cardCache struct {
	mu sync.Mutex
	m  map[[32]byte][]byte
}

// cards is the process-wide cache of rendered cards.
var cards cardCache

// aiCard (POST /api/ai/card {card, props}) renders a card with the vm-agent renderer and returns its HTML.
func (s *server) aiCard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Card  string          `json:"card"`
		Props json.RawMessage `json:"props"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || !cardNameRe.MatchString(req.Card) {
		writeJSON(w, 400, map[string]string{"error": "a card is {\"card\": \"<name>\", \"props\": {…}}"})
		return
	}
	dir := filepath.Join(s.cfg.home, "projects/vm-agent/attachments")
	fi, err := os.Stat(filepath.Join(dir, "cards", req.Card+".tsx"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "no card " + req.Card})
		return
	}
	props := req.Props
	if len(bytes.TrimSpace(props)) == 0 {
		props = json.RawMessage("{}")
	}
	if json.Unmarshal(props, new(map[string]any)) != nil {
		writeJSON(w, 400, map[string]string{"error": "props must be a JSON object"})
		return
	}
	key := sha256.Sum256([]byte(req.Card + "\x00" + fi.ModTime().String() + "\x00" + string(props)))
	cards.mu.Lock()
	html, ok := cards.m[key]
	cards.mu.Unlock()
	if !ok {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(dir, "node_modules/.bin/tsx"), "render.tsx", req.Card, "--html", "--props", string(props))
		cmd.Dir = dir
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			writeJSON(w, 502, map[string]string{"error": trunc(cmp.Or(errb.String(), err.Error()), 300)})
			return
		}
		html = out.Bytes()
		cards.mu.Lock()
		if cards.m == nil || len(cards.m) > 200 {
			cards.m = map[[32]byte][]byte{}
		}
		cards.m[key] = html
		cards.mu.Unlock()
	}
	writeJSON(w, 200, map[string]string{"html": string(html)})
}
