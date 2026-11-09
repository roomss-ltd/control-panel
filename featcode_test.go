package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for the feature page's code delivery (featcode.go): read from real local git fixtures and parsed GitHub
// deployment answers. There is no network: gh is a fake, and the git origin URLs are never fetched.

// gitT runs git in dir with a fixed identity and no signing, failing the test on error.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitT makes an empty commit in dir and returns its hash.
func commitT(t *testing.T, dir, msg string) string {
	gitT(t, dir, "commit", "--allow-empty", "-q", "-m", msg)
	return gitT(t, dir, "rev-parse", "HEAD")
}

// deployJSON: one GitHub deployment, with its statuses newest first, a minute apart after at
func deployJSON(env, sha string, at time.Time, states ...string) string {
	var st []string
	for i, s := range states {
		st = append(st, fmt.Sprintf(`{"state":%q,"createdAt":%q,"logUrl":"https://railway.com/%s"}`, s, at.Add(time.Duration(len(states)-i)*time.Minute).Format(time.RFC3339), sha))
	}
	return fmt.Sprintf(`{"environment":%q,"commitOid":%q,"createdAt":%q,"newest":{"nodes":[%s]},"oldest":{"nodes":[%s]}}`, env, sha, at.Format(time.RFC3339), strings.Join(st[:min(2, len(st))], ","), strings.Join(st, ","))
}

