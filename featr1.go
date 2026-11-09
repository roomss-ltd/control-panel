package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Evidence files: where a step's file evidence ("evidence: file|image|doc|ui <ref>") came from, and whether it has
// changed since. Two mechanical facts are recorded about each file:
//   - a seal: its sha256 at first sight, taken only on write paths (the plan writer, the owner's attestation), never on
//     a GET and never replaced;
//   - provenance: the file-writing tool call in this feature's agent transcripts that produced it (or, weaker, a Bash
//     run that merely names it).
// Neither verifies a step, meets an objective or accepts anything; the feature page shows them next to the evidence so
// the owner can judge it. The file also holds the guarded path resolution every evidence ref goes through: inside the
// feature's folders only, regular files only, never secrets or VCS internals.

// maxEvidenceBytes is the largest evidence file the panel will hash (a variable so tests can lower it).
var maxEvidenceBytes int64 = 50 << 20

// featArtifact is a resolved evidence file: its real path, content hash and size. FirstSeen and PlanRev are set on a
// seal: when the file was first hashed and at which plan revision.
type featArtifact struct {
	Path      string `json:"path"`
	Sha256    string `json:"sha256"`
	Size      int64  `json:"size"`
	FirstSeen int64  `json:"firstSeen,omitempty"`
	PlanRev   string `json:"planRev,omitempty"`
}

// featSealFile resolves ref inside one of roots (symlinks followed, still inside), requires a regular file within the
// size limit, and hashes it. why says why it can't be (outside the feature's folders, not a regular file, too large,
// missing).
func featSealFile(roots, secrets []string, ref string) (featArtifact, string) {
	return featResolveFile(roots, secrets, ref, &struct{}{})
}

// featResolve runs featSealFile's checks without reading the file (Path and Size only).
func featResolve(roots, secrets []string, ref string) (featArtifact, string) {
	return featResolveFile(roots, secrets, ref, nil)
}

// featResolveFile implements featSealFile and featResolve: a non-nil hashIt also reads and hashes the file. secrets
// are paths that are refused even inside roots.
func featResolveFile(roots, secrets []string, ref string, hashIt *struct{}) (featArtifact, string) {
	var cands []string
	if filepath.IsAbs(ref) {
		cands = []string{filepath.Clean(ref)}
	} else {
		for _, r := range roots {
			cands = append(cands, filepath.Join(r, ref))
		}
	}
	why := "outside the feature's folders"
	for _, c := range cands {
		real, err := filepath.EvalSymlinks(c)
		if err != nil {
			if os.IsNotExist(err) && featInside(roots, c) {
				why = "missing"
			}
			continue
		}
		if !featInside(roots, real) || featInside(secrets, real) || slices.Contains(strings.Split(real, string(filepath.Separator)), ".git") ||
			featSensitive(filepath.Base(real)) || featSensitive(filepath.Base(c)) { // secret, credential or VCS: refused like outside, never named
			continue
		}
		fi, err := os.Stat(real)
		switch {
		case err != nil:
			why = "missing"
			continue
		case !fi.Mode().IsRegular():
			return featArtifact{}, "not a regular file"
		case fi.Size() > maxEvidenceBytes:
			return featArtifact{}, "too large (> " + itoa64(maxEvidenceBytes>>20) + " MiB)"
		}
		if hashIt == nil {
			return featArtifact{Path: real, Size: fi.Size()}, ""
		}
		f, err := os.Open(real)
		if err != nil {
			return featArtifact{}, "unreadable"
		}
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(f, maxEvidenceBytes+1))
		f.Close()
		if err != nil || n > maxEvidenceBytes {
			return featArtifact{}, "unreadable"
		}
		return featArtifact{Path: real, Sha256: hex.EncodeToString(h.Sum(nil)), Size: n}, ""
	}
	return featArtifact{}, why
}

// featSensitiveRe matches file names that commonly hold credentials or keys.
var featSensitiveRe = regexp.MustCompile(`(?i)^(\.env(\..*)?|.*\.(pem|key|p12|pfx|jks|kdbx|asc|gpg)|id_(rsa|dsa|ecdsa|ed25519)(\.pub)?|\.netrc|\.npmrc|\.pypirc|\.git-credentials|\.htpasswd|credentials(\..*)?|secrets?(\..*)?)$`)

