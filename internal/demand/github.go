// Package demand translates external work queues into workload activation.
package demand

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/feza-ai/spark/internal/state"
)

type GitHubController struct {
	store      *state.PodStore
	wake       func()
	tokenFile  string
	interval   time.Duration
	client     *http.Client
	baseURL    string
	lastErrors map[string]string
}

type job struct {
	ID         int64    `json:"id"`
	Status     string   `json:"status"`
	RunnerName string   `json:"runner_name"`
	Labels     []string `json:"labels"`
}

func NewGitHubController(store *state.PodStore, wake func(), tokenFile string, interval time.Duration) (*GitHubController, error) {
	if store == nil || interval <= 0 {
		return nil, fmt.Errorf("GitHub controller requires a store and positive poll interval")
	}
	return &GitHubController{store: store, wake: wake, tokenFile: tokenFile, interval: interval, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, baseURL: "https://api.github.com", lastErrors: make(map[string]string)}, nil
}

func (c *GitHubController) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		c.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *GitHubController) poll(ctx context.Context) {
	groups := make(map[string][]state.PodRecord)
	for _, p := range c.store.List("") {
		if p.Spec.OnDemand != nil && p.Spec.OnDemand.ActivationMode() == "github" {
			groups[p.Spec.OnDemand.GitHubRepo] = append(groups[p.Spec.OnDemand.GitHubRepo], p)
		}
	}
	if len(groups) == 0 {
		return
	}
	tokenBytes, tokenErr := os.ReadFile(c.tokenFile)
	token := strings.TrimSpace(string(tokenBytes))
	if tokenErr == nil && token == "" {
		tokenErr = fmt.Errorf("empty GitHub token file")
	}
	for repo, pods := range groups {
		jobs, err := []job(nil), tokenErr
		if err == nil {
			jobs, err = c.jobs(ctx, repo, token)
		}
		if err != nil {
			for _, p := range pods {
				c.store.SetQueueDemand(p.Spec.Name, false, time.Time{})
			}
			if c.lastErrors[repo] != err.Error() {
				c.lastErrors[repo] = err.Error()
				for _, p := range pods {
					c.store.AddEvent(p.Spec.Name, "QueuePollFailed", err.Error())
				}
				slog.Warn("GitHub queue poll failed", "repository", repo, "error", err)
			}
			continue
		}
		delete(c.lastErrors, repo)
		desired := selectRunners(pods, jobs)
		now := time.Now()
		for _, p := range pods {
			c.store.SetQueueDemand(p.Spec.Name, desired[p.Spec.Name], now)
		}
		if len(desired) > 0 && c.wake != nil {
			c.wake()
		}
	}
}

// selectRunners wakes at most one matching free runner per queued job. Active
// jobs are never used as a wake signal: they cannot be resumed in a new runner.
func selectRunners(pods []state.PodRecord, jobs []job) map[string]bool {
	busy := make(map[string]bool)
	for _, j := range jobs {
		if j.Status == "in_progress" {
			busy[j.RunnerName] = true
		}
	}
	sort.Slice(pods, func(i, j int) bool {
		// Reuse listeners already waiting for a job before starting another pod.
		wi := pods[i].Status == state.StatusRunning && !pods[i].WorkerRetired
		wj := pods[j].Status == state.StatusRunning && !pods[j].WorkerRetired
		if wi != wj {
			return wi
		}
		return pods[i].Spec.Name < pods[j].Spec.Name
	})
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	selected := make(map[string]bool)
	for _, j := range jobs {
		if j.Status != "queued" {
			continue
		}
		for _, p := range pods {
			cfg := p.Spec.OnDemand
			if selected[p.Spec.Name] || busy[cfg.RunnerName] || p.Status == state.StatusFailed || p.Status == state.StatusCompleted {
				continue
			}
			if j.RunnerName != "" && j.RunnerName != cfg.RunnerName {
				continue
			}
			labels := make(map[string]bool)
			for _, l := range cfg.RunnerLabels {
				labels[strings.ToLower(l)] = true
			}
			match := len(j.Labels) > 0
			for _, l := range j.Labels {
				if !labels[strings.ToLower(l)] {
					match = false
					break
				}
			}
			if match {
				selected[p.Spec.Name] = true
				break
			}
		}
	}
	return selected
}

func (c *GitHubController) jobs(ctx context.Context, repo, token string) ([]job, error) {
	// Both statuses matter: a running workflow can contain queued later jobs.
	runs := make(map[int64]bool)
	for _, status := range []string{"queued", "in_progress"} {
		for page := 1; ; page++ {
			if page > 10 {
				return nil, fmt.Errorf("GitHub workflow pagination exceeded safe poll limit")
			}
			var body struct {
				Runs []struct {
					ID int64 `json:"id"`
				} `json:"workflow_runs"`
			}
			path := fmt.Sprintf("/repos/%s/actions/runs?status=%s&per_page=100&page=%d", repo, status, page)
			if err := c.get(ctx, path, token, &body); err != nil {
				return nil, err
			}
			for _, r := range body.Runs {
				runs[r.ID] = true
			}
			if len(body.Runs) < 100 {
				break
			}
		}
	}
	var jobs []job
	for id := range runs {
		for page := 1; ; page++ {
			if page > 10 {
				return nil, fmt.Errorf("GitHub job pagination exceeded safe poll limit")
			}
			var body struct {
				Jobs []job `json:"jobs"`
			}
			path := fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?filter=latest&per_page=100&page=%d", repo, id, page)
			if err := c.get(ctx, path, token, &body); err != nil {
				return nil, err
			}
			jobs = append(jobs, body.Jobs...)
			if len(body.Jobs) < 100 {
				break
			}
		}
	}
	return jobs, nil
}

func (c *GitHubController) get(ctx context.Context, path, token string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "spark-workload-activator")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub queue request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		u, _ := url.Parse(path)
		return fmt.Errorf("GitHub %s returned HTTP %d", u.Path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode GitHub queue response: %w", err)
	}
	return nil
}