func shipT(t *testing.T, deps ...string) *shipInfo {
	t.Helper()
	si, err := parseShip([]byte(`{"data":{"repository":{"deployments":{"nodes":[` + strings.Join(deps, ",") + `]},"pullRequests":{"nodes":[]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	return si
}

// codeFixture is a set of real git repos and worktrees built by newCodeFixture.
type codeFixture struct {
	app, feat, api, lt         string // worktrees
	c0, f1, a1, a2, m1, ltHead string
	t0                         time.Time
}

// app: main (c0 tagged v1.0) -> feature (f1) -> feat/api (a1 pushed, a2 not), merged back into feature as m1.
// loadtests: a test-only repo with no upstream.
func newCodeFixture(t *testing.T) codeFixture { return newCodeFixtureIn(t, t.TempDir()) }

// newCodeFixtureIn builds the fixture described on newCodeFixture under directory d.
func newCodeFixtureIn(t *testing.T, d string) codeFixture {
	os.MkdirAll(d, 0o755)
	fx := codeFixture{app: filepath.Join(d, "app"), feat: filepath.Join(d, "wt-feat"), api: filepath.Join(d, "wt-api"), lt: filepath.Join(d, "lt"), t0: time.Now().Add(-time.Hour).Truncate(time.Second)}
	gitT(t, d, "init", "-q", "-b", "main", fx.app)
	gitT(t, fx.app, "remote", "add", "origin", "https://github.com/acme/app.git")
	fx.c0 = commitT(t, fx.app, "base")
	gitT(t, fx.app, "tag", "v1.0")
	gitT(t, fx.app, "worktree", "add", "-q", "-b", "feature/x", fx.feat, "main")
	fx.f1 = commitT(t, fx.feat, "start x")
	gitT(t, fx.app, "worktree", "add", "-q", "-b", "feat/api", fx.api, "feature/x")
	fx.a1 = commitT(t, fx.api, "api 1")
	gitT(t, fx.api, "update-ref", "refs/remotes/origin/feat/api", fx.a1) // as if pushed: no network
	gitT(t, fx.api, "branch", "-q", "--set-upstream-to=origin/feat/api")
	fx.a2 = commitT(t, fx.api, "api 2")
	gitT(t, fx.feat, "merge", "-q", "--no-ff", "-m", "merge api", "feat/api")
	fx.m1 = gitT(t, fx.feat, "rev-parse", "HEAD")
	gitT(t, d, "init", "-q", "-b", "main", fx.lt)
	gitT(t, fx.lt, "remote", "add", "origin", "git@github.com:acme/loadtests.git")
	fx.ltHead = commitT(t, fx.lt, "k6")
	return fx
}

// in returns the snapshot inputs for the fixture: its worktrees and agents, canned PRs and GitHub deployments, and the
// real read-only git runner over the fixture repos.
func (fx codeFixture) in(t *testing.T) codeIn {
	at := func(m int) time.Time { return fx.t0.Add(time.Duration(m) * time.Minute) }
	app := shipT(t,
		deployJSON("App / production", fx.c0, at(0), "SUCCESS"),
		deployJSON("App / stage", fx.f1, at(10), "SUCCESS"),
		deployJSON("App / stage", fx.m1, at(20), "FAILURE", "IN_PROGRESS"), // a newer deploy that failed: stage still runs f1
	)
	now := fx.t0.Add(time.Hour)
	pr := func(n int, head, base, state, merge string) *PRStatus {
		return &PRStatus{Number: n, URL: fmt.Sprintf("https://github.com/acme/app/pull/%d", n), Repo: "acme/app", Head: head, Base: base, State: state,
			merge: merge, Author: "owner-login", Checks: []PRCheck{}, MergedAt: at(15), ClosedAt: at(16), CreatedAt: at(5)}
	}
	return codeIn{
		trees: []featTree{{Root: fx.feat, Branch: "feature/x", Main: true, Agents: []string{"Lead"}}, {Root: fx.api, Branch: "feat/api", Agents: []string{"Coder"}}, {Root: fx.lt, Branch: "main", Agents: []string{"Tester"}}},
		team:  []featAgent{{Key: "s:1", Tab: "Coder", Root: fx.api, Branch: "feat/api"}, {Key: "s:2", Tab: "Lead", Root: fx.feat, Branch: "feature/x"}},
		refs:  map[string][]prRef{"s:1": {{pr: 5}}},
		prs: []*PRStatus{
			pr(5, "feat/api", "feature/x", "MERGED", fx.m1),
			pr(6, "feat/old", "feature/x", "CLOSED", ""),
			pr(7, "feat/gone", "feature/x", "MERGED", "0123456789abcdef0123456789abcdef01234567"), // merge commit not in this checkout
		},
		ships: map[string]shipState{"acme/app": {si: app, okAt: now.Add(-time.Minute), triedAt: now.Add(-time.Minute)}, "acme/loadtests": {si: shipT(t), okAt: now, triedAt: now}},
		git:   gitRO,
		now:   now,
	}
}

// findBranch returns the row with the given id, or nil.
func findBranch(bs []featCodeBranch, id string) *featCodeBranch {
	for i := range bs {
		if bs[i].ID == id {
			return &bs[i]
		}
	}
	return nil
}

func TestFeatCodeBranchesReleasesAndPRs(t *testing.T) {
	fx := newCodeFixture(t)
	in := fx.in(t)
	bs, rs, prx := in.build()

	// the child branch: parent and source commit recorded by the reflog, one unpushed commit, merged by its PR
	api := findBranch(bs, "acme/app:feat/api")
	if api == nil {
		t.Fatalf("no feat/api branch: %+v", bs)
	}
	if api.Parent == nil || *api.Parent != "acme/app:feature/x" || api.SrcMethod != "reflog" || api.Src != fx.f1 || api.From == nil {
		t.Fatalf("feat/api ancestry: parent %v src %s (%s) from %v, want feature/x @ %s by reflog", api.Parent, api.Src, api.SrcMethod, api.From, fx.f1)
	}
	if api.Unpushed == nil || *api.Unpushed != 1 || api.Commits[0].Sha != fx.a2 || api.Commits[0].Kind != "unpushed" || api.Commits[1].Kind != "" {
		t.Fatalf("feat/api push state: %v %+v", api.Unpushed, api.Commits)
	}
	if api.PR != 5 || *api.IntoState != "merged" || api.To == nil || len(api.Agents) != 1 || api.Agents[0].Key != "s:1" || api.By != "" {
		t.Fatalf("feat/api PR and agents: %+v", api)
	}
	feat := findBranch(bs, "acme/app:feature/x")
	if feat == nil || *feat.Parent != "acme/app:main" || feat.Src != fx.c0 || feat.Commits[0].Kind != "merge" || feat.Unpushed != nil || feat.Why == "" {
		t.Fatalf("feature branch: %+v", feat)
	}
	// its own line only (git log --first-parent): the merge stands for feat/api's commits, which are feat/api's
	if got := fmt.Sprint(len(feat.Commits), feat.Commits[0].Sha, feat.Commits[1].Sha); got != fmt.Sprint(2, fx.m1, fx.f1) {
		t.Fatalf("feature/x commits: %s, want the merge then start x", got)
	}
	// the parent a row names (main) gets a row of its own, read from its ref
	if m := findBranch(bs, "acme/app:main"); m == nil || m.Head != fx.c0 || m.WtSource != "git ref (no agent works in a worktree of it)" || m.Wt != fx.app || len(m.Agents) != 0 {
		t.Fatalf("main row: %+v", m)
	}
	// a closed PR stays closed, never merged; with no worktree, its git facts are unknown
	old := findBranch(bs, "acme/app:feat/old")
	if old == nil || *old.IntoState != "closed" || old.State != "unknown" || old.Wt != "" || old.To == nil {
		t.Fatalf("closed PR branch: %+v", old)
	}

	if len(rs) != 2 || rs[0].Repo != "acme/app" || !rs[0].Primary || rs[1].Repo != "acme/loadtests" || rs[1].Primary {
		t.Fatalf("releases: %+v", rs)
	}
	app := rs[0]
	if app.Prod.Sha != fx.c0 || app.Prod.Version != "v1.0" || app.Prod.State != "fresh" || app.Prod.Unknown || app.Prod.FetchedAt == 0 || app.Prod.Source != srcDeploy {
		t.Fatalf("production: %+v", app.Prod)
	}
	if app.Stage.Sha != fx.f1 || app.Stage.Attempt == nil || app.Stage.Attempt.State != "fail" || app.Stage.Attempt.Sha != fx.m1 {
		t.Fatalf("stage: a failed newer deploy must not replace the live one: %+v", app.Stage)
	}
	if fmt.Sprint(app.Stage.PRs, app.Stage.PRsNot, app.Stage.PRsUnknown) != "[] [5] [7]" {
		t.Fatalf("stage PRs: in %v, not %v, unknown %v", app.Stage.PRs, app.Stage.PRsNot, app.Stage.PRsUnknown)
	}
	if app.Branch.Name != "feature/x" || app.Branch.Head != fx.m1 || app.Branch.State != "fresh" || app.Branch.Deployed == nil || *app.Branch.Deployed || !strings.Contains(app.Branch.Note, "not deployed") {
		t.Fatalf("feature branch HEAD must not read as deployed: %+v", app.Branch)
	}
	if app.Preparing.State != "unknown" || app.Preparing.PR != 0 {
		t.Fatalf("preparing with no linked release PR: %+v", app.Preparing)
	}
	lt := rs[1]
	if !lt.Prod.Unknown || lt.Prod.State != "unknown" || lt.Why != "no GitHub deployments recorded for this repo" || lt.Branch.Deployed != nil {
		t.Fatalf("test-only repo: unknown, never 'never deployed': %+v", lt)
	}

	// attribution: the pr-link names the agent; the shared GitHub author never does
	if x := prx["acme/app#5"]; fmt.Sprint(x.Sessions) != "[s:1]" || !strings.HasPrefix(x.SessionSource, "pr-link") || x.CreatedAt == 0 || x.URL == "" {
		t.Fatalf("PR 5 extras: %+v", x)
	}
	if x := prx["acme/app#6"]; len(x.Sessions) != 0 || x.SessionWhy != "no agent linked it" {
		t.Fatalf("PR 6 has no evidence of an agent: %+v", x)
	}
	b, _ := json.Marshal(map[string]any{"branches": bs, "releases": rs, "prx": prx})
	if strings.Contains(string(b), "owner-login") {
		t.Fatal("the GitHub author leaked into the code payload")
	}
	for _, k := range []string{`"source"`, `"fetchedAt"`, `"state"`, `"intoState"`, `"srcMethod"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("payload lacks %s", k)
		}
	}
}