// featSensitive reports whether a file name commonly holds credentials (conservative: refused even inside the
// feature's folders).
func featSensitive(name string) bool { return featSensitiveRe.MatchString(name) }

// featInside reports whether p is one of roots or under one (roots resolved through symlinks too).
func featInside(roots []string, p string) bool {
	for _, r := range roots {
		if r == "" {
			continue
		}
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			r = rr
		}
		if rel, err := filepath.Rel(r, p); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// featProv is the provenance of an evidence file: the tool call in an agent's transcript that wrote it (or, for a
// mention, ran a Bash command naming it).
type featProv struct {
	Tool       string `json:"tool"`
	ToolUseID  string `json:"toolUseId"`
	BytesMatch *bool  `json:"bytesMatch,omitempty"` // a Write: its content's sha256 is the seal's (Edit and others: unknown)
	At         string `json:"at"`
	Transcript string `json:"transcript"`
}

// featWriteTools are Claude Code's file-writing tools: a successful call to one of them on a path is provenance.
var featWriteTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true}

// featProvenance searches the transcripts for calls made after `after` (the step's criterion time). prov is the latest
// successful file-writing tool call whose target (cleaned) is exactly one of paths; sealSha, when given, lets a Write
// say whether the bytes it wrote are the sealed ones. mention is the latest successful Bash run whose command names
// one of the paths: a correlation, never provenance or proof. Either is nil when there is none.
func featProvenance(transcripts, paths []string, after time.Time, sealSha string) (prov, mention *featProv) {
	is := map[string]bool{}
	for _, p := range paths {
		is[filepath.Clean(p)] = true
	}
	names := func(cmd string) bool {
		for p := range is {
			for i := strings.Index(cmd, p); i >= 0; {
				end := i + len(p)
				if end == len(cmd) || strings.ContainsRune(" \t\n'\";|&)>", rune(cmd[end])) { // the whole path, not a longer one
					return true
				}
				j := strings.Index(cmd[end:], p)
				if j < 0 {
					break
				}
				i = end + j
			}
		}
		return false
	}
	for _, tp := range transcripts {
		f, err := os.Open(tp)
		if err != nil {
			continue
		}
		calls := map[string]featProv{}
		rd := bufio.NewReaderSize(f, 1<<20)
		for {
			line, err := rd.ReadString('\n')
			if strings.Contains(line, `"tool_use"`) || len(calls) > 0 && strings.Contains(line, `"tool_use_id"`) {
				var o struct {
					Type, Timestamp string
					Message         struct{ Content json.RawMessage }
					ToolUseResult   struct{ Interrupted bool } `json:"toolUseResult"`
				}
				if json.Unmarshal([]byte(line), &o) == nil {
					var parts []struct {
						Type, ID, Name string
						ToolUseID      string `json:"tool_use_id"`
						IsError        bool   `json:"is_error"`
						Input          struct {
							FilePath     string `json:"file_path"`
							NotebookPath string `json:"notebook_path"`
							Command      string
							Content      *string
						}
					}
					json.Unmarshal(o.Message.Content, &parts)
					for _, x := range parts {
						switch {
						case o.Type == "assistant" && x.Type == "tool_use" && featWriteTools[x.Name] &&
							(x.Input.FilePath != "" && is[filepath.Clean(x.Input.FilePath)] || x.Input.NotebookPath != "" && is[filepath.Clean(x.Input.NotebookPath)]):
							c := featProv{Tool: x.Name, ToolUseID: x.ID, Transcript: tp}
							if x.Name == "Write" && x.Input.Content != nil && sealSha != "" {
								h := sha256.Sum256([]byte(*x.Input.Content))
								m := hex.EncodeToString(h[:]) == sealSha
								c.BytesMatch = &m
							}
							calls[x.ID] = c
						case o.Type == "assistant" && x.Type == "tool_use" && x.Name == "Bash" && names(x.Input.Command):
							calls[x.ID] = featProv{Tool: "Bash", ToolUseID: x.ID, Transcript: tp}
						case o.Type == "user" && x.Type == "tool_result":
							c, ok := calls[x.ToolUseID]
							at, _ := time.Parse(time.RFC3339Nano, o.Timestamp)
							if !ok || x.IsError || o.ToolUseResult.Interrupted || !at.After(after) { // failed, interrupted or too early
								continue
							}
							c.At = at.UTC().Format(time.RFC3339)
							if c.Tool != "Bash" {
								if prov == nil || c.At >= prov.At {
									prov = &c
								}
							} else if mention == nil || c.At >= mention.At {
								mention = &c
							}
						}
					}
				}
			}
			if err != nil {
				break
			}
		}
		f.Close()
	}
	return prov, mention
}

