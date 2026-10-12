package demand

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

func runner(name string, status state.PodStatus) state.PodRecord {
	return state.PodRecord{Spec: manifest.PodSpec{Name: name, OnDemand: &manifest.OnDemandSpec{Mode: "github", GitHubRepo: "owner/repo", RunnerName: name, WorkerContainer: "runner", RunnerLabels: []string{"self-hosted", "linux", "arm64", "dgx"}}}, Status: status}
}

func TestRunnerSelection(t *testing.T) {
	pods := []state.PodRecord{runner("cold", state.StatusDormant), runner("warm", state.StatusRunning), runner("busy", state.StatusRunning)}
	jobs := []job{{ID: 1, Status: "in_progress", RunnerName: "busy"}, {ID: 2, Status: "queued", Labels: []string{"self-hosted", "Linux", "ARM64", "dgx"}}, {ID: 3, Status: "queued", Labels: []string{"ubuntu-latest"}}}
	got := selectRunners(pods, jobs)
	if len(got) != 1 || !got["warm"] {
		t.Fatalf("selection=%v", got)
	}
	jobs = append(jobs, job{ID: 4, Status: "queued", Labels: []string{"dgx"}})
	got = selectRunners(pods, jobs)
	if len(got) != 2 || !got["cold"] || !got["warm"] {
		t.Fatalf("selection=%v", got)
	}
}

func TestQueuePollingAndUnavailableSource(t *testing.T) {
	var unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing auth")
		}
		if unavailable.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/jobs") {
			fmt.Fprint(w, `{"jobs":[{"id":1,"status":"queued","labels":["dgx"]}]}`)
			return
		}
		if r.URL.Query().Get("status") == "in_progress" {
			fmt.Fprint(w, `{"workflow_runs":[{"id":10}]}`)
			return
		}
		fmt.Fprint(w, `{"workflow_runs":[]}`)
	}))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := state.NewPodStore()
	s.Apply(runner("a", state.StatusDormant).Spec)
	wakes := 0
	c, err := NewGitHubController(s, func() { wakes++ }, token, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = server.URL
	c.poll(context.Background())
	p, _ := s.Get("a")
	if !p.QueueDemand || p.QueueCheckedAt.IsZero() || wakes != 1 {
		t.Fatalf("queue demand missing: %+v", p)
	}
	unavailable.Store(true)
	c.poll(context.Background())
	p, _ = s.Get("a")
	if p.QueueDemand || !p.QueueCheckedAt.IsZero() {
		t.Fatal("stale source retained")
	}
}

func TestActiveJobNeverWakesReplacement(t *testing.T) {
	got := selectRunners([]state.PodRecord{runner("a", state.StatusDormant)}, []job{{Status: "in_progress", RunnerName: "a", Labels: []string{"dgx"}}})
	if len(got) != 0 {
		t.Fatal("attempted to resume an already assigned job")
	}
}