func TestFeatCodeStaleUnknownAndPreparing(t *testing.T) {
	fx := newCodeFixture(t)
	in := fx.in(t)
	good := in.ships["acme/app"]

	// gh failed after a success: last known values, the old fetch time, the error
	in.ships["acme/app"] = shipState{si: good.si, okAt: in.now.Add(-52 * time.Minute), triedAt: in.now, err: "gh: timeout"}
	_, rs, _ := in.build()
	if p := rs[0].Prod; p.State != "stale" || p.Sha != fx.c0 || p.FetchedAt != in.now.Add(-52*time.Minute).Unix() || !strings.Contains(p.Why, "timeout") {
		t.Fatalf("stale production: %+v", p)
	}
	// an old answer with no error is stale too
	in.ships["acme/app"] = shipState{si: good.si, okAt: in.now.Add(-time.Hour), triedAt: in.now.Add(-time.Hour)}
	if _, rs, _ = in.build(); rs[0].Stage.State != "stale" {
		t.Fatalf("an hour-old answer is fresh: %+v", rs[0].Stage)
	}
	// never fetched
	delete(in.ships, "acme/app")
	// deploys unknown: the branch itself is still known (name, head, fresh); only Deployed is unknown
	if _, rs, _ = in.build(); !rs[0].Prod.Unknown || rs[0].Prod.Why != "not fetched yet" || rs[0].Prod.Sha != "" || rs[0].Branch.Deployed != nil ||
		rs[0].Branch.State != "fresh" || rs[0].Branch.Name != "feature/x" || rs[0].Branch.Head != fx.m1 {
		t.Fatalf("never fetched: %+v / %+v", rs[0].Prod, rs[0].Branch)
	}
	// a release PR this feature linked is what's being prepared; its version stays unknown without a tag
	in.prs = append(in.prs, &PRStatus{Number: 9, Repo: "acme/app", Head: "stage", Base: "main", State: "OPEN", Title: "Release", URL: "https://github.com/acme/app/pull/9"})
	if _, rs, _ = in.build(); rs[0].Preparing.PR != 9 || rs[0].Preparing.Version != "" {
		t.Fatalf("preparing: %+v", rs[0].Preparing)
	}
	// stage running the feature HEAD: deployed, in stage
	in.ships["acme/app"] = shipState{si: shipT(t, deployJSON("App / stage", fx.m1, fx.t0, "SUCCESS")), okAt: in.now, triedAt: in.now}
	if _, rs, _ = in.build(); rs[0].Branch.Deployed == nil || !*rs[0].Branch.Deployed || rs[0].Branch.DeployedIn != "stage" || fmt.Sprint(rs[0].Stage.PRs) != "[5]" {
		t.Fatalf("HEAD on stage: %+v %+v", rs[0].Branch, rs[0].Stage)
	}
	// only pending/queued deploys: nothing is deployed
	in.ships["acme/app"] = shipState{si: shipT(t, deployJSON("App / production", fx.m1, fx.t0, "IN_PROGRESS")), okAt: in.now, triedAt: in.now}
	if _, rs, _ = in.build(); !rs[0].Prod.Unknown || rs[0].Prod.Sha != "" || rs[0].Prod.Attempt == nil || rs[0].Prod.Attempt.State != "run" {
		t.Fatalf("a running deploy read as deployed: %+v", rs[0].Prod)
	}
}