// ---- the seal store, the write paths that seal, the bounded hash cache, the page view ----

// featSealRec is an evidence line's seal: the file as first seen through a write path, never replaced. CriterionAt is
// the step's criterion time (when its words were first seen) used to judge provenance.
type featSealRec struct {
	featArtifact
	StepKey     string `json:"stepKey"`
	CriterionAt int64  `json:"criterionAt,omitempty"`
	SubmittedBy string `json:"submittedBy"` // writer, attest or look
}

// featFileKinds are the evidence kinds that name a file ("log" is what older records wrote for a file).
var featFileKinds = map[string]bool{"file": true, "image": true, "doc": true, "ui": true, "log": true}

// featEvLines returns a step's evidence lines (several, or the single one older records have).
func featEvLines(st featPlanStep) []string {
	if len(st.Evidences) > 0 {
		return st.Evidences
	}
	if st.Evidence != "" {
		return []string{st.Evidence}
	}
	return nil
}

// featEvRef splits an evidence line into kind and ref; ok is true only when the kind names a file.
func featEvRef(line string) (kind, ref string, ok bool) {
	k, r, cut := strings.Cut(line, " ")
	r = strings.TrimSpace(r)
	return k, r, cut && featFileKinds[k] && r != ""
}

// featSealKey is the seal store key for one evidence line of one step (a new line is a new submission, with its own
// seal).
func featSealKey(session, stepKey, line string) string {
	return session + "/" + stepKey + "/" + featRev([]byte(line))
}

// featEvidenceRoots returns the folders evidence files may live in: the record folder, and the feature's main worktree
// when featMainTree accepts the one FEATURE.md names.
func (s *server) featEvidenceRoots(d featDossier) []string {
	roots := []string{d.Dir}
	if d.Main != "" {
		if t, why := s.featMainTree(d.Main); why == "" {
			roots = append(roots, t.Root)
		}
	}
	return roots
}

// featSealSteps seals the file evidence of the feature's steps that isn't sealed yet (first sight wins; a seal is never
// replaced). by records what triggered it: writer (a plan write), attest (the owner's attestation) or look (a
// supervisor pass). Only write paths and the supervisor call it; a GET never does.
func (s *server) featSealSteps(session, by string) {
	d := s.featDossier(session)
	roots, secrets := s.featEvidenceRoots(d), s.secretPaths()
	var keys []string
	for _, m := range d.Milestones {
		for _, st := range m.Steps {
			keys = append(keys, st.Key)
		}
	}
	crit := s.sup.crits(session, keys, time.Now())
	type cand struct {
		k   string
		rec featSealRec
	}
	var add []cand
	for _, m := range d.Milestones {
		for _, st := range m.Steps {
			for _, line := range featEvLines(st) {
				_, ref, ok := featEvRef(line)
				if !ok {
					continue
				}
				k := featSealKey(session, st.Key, line)
				s.sup.mu.Lock()
				_, had := s.sup.Sealed[k]
				s.sup.mu.Unlock()
				if had {
					continue
				}
				a, why := featSealFile(roots, secrets, ref)
				if why != "" {
					continue
				}
				sha, _, at, w := featHashNow(a.Path, true, nil)
				if w != "" {
					continue
				}
				a.Sha256, a.FirstSeen, a.PlanRev = sha, at.Unix(), d.Rev
				add = append(add, cand{k, featSealRec{featArtifact: a, StepKey: st.Key, CriterionAt: s.sup.critAt(session, crit[st.Key]), SubmittedBy: by}})
			}
		}
	}
	if len(add) == 0 {
		return
	}
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	for _, c := range add {
		if _, had := s.sup.Sealed[c.k]; !had {
			s.sup.Sealed[c.k] = c.rec
		}
	}
	s.sup.save()
}

