package main

import "net/http"

// Owner-only routes for the feature page's code-delivery and session-ledger views, registered through v8Register
// (featv8.go). Both serve what's already in memory: nothing is sampled, run or fetched for the request.
func init() { v8Register = append(v8Register, (*server).registerV8B) }

// registerV8B adds the code-delivery and session-ledger routes.
func (s *server) registerV8B(mux *http.ServeMux, owner func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/term/feature/code", owner(s.termFeatureCode))
	mux.HandleFunc("GET /api/term/feature/sessions", owner(s.termFeatureSessions))
}

// termFeatureCode handles GET /api/term/feature/code?session=: the cached code-delivery snapshot (branches, releases,
// per-PR extras and the main worktree) that the feature page gets, with when it was taken and whether a refresh is
// under way.
func (s *server) termFeatureCode(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	if !sessionNameRe.MatchString(session) {
		writeJSON(w, 400, map[string]any{"error": "bad session"})
		return
	}
	snap, refreshing := s.featCodeSnap(session)
	writeJSON(w, 200, map[string]any{"branches": snap.branches, "releases": snap.releases, "prx": snap.prx, "mainWorktree": snap.main, "at": unixOr0(snap.at), "refreshing": refreshing})
}

// termFeatureSessions handles GET /api/term/feature/sessions?session=: the feature's session ledger (which agent
// sessions worked on which steps) and the explicit relations between steps.
func (s *server) termFeatureSessions(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	if !sessionNameRe.MatchString(session) {
		writeJSON(w, 400, map[string]any{"error": "bad session"})
		return
	}
	ss, rs, at := s.featSessionsPage(session, s.featDossier(session))
	writeJSON(w, 200, map[string]any{"sessions": ss, "relations": rs, "observedAt": at})
}