func TestFeatCodeNoReflogFallsBackToMergeBase(t *testing.T) {
	fx := newCodeFixture(t)
	gitT(t, fx.api, "reflog", "expire", "--expire=all", "--all")
	in := fx.in(t)
	bs, _, _ := in.build()
	api := findBranch(bs, "acme/app:feat/api")
	if api.SrcMethod != "merge-base" || api.From != nil || api.Src == "" || api.ParentSource == "" {
		t.Fatalf("without a reflog: %+v", api)
	}
}

func TestFeatCodeAttributionAmbiguousAndBranch(t *testing.T) {
	in := codeIn{
		team: []featAgent{{Key: "s:1", Root: "/nowhere", Branch: "x"}},
		refs: map[string][]prRef{"s:1": {{pr: 3}}},
		prs:  []*PRStatus{{Number: 3, Repo: "a/one", Head: "y"}, {Number: 3, Repo: "a/two", Head: "y"}, {Number: 4, Repo: "a/one", Head: "x"}},
		git: func(dir string, args ...string) (string, error) {
			if args[0] == "remote" {
				return "https://github.com/a/one.git", nil
			}
			return "", errors.New("no")
		},
	}
	x := in.prExtras()
	if len(x["a/one#3"].Sessions) != 0 || !strings.HasPrefix(x["a/one#3"].SessionWhy, "ambiguous") {
		t.Fatalf("a number in two repos, linked without a repo: %+v", x["a/one#3"])
	}
	if fmt.Sprint(x["a/one#4"].Sessions) != "[s:1]" || !strings.Contains(x["a/one#4"].SessionSource, "checked-out branch") {
		t.Fatalf("the agent on the PR's head: %+v", x["a/one#4"])
	}
}

func TestRepoSlug(t *testing.T) {
	for u, want := range map[string]string{
		"https://github.com/acme/app.git": "acme/app", "git@github.com:acme/app.git": "acme/app", "ssh://git@github.com/acme/app": "acme/app",
		"https://evil.example/acme/app": "", "https://github.com/acme/app/../../x": "", "https://github.com/-f/x;rm": "", "": "",
	} {
		if got := repoSlug(u); got != want {
			t.Errorf("repoSlug(%q) = %q, want %q", u, got, want)
		}
	}
}

// resetFeatCode empties the shared code cache between tests.
func resetFeatCode() {
	featCode.Lock()
	featCode.snaps, featCode.busy, featCode.ships, featCode.shipB = map[string]codeSnap{}, map[string]bool{}, map[string]shipState{}, map[string]bool{}
	featCode.Unlock()
}

// The page never waits on git or GitHub, and a burst of page ticks makes one refresh and one gh call.
func TestFeatCodePageNeverBlocksAndRefreshesOnce(t *testing.T) {
	resetFeatCode()
	defer func() { setRefreshHook(nil); resetFeatCode() }()
	var hooks, fetches atomic.Int32
	release := make(chan struct{})
	setRefreshHook(func(s *server, session string) codeIn {
		hooks.Add(1)
		return codeIn{prs: []*PRStatus{{Number: 1, Repo: "acme/app", Head: "feat/a", Base: "main", State: "OPEN"}}, git: func(string, ...string) (string, error) { return "", errors.New("no git") }, now: time.Now()}
	})
	s := &server{}
	s.prs.fetch = func(dir string, args ...string) ([]byte, error) {
		fetches.Add(1)
		<-release
		return nil, errors.New("gh: offline")
	}
	var wg sync.WaitGroup
	start := time.Now()
	for range 50 {
		wg.Go(func() {
			if b, r, _ := s.featCodePage("demo"); b != nil || r != nil {
				t.Error("a snapshot before the first refresh")
			}
		})
	}
	wg.Wait()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("featCodePage waited %v on a blocked gh", d)
	}
	close(release)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		featCode.Lock()
		_, ok := featCode.snaps["demo"]
		featCode.Unlock()
		if ok || time.Now().After(deadline) {
			break
		}
	}
	if hooks.Load() != 1 || fetches.Load() != 1 {
		t.Fatalf("refreshes %d, gh calls %d: want 1 and 1", hooks.Load(), fetches.Load())
	}
	_, rs, _ := s.featCodePage("demo")
	if len(rs) != 1 || !rs[0].Prod.Unknown || !strings.Contains(rs[0].Prod.Why, "gh failed, never fetched") {
		t.Fatalf("after a failed first fetch: %+v", rs)
	}
	// within the retry window a failed repo isn't asked again
	featCode.Lock()
	featCode.snaps["demo"] = codeSnap{at: time.Now().Add(-time.Hour)}
	featCode.Unlock()
	refreshNow(s, "demo")
	if fetches.Load() != 1 {
		t.Fatalf("gh retried inside the retry window: %d calls", fetches.Load())
	}
}

