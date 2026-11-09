package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPollBothRunnerFleets(t *testing.T) {
	legacy := setupRunners(t, map[string]string{"r1": "2026-11-04 12:00:00Z: Listening for Jobs\n"})
	aws := setupRunners(t, map[string]string{"aws-vm-be-heavy": "2026-11-04 12:22:45Z: Running job: publish (backend)\n"})
	c := &ciTracker{dir: legacy, extraDirs: []string{aws, aws}}
	got := c.poll(time.Now(), func(pid int, dir string) bool {
		return pid == 4242 && (strings.HasPrefix(dir, legacy) || strings.HasPrefix(dir, aws))
	})
	if len(got.Runners) != 2 || got.Runners[0].Name != "r1" || got.Runners[1].Fleet != "AWS" || got.Runners[1].State != "busy" {
		t.Fatalf("both fleets, no duplicates: %+v", got.Runners)
	}
	f, _ := os.OpenFile(filepath.Join(aws, "aws-vm-be-heavy", "runner.log"), os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("2026-11-04 12:50:00Z: Job publish (backend) completed with result: Succeeded\n")
	f.Close()
	got = c.poll(time.Now(), func(int, string) bool { return true })
	if got.Runners[1].State != "idle" || got.All.Jobs != 1 {
		t.Fatalf("completion lost: %+v", got)
	}
}

func TestCIProgressMatchesJobNotRunStateOrOldAttempt(t *testing.T) {
	r := Runner{Name: "aws-vm-be-heavy", Job: "publish (backend)", Since: 1000, Meta: &JobMeta{Repo: "owner/repo", RunID: "123"}}
	c := newCIProgressCache(func(string, string) ([]ciGitHubJob, error) { t.Error("fresh cache should not fetch"); return nil, nil })
	c.entries["owner/repo/123"] = ciProgressEntry{checked: time.Now(), attempt: time.Now(), jobs: []ciGitHubJob{
		{Name: r.Job, Runner: r.Name, Status: "completed", Started: time.Unix(500, 0)},
		{Name: r.Job, Runner: r.Name, Status: "in_progress", Started: time.Unix(1000, 0), Steps: []CIJobStep{{Name: "Build", Status: "completed", Conclusion: "success"}, {Name: "Scan", Status: "in_progress"}, {Name: "Tag", Status: "pending"}}},
	}}
	p := c.get(r)
	if p.Status != "in_progress" || p.Completed != 1 || p.Total != 3 || p.CheckedAt == 0 {
		t.Fatalf("progress: %+v", p)
	}
	if c.get(Runner{Meta: &JobMeta{Repo: "owner/repo", RunID: "123; bad"}}) != nil {
		t.Fatal("invalid ref")
	}
}

func TestCIProgressFailureRetainsLastReceipt(t *testing.T) {
	r := Runner{Name: "aws-vm-be-heavy", Job: "publish", Since: 1000, Meta: &JobMeta{Repo: "owner/repo", RunID: "1"}}
	c := newCIProgressCache(func(string, string) ([]ciGitHubJob, error) {
		return nil, errors.New("private credential must not appear")
	})
	c.entries["owner/repo/1"] = ciProgressEntry{checked: time.Unix(1000, 0), jobs: []ciGitHubJob{{Name: r.Job, Runner: r.Name, Started: time.Unix(1000, 0), Status: "in_progress"}}}
	c.get(r)
	for i := 0; i < 100; i++ {
		time.Sleep(time.Millisecond)
		p := c.get(r)
		if p.Error != "" {
			if p.CheckedAt != 1000 || p.Status != "in_progress" || strings.Contains(p.Error, "credential") {
				t.Fatalf("unsafe/stale metadata: %+v", p)
			}
			return
		}
	}
	t.Fatal("async failure not recorded")
}

func TestCIDurationNeedsComparableSuccessfulHistory(t *testing.T) {
	r := Runner{Name: "aws-vm-be-heavy", Job: "publish", Meta: &JobMeta{Repo: "owner/repo", Workflow: "aws-image"}}
	var jobs []Job
	for i, d := range []int64{100, 200, 300} {
		jobs = append(jobs, Job{Runner: r.Name, Name: r.Job, Result: "Succeeded", Start: int64(i + 1), End: int64(i+1) + d, Meta: r.Meta})
	}
	if n, samples := ciDurationEstimate(r, jobs[:2]); n != 0 || samples != 2 {
		t.Fatal(n, samples)
	}
	if n, samples := ciDurationEstimate(r, jobs); n != 200 || samples != 3 {
		t.Fatal(n, samples)
	}
	jobs[0].Result = "Failed"
	if n, _ := ciDurationEstimate(r, jobs); n != 0 {
		t.Fatal("included failed run")
	}
	jobs[0].Result = "Succeeded"
	jobs[0].Runner = "r1"
	if n, _ := ciDurationEstimate(r, jobs); n != 0 {
		t.Fatal("included legacy fleet")
	}
}

func TestAWSProcessGrouping(t *testing.T) {
	for _, mode := range []string{"path", "env", "cwd"} {
		var cmd, env []byte
		cwd := ""
		switch mode {
		case "path":
			cmd = []byte("/home/runner/ci-aws/runners/aws-vm-be-heavy/bin/Runner.Listener\x00run")
		case "env":
			env = []byte("RUNNER_NAME=aws-vm-be-heavy\x00")
		case "cwd":
			cwd = "/home/runner/ci-aws/runners/aws-vm-be-heavy/_work/app"
		}
		key, _ := groupKey(cmd, env, "bash", "init", 1, "", "", "", cwd)
		if key != "runner:aws-vm-be-heavy" {
			t.Fatal(mode, key)
		}
	}
}

func TestRegisteredLegacyRunnerProgress(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".runner"), []byte("\xef\xbb\xbf"+`{"agentName":"cube-r1"}`), 0600)
	if got := registeredRunnerName(dir, "r1"); got != "cube-r1" {
		t.Fatal(got)
	}
	r := Runner{Name: "r1", GitHubName: "cube-r1", Job: "test", Since: 1000, Meta: &JobMeta{Repo: "owner/repo", RunID: "1"}}
	c := newCIProgressCache(nil)
	c.entries["owner/repo/1"] = ciProgressEntry{checked: time.Now(), attempt: time.Now(), jobs: []ciGitHubJob{{Name: "test", Runner: "cube-r1", Started: time.Unix(1000, 0), Status: "in_progress"}}}
	if got := c.get(r); got.Status != "in_progress" {
		t.Fatalf("%+v", got)
	}
}

func TestAWSRunnerAliveRelativeScript(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/bash\nwhile :; do read -r -t 1 ignored || true; done\n"), 0700)
	cmd := exec.Command("bash", "./run.sh")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	if !runnerAlive(cmd.Process.Pid, dir) {
		t.Fatal("keeper-run relative script classified dead")
	}
	if runnerAlive(cmd.Process.Pid, dir+"/wrong") {
		t.Fatal("wrong runner accepted")
	}
}
