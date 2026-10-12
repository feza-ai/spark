package reconciler

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

func lazyPod(backend string) manifest.PodSpec {
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(backend, "http://"))
	n, _ := strconv.Atoi(port)
	p := testPodSpec("model", "Always", 3)
	p.OnDemand = &manifest.OnDemandSpec{HostPort: n, ReadinessPath: "/ready", IdleTimeout: time.Minute, StartupTimeout: time.Minute, MaxRequests: 64}
	return p
}

func TestOnDemandLifecycle(t *testing.T) {
	var ready atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer backend.Close()
	s := state.NewPodStore()
	p := lazyPod(backend.URL)
	s.Apply(p)
	s.SetRawManifest(p.Name, []byte("manifest"))
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(s, sched, exec, time.Second)
	ctx := context.Background()
	r.reconcileOnce(ctx)
	if len(exec.creates) != 0 || sched.Tracker().Allocated().CPUMillis != 0 {
		t.Fatal("dormant pod was scheduled")
	}
	_, release, err := s.AcquireRequest(p.Name)
	if err != nil {
		t.Fatal(err)
	}
	_, release2, err := s.AcquireRequest(p.Name)
	if err != nil {
		t.Fatal(err)
	}
	r.reconcileOnce(ctx)
	r.reconcileOnce(ctx)
	rec, _ := s.Get(p.Name)
	if rec.Ready || rec.Status != state.StatusRunning || len(exec.creates) != 1 {
		t.Fatalf("unexpected startup: %+v, creates=%d", rec, len(exec.creates))
	}
	ready.Store(true)
	r.reconcileOnce(ctx)
	rec, _ = s.Get(p.Name)
	if !rec.Ready {
		t.Fatal("never became ready")
	}
	r.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
	r.reconcileOnce(ctx)
	if len(exec.stops) != 0 {
		t.Fatal("stopped active requests")
	}
	release()
	release2()
	r.reconcileOnce(ctx)
	rec, _ = s.Get(p.Name)
	if rec.Status != state.StatusDormant || len(exec.stops) != 1 || sched.Tracker().Allocated().CPUMillis != 0 || string(rec.RawManifest) != "manifest" {
		t.Fatalf("failed eviction: %+v", rec)
	}
	_, release, err = s.AcquireRequest(p.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r.reconcileOnce(ctx)
	rec, _ = s.Get(p.Name)
	if !rec.Ready || len(exec.creates) != 2 {
		t.Fatal("did not reload")
	}
}

func TestIdleCleanupFailureRetainsReservation(t *testing.T) {
	s := state.NewPodStore()
	p := lazyPod("http://127.0.0.1:1")
	s.Apply(p)
	sched := newTestScheduler()
	exec := newStubExecutor()
	r := NewReconciler(s, sched, exec, time.Second)
	_, release, _ := s.AcquireRequest(p.Name)
	r.reconcileOnce(context.Background())
	release()
	exec.removeErr = errors.New("cleanup failed")
	r.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
	r.reconcileOnce(context.Background())
	rec, _ := s.Get(p.Name)
	if rec.Status != state.StatusStopping || sched.Tracker().Allocated().CPUMillis == 0 {
		t.Fatal("failed cleanup released reservation")
	}
	_, release, _ = s.AcquireRequest(p.Name)
	defer release()
	exec.removeErr = nil
	r.reconcileOnce(context.Background())
	r.reconcileOnce(context.Background())
	if len(exec.creates) != 2 {
		t.Fatal("request during eviction did not reactivate")
	}
}

func TestOnDemandRecoveryAndAlwaysWarm(t *testing.T) {
	for _, status := range []state.PodStatus{state.StatusDormant, state.StatusRunning, state.StatusStopping} {
		t.Run(string(status), func(t *testing.T) {
			s := state.NewPodStore()
			p := lazyPod("http://127.0.0.1:1")
			s.LoadFrom(map[string]*state.PodRecord{p.Name: {Spec: p, Status: status}})
			exec := newStubExecutor()
			if status != state.StatusDormant {
				exec.listPods = []executor.PodListEntry{{Name: p.Name, Running: true}}
			}
			sched := newTestScheduler()
			r := NewReconciler(s, sched, exec, time.Second)
			if err := r.RecoverPods(context.Background()); err != nil {
				t.Fatal(err)
			}
			rec, _ := s.Get(p.Name)
			if rec.Status != status {
				t.Fatalf("recovery changed %s to %s", status, rec.Status)
			}
			if status == state.StatusStopping && sched.Tracker().Allocated().CPUMillis == 0 {
				t.Fatal("stopping pod not accounted during recovery")
			}
		})
	}
	s := state.NewPodStore()
	p := lazyPod("http://127.0.0.1:1")
	p.OnDemand.AlwaysWarm = true
	s.Apply(p)
	exec := newStubExecutor()
	r := NewReconciler(s, newTestScheduler(), exec, time.Second)
	r.reconcileOnce(context.Background())
	if len(exec.creates) != 1 {
		t.Fatal("always-warm pod did not start")
	}
	r.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
	r.reconcileOnce(context.Background())
	if len(exec.stops) != 0 {
		t.Fatal("always-warm pod evicted")
	}
}

func TestReadinessDoesNotFollowRedirect(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/login", http.StatusFound) }))
	defer backend.Close()
	s := state.NewPodStore()
	p := lazyPod(backend.URL)
	s.Apply(p)
	s.UpdateStatus(p.Name, state.StatusRunning, "started")
	r := NewReconciler(s, newTestScheduler(), newStubExecutor(), time.Second)
	rec, _ := s.Get(p.Name)
	r.checkReadiness(context.Background(), rec)
	rec, _ = s.Get(p.Name)
	if rec.Ready {
		t.Fatal("redirect treated as ready")
	}
}

func TestGitHubWorkerRetiresOnlyAfterProcessExit(t *testing.T) {
	s := state.NewPodStore()
	p := testPodSpec("runner", "Always", 3)
	p.OnDemand = &manifest.OnDemandSpec{Mode: "github", WorkerContainer: "worker", IdleTimeout: time.Second, MaxRequests: 1}
	s.Apply(p)
	s.SetQueueDemand(p.Name, true, time.Now())
	exec := newStubExecutor()
	sched := newTestScheduler()
	r := NewReconciler(s, sched, exec, time.Second)
	r.reconcileOnce(context.Background())
	s.SetQueueDemand(p.Name, false, time.Now())
	r.SetClock(func() time.Time { return time.Now().Add(30 * time.Second) })
	r.reconcileOnce(context.Background())
	if len(exec.stops) != 0 {
		t.Fatal("queue looked idle but live runner was stopped")
	}
	exec.setStatus(p.Name, executor.Status{Running: true, Containers: []executor.ContainerStatus{{Name: "runner-worker", Running: false, ExitCode: 0}, {Name: "runner-postgres", Running: true}}})
	r.reconcileOnce(context.Background())
	rec, _ := s.Get(p.Name)
	if !rec.WorkerRetired {
		t.Fatal("finished worker not retired")
	}
	r.reconcileOnce(context.Background())
	rec, _ = s.Get(p.Name)
	if rec.Status != state.StatusDormant || len(exec.containerStarts) != 0 {
		t.Fatal("finished worker restarted instead of returning dormant")
	}
	s.SetQueueDemand(p.Name, true, time.Now())
	r.reconcileOnce(context.Background())
	if len(exec.creates) != 2 {
		t.Fatal("next job did not activate a new runner")
	}
}