func TestFeatShipKeepsTheLastGoodAnswer(t *testing.T) {
	resetFeatCode()
	defer resetFeatCode()
	t0 := time.Now()
	fail := false
	s := &server{}
	s.prs.fetch = func(dir string, args ...string) ([]byte, error) {
		if fail {
			return nil, errors.New("HTTP 502")
		}
		return []byte(`{"data":{"repository":{"deployments":{"nodes":[` + deployJSON("App / production", "abc", t0, "SUCCESS") + `]},"pullRequests":{"nodes":[]}}}}`), nil
	}
	st := s.featShip("acme/app", t0)
	if st.si == nil || !st.okAt.Equal(t0) {
		t.Fatalf("first fetch: %+v", st)
	}
	if st2 := s.featShip("acme/app", t0.Add(time.Minute)); !st2.triedAt.Equal(t0) {
		t.Fatal("refetched inside the TTL")
	}
	fail = true
	st = s.featShip("acme/app", t0.Add(4*time.Minute))
	if st.si == nil || !st.okAt.Equal(t0) || st.err == "" || !st.triedAt.Equal(t0.Add(4*time.Minute)) {
		t.Fatalf("after a failure the old answer and its time stay: %+v", st)
	}
}

func TestParsePRCreatedAt(t *testing.T) {
	p, err := parsePR([]byte(`{"number":1,"url":"https://github.com/a/b/pull/1","state":"OPEN","createdAt":"2026-11-08T10:00:00Z"}`))
	if err != nil || p.CreatedAt.Unix() != time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("createdAt: %v %v", p.CreatedAt, err)
	}
}

// setRefreshHook installs (or clears) the hook that replaces what a background refresh reads.
func setRefreshHook(h func(*server, string) codeIn) {
	featCode.Lock()
	featCodeRefreshHook = h
	featCode.Unlock()
}

func TestFeatMainTreeGuard(t *testing.T) {
	home := t.TempDir()
	// the user's home is an allowed root too: pin it to this test's home, so "outside" (a sibling temp dir) is
	// outside both roots even when TMPDIR itself sits under the real home
	t.Setenv("HOME", home)
	s := &server{cfg: config{home: home, pinFile: filepath.Join(home, ".config", "cp", "pin")}}
	fx := newCodeFixtureIn(t, filepath.Join(home, "work"))
	if tr, why := s.featMainTree(fx.feat); why != "" || tr.Root != fx.feat || tr.Branch != "feature/x" || !tr.Main {
		t.Fatalf("a worktree under home: %+v %q", tr, why)
	}
	outside := newCodeFixture(t) // under the test's temp dir, not under either home
	os.Symlink(outside.feat, filepath.Join(home, "link"))
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	gitT(t, home, "init", "-q", "-b", "main", filepath.Join(home, ".ssh", "repo"))
	commitT(t, filepath.Join(home, ".ssh", "repo"), "x")
	gitT(t, fx.app, "checkout", "-q", "--detach")
	for p, want := range map[string]string{
		"work/app":                          "not a clean absolute path",
		fx.feat + "/../wt-feat":             "not a clean absolute path",
		filepath.Join(home, "nope"):         "not found",
		outside.feat:                        "outside the allowed roots (the panel's home, the user's home)",
		filepath.Join(home, "link"):         "outside the allowed roots (the panel's home, the user's home)",
		filepath.Join(home, ".ssh", "repo"): "inside a secret path",
		filepath.Join(fx.feat, ".git"):      "not a directory", // a worktree's .git is a file
		filepath.Join(home, "work"):         "not the top directory of a git checkout",
		fx.app:                              "no branch checked out",
	} {
		if _, why := s.featMainTree(p); why != want {
			t.Errorf("%s: %q, want %q", p, why, want)
		}
	}
}

