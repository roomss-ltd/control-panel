package main

import (
	"net/http"
	"os"
	"sync"
)

// GET /api/term/features: what the Terminals page shows of each zellij session as a feature, beyond the live
// stream: who runs it (the JARVIS thread whose team it is, or nobody: the owner started it), that thread's status,
// note, branch and agreed plan, and for each agent in it the to-do list it is working through (its own words, from
// its transcript: what it's on now and what's left).

// featView is what the Terminals page shows for one session: its leading thread, if any, and its agents' to-do lists.
type featView struct {
	Thread *featThread            `json:"thread,omitempty"`
	Agents map[string]featTodoSet `json:"agents"` // by session:pane
}

// featThread is the part of a thread's record the Terminals page shows for the session it leads.
type featThread struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Status string     `json:"status"`
	Note   string     `json:"note,omitempty"`
	Branch string     `json:"branch,omitempty"`
	Plan   []planStep `json:"plan,omitempty"`
	Live   bool       `json:"live"`
}

// featTodoSet is an agent's newest to-do list and when it was written.
type featTodoSet struct {
	Todos []teamTodo `json:"todos"`
	T     int64      `json:"t"`
}

// todoCache keeps each transcript's newest to-do list, re-reading a transcript only when the file has grown.
type todoCache struct {
	mu sync.Mutex
	m  map[string]todoSeen
}

// todoSeen is a cached to-do list and the transcript size it was read at.
type todoSeen struct {
	size int64
	set  featTodoSet
}

// todos is the process-wide to-do list cache.
var todos = &todoCache{m: map[string]todoSeen{}}

// of returns the newest to-do list in the transcript at path.
func (c *todoCache) of(path string) featTodoSet {
	fi, err := os.Stat(path)
	if err != nil {
		return featTodoSet{}
	}
	c.mu.Lock()
	seen, ok := c.m[path]
	c.mu.Unlock()
	if ok && seen.size == fi.Size() {
		return seen.set
	}
	ts, t := lastTodos(path)
	set := featTodoSet{ts, t}
	c.mu.Lock()
	c.m[path] = todoSeen{fi.Size(), set}
	c.mu.Unlock()
	return set
}

// termFeatures handles GET /api/term/features (see the comment at the top of this file).
func (s *server) termFeatures(w http.ResponseWriter, r *http.Request) {
	s.ai.mu.Lock()
	byTeam := map[string]*featThread{}
	for _, t := range s.ai.threadViews() { // newest wins a session two threads name
		if t.Team == "" || t.Status == "archived" {
			continue
		}
		if o := byTeam[t.Team]; o != nil && o.ID != "" {
			continue
		}
		byTeam[t.Team] = &featThread{t.ID, t.Title, t.Status, t.Note, t.Branch, t.Plan, t.Live}
	}
	s.ai.mu.Unlock()
	out := map[string]featView{}
	for _, se := range s.term.listing().Sessions {
		v := featView{Thread: byTeam[se.Name], Agents: map[string]featTodoSet{}}
		for _, tb := range se.Tabs {
			for _, p := range tb.Panes {
				full, ok := s.term.pane(se.Name, p.ID)
				if !ok || full.Agent == nil || full.Agent.path == "" {
					continue
				}
				if set := todos.of(full.Agent.path); len(set.Todos) > 0 {
					v.Agents[se.Name+":"+itoa(p.ID)] = set
				}
			}
		}
		out[se.Name] = v
	}
	writeJSON(w, 200, map[string]any{"sessions": out})
}
