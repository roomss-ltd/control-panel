package main

import (
	"net/http"
	"sort"
)

// navInfo serves GET /api/nav: what the sidebar shows under the assistant on any page. That is the threads open
// now (not done, not archived), at most ten, in a fixed order (newest opened first, so working on one never
// moves it), each with what its orb needs.
func (s *server) navInfo(w http.ResponseWriter, r *http.Request) {
	type th struct {
		ID      string  `json:"id"`
		Title   string  `json:"title"`
		Status  string  `json:"status"`
		Live    bool    `json:"live"`
		Created float64 `json:"created"`
	}
	s.ai.mu.Lock()
	v := s.ai.threadViews()
	s.ai.mu.Unlock()
	out := []th{}
	for _, t := range v {
		if t.Status != "archived" && t.Status != "done" {
			out = append(out, th{t.ID, t.Title, t.Status, t.Live, t.Created})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	writeJSON(w, 200, map[string]any{"threads": out[:min(10, len(out))]})
}