// featHashEnt is one cached hash with the file stat it was taken under.
type featHashEnt struct {
	size, mtime, ctime int64
	ino                uint64
	sha                string
	at                 time.Time
}

// featHashCache: the last hash of each evidence file, in memory only, keyed by its stat (size, mtime, ctime, inode).
// A match is an observation that the file looks unchanged, not a guarantee: decisive checks always hash again.
var featHashCache = struct {
	sync.Mutex
	m map[string]featHashEnt
}{m: map[string]featHashEnt{}}

// featHashNow: path's sha256. decisive: always read the bytes; else a stat-identical cached hash may be reported
// (how "stat-unchanged", at = when it was hashed). budget: bytes this call may still read (nil: no limit), consumed;
// exhausted → how "unchecked", why "budget".
func featHashNow(path string, decisive bool, budget *int64) (sha, how string, at time.Time, why string) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", "unchecked", time.Time{}, "missing"
	}
	var ctime int64
	var ino uint64
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		ctime, ino = st.Ctim.Sec*1e9+st.Ctim.Nsec, st.Ino
	}
	key := featHashEnt{size: fi.Size(), mtime: fi.ModTime().UnixNano(), ctime: ctime, ino: ino}
	featHashCache.Lock()
	ent, ok := featHashCache.m[path]
	featHashCache.Unlock()
	// racy (as git's index): a file changed within 2 s of being hashed may change again inside the same timestamp tick,
	// so its stat can't vouch for it; it's hashed again until it has settled
	settled := ok && max(ent.ctime, ent.mtime) < ent.at.Add(-2*time.Second).UnixNano()
	if !decisive && settled && ent.size == key.size && ent.mtime == key.mtime && ent.ctime == key.ctime && ent.ino == key.ino {
		return ent.sha, "stat-unchanged", ent.at, ""
	}
	if budget != nil && *budget < fi.Size() {
		return "", "unchecked", time.Time{}, "budget"
	}
	if fi.Size() > maxEvidenceBytes {
		return "", "unchecked", time.Time{}, "too large"
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "unchecked", time.Time{}, "unreadable"
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxEvidenceBytes+1))
	f.Close()
	if err != nil || n > maxEvidenceBytes {
		return "", "unchecked", time.Time{}, "unreadable"
	}
	if budget != nil {
		*budget -= n
	}
	key.sha, key.at = hex.EncodeToString(h.Sum(nil)), time.Now()
	featHashCache.Lock()
	if len(featHashCache.m) > 2000 { // crude memory bound: drop everything, files are re-hashed on their next look
		featHashCache.m = map[string]featHashEnt{}
	}
	featHashCache.m[path] = key
	featHashCache.Unlock()
	return key.sha, "hashed", key.at, ""
}

