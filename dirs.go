package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Folders to start a session, tab or pane in: home, each repo and each project; not the repos' worktrees.
// The page adds the folders terminals use right now, a worktree as its repo.

// Dir is one folder offered as a place to start a terminal, with the group the page lists it under.
type Dir struct {
	Path  string `json:"path"`
	Group string `json:"group"`
}

// subdirs returns the visible (not dot-prefixed) folders directly inside dir.
func subdirs(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// listDirs lists home, each repo in home/repos, and each folder in home/projects and home/side-quests.
func listDirs(home string) []Dir {
	out := []Dir{{home, "Home"}}
	repos := subdirs(filepath.Join(home, "repos"))
	for _, r := range repos {
		out = append(out, Dir{r, "Repos"})
	}
	for _, base := range []string{"projects", "side-quests"} {
		for _, p := range subdirs(filepath.Join(home, base)) {
			out = append(out, Dir{p, map[string]string{"projects": "Projects", "side-quests": "Side quests"}[base]})
		}
	}
	return out
}

// dirs serves GET /api/dirs: the folders to start a session in, with home shown as "~".
func (s *server) dirs(w http.ResponseWriter, r *http.Request) {
	ds := listDirs(s.cfg.home)
	for i := range ds {
		ds[i].Path = tilde(ds[i].Path)
	}
	writeJSON(w, 200, map[string]any{"dirs": ds})
}
