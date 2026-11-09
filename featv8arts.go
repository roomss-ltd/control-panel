package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The feature page's artifacts (page.arts): the visual artifacts (UI pages, issue screenshots) that a feature's
// record links to a job, explicitly. The links come from:
//   - a PLAN.md step's "  - artifact: <path> | ui|issue | <title>" lines, which parseMilestones keeps as raw strings;
//   - a LOG entry's art: segment (the logRefs argument of featArts).
// Both become featArtRefs. Nothing is inferred: a file an agent wrote isn't an artifact until a line names it.
// A file is accepted only through the guarded evidence resolution (featResolve over featEvidenceRoots: the record
// folder and the vetted main worktree; no secret path, no credential name, no .git, symlinks resolved and still
// inside, a regular file within the size limit). The preview link is added only when the existing Files preview would
// also serve it, so the preview never widens what's allowed.
// An https URL is shown as written and never fetched; any other scheme is refused. A refused or missing ref
// carries a generic why and no path. Nothing is sealed, hashed or counted as proof.
// On the page's GET it only stats files and reads one small comments file per HTML page: no git, network or exec.

// featArtRef is one artifact line as written, with where it was found and who wrote it.
type featArtRef struct {
	Raw       string // "<path or https URL> | ui|issue | <title>", as written after "artifact:"/"art:"
	Step      string // step key, or ""
	Milestone string // milestone title, or ""
	Source    string // "PLAN.md", "LOG ev-12"
	By        string // the writer's session as the panel observed it, or ""
	ByClaimed string // a session the writer only claimed (X-Agent-Sid with no observed pane), or ""
}

// featArt is one artifact as the page shows it. State is ok (a file the feature may show), link (an https link),
// missing (the file isn't there) or refused (invalid line, or a file or link that must not be shown).
type featArt struct {
	ID        string `json:"id"`
	Step      string `json:"step,omitempty"`
	Milestone string `json:"milestone,omitempty"`
	Purpose   string `json:"purpose"` // the <kind> word as written (ui, issue, …)
	Title     string `json:"title"`
	Kind      string `json:"kind"` // image, html, markdown, pdf, text, binary (the file's), or link
	Path      string `json:"path,omitempty"`
	URL       string `json:"url,omitempty"` // the existing preview, or the https link as written
	At        int64  `json:"at"`            // the file's mtime (0: unknown)
	Size      int64  `json:"size"`
	Comments  int    `json:"comments"`            // in "<page>.comments.json", the preview's review layer
	By        string `json:"by"`                  // observed only
	BySource  string `json:"bySource,omitempty"`  // how it was observed: attribution, never proof
	ByClaimed string `json:"byClaimed,omitempty"` // claimed, not observed: shown as a claim
	Source    string `json:"source"`
	State     string `json:"state"` // ok, link, missing, refused
	Why       string `json:"why,omitempty"`
}

// maxFeatArts caps how many artifacts one page lists.
const maxFeatArts = 100

// featCtlRe matches control characters, which an artifact line may not contain.
var featCtlRe = regexp.MustCompile(`[\x00-\x1f\x7f]+`)

const maxArtTitle = 200 // characters, as the writer and the page's client enforce

// featFileKind classifies a file by extension for the page (image, html, markdown, pdf, text or binary).
func featFileKind(p string) string {
	ext := strings.ToLower(filepath.Ext(p))
	switch {
	case strings.HasPrefix(fileTypes[ext], "image/"):
		return "image"
	case ext == ".html" || ext == ".htm":
		return "html"
	case ext == ".md" || ext == ".markdown":
		return "markdown"
	case ext == ".pdf":
		return "pdf"
	case ext == ".txt" || ext == ".log" || ext == ".json" || ext == ".csv":
		return "text"
	}
	return "binary"
}

// featPlanArtRefs collects each step's "artifact:" lines, which parseMilestones keeps raw in featPlanStep.Artifacts.
// A milestone's own lines (under its heading, before any job) come first, with no step.
func featPlanArtRefs(ms []featMilestone) []featArtRef {
	var out []featArtRef
	for _, m := range ms {
		for _, a := range m.Artifacts {
			out = append(out, featArtRef{Raw: a, Milestone: m.Title, Source: "PLAN.md"})
		}
		for _, st := range m.Steps {
			for _, a := range st.Artifacts {
				out = append(out, featArtRef{Raw: a, Step: st.Key, Milestone: m.Title, Source: "PLAN.md"})
			}
		}
	}
	return out
}

