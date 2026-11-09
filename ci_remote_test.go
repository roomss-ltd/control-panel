package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteRunnersFromGitHub(t *testing.T) {
	var fail atomic.Bool // read by the poll get() starts in the background
	gh := func(path string) ([]byte, error) {
		switch {
		case fail.Load():
			return nil, errors.New("HTTP 502")
		case strings.HasSuffix(path, "/actions/runners?per_page=100"):
			return []byte(`[{"runners":[{"name":"aws-ec2-be-1","status":"online","busy":true},{"name":"aws-ec2-be-2","status":"online","busy":false},
				{"name":"aws-ec2-be-3","status":"offline","busy":false},{"name":"aws-vm-be-light-1","status":"online","busy":true}]}]`), nil
		case strings.Contains(path, "/actions/runs?status=in_progress"):
			return []byte(`[{"workflow_runs":[{"id":77,"name":"CI","event":"pull_request","head_branch":"feat/x","pull_requests":[{"number":12}]}]}]`), nil
		case strings.HasSuffix(path, "/runs/77/jobs?per_page=100"):
			return []byte(`[{"jobs":[{"name":"test","runner_name":"aws-ec2-be-1","status":"in_progress","started_at":"2026-11-05T10:00:00Z",
				"steps":[{"name":"checkout","status":"completed"},{"name":"go test","status":"in_progress"}]}]}]`), nil
		}
		t.Fatalf("unexpected %s", path)
		return nil, nil
	}
	c := &ciRemote{repos: func() []string { return []string{"o/r"} }, gh: gh}
	c.fetch()
	rs, msg := c.get(time.Now())
	if msg != "" || len(rs) != 3 {
		t.Fatalf("%q %+v", msg, rs)
	}
	st := map[string]Runner{}
	for _, r := range rs {
		st[r.Name] = r
	}
	b := st["aws-ec2-be-1"]
	if b.State != "busy" || b.Job != "test" || b.Meta.PR != 12 || b.Meta.RunID != "77" || b.Progress.Completed != 1 || b.Progress.Total != 2 || b.Since == 0 || b.CheckedAt == 0 {
		t.Fatalf("busy: %+v %+v", b, b.Progress)
	}
	if st["aws-ec2-be-2"].State != "idle" || st["aws-ec2-be-3"].State != "offline" || b.Fleet != "AWS EC2" {
		t.Fatalf("%+v", st)
	}
	// GitHub stops answering: the last known runners stay, all stale, with the error said
	fail.Store(true)
	c.fetch()
	rs, msg = c.get(time.Now())
	if len(rs) != 3 || msg == "" {
		t.Fatalf("%q %+v", msg, rs)
	}
	for _, r := range rs {
		if r.State != "stale" {
			t.Fatalf("not stale: %+v", r)
		}
	}
	// and a poll over 60 s old is stale too, never idle
	fail.Store(false)
	c.fetch()
	if rs, _ = c.get(time.Now().Add(61 * time.Second)); rs[0].State != "stale" {
		t.Fatalf("%+v", rs[0])
	}
}

func TestParseRepoList(t *testing.T) {
	if got := parseRepoList(" acme/api, ,acme/web ,"); strings.Join(got, "|") != "acme/api|acme/web" {
		t.Fatalf("%q", got)
	}
	if got := parseRepoList(""); len(got) != 0 {
		t.Fatalf("unset must mean no repos: %q", got)
	}
}
