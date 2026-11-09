package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The owner's page (GET /api/term/feature) runs no git in the request, even with git stalled: parallel GETs
// answer promptly from the background measurement or as "not measured" (checked false, treesAt 0), and start one
// refresh.
func TestV8PageTreesNoGitInRequest(t *testing.T) {
	resetFeatCode()
	defer func() { setRefreshHook(nil); resetFeatCode() }()
	v := newSupEnv(t, "## Ship\n- [ ] Write docs @Builder\n")
	fx := newCodeFixture(t)
	p := v.agent("working", v.now.Unix())
	p.Git = &GitInfo{Root: fx.feat, Branch: "feature/x"}
	v.look(p)
	var refreshes atomic.Int32
	release := make(chan struct{})
	setRefreshHook(func(*server, string) codeIn { refreshes.Add(1); <-release; return codeIn{now: time.Now()} }) // a stalled refresh
	defer close(release)
	d := t.TempDir() // a git that stalls 8 s and logs every call
	log := filepath.Join(d, "calls")
	writeFile(t, filepath.Join(d, "git"), "#!/bin/sh\necho \"$*\" >> "+log+"\nsleep 8\n")
	os.Chmod(filepath.Join(d, "git"), 0o755)
	t.Setenv("PATH", d+":"+os.Getenv("PATH"))
	h := v.e.authed(v.e.login(t))
	start := time.Now()
	var wg sync.WaitGroup
	outs := make([]struct {
		Trees   []featTree
		TreesAt *int64
	}, 10)
	for i := range outs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			decodeBody(t, v.e.do(t, "GET", "/api/term/feature?session="+supTeam, "", h), &outs[i])
		}()
	}
	wg.Wait()
	if el := time.Since(start); el > time.Second {
		b, _ := os.ReadFile(log)
		t.Fatalf("10 GETs took %v with git stalled; git calls: %q", el, b)
	}
	for _, o := range outs {
		if len(o.Trees) != 1 || o.Trees[0].Root != fx.feat || o.Trees[0].Checked || o.Trees[0].Ahead != 0 || o.TreesAt == nil || *o.TreesAt != 0 {
			t.Fatalf("not measured yet: %+v at %v", o.Trees, o.TreesAt)
		}
	}
	// the merge fields' git (featMerge) never runs here: the refresh is the stalled hook. A PR card's gh may still
	// start in the background (a first-sight agent), never in the request: the 1 s bound above holds against git's 8 s
	if b, _ := os.ReadFile(log); strings.Contains(string(b), "branch --show-current") || strings.Contains(string(b), "rev-parse") {
		t.Fatalf("featMerge's git ran: %q", b)
	}
	if n := refreshes.Load(); n != 1 {
		t.Fatalf("refreshes: %d (want one)", n)
	}
}