// featR1Items returns, by step key, the evidence lines of each step as the feature page shows them. A line that names
// a file carries either why it was refused (no path is given then), or its resolved path, its seal, the file's current
// hash, whether that differs from the seal (stale), and its provenance or a weaker mention from the agents'
// transcripts. Hashing is bounded to 64 MiB and 2 s per call (cached hashes are used beyond that), and transcripts are
// read up to 64 MiB in total. Reads only: it never seals.
func (s *server) featR1Items(session string, d featDossier) map[string][]any {
	out := map[string][]any{}
	roots, secrets := s.featEvidenceRoots(d), s.secretPaths()
	paths := s.featPaths(session)
	var tb int64 // transcripts are read for provenance up to 64 MiB in total per call
	var trs []string
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && tb+fi.Size() <= 64<<20 {
			tb += fi.Size()
			trs = append(trs, p)
		}
	}
	budget, deadline := int64(64<<20), time.Now().Add(2*time.Second)
	for _, m := range d.Milestones {
		for _, st := range m.Steps {
			for _, line := range featEvLines(st) {
				kind, ref, ok := featEvRef(line)
				if !ok {
					out[st.Key] = append(out[st.Key], map[string]any{"id": featRev([]byte(line))[:12], "line": line})
					continue
				}
				it := map[string]any{"id": featRev([]byte(line))[:12], "kind": kind, "ref": ref}
				out[st.Key] = append(out[st.Key], it)
				a, why := featSealFile(roots, secrets, ref)
				s.sup.mu.Lock()
				seal, sealed := s.sup.Sealed[featSealKey(session, st.Key, line)]
				s.sup.mu.Unlock()
				if sealed {
					it["seal"] = map[string]any{"sha256": seal.Sha256, "size": seal.Size, "firstSeen": seal.FirstSeen, "planRev": seal.PlanRev,
						"stepKey": seal.StepKey, "criterionAt": seal.CriterionAt, "submittedBy": seal.SubmittedBy}
				}
				if why != "" {
					it["refused"] = why
					if sealed {
						it["stale"], it["staleWhy"] = true, "missing since sealed"
					}
					continue
				}
				it["path"] = a.Path
				cur := map[string]any{}
				var sha, how, w string
				var at time.Time
				if time.Now().After(deadline) {
					how, w = "unchecked", "budget"
				} else {
					sha, how, at, w = featHashNow(a.Path, false, &budget)
				}
				cur["how"] = how
				if sha != "" {
					cur["sha256"], cur["checkedAt"] = sha, at.Unix()
				}
				if w != "" {
					cur["why"] = w
				}
				it["current"] = cur
				if sealed && sha != "" && sha != seal.Sha256 {
					it["stale"], it["staleWhy"] = true, "bytes changed since sealed"
				}
				pv, mn := featProvenance(trs, []string{a.Path}, time.Unix(seal.CriterionAt, 0), seal.Sha256)
				if !sealed || seal.CriterionAt == 0 { // no criterion time known: nothing counts as after it
					pv = nil
				}
				switch {
				case pv != nil:
					it["provenance"] = pv
				default:
					it["provenanceNone"] = true
				}
				if mn != nil {
					it["mention"] = mn
				}
			}
		}
	}
	return out
}

// featAttestSeals returns the bytes of a step's file evidence as the owner attests it (path → sha256, read now). A
// file that is missing when attested is recorded as absent, so its appearing later counts as a change.
func (s *server) featAttestSeals(d featDossier, st featPlanStep) map[string]string {
	out := map[string]string{}
	roots, secrets := s.featEvidenceRoots(d), s.secretPaths()
	for _, line := range featEvLines(st) {
		_, ref, ok := featEvRef(line)
		if !ok {
			continue
		}
		a, why := featSealFile(roots, secrets, ref)
		if why == "" {
			if sha, _, _, w := featHashNow(a.Path, true, nil); w == "" {
				out[a.Path] = sha
			}
		} else if why == "missing" { // the owner attested its absence: the file appearing later is a change
			out["missing:"+ref] = "missing"
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// featAttestStale: why an attestation no longer holds for its evidence bytes ("" when they're as attested). Every path
// goes through the same resolution as a seal (roots, secrets, symlinks, regular file, size) before anything is read.
// decisive: hash the bytes now (the done gate); else a stat-identical cached hash is enough (the page).
func featAttestStale(seals map[string]string, decisive bool, roots, secrets []string) string {
	for p, want := range seals {
		if ref, ok := strings.CutPrefix(p, "missing:"); ok {
			if _, why := featResolve(roots, secrets, ref); why != "missing" {
				return "evidence changed since you attested: " + filepath.Base(ref) + " appeared"
			}
			continue
		}
		a, why := featResolve(roots, secrets, p)
		if why == "missing" {
			return "evidence changed since you attested: " + filepath.Base(p) + " is missing"
		}
		if why != "" || a.Path != p {
			return "evidence changed since you attested: " + filepath.Base(p) + " isn't the bytes you saw"
		}
		if sha, _, _, w := featHashNow(p, decisive, nil); w != "" || sha != want {
			return "evidence changed since you attested: " + filepath.Base(p) + " isn't the bytes you saw"
		}
	}
	return ""
}
