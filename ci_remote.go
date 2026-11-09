package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The "AWS EC2" fleet: runners on another host, so there's no runner.pid or log to read here. Their state comes from
// GitHub (outbound gh only): online/offline and busy from repos/<repo>/actions/runners, and a busy one's job and steps
// from the in-progress runs' jobs. Every runner carries when GitHub last answered; a poll that fails or is over 60 s
// old makes them all "stale" (never "idle": we don't know).

// ec2RunnerName matches the names of the runners that live on other hosts.
var ec2RunnerName = regexp.MustCompile(`^aws-ec2-[a-z0-9-]+$`)

// ciRemote polls GitHub for the runners that live on other hosts and keeps the last answer.
type ciRemote struct {
	repos func() []string                   // the repos to ask (the AWS runners' own)
	gh    func(path string) ([]byte, error) // gh api <path> --paginate --slurp

	mu      sync.Mutex
	pending bool
	attempt time.Time
	checked time.Time // the last poll that fully worked
	err     string
	runners []Runner
}

// ghRunnerPage is one page of GitHub's list of a repo's runners.
type ghRunnerPage struct {
	Runners []struct {
		Name   string `json:"name"`
		Status string `json:"status"` // online, offline
		Busy   bool   `json:"busy"`
	} `json:"runners"`
}

// ghRunPage is one page of GitHub's list of a repo's workflow runs.
type ghRunPage struct {
	Runs []struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Event      string `json:"event"`
		HeadBranch string `json:"head_branch"`
		Actor      struct {
			Login string `json:"login"`
		} `json:"actor"`
		PRs []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"workflow_runs"`
}

// get starts a poll in the background when the last is 20 s old, and returns what GitHub said last, aged.
func (c *ciRemote) get(now time.Time) ([]Runner, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.pending && now.Sub(c.attempt) >= 20*time.Second {
		c.pending, c.attempt = true, now
		go c.fetch()
	}
	out := make([]Runner, len(c.runners))
	copy(out, c.runners)
	stale := c.err != "" || now.Sub(c.checked) > 60*time.Second
	for i := range out {
		if stale {
			out[i].State = "stale"
		}
	}
	msg := ""
	if c.err != "" {
		msg = "AWS EC2 runners: " + c.err
	}
	return out, msg
}

// fetch runs one poll and stores the result: the runners, or the error and the previous runners kept.
func (c *ciRemote) fetch() {
	rs, err := c.list()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = false
	if err != nil {
		c.err = trunc(err.Error(), 200)
		return
	}
	c.err, c.checked, c.runners = "", time.Now(), rs
}

// list asks GitHub, for each repo, which runners are online and busy and what the busy ones are running.
func (c *ciRemote) list() ([]Runner, error) {
	var out []Runner
	at := time.Now().Unix()
	for _, repo := range c.repos() {
		b, err := c.gh("repos/" + repo + "/actions/runners?per_page=100")
		if err != nil {
			return nil, fmt.Errorf("%s runners: %w", repo, err)
		}
		var pages []ghRunnerPage
		if err := json.Unmarshal(b, &pages); err != nil {
			return nil, err
		}
		busy := 0
		start := len(out)
		for _, p := range pages {
			for _, r := range p.Runners {
				if !ec2RunnerName.MatchString(r.Name) {
					continue
				}
				st := Runner{Name: r.Name, Fleet: "AWS EC2", State: "idle", CheckedAt: at}
				if r.Status != "online" {
					st.State = "offline"
				} else if r.Busy {
					st.State, st.Job = "busy", "Starting a job"
					busy++
				}
				out = append(out, st)
			}
		}
		if busy == 0 {
			continue
		}
		if err := c.jobs(repo, out[start:], at); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// jobs fills in the busy runners' job, steps and start from the repo's in-progress runs
func (c *ciRemote) jobs(repo string, rs []Runner, at int64) error {
	b, err := c.gh("repos/" + repo + "/actions/runs?status=in_progress&per_page=100")
	if err != nil {
		return fmt.Errorf("%s runs: %w", repo, err)
	}
	var pages []ghRunPage
	if err := json.Unmarshal(b, &pages); err != nil {
		return err
	}
	for _, p := range pages {
		for _, run := range p.Runs {
			id := strconv.FormatInt(run.ID, 10)
			b, err := c.gh("repos/" + repo + "/actions/runs/" + id + "/jobs?per_page=100")
			if err != nil {
				return fmt.Errorf("%s run %s: %w", repo, id, err)
			}
			var jp []struct {
				Jobs []ciGitHubJob `json:"jobs"`
			}
			if err := json.Unmarshal(b, &jp); err != nil {
				return err
			}
			for _, pg := range jp {
				for _, j := range pg.Jobs {
					for i := range rs {
						r := &rs[i]
						if r.State != "busy" || j.Runner != r.Name || j.Status != "in_progress" {
							continue
						}
						r.Job, r.Since = j.Name, j.Started.Unix()
						r.Meta = &JobMeta{Repo: repo, RunID: id, Workflow: run.Name, Event: run.Event, Branch: run.HeadBranch, Actor: run.Actor.Login}
						if len(run.PRs) > 0 {
							r.Meta.PR = run.PRs[0].Number
						}
						pr := &CIJobProgress{CheckedAt: at, Status: j.Status, Steps: j.Steps, Total: len(j.Steps)}
						for _, s := range j.Steps {
							if s.Status == "completed" {
								pr.Completed++
							}
						}
						r.Progress = pr
					}
				}
			}
		}
	}
	return nil
}

// ghSlurp runs gh api path --paginate --slurp and returns the JSON (one array of pages).
func ghSlurp(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", path, "--paginate", "--slurp")
	cmd.Env = cleanEnv(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, fmt.Errorf("%s", s)
		}
		return nil, err
	}
	return b, nil
}

// awsRepos is the fallback list of repos whose self-hosted EC2 runners the CI panel asks GitHub about,
// from CP_AWS_REPOS (comma-separated owner/repo). Unset or empty means none, so that fleet just shows nothing.
var awsRepos = parseRepoList(os.Getenv("CP_AWS_REPOS"))

// parseRepoList splits a comma-separated list of owner/repo, trimming spaces and skipping empty entries.
func parseRepoList(s string) []string {
	var out []string
	for _, r := range strings.Split(s, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// awsRunnerRepos returns a func that lists the repos the runners under dir are registered to (read off each
// runner's .runner file), falling back to awsRepos when there are none.
func awsRunnerRepos(dir string) func() []string {
	return func() []string {
		files, _ := filepath.Glob(filepath.Join(dir, "*", ".runner"))
		seen, out := map[string]bool{}, []string{}
		for _, f := range files {
			b, _ := os.ReadFile(f)
			var reg struct {
				URL string `json:"gitHubUrl"`
			}
			if json.Unmarshal(bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf}), &reg) != nil {
				continue
			}
			repo := strings.TrimPrefix(reg.URL, "https://github.com/")
			if repoRe.MatchString(repo) && !seen[repo] {
				seen[repo] = true
				out = append(out, repo)
			}
		}
		if len(out) == 0 {
			return awsRepos
		}
		return out
	}
}