// A feature whose FEATURE.md names its main worktree has branch and release rows even with no agent pane.
func TestFeatCodeFromMainWorktreeLineWithoutPanes(t *testing.T) {
	resetFeatCode()
	defer resetFeatCode()
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	home := v.e.s.cfg.home
	v.e.s.cfg.pinFile = filepath.Join(home, "cp-pin", "pin") // its own folder, as on a real instance (the PIN's folder is secret)
	fx := newCodeFixtureIn(t, filepath.Join(home, "work"))
	writeFile(t, filepath.Join(home, "jarvis-files", "threads", supTeam, "FEATURE.md"), "# Demo\n\nMain worktree: `~/work/wt-feat`\n")
	var calls []string
	v.e.s.prs.fetch = func(dir string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return []byte(`{"data":{"repository":{"deployments":{"nodes":[` + deployJSON("App / stage", fx.f1, fx.t0, "SUCCESS") + `]},"pullRequests":{"nodes":[]}}}}`), nil
	}
	refreshNow(v.e.s, supTeam)
	snap, _ := v.e.s.featCodeSnap(supTeam)
	if snap.main == nil || !snap.main.Used || len(snap.branches) != 2 || len(snap.releases) != 1 || snap.branches[1].Name != "main" || snap.branches[1].WtSource != "git ref (no agent works in a worktree of it)" {
		t.Fatalf("rows from the Main worktree line: main %+v branches %+v releases %+v", snap.main, snap.branches, snap.releases)
	}
	b, r := snap.branches[0], snap.releases[0]
	if b.Name != "feature/x" || b.WtSource != srcMainLine || len(b.Agents) != 0 || !b.Main || b.Src != fx.c0 || b.SrcMethod != "reflog" {
		t.Fatalf("branch: %+v", b)
	}
	if r.Repo != "acme/app" || !r.Primary || r.Stage.Sha != fx.f1 || r.Stage.State != "fresh" || !r.Prod.Unknown || r.Branch.Deployed == nil || *r.Branch.Deployed {
		t.Fatalf("release: %+v", r)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "o=acme -f n=app") {
		t.Fatalf("gh calls: %q", calls)
	}
	// a Main worktree line naming a secret path is refused, with why, and gives no rows
	resetFeatCode()
	writeFile(t, filepath.Join(home, "jarvis-files", "threads", supTeam, "FEATURE.md"), "Main worktree: "+filepath.Join(home, ".ssh")+"\n")
	refreshNow(v.e.s, supTeam)
	snap, _ = v.e.s.featCodeSnap(supTeam)
	if snap.main == nil || snap.main.Used || snap.main.Why != mainRefused || snap.main.Path != "" || len(snap.branches) != 0 {
		t.Fatalf("refused Main worktree: %+v %+v", snap.main, snap.branches)
	}
	// the raw route never echoes a refused path or which rule refused it
	cookie := v.e.login(t)
	resp := v.e.do(t, "GET", "/api/term/feature/code?session="+supTeam, "", map[string]string{"Cf-Access-Jwt-Assertion": v.e.jwt, "Cookie": "cp_session=" + cookie})
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || strings.Contains(string(body), ".ssh") || strings.Contains(string(body), "inside a secret") || !strings.Contains(string(body), `"used":false`) {
		t.Fatalf("raw route on a refused line: %d %s", resp.StatusCode, body)
	}
}

