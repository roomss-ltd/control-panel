package main

import (
	"cmp"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Code delivery for the feature page: where a feature's code lives and what is actually deployed. It builds:
//   - branches: each worktree's branch, with its parent, source commit and commits;
//   - per repo: what production and stage actually run, what's being prepared, and the feature branch;
//   - per PR: who linked it.
// Everything is read-only:
//   - local git with --no-optional-locks: never fetch, checkout or write;
//   - GitHub's deployments through the existing shipQuery/parseShip and gh.
// Every fact says where it came from, when, and whether it's fresh, stale (last known) or unknown.
// "Deployed" is only a deployment GitHub records as gone live, never a branch HEAD or "merged to main".
// An agent is named only where there's evidence: its pr-link, or its checkout on the PR's head. The GitHub
// author is usually one shared login, so it is never mapped to an agent.
// featCodePage never touches the network or git: it serves the last snapshot, and a stale one starts one
// background refresh.

// featFact says where a value came from and how current it is. It is embedded in the branch, deploy and
// preparing records so the page can show "last known" values honestly.
type featFact struct {
	Source    string `json:"source"`
	FetchedAt int64  `json:"fetchedAt"` // unix s of the read the values come from; 0 = never
	State     string `json:"state"`     // fresh, stale (last known), unknown
	Why       string `json:"why,omitempty"`
}

// featCodeCommit is one commit on a branch's own (first-parent) line. Kind marks merges and unpushed commits.
type featCodeCommit struct {
	At   int64  `json:"at"`
	Sha  string `json:"sha"`
	Msg  string `json:"msg"`
	Kind string `json:"kind"` // merge, unpushed, ""
}

// featCodeAgent is a team agent working in a branch's worktree; Source says how that was learned.
type featCodeAgent struct {
	Key    string `json:"key"`
	Tab    string `json:"tab"`
	Source string `json:"source"`
}

// featCodeBranch is one row of the page's branch tree: a branch with its parent, the commit it was cut from,
// ahead/behind counts against the parent, its worktree, its agents, its PR and its own commits. Pointer fields are
// nil when git could not tell, so "unknown" is never shown as zero.
type featCodeBranch struct {
	ID           string           `json:"id"` // repo:branch
	Repo         string           `json:"repo"`
	Name         string           `json:"name"`
	Parent       *string          `json:"parent"`
	ParentSource string           `json:"parentSource"`
	Src          string           `json:"src"`       // the commit it was checked out from
	SrcMethod    string           `json:"srcMethod"` // reflog (recorded), merge-base (computed), ""
	From         *int64           `json:"from"`      // created: reflog only
	To           *int64           `json:"to"`        // its PR merged or closed
	Wt           string           `json:"wt"`
	WtSource     string           `json:"wtSource"` // live sample (pane cwd), or FEATURE.md's Main worktree: line
	Main         bool             `json:"main,omitempty"`
	Agents       []featCodeAgent  `json:"agents"`
	By           string           `json:"by"`
	ByWhy        string           `json:"byWhy,omitempty"`
	Head         string           `json:"head"`
	Upstream     string           `json:"upstream"`
	Ahead        *int             `json:"ahead"`
	Behind       *int             `json:"behind"`
	Unpushed     *int             `json:"unpushed"`
	PR           int              `json:"pr"`
	Into         *string          `json:"into"`
	IntoState    *string          `json:"intoState"` // merged, closed (not merged), open, draft
	Commits      []featCodeCommit `json:"commits"`
	featFact
}

// featCodeSvc is the commit one service (one GitHub deployment environment) of a tier runs, when a tier has several.
type featCodeSvc struct {
	Env string `json:"env"`
	Sha string `json:"sha"`
	At  int64  `json:"at"`
}

// featCodeAttempt is a deployment newer than the live one that did not go live (still running, or failed).
type featCodeAttempt struct {
	State string `json:"state"` // run, fail
	At    int64  `json:"at"`
	Sha   string `json:"sha"`
}

// featCodeDeploy is what one tier (production or stage) of a repo runs, according to GitHub deployments. Unknown is
// set when no live deployment could be found; the PRs lists say which of the feature's merged PRs the commit contains.
type featCodeDeploy struct {
	Version    string           `json:"version"` // a local tag pointing at sha, else ""
	Sha        string           `json:"sha"`
	At         int64            `json:"at"`
	Env        string           `json:"env"`
	URL        string           `json:"url,omitempty"`
	Services   []featCodeSvc    `json:"services,omitempty"`
	Attempt    *featCodeAttempt `json:"attempt"` // a newer deploy that didn't go live
	Unknown    bool             `json:"unknown"`
	PRs        []int            `json:"prs,omitempty"` // stage: the feature's merged PRs it contains (local ancestry)
	PRsNot     []int            `json:"prsNot,omitempty"`
	PRsUnknown []int            `json:"prsUnknown,omitempty"`
	featFact
}

// featCodePrep is the release being prepared: an open stage-to-production PR this feature linked.
type featCodePrep struct {
	Version string `json:"version"`
	PR      int    `json:"pr"`
	Note    string `json:"note"`
	featFact
}

// featCodeHead answers "is the feature branch's HEAD what stage or production runs?" for one repo.
type featCodeHead struct {
	Name       string `json:"name"`
	Head       string `json:"head"`
	Deployed   *bool  `json:"deployed"`
	DeployedIn string `json:"deployedIn"`
	Note       string `json:"note"`
	State      string `json:"state"`
}

// featCodeRelease is one repo's release picture: production, stage, the release being prepared, and the feature branch.
// Primary marks the repo of the feature's main worktree.
type featCodeRelease struct {
	Repo      string         `json:"repo"`
	Primary   bool           `json:"primary"`
	Why       string         `json:"why,omitempty"`
	Prod      featCodeDeploy `json:"prod"`
	Stage     featCodeDeploy `json:"stage"`
	Preparing featCodePrep   `json:"preparing"`
	Branch    featCodeHead   `json:"branch"`
}

// featCodePRX is the extra information the page shows per PR: its link and which agents are known to have made it.
type featCodePRX struct {
	URL           string   `json:"url"`
	Sessions      []string `json:"sessions"`
	SessionSource string   `json:"sessionSource"`
	SessionWhy    string   `json:"sessionWhy,omitempty"`
	CreatedAt     int64    `json:"createdAt,omitempty"`
}

// Source labels shown beside facts, and the cache lifetimes. A code snapshot is refreshed after codeTTL; a repo's
// GitHub deployments after shipTTL (codeTTL while one is running), retried after shipRetry when the last fetch failed,
// and shown as stale once the last good answer is older than shipFresh.
const (
	srcGit      = "git (local, read-only)"
	srcDeploy   = "GitHub Deployments (gh api graphql)"
	srcLive     = "live sample (pane cwd)"
	srcMainLine = "FEATURE.md Main worktree: line"
	codeTTL     = 30 * time.Second
	shipTTL     = 3 * time.Minute
	shipRetry   = time.Minute
	shipFresh   = 10 * time.Minute // older than this the last good answer is stale, even with no error
)

// repoSlugRe matches an "owner/name" GitHub repository slug.
var repoSlugRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// repoSlug returns owner/name for a GitHub origin URL (https or ssh), and "" for anything else.
func repoSlug(url string) string {
	u := strings.TrimSuffix(strings.TrimSpace(url), ".git")
	for _, p := range []string{"https://github.com/", "git@github.com:", "ssh://git@github.com/"} {
		if s, ok := strings.CutPrefix(u, p); ok && repoSlugRe.MatchString(s) {
			return s
		}
	}
	return ""
}

// gitFn runs one git command in dir and returns its trimmed output; tests swap it for a fake.
type gitFn func(dir string, args ...string) (string, error)

// gitRO runs one read-only git call (no index refresh, no prompts), 5 s at most.
func gitRO(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", dir}, args...)...)
	cmd.Env = append(cleanEnv(os.Environ()), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// shipState is one repo's GitHub deployments, with when they were really fetched and the last error.
type shipState struct {
	si      *shipInfo
	okAt    time.Time
	triedAt time.Time
	err     string
}

// codeIn is everything one snapshot build reads: the feature's worktrees and agents, its PRs, the GitHub deployments
// per repo, and the git runner. Keeping it a plain value lets tests build snapshots without a real panel.
type codeIn struct {
	session string // the feature: its integration branch is feature/<session>
	trees   []featTree
	team    []featAgent
	refs    map[string][]prRef // agent key -> its pr-link refs
	prs     []*PRStatus
	ships   map[string]shipState
	git     gitFn
	now     time.Time
}

// codeTree is a worktree's git facts, read once per build.
type codeTree struct {
	t        featTree
	repo     string // owner/name, or the repository's directory name
	head     string
	upstream string
	ref      string // refs/heads/<branch> for a branch read from its ref (no agent in a worktree of it); "" = the checkout's HEAD
	wt       string // a ref row: the worktree that has it out, if any
}

// refp returns a pointer to a copy of v, for the optional (nullable) JSON fields.
func refp[T any](v T) *T { return &v }

// short abbreviates a commit hash to 7 characters.
func short(sha string) string { return sha[:min(7, len(sha))] }

// repoOf names the repo a worktree belongs to: the GitHub slug of its origin, else the repository's directory name.
func (in *codeIn) repoOf(root string) string {
	if u, err := in.git(root, "remote", "get-url", "origin"); err == nil {
		if s := repoSlug(u); s != "" {
			return s
		}
	}
	if d, err := in.git(root, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil && d != "" {
		return filepath.Base(filepath.Dir(d))
	}
	return filepath.Base(root)
}

// prFor returns the PR whose head is repo's branch: an open one if any, else the newest.
func prFor(prs []*PRStatus, repo, branch string) *PRStatus {
	var best *PRStatus
	for _, p := range prs {
		if p.Repo != repo || p.Head != branch {
			continue
		}
		if best == nil || (p.State == "OPEN") != (best.State == "OPEN") && p.State == "OPEN" || (p.State == "OPEN") == (best.State == "OPEN") && p.Number > best.Number {
			best = p
		}
	}
	return best
}

// intoState maps a PR's GitHub state to the page's merged, closed, draft or open.
func intoState(p *PRStatus) string {
	switch {
	case p.State == "MERGED":
		return "merged"
	case p.State == "CLOSED":
		return "closed"
	case p.Draft:
		return "draft"
	}
	return "open"
}

// branch builds one branch row from a worktree (or a bare ref): its parent and source commit, ahead/behind counts,
// unpushed commits, own commits and PR link. When git cannot read HEAD the row is returned as "unknown".
func (in *codeIn) branch(ct codeTree, mainOf map[string]codeTree) featCodeBranch {
	g, root, name, tip := in.git, ct.t.Root, ct.t.Branch, cmp.Or(ct.ref, "HEAD")
	b := featCodeBranch{ID: ct.repo + ":" + name, Repo: ct.repo, Name: name, Wt: root, WtSource: srcLive, Main: ct.t.Main, Head: ct.head, Upstream: ct.upstream,
		Agents: []featCodeAgent{}, Commits: []featCodeCommit{}, ByWhy: "no worktree-creation record",
		featFact: featFact{Source: srcGit, FetchedAt: in.now.Unix(), State: "fresh"}}
	switch {
	case ct.ref != "":
		b.Wt, b.WtSource, b.Main = ct.wt, "git ref (no agent works in a worktree of it)", false
	case len(ct.t.Agents) == 0:
		b.WtSource = srcMainLine
	}
	for _, a := range in.team {
		if a.Root == root && a.Branch == name {
			b.Agents = append(b.Agents, featCodeAgent{a.Key, a.Tab, srcLive})
		}
	}
	if ct.head == "" {
		b.State, b.Why = "unknown", "git could not read HEAD"
		return b
	}
	// parent: what the reflog recorded at creation, else the PR's base, else the feature's main worktree, else origin's default
	parentRef, parentName := "", ""
	if out, err := g(root, "reflog", "show", "--date=unix", "--format=%H%x09%gd%x09%gs", "refs/heads/"+name, "--"); err == nil && out != "" {
		lines := strings.Split(out, "\n")
		f := strings.SplitN(lines[len(lines)-1], "\t", 3)
		if len(f) == 3 {
			if from, ok := strings.CutPrefix(f[2], "branch: Created from "); ok {
				b.Src, b.SrcMethod = f[0], "reflog"
				if i, j := strings.LastIndex(f[1], "{"), strings.LastIndex(f[1], "}"); i >= 0 && j > i {
					if n, err := strconv.ParseInt(f[1][i+1:j], 10, 64); err == nil {
						b.From = &n
					}
				}
				if from != "HEAD" && !strings.HasPrefix(from, "refs/tags/") {
					parentRef = from
					parentName = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(from, "refs/heads/"), "refs/remotes/"), "origin/")
					b.ParentSource = "reflog: branch created from " + from
				}
			}
		}
	}
	pr := prFor(in.prs, ct.repo, name)
	if parentName == "" && pr != nil {
		parentName, parentRef, b.ParentSource = pr.Base, pr.Base, "pr base"
	}
	if m, ok := mainOf[ct.repo]; parentName == "" && ct.ref == "" && ok && m.t.Root != root && m.t.Branch != "" {
		parentName, parentRef, b.ParentSource = m.t.Branch, m.t.Branch, "feature main worktree"
	}
	if parentName == "" {
		if d, err := g(root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && d != "" && d != "origin/"+name {
			parentName, parentRef, b.ParentSource = strings.TrimPrefix(d, "origin/"), d, "origin default branch"
		}
	}
	if parentName != "" && parentName != name {
		b.Parent = refp(ct.repo + ":" + parentName)
		if b.Src == "" {
			if mb, err := g(root, "merge-base", parentRef, tip); err == nil && mb != "" {
				b.Src, b.SrcMethod = mb, "merge-base"
			}
		}
		if out, err := g(root, "rev-list", "--left-right", "--count", parentRef+"..."+tip); err == nil {
			if f := strings.Fields(out); len(f) == 2 {
				be, e1 := strconv.Atoi(f[0])
				ah, e2 := strconv.Atoi(f[1])
				if e1 == nil && e2 == nil {
					b.Behind, b.Ahead = &be, &ah
				}
			}
		}
	}
	unpushed := map[string]bool{}
	if ct.upstream != "" {
		if out, err := g(root, "rev-list", "-n", "500", ct.upstream+".."+tip); err == nil {
			n := 0
			for _, h := range strings.Fields(out) {
				unpushed[h] = true
				n++
			}
			b.Unpushed = &n
		}
	} else {
		b.Why = "no upstream: push state unknown"
	}
	rng := tip
	if b.Src != "" {
		rng = b.Src + ".." + tip
	}
	// its own line only (first parent): a merge commit stands for what it brought in, so commits from branches merged
	// into this one never show as its own
	if out, err := g(root, "log", "--first-parent", "-n", "50", "--format=%H%x09%ct%x09%P%x09%s", rng, "--"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			f := strings.SplitN(l, "\t", 4)
			if len(f) < 4 {
				continue
			}
			at, _ := strconv.ParseInt(f[1], 10, 64)
			c := featCodeCommit{At: at, Sha: f[0], Msg: trunc(f[3], 200)}
			switch {
			case unpushed[f[0]]:
				c.Kind = "unpushed"
			case len(strings.Fields(f[2])) > 1:
				c.Kind = "merge"
			}
			b.Commits = append(b.Commits, c)
		}
	} else {
		b.State, b.Why = "stale", "git log failed: "+err.Error()
	}
	if pr != nil {
		in.linkPR(&b, pr)
	}
	return b
}

// linkPR fills a branch row's PR fields: the number, the branch it goes into and its state, and when it ended.
func (in *codeIn) linkPR(b *featCodeBranch, p *PRStatus) {
	b.PR = p.Number
	b.Into, b.IntoState = refp(p.Repo+":"+p.Base), refp(intoState(p))
	switch {
	case p.State == "MERGED" && !p.MergedAt.IsZero():
		b.To = refp(p.MergedAt.Unix())
	case p.State == "CLOSED" && !p.ClosedAt.IsZero():
		b.To = refp(p.ClosedAt.Unix())
	}
}

// deploy works out what a tier (production or stage) of repo runs from its GitHub deployments: the newest one that
// went live in each environment of the tier, plus a newer attempt that did not. local, if set, is a checkout used to
// find a version tag on that commit.
func (in *codeIn) deploy(repo, tier string, ss shipState, ok bool, local string) featCodeDeploy {
	d := featCodeDeploy{featFact: featFact{Source: srcDeploy}}
	if !repoSlugRe.MatchString(repo) {
		d.Unknown, d.State, d.Why = true, "unknown", "not a GitHub repository: no deploy source"
		return d
	}
	if !ok || ss.si == nil {
		d.Unknown, d.State, d.Why = true, "unknown", cmp.Or(ss.err, "not fetched yet")
		if ss.err != "" {
			d.Why = "gh failed, never fetched: " + ss.err
		}
		return d
	}
	d.FetchedAt, d.State = ss.okAt.Unix(), "fresh"
	switch {
	case ss.err != "" && ss.triedAt.After(ss.okAt):
		d.State, d.Why = "stale", "last fetch failed: "+ss.err
	case in.now.Sub(ss.okAt) > shipFresh:
		d.State, d.Why = "stale", "not refreshed since"
	}
	if len(ss.si.deploys) == 0 {
		d.Unknown, d.State, d.Why = true, "unknown", "no GitHub deployments recorded"
		return d
	}
	envs := map[string]bool{}
	for _, x := range ss.si.deploys {
		if envTier(x.env) == tier {
			envs[x.env] = true
		}
	}
	for _, env := range slices.Sorted(func(yield func(string) bool) {
		for e := range envs {
			if !yield(e) {
				return
			}
		}
	}) {
		var live *ghDeploy
		var after *ghDeploy
		for i := len(ss.si.deploys) - 1; i >= 0; i-- { // newest first
			x := &ss.si.deploys[i]
			if x.env != env {
				continue
			}
			if x.state == "ok" {
				live = x
				break
			}
			if after == nil && (x.state == "run" || x.state == "fail") {
				after = x
			}
		}
		if live == nil {
			if after != nil && d.Attempt == nil {
				d.Attempt = &featCodeAttempt{after.state, unixOr0(cmp.Or(after.done, after.at)), after.sha}
			}
			continue
		}
		d.Services = append(d.Services, featCodeSvc{env, live.sha, live.done.Unix()})
		if d.Sha == "" || live.done.Unix() > d.At {
			d.Sha, d.At, d.Env, d.URL = live.sha, live.done.Unix(), env, live.url
			d.Attempt = nil
			if after != nil {
				d.Attempt = &featCodeAttempt{after.state, unixOr0(cmp.Or(after.done, after.at)), after.sha}
			}
		}
	}
	if len(d.Services) < 2 {
		d.Services = nil
	}
	if d.Sha == "" {
		d.Unknown, d.State = true, "unknown"
		d.Why = "no " + tier + " deployment that went live among the repo's last 100 GitHub deployments"
		return d
	}
	if local != "" {
		if tags, err := in.git(local, "tag", "--points-at", d.Sha); err == nil && tags != "" {
			d.Version = strings.Fields(tags)[0]
		}
	}
	return d
}

// unixOr0 returns t as unix seconds, or 0 for the zero time.
func unixOr0(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// contains reports whether commit c is in the history of tip, by local git; nil when either commit isn't in dir.
func (in *codeIn) contains(dir, c, tip string) *bool {
	if dir == "" || c == "" || tip == "" {
		return nil
	}
	if c == tip {
		return refp(true)
	}
	for _, x := range []string{c, tip} {
		if _, err := in.git(dir, "cat-file", "-e", x+"^{commit}"); err != nil {
			return nil
		}
	}
	_, err := in.git(dir, "merge-base", "--is-ancestor", c, tip)
	var ee *exec.ExitError
	switch {
	case err == nil:
		return refp(true)
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return refp(false)
	}
	return nil
}

// build turns the inputs into the page's three answers: branch rows, one release per repo, and per-PR extras.
func (in *codeIn) build() ([]featCodeBranch, []featCodeRelease, map[string]featCodePRX) {
	var cts []codeTree
	mainOf := map[string]codeTree{} // repo -> the feature's worktree on it (the main one when it's that repo)
	for _, t := range in.trees {
		if len(cts) == 40 { // a team rarely has more worktrees than this; the rest aren't read
			break
		}
		ct := codeTree{t: t, repo: in.repoOf(t.Root)}
		ct.head, _ = in.git(t.Root, "rev-parse", "HEAD")
		ct.upstream, _ = in.git(t.Root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
		cts = append(cts, ct)
		if m, ok := mainOf[ct.repo]; !ok || t.Main && !m.t.Main || !m.t.Main && len(t.Agents) > len(m.t.Agents) {
			mainOf[ct.repo] = ct
		}
	}
	branches := []featCodeBranch{}
	have := map[string]bool{}
	for _, ct := range cts {
		if ct.t.Branch == "" {
			continue
		}
		b := in.branch(ct, mainOf)
		if have[b.ID] {
			continue
		}
		have[b.ID] = true
		branches = append(branches, b)
	}
	// the feature's integration branch, and every parent a row names, as rows of their own even with no agent in a
	// worktree of them (read from the ref): the team's branches hang under them and their merges show on them
	for _, ct := range in.refRows(branches, mainOf) {
		b := in.branch(ct, mainOf)
		if !have[b.ID] {
			have[b.ID] = true
			branches = append(branches, b)
		}
	}
	for _, p := range in.prs { // a PR whose branch has no live worktree (closed, merged and removed, or someone else's checkout)
		id := p.Repo + ":" + p.Head
		if p.Repo == "" || p.Head == "" || have[id] || branchTier(p.Head) != "" {
			continue
		}
		have[id] = true
		b := featCodeBranch{ID: id, Repo: p.Repo, Name: p.Head, Parent: refp(p.Repo + ":" + p.Base), ParentSource: "pr base",
			Agents: []featCodeAgent{}, Commits: []featCodeCommit{}, ByWhy: "no worktree-creation record",
			featFact: featFact{Source: "GitHub PR (gh)", State: "unknown", Why: "no live worktree found: commits and source commit not read"}}
		in.linkPR(&b, p)
		branches = append(branches, b)
	}

	// repos: the main worktree's first, then the other worktrees', then the PRs'
	var repos []string
	for _, ct := range cts {
		if ct.t.Main && !slices.Contains(repos, ct.repo) {
			repos = append(repos, ct.repo)
		}
	}
	for _, ct := range cts {
		if !slices.Contains(repos, ct.repo) {
			repos = append(repos, ct.repo)
		}
	}
	for _, p := range in.prs {
		if p.Repo != "" && !slices.Contains(repos, p.Repo) {
			repos = append(repos, p.Repo)
		}
	}
	releases := []featCodeRelease{}
	for i, repo := range repos {
		m, local := mainOf[repo]
		dir := ""
		if local {
			dir = m.t.Root
		}
		ss, ok := in.ships[repo]
		r := featCodeRelease{Repo: repo, Primary: i == 0 && local && m.t.Main}
		r.Prod = in.deploy(repo, "production", ss, ok, dir)
		r.Stage = in.deploy(repo, "stage", ss, ok, dir)
		if r.Prod.Unknown && r.Stage.Unknown && ok && ss.si != nil && len(ss.si.deploys) == 0 {
			r.Why = "no GitHub deployments recorded for this repo"
		}
		if r.Stage.Sha != "" {
			for _, p := range in.prs {
				if p.Repo != repo || p.State != "MERGED" {
					continue
				}
				switch c := in.contains(dir, p.merge, r.Stage.Sha); {
				case c == nil:
					r.Stage.PRsUnknown = append(r.Stage.PRsUnknown, p.Number)
				case *c:
					r.Stage.PRs = append(r.Stage.PRs, p.Number)
				default:
					r.Stage.PRsNot = append(r.Stage.PRsNot, p.Number)
				}
			}
		}
		// preparing: only a stage -> production PR this feature linked itself
		r.Preparing = featCodePrep{featFact: featFact{State: "unknown", Why: "no release PR linked to this feature"}}
		for _, p := range in.prs {
			if p.Repo == repo && p.State == "OPEN" && branchTier(p.Base) == "production" && branchTier(p.Head) == "stage" {
				r.Preparing = featCodePrep{PR: p.Number, Note: "release PR #" + strconv.Itoa(p.Number) + " open: " + p.Title,
					featFact: featFact{Source: "GitHub PR linked by this feature (gh)", State: "fresh", Why: "version unknown: no release tag"}}
				break
			}
		}
		// the feature branch: is its HEAD what stage or production runs?
		if local {
			h := featCodeHead{Name: m.t.Branch, Head: m.head, State: "fresh"} // the branch, read from git now; whether it's deployed is Deployed
			if h.Head == "" {
				h.State = "unknown"
			}
			for _, t := range []struct {
				tier string
				d    featCodeDeploy
			}{{"production", r.Prod}, {"stage", r.Stage}} {
				if t.d.Sha == "" || h.Head == "" {
					continue
				}
				c := in.contains(dir, h.Head, t.d.Sha)
				if c == nil {
					if h.Note == "" {
						h.Note = "can't tell: " + t.tier + " runs " + short(t.d.Sha) + ", not in this checkout"
					}
					continue
				}
				if *c {
					h.Deployed, h.DeployedIn, h.Note = refp(true), t.tier, "HEAD "+short(h.Head)+" is in "+t.tier+" ("+short(t.d.Sha)+")"
				} else if h.Deployed == nil || !*h.Deployed {
					h.Deployed, h.Note = refp(false), "HEAD "+short(h.Head)+" is not deployed: "+t.tier+" runs "+short(t.d.Sha)
				}
			}
			if h.Note == "" {
				h.Note = "deployed version unknown"
			}
			r.Branch = h
		} else {
			r.Branch = featCodeHead{State: "unknown", Note: "no worktree of this repo in the feature"}
		}
		releases = append(releases, r)
	}
	return branches, releases, in.prExtras()
}

// refRows returns the local branches that should have a row although no agent works in them: feature/<session> in each repo, and the
// parents existing rows name (one level). Each is read from its ref through a checkout of the same repo; the worktree
// that has it out, if any, is named.
func (in *codeIn) refRows(rows []featCodeBranch, mainOf map[string]codeTree) []codeTree {
	want := map[string][]string{} // repo -> branch names
	for repo := range mainOf {
		if in.session != "" {
			want[repo] = append(want[repo], "feature/"+in.session)
		}
	}
	for _, b := range rows {
		if b.Parent != nil {
			if repo, name, ok := strings.Cut(*b.Parent, ":"); ok {
				want[repo] = append(want[repo], name)
			}
		}
	}
	var out []codeTree
	seen := map[string]bool{}
	for _, b := range rows {
		seen[b.ID] = true
	}
	for repo, names := range want {
		m, ok := mainOf[repo]
		if !ok {
			continue
		}
		wts := map[string]string{} // branch -> the worktree that has it out
		if out, err := in.git(m.t.Root, "worktree", "list", "--porcelain"); err == nil {
			wt := ""
			for _, l := range strings.Split(out, "\n") {
				if p, ok := strings.CutPrefix(l, "worktree "); ok {
					wt = p
				} else if br, ok := strings.CutPrefix(l, "branch refs/heads/"); ok {
					wts[br] = wt
				}
			}
		}
		for _, name := range names {
			if seen[repo+":"+name] || !branchRe.MatchString(name) {
				continue
			}
			seen[repo+":"+name] = true
			ref := "refs/heads/" + name
			head, err := in.git(m.t.Root, "rev-parse", "--verify", "--quiet", ref)
			if err != nil || head == "" {
				continue // not a local branch: its row would be a guess
			}
			up, _ := in.git(m.t.Root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", name+"@{upstream}")
			out = append(out, codeTree{t: featTree{Root: m.t.Root, Branch: name}, repo: repo, head: head, upstream: up, ref: ref, wt: wts[name]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].repo+out[i].t.Branch < out[j].repo+out[j].t.Branch })
	return out
}

// prExtras collects, per PR, the agents with evidence that they made it (a pr-link in their transcript, else their
// checked-out branch being the PR's head).
func (in *codeIn) prExtras() map[string]featCodePRX {
	out := map[string]featCodePRX{}
	repos := map[int]int{} // number -> how many repos have it
	for _, p := range in.prs {
		repos[p.Number]++
	}
	for _, p := range in.prs {
		x := featCodePRX{URL: p.URL, Sessions: []string{}}
		if !p.CreatedAt.IsZero() {
			x.CreatedAt = p.CreatedAt.Unix()
		}
		ambiguous := false
		for _, a := range in.team {
			for _, r := range in.refs[a.Key] {
				if r.pr != p.Number || r.repo != "" && r.repo != p.Repo {
					continue
				}
				if r.repo == "" && repos[p.Number] > 1 {
					ambiguous = true
					continue
				}
				if !slices.Contains(x.Sessions, a.Key) {
					x.Sessions = append(x.Sessions, a.Key)
				}
			}
		}
		if len(x.Sessions) > 0 {
			x.SessionSource = "pr-link (Claude Code transcript)"
		} else {
			for _, a := range in.team {
				if a.Branch == p.Head && a.Root != "" && in.repoOf(a.Root) == p.Repo {
					x.Sessions = append(x.Sessions, a.Key)
				}
			}
			if len(x.Sessions) > 0 {
				x.SessionSource = "agent's checked-out branch = PR head (" + srcLive + ")"
			}
		}
		switch {
		case len(x.Sessions) > 0:
		case ambiguous:
			x.SessionWhy = "ambiguous: PR #" + strconv.Itoa(p.Number) + " exists in several of the feature's repos"
		default:
			x.SessionWhy = "no agent linked it"
		}
		out[p.Repo+"#"+strconv.Itoa(p.Number)] = x
	}
	return out
}

// ---- the cache ----

// codeSnap is one finished snapshot of a feature's code delivery, with when it was taken.
type codeSnap struct {
	at       time.Time
	branches []featCodeBranch
	releases []featCodeRelease
	prx      map[string]featCodePRX
	main     *featCodeMain
	trees    []featTree // featMerge's answer, measured in this refresh: what featCodeTrees serves
}

// featCodeMain reports what became of FEATURE.md's "Main worktree:" line: used, or refused with a generic reason.
type featCodeMain struct {
	Path string `json:"path,omitempty"` // only when used: a refused line's path (it may name a secret) is never echoed
	Used bool   `json:"used"`
	Why  string `json:"why,omitempty"` // a refused line: one generic reason, never which rule or what it found there
}

// mainRefused is the one reason shown for a refused Main worktree line (it never says which rule failed).
const mainRefused = "the Main worktree line doesn't name an allowed git checkout (the top of a checkout with a branch, under home, outside secret paths)"

// featMainTree returns the feature's main worktree as FEATURE.md names it, read even when no agent works in it. Only the
// top directory of a git checkout under the panel's home or the user's, outside every secret path, symlinks resolved.
func (s *server) featMainTree(main string) (featTree, string) {
	if !filepath.IsAbs(main) || filepath.Clean(main) != main {
		return featTree{}, "not a clean absolute path"
	}
	real, err := filepath.EvalSymlinks(main)
	if err != nil {
		return featTree{}, "not found"
	}
	under := func(p, root string) bool {
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
		rel, err := filepath.Rel(root, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
	}
	roots := []string{s.cfg.home}
	if h, err := os.UserHomeDir(); err == nil {
		roots = append(roots, h)
	}
	if !slices.ContainsFunc(roots, func(r string) bool { return r != "" && under(real, r) }) {
		return featTree{}, "outside the allowed roots (the panel's home, the user's home)"
	}
	for _, sp := range s.secretPaths() {
		if under(real, sp) {
			return featTree{}, "inside a secret path"
		}
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return featTree{}, "not a directory"
	}
	g := gitInfo(real)
	if g.Root != real {
		return featTree{}, "not the top directory of a git checkout"
	}
	if g.Branch == "" || g.Detached {
		return featTree{}, "no branch checked out"
	}
	return featTree{Root: real, Branch: g.Branch, Main: true, Agents: []string{}}, ""
}

// featCode is the shared cache: snapshots per session, which sessions are refreshing, and the GitHub deployments per
// repo (shared by every feature that uses the repo), each with a one-fetch-at-a-time marker.
var featCode = struct {
	sync.Mutex
	snaps map[string]codeSnap
	busy  map[string]bool
	ships map[string]shipState
	shipB map[string]bool
}{snaps: map[string]codeSnap{}, busy: map[string]bool{}, ships: map[string]shipState{}, shipB: map[string]bool{}}

// featCodePage returns the session's last snapshot (nil before the first one), never waiting on git or GitHub.
// A stale one starts one background refresh.
func (s *server) featCodePage(session string) ([]featCodeBranch, []featCodeRelease, map[string]featCodePRX) {
	snap, _ := s.featCodeSnap(session)
	return snap.branches, snap.releases, snap.prx
}

// featCodeSnap returns the session's last snapshot and whether a refresh is running, starting one if the snapshot is
// missing or older than codeTTL.
func (s *server) featCodeSnap(session string) (codeSnap, bool) {
	featCode.Lock()
	snap, ok := featCode.snaps[session]
	refreshing := featCode.busy[session]
	if (!ok || time.Since(snap.at) > codeTTL) && !refreshing {
		featCode.busy[session], refreshing = true, true
		go s.featCodeRefresh(session)
	}
	featCode.Unlock()
	return snap, refreshing
}

// featCodeTrees returns featMerge's fields (Main, Ahead, Checked, OtherRepo) for the request's live trees, from memory:
// no git. Main is chosen as featMerge chooses it (FEATURE.md's main worktree, else the tree most of the team works in).
// The git-measured fields are copied only from the last background refresh's measurement of that same root and
// branch, taken against this same main (a different main voids them all). A tree it hasn't measured stays Checked:false, Ahead:0, OtherRepo:false: "not measured", never a guess.
// at is when that measurement was taken (0: never). A stale snapshot starts the one background refresh.
func (s *server) featCodeTrees(session string, live []featTree, main string) ([]featTree, int64) {
	snap, _ := s.featCodeSnap(session)
	out := slices.Clone(live)
	if main == "" {
		best := 0
		for _, t := range out {
			if len(t.Agents) > best {
				best, main = len(t.Agents), t.Root
			}
		}
	}
	got, against := map[string]featTree{}, "" // the measurement, and the main it was measured against
	for _, t := range snap.trees {
		got[t.Root+"@"+t.Branch] = t
		if t.Main {
			against = t.Root
		}
	}
	for i := range out {
		t := &out[i]
		t.Main, t.Ahead, t.Checked, t.OtherRepo = t.Root == main, 0, false, false
		if m, ok := got[t.Root+"@"+t.Branch]; ok && against != "" && against == main {
			t.Ahead, t.Checked, t.OtherRepo = m.Ahead, m.Checked, m.OtherRepo
		}
	}
	return out, unixOr0(snap.at)
}

// featCodeRefreshHook replaces what a refresh reads (tests only).
var featCodeRefreshHook func(s *server, session string) codeIn

// featCodeRefresh reads the session's worktrees, PRs and GitHub deployments and stores a new snapshot. It runs in the
// background, one at a time per session, so a page request never waits for git or gh.
func (s *server) featCodeRefresh(session string) {
	defer func() {
		featCode.Lock()
		delete(featCode.busy, session)
		featCode.Unlock()
	}()
	var in codeIn
	var main *featCodeMain
	mainPath := ""
	featCode.Lock()
	hook := featCodeRefreshHook
	featCode.Unlock()
	if hook != nil {
		in = hook(s, session)
	} else {
		f, ok := s.featFacts(session)
		if !ok && f.Dossier.Main == "" { // no session and no canonical worktree: nothing to read
			return
		}
		in = codeIn{session: session, trees: f.Trees, team: f.Team, prs: f.PRs, refs: map[string][]prRef{}, git: gitRO, now: time.Now()}
		mainPath = f.Dossier.Main
		if m := f.Dossier.Main; m != "" {
			main = &featCodeMain{Path: m, Used: true}
			if !slices.ContainsFunc(in.trees, func(t featTree) bool { return t.Main && t.Root == m }) {
				if t, why := s.featMainTree(m); why != "" {
					main = &featCodeMain{Why: mainRefused}
				} else {
					for i := range in.trees { // it's the main one, whatever featMerge picked
						in.trees[i].Main = false
					}
					in.trees = append([]featTree{t}, in.trees...)
				}
			}
		}
		for _, a := range f.Team {
			if a.path != "" {
				in.refs[a.Key] = s.term.tr.prs.refs(a.path)
			}
		}
	}
	in.ships = s.featShips(in)
	var trees []featTree
	if hook == nil { // featMerge's git, here in the background instead of in the page's request (featCodeTrees)
		trees = featMerge(slices.Clone(in.trees), mainPath)
	}
	b, r, x := in.build()
	featCode.Lock()
	if old, ok := featCode.snaps[session]; !ok || !old.at.After(in.now) {
		featCode.snaps[session] = codeSnap{at: in.now, branches: b, releases: r, prx: x, main: main, trees: trees}
	}
	featCode.Unlock()
}

// featShips returns the GitHub deployments of every GitHub repo of the feature, fetching those that are due (called
// from the background refresh), one fetch per repo at a time.
func (s *server) featShips(in codeIn) map[string]shipState {
	repos := map[string]bool{}
	for _, t := range in.trees {
		repos[in.repoOf(t.Root)] = true
	}
	for _, p := range in.prs {
		repos[p.Repo] = true
	}
	out := map[string]shipState{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for repo := range repos {
		if !repoSlugRe.MatchString(repo) {
			continue
		}
		wg.Go(func() {
			st := s.featShip(repo, in.now)
			mu.Lock()
			out[repo] = st
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// featShip returns one repo's deployments from the cache, fetching them first if they are due and nobody else is
// already fetching. A failed fetch keeps the last good answer and records the error.
func (s *server) featShip(repo string, now time.Time) shipState {
	featCode.Lock()
	st := featCode.ships[repo]
	ttl := shipTTL
	if st.si != nil && slices.ContainsFunc(st.si.deploys, func(d ghDeploy) bool { return d.state == "run" }) {
		ttl = codeTTL
	}
	due := (st.okAt.IsZero() || now.Sub(st.okAt) >= ttl) && (st.err == "" || now.Sub(st.triedAt) >= shipRetry)
	if !due || featCode.shipB[repo] || s.prs.fetch == nil {
		featCode.Unlock()
		return st
	}
	featCode.shipB[repo] = true
	featCode.Unlock()
	o, n, _ := strings.Cut(repo, "/")
	b, err := s.prs.fetch("", "api", "graphql", "-f", "query="+shipQuery, "-f", "o="+o, "-f", "n="+n, "-f", "h=stage")
	var si *shipInfo
	if err == nil {
		si, err = parseShip(b)
	}
	featCode.Lock()
	defer featCode.Unlock()
	delete(featCode.shipB, repo)
	st = featCode.ships[repo]
	st.triedAt = now
	if err != nil {
		st.err = trunc(err.Error(), 200)
	} else {
		st.si, st.okAt, st.err = si, now, ""
	}
	featCode.ships[repo] = st
	return st
}
