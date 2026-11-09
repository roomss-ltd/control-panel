package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Step-by-step progress of the job a CI runner is on, fetched from GitHub Actions (outbound gh only) and cached,
// so the CI view can show "step 3 of 8" and estimate how long the job has left.

// awsRunnerName matches the names of the runners of the second fleet (shown as AWS), which live in their own
// directory beside the first fleet's.
var awsRunnerName = regexp.MustCompile(`^aws-vm-[a-z0-9-]+$`)

// ciRunID is what a GitHub Actions run id looks like.
var ciRunID = regexp.MustCompile(`^[0-9]+$`)

// Only public job metadata is exposed, never raw logs, commands or environment values.
type CIJobStep struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	Started    time.Time `json:"started_at"`
	Completed  time.Time `json:"completed_at"`
}

// ciGitHubJob is one job of a GitHub Actions run, as GitHub reports it.
type ciGitHubJob struct {
	Name    string      `json:"name"`
	Runner  string      `json:"runner_name"`
	Status  string      `json:"status"`
	Started time.Time   `json:"started_at"`
	Steps   []CIJobStep `json:"steps"`
}

// CIJobProgress is how far a running job has got: its steps and how many are completed.
type CIJobProgress struct {
	CheckedAt int64       `json:"checkedAt"`
	Status    string      `json:"status"`
	Steps     []CIJobStep `json:"steps,omitempty"`
	Completed int         `json:"completed"`
	Total     int         `json:"total"`
	Error     string      `json:"error,omitempty"`
}

// ciProgressEntry is the cached answer for one run, with when it was fetched or attempted.
type ciProgressEntry struct {
	checked time.Time
	attempt time.Time
	pending bool
	failed  bool
	jobs    []ciGitHubJob
}

// ciProgressCache keeps GitHub's step-by-step progress of the runs the runners are on, fetched in the background.
type ciProgressCache struct {
	mu      sync.Mutex
	entries map[string]ciProgressEntry
	fetch   func(string, string) ([]ciGitHubJob, error)
}

// newCIProgressCache makes a progress cache that fetches a run's jobs (repo, run id) with fetch.
func newCIProgressCache(fetch func(string, string) ([]ciGitHubJob, error)) *ciProgressCache {
	return &ciProgressCache{entries: map[string]ciProgressEntry{}, fetch: fetch}
}

// Poll asynchronously: a GitHub timeout must never freeze the panel's sampler.
func (c *ciProgressCache) get(r Runner) *CIJobProgress {
	if r.Meta == nil || !repoRe.MatchString(r.Meta.Repo) || !ciRunID.MatchString(r.Meta.RunID) {
		return nil
	}
	k := r.Meta.Repo + "/" + r.Meta.RunID
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[k]
	if !e.pending && time.Since(e.attempt) >= 20*time.Second {
		e.pending, e.attempt = true, time.Now()
		c.entries[k] = e
		// Bound the cache across long-running panel sessions.
		for key, old := range c.entries {
			if !old.pending && time.Since(old.attempt) > time.Hour {
				delete(c.entries, key)
			}
		}
		go func(repo, run string) {
			jobs, err := c.fetch(repo, run)
			c.mu.Lock()
			defer c.mu.Unlock()
			v := c.entries[k]
			v.pending, v.failed = false, err != nil
			if err == nil {
				v.jobs, v.checked = jobs, time.Now()
			}
			c.entries[k] = v
		}(r.Meta.Repo, r.Meta.RunID)
	}
	p := &CIJobProgress{Status: "pending"}
	githubName := r.GitHubName
	if githubName == "" {
		githubName = r.Name
	}
	if !e.checked.IsZero() {
		p.CheckedAt = e.checked.Unix()
	}
	if e.failed {
		p.Error = "GitHub progress unavailable; last successful update retained"
	}
	for _, j := range e.jobs {
		// Match this attempt, not an older retry on the same runner/run ID.
		if j.Runner != githubName || j.Name != r.Job || abs64(j.Started.Unix()-r.Since) > 120 {
			continue
		}
		p.Status, p.Steps, p.Total = j.Status, j.Steps, len(j.Steps)
		for _, step := range j.Steps {
			if step.Status == "completed" {
				p.Completed++
			}
		}
		break
	}
	return p
}

// ghCIJobs lists the jobs of a GitHub Actions run through gh.
func ghCIJobs(repo, run string) ([]ciGitHubJob, error) {
	if !repoRe.MatchString(repo) || !ciRunID.MatchString(run) {
		return nil, errors.New("invalid GitHub job reference")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", "repos/"+repo+"/actions/runs/"+run+"/jobs?per_page=100", "--paginate", "--slurp")
	cmd.Env = cleanEnv(os.Environ())
	b, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var pages []struct {
		Jobs []ciGitHubJob `json:"jobs"`
	}
	if err := json.Unmarshal(b, &pages); err != nil {
		return nil, err
	}
	var jobs []ciGitHubJob
	for _, p := range pages {
		jobs = append(jobs, p.Jobs...)
	}
	return jobs, nil
}

// A history-based duration is deliberately absent for first publications. A build is
// not comparable to a publish, nor another repository or workflow with the same name.
func ciDurationEstimate(r Runner, jobs []Job) (int64, int) {
	if r.Meta == nil {
		return 0, 0
	}
	var matching []Job
	for _, j := range jobs {
		if j.Result == "Succeeded" && j.End > j.Start && j.Name == r.Job && j.Meta != nil && j.Meta.Repo == r.Meta.Repo && j.Meta.Workflow == r.Meta.Workflow && awsRunnerName.MatchString(j.Runner) == awsRunnerName.MatchString(r.Name) {
			matching = append(matching, j)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].Start > matching[j].Start })
	matching = matching[:min(len(matching), 10)]
	if len(matching) < 3 {
		return 0, len(matching)
	}
	var durations []int64
	for _, j := range matching {
		durations = append(durations, j.End-j.Start)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[len(durations)/2], len(durations)
}

// .runner is non-secret registration metadata; credentials live in separate files.
func registeredRunnerName(dir, fallback string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".runner"))
	if err != nil {
		return fallback
	}
	var registration struct {
		Name string `json:"agentName"`
	}
	if json.Unmarshal(bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf}), &registration) != nil || registration.Name == "" {
		return fallback
	}
	return registration.Name
}