// The page's invariant:
//   - a burst of page reads makes ONE background refresh per session per TTL, and none within the TTL;
//   - that refresh runs git only in the feature's declared checkouts, only read-only subcommands;
//   - it asks gh only about the repos those checkouts' origins (and the feature's PRs) name;
//   - it writes nothing to the repos.
func TestFeatCodeBurstOneBoundedReadOnlyRefresh(t *testing.T) {
	resetFeatCode()
	defer func() { setRefreshHook(nil); resetFeatCode() }()
	fx := newCodeFixture(t)
	base := fx.in(t)
	snapGit := func() map[string]string { // every file under the repos' .git dirs: size and mtime
		m := map[string]string{}
		for _, d := range []string{filepath.Join(fx.app, ".git"), filepath.Join(fx.lt, ".git")} {
			filepath.Walk(d, func(p string, fi os.FileInfo, err error) error {
				if err == nil && !fi.IsDir() {
					m[p] = fmt.Sprint(fi.Size(), fi.ModTime().UnixNano())
				}
				return nil
			})
		}
		return m
	}
	before := snapGit()
	var mu sync.Mutex
	var gits [][]string
	var refreshes atomic.Int32
	readOnly := map[string]bool{"remote get-url": true, "rev-parse": true, "reflog show": true, "symbolic-ref": true, "merge-base": true,
		"rev-list": true, "log": true, "tag --points-at": true, "cat-file -e": true, "worktree list": true}
	allowed := map[string]bool{fx.feat: true, fx.api: true, fx.lt: true}
	setRefreshHook(func(s *server, session string) codeIn {
		refreshes.Add(1)
		in := base
		in.ships, in.now = nil, time.Now()
		in.git = func(dir string, args ...string) (string, error) {
			mu.Lock()
			gits = append(gits, append([]string{dir}, args...))
			mu.Unlock()
			return gitRO(dir, args...)
		}
		return in
	})
	var ghs []string
	s := &server{}
	s.prs.fetch = func(dir string, args ...string) ([]byte, error) {
		mu.Lock()
		ghs = append(ghs, strings.Join(args, " "))
		mu.Unlock()
		return []byte(`{"data":{"repository":{"deployments":{"nodes":[]},"pullRequests":{"nodes":[]}}}}`), nil
	}
	burst := func() {
		var wg sync.WaitGroup
		for range 100 {
			wg.Go(func() { s.featCodePage("demo") })
		}
		wg.Wait()
	}
	waitIdle := func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			featCode.Lock()
			_, done := featCode.snaps["demo"]
			busy := featCode.busy["demo"]
			featCode.Unlock()
			if done && !busy {
				return
			}
		}
		t.Fatal("the refresh didn't finish")
	}
	burst()
	waitIdle()
	if refreshes.Load() != 1 {
		t.Fatalf("100 page reads made %d refreshes", refreshes.Load())
	}
	mu.Lock()
	n, nGH := len(gits), len(ghs)
	for _, c := range gits {
		sub := c[1]
		if c[1] == "remote" || c[1] == "reflog" || c[1] == "tag" || c[1] == "cat-file" || c[1] == "worktree" {
			sub += " " + c[2]
		}
		if !allowed[c[0]] || !readOnly[sub] {
			t.Errorf("git outside the contract: %q", c)
		}
	}
	for _, g := range ghs {
		if !strings.HasPrefix(g, "api graphql ") || !(strings.Contains(g, "-f o=acme -f n=app -f") || strings.Contains(g, "-f o=acme -f n=loadtests -f")) {
			t.Errorf("gh outside the contract: %q", g)
		}
	}
	mu.Unlock()
	if nGH != 2 {
		t.Fatalf("gh calls: %d, want one per declared repo (2)", nGH)
	}
	t.Logf("one refresh: %d git calls (3 checkouts), %d gh calls", n, nGH)

	burst() // within the TTL: served from memory
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if refreshes.Load() != 1 || len(gits) != n || len(ghs) != nGH {
		t.Fatalf("a burst within the TTL did work: refreshes %d, git %d -> %d, gh %d -> %d", refreshes.Load(), n, len(gits), nGH, len(ghs))
	}
	mu.Unlock()
	if after := snapGit(); fmt.Sprint(after) != fmt.Sprint(before) {
		for p, v := range after {
			if before[p] != v {
				t.Errorf("a refresh changed %s", p)
			}
		}
		for p := range before {
			if _, ok := after[p]; !ok {
				t.Errorf("a refresh removed %s", p)
			}
		}
	}
}

// gitShim puts a logging git first on PATH: every git process anyone in this test starts lands in the log
func gitShim(t *testing.T) func() []string {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	d := t.TempDir()
	log := filepath.Join(d, "calls")
	writeFile(t, filepath.Join(d, "git"), "#!/bin/sh\necho \"$*\" >> "+log+"\nexec "+real+" \"$@\"\n")
	os.Chmod(filepath.Join(d, "git"), 0o755)
	t.Setenv("PATH", d+":"+os.Getenv("PATH"))
	return func() []string {
		b, _ := os.ReadFile(log)
		return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(b)), " ", "_"))
	}
}

// featCodeTrees serves featMerge's fields from the background refresh's measurement, with no git in the request;
// what it hasn't measured (another branch, a changed main) is "not measured", never copied or guessed.
func TestFeatCodeTreesNoGitInRequest(t *testing.T) {
	resetFeatCode()
	defer func() { setRefreshHook(nil); resetFeatCode() }()
	fx := newCodeFixture(t)
	live := []featTree{{Root: fx.feat, Branch: "feature/x", Agents: []string{"Lead"}}, {Root: fx.api, Branch: "feat/api", Agents: []string{"Coder", "Coder2"}}, {Root: fx.lt, Branch: "main", Agents: []string{"T"}}}
	measured := featMerge(slices.Clone(live), fx.feat)                              // what a refresh stores (real git, before the shim)
	setRefreshHook(func(*server, string) codeIn { return codeIn{now: time.Now()} }) // the background refresh runs no git here
	s := &server{}
	calls := gitShim(t)

	// nothing measured yet: Main chosen, the rest not measured
	got, at := s.featCodeTrees("demo", live, fx.feat)
	if at != 0 || !got[0].Main || got[1].Checked || got[1].Ahead != 0 || got[2].OtherRepo {
		t.Fatalf("before any refresh: %+v at %d", got, at)
	}
	time.Sleep(50 * time.Millisecond) // let the (no-op) background refresh end
	resetFeatCode()
	featCode.Lock()
	featCode.snaps["demo"] = codeSnap{at: time.Now(), trees: measured}
	featCode.Unlock()

	got, at = s.featCodeTrees("demo", live, fx.feat)
	if at == 0 || !got[0].Main || !got[1].Checked || got[1].Ahead != measured[1].Ahead || !got[2].OtherRepo {
		t.Fatalf("from the measurement: %+v (measured %+v)", got, measured)
	}
	// another branch in that root, and a main that moved: not measured
	got, _ = s.featCodeTrees("demo", []featTree{{Root: fx.api, Branch: "other"}}, fx.feat)
	if got[0].Checked {
		t.Fatalf("another branch got the old measurement: %+v", got[0])
	}
	got, _ = s.featCodeTrees("demo", live, "") // main now the tree with most agents (api): the measurement was against feat
	if !got[1].Main || got[0].Checked || got[2].Checked || got[2].OtherRepo {
		t.Fatalf("a changed main kept measurements against the old one: %+v", got)
	}
	if c := calls(); len(c) != 0 {
		t.Fatalf("git ran in the request: %q", c)
	}
}