// featArts returns the feature's linked artifacts (PLAN.md artifact: lines, then LOG art: segments), at most 100.
// A file is credited to a team agent only when that agent's transcript shows it writing the file.
func (s *server) featArts(d featDossier, team []featAgent, logRefs []featArtRef) []featArt {
	refs := append(featPlanArtRefs(d.Milestones), logRefs...)
	if len(refs) == 0 {
		return nil
	}
	roots, secrets := s.featEvidenceRoots(d), s.secretPaths()
	written := map[string]string{} // real path -> the team agent whose transcript wrote it
	for _, a := range team {
		if a.path == "" {
			continue
		}
		for _, f := range s.arts.list(a.path, a.Root) {
			if !f.Mentioned && f.Exists {
				if real, err := filepath.EvalSymlinks(f.Path); err == nil && written[real] == "" {
					written[real] = a.Key
				}
			}
		}
	}
	out := []featArt{}
	for _, r := range refs {
		if len(out) == maxFeatArts {
			break
		}
		out = append(out, s.featArt(r, roots, secrets, written))
	}
	return out
}

// featArt validates one artifact line and resolves it to a featArt: a link is checked but never fetched, a file goes
// through the guarded resolution and gets a preview link when the Files preview could serve it.
func (s *server) featArt(r featArtRef, roots, secrets []string, written map[string]string) featArt {
	h := sha256.Sum256([]byte(r.Source + "\x00" + r.Step + "\x00" + r.Milestone + "\x00" + r.Raw))
	a := featArt{ID: "art-" + hex.EncodeToString(h[:6]), Step: r.Step, Milestone: r.Milestone, Source: r.Source, By: r.By, ByClaimed: r.ByClaimed}
	if r.By != "" {
		a.BySource = "observed: the session that wrote the line (attribution, not proof)"
	}
	// an invalid authored line is refused as written, never repaired into a valid-looking artifact
	parts := strings.SplitN(r.Raw, "|", 3)
	ref, purpose, title := strings.TrimSpace(parts[0]), "", ""
	if len(parts) > 1 {
		purpose = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		title = strings.TrimSpace(parts[2])
	}
	switch {
	case ref == "" || featCtlRe.MatchString(r.Raw):
		a.State, a.Why = "refused", "not '<path> | ui|issue | <title>'"
		return a
	case purpose != "" && purpose != "ui" && purpose != "issue":
		a.State, a.Why = "refused", "purpose must be ui or issue"
		return a
	case utf8.RuneCountInString(title) > maxArtTitle:
		a.State, a.Why = "refused", "title longer than 200 characters"
		return a
	}
	a.Purpose, a.Title = purpose, title
	if u, err := url.Parse(ref); err == nil && len(u.Scheme) > 1 { // a URL: shown, never fetched
		a.Kind = "link"
		if u.Scheme == "https" && link(ref) != "" && u.User == nil {
			a.State, a.URL = "link", ref
			if a.Title == "" {
				a.Title = u.Host
			}
		} else {
			a.State, a.Why = "refused", "only https links without credentials are shown"
		}
		return a
	}
	if strings.HasPrefix(ref, "~/") {
		ref = filepath.Join(s.cfg.home, ref[2:])
	}
	f, why := featResolve(roots, secrets, ref)
	if a.Title == "" && why == "" {
		a.Title = filepath.Base(f.Path)
	}
	switch why {
	case "":
	case "missing":
		a.State, a.Why = "missing", "the file isn't there"
		return a
	default:
		a.State, a.Why = "refused", "not a file this feature may show"
		return a
	}
	a.State, a.Path, a.Size, a.Kind = "ok", f.Path, f.Size, featFileKind(f.Path)
	if fi, err := os.Stat(f.Path); err == nil {
		a.At = fi.ModTime().Unix()
	}
	if k := written[f.Path]; k != "" && a.By == "" {
		a.By, a.BySource = k, "observed: its transcript wrote this file (attribution, not proof)"
	}
	if s.featPreviewable(f.Path) { // the existing preview serves it
		a.URL = "/api/fs/raw?path=" + url.QueryEscape(f.Path)
		if a.Kind == "html" {
			a.URL += "&review=1"
			if c, why := featResolve(roots, secrets, f.Path+".comments.json"); why == "" { // the same guard: a planted link isn't read
				a.Comments = featComments(c.Path)
			}
		}
	}
	return a
}

// featPreviewable reports whether the Files preview (/api/fs/raw) would serve p, i.e. p is inside its roots.
func (s *server) featPreviewable(p string) bool {
	if s.fsx == nil || len(s.fsx.roots) == 0 {
		return false
	}
	_, err := fsResolve(s.fsx.roots, p, true)
	return err == nil
}

// featComments counts the review comments in "<page>.comments.json" (0: none or unreadable), reading 2 MB at most.
func featComments(p string) int {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 2<<20))
	if err != nil {
		return 0
	}
	var v struct {
		Comments []json.RawMessage `json:"comments"`
	}
	if json.Unmarshal(b, &v) != nil {
		return 0
	}
	return len(v.Comments)
}