// A real refresh stores featMerge's answer, including the Main worktree line's tree, for featCodeTrees.
func TestFeatCodeRefreshStoresMergeTrees(t *testing.T) {
	resetFeatCode()
	defer resetFeatCode()
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	home := v.e.s.cfg.home
	v.e.s.cfg.pinFile = filepath.Join(home, "cp-pin", "pin")
	fx := newCodeFixtureIn(t, filepath.Join(home, "work"))
	writeFile(t, filepath.Join(home, "jarvis-files", "threads", supTeam, "FEATURE.md"), "Main worktree: "+fx.feat+"\n")
	v.e.s.prs.fetch = func(string, ...string) ([]byte, error) { return nil, errors.New("offline") }
	refreshNow(v.e.s, supTeam)
	calls := gitShim(t)
	got, at := v.e.s.featCodeTrees(supTeam, []featTree{{Root: fx.feat, Branch: "feature/x"}}, fx.feat)
	if at == 0 || !got[0].Main {
		t.Fatalf("main tree from the refresh: %+v at %d", got, at)
	}
	featCode.Lock()
	n := len(featCode.snaps[supTeam].trees)
	featCode.Unlock()
	if n != 1 {
		t.Fatalf("stored trees: %d", n)
	}
	if c := calls(); len(c) != 0 {
		t.Fatalf("git ran in the request: %q", c)
	}
}

// refreshNow: one refresh, synchronously, as single-flight as the page's (featCodeSnap marks it busy first), so the
// featFacts it reads (whose featCodeTrees would otherwise start a second one) doesn't race it
func refreshNow(s *server, session string) {
	featCode.Lock()
	featCode.busy[session] = true
	featCode.Unlock()
	s.featCodeRefresh(session)
}

// The integration branch feature/<session> always gets a row, read from its ref, even when no agent works in its
// worktree; the branches cut from it hang under it, and the merges into it show on its own (first-parent) line.
func TestFeatCodeIntegrationBranchRow(t *testing.T) {
	fx := newCodeFixture(t)
	in := fx.in(t)
	in.session = "x" // its integration branch: feature/x, checked out in wt-feat by nobody on the team
	in.trees = []featTree{{Root: fx.api, Branch: "feat/api", Agents: []string{"Coder"}}}
	in.team = []featAgent{{Key: "s:1", Tab: "Coder", Root: fx.api, Branch: "feat/api"}}
	bs, _, _ := in.build()
	integ, api := findBranch(bs, "acme/app:feature/x"), findBranch(bs, "acme/app:feat/api")
	if integ == nil || api == nil {
		t.Fatalf("rows: %+v", bs)
	}
	if integ.WtSource != "git ref (no agent works in a worktree of it)" || integ.Wt != fx.feat || integ.Head != fx.m1 || len(integ.Agents) != 0 || integ.Main {
		t.Fatalf("integration row: %+v", integ)
	}
	if integ.Parent == nil || *integ.Parent != "acme/app:main" || integ.SrcMethod != "reflog" { // its own parent from its reflog, never its children's
		t.Fatalf("integration parent: %+v", integ)
	}
	if len(integ.Commits) != 2 || integ.Commits[0].Kind != "merge" || integ.Commits[0].Sha != fx.m1 {
		t.Fatalf("the lane merge on the integration line: %+v", integ.Commits)
	}
	if *api.Parent != integ.ID {
		t.Fatalf("the lane's parent %q isn't the integration row %q", *api.Parent, integ.ID)
	}
	// with no reflog its parent is unknown, never the branch working off it (the main-worktree fallback is for worktrees)
	gitT(t, fx.app, "reflog", "expire", "--expire=all", "refs/heads/feature/x")
	bs, _, _ = in.build()
	if integ = findBranch(bs, "acme/app:feature/x"); integ == nil || integ.Parent != nil {
		t.Fatalf("integration row without a reflog: parent %v", integ.Parent)
	}
	// a branch that doesn't exist locally gets no row: never a guessed one
	in.session = "nope"
	bs, _, _ = in.build()
	if findBranch(bs, "acme/app:feature/nope") != nil {
		t.Fatal("a row for a branch that isn't there")
	}
}
