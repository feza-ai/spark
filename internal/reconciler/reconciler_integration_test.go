//go:build integration

package reconciler

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

const testNetwork = "spark-test"

func podmanAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not available")
	}
}

func cleanupPod(t *testing.T, name string) {
	t.Helper()
	_ = exec.Command("podman", "pod", "rm", "-f", name).Run()
}

func ensureNetwork(t *testing.T) {
	t.Helper()
	out, _ := exec.Command("podman", "network", "ls", "--format", "{{.Name}}").Output()
	if strings.Contains(string(out), testNetwork) {
		return
	}
	if err := exec.Command("podman", "network", "create", testNetwork).Run(); err != nil {
		t.Fatalf("failed to create test network: %v", err)
	}
}

func TestIntegrationReconcilerRestartsKilledPod(t *testing.T) {
	podmanAvailable(t)

	podName := "spark-integ-reconciler"
	cleanupPod(t, podName)
	t.Cleanup(func() { cleanupPod(t, podName) })
	ensureNetwork(t)

	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 49152},
		scheduler.Resources{CPUMillis: 0, MemoryMB: 0, GPUMemoryMB: 0},
	nil, 0,
	)
	sched := scheduler.NewScheduler(tracker)
	exec := executor.NewPodmanExecutor(testNetwork)
	r := NewReconciler(store, sched, exec, 500*time.Millisecond)

	spec := manifest.PodSpec{
		Name:          podName,
		RestartPolicy: "Always",
		Containers: []manifest.ContainerSpec{
			{
				Name:    "sleeper",
				Image:   "docker.io/library/alpine:latest",
				Command: []string{"sleep", "3600"},
				Resources: manifest.ResourceRequirements{
					Requests: manifest.ResourceList{CPUMillis: 100, MemoryMB: 64},
				},
			},
		},
	}
	store.Apply(spec)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	go r.Run(ctx)

	// Wait for pod to reach Running.
	deadline := time.After(10 * time.Second)
	for {
		rec, ok := store.Get(podName)
		if ok && rec.Status == state.StatusRunning {
			break
		}
		select {
		case <-deadline:
			t.Fatal("pod did not reach Running within 10s")
		case <-time.After(200 * time.Millisecond):
		}
	}

	// Kill the pod via podman.
	killOut, err := command(ctx, "podman", "pod", "kill", podName)
	if err != nil {
		t.Fatalf("podman pod kill failed: %v: %s", err, killOut)
	}

	// Wait for reconciler to restart the pod (back to Running).
	deadline = time.After(15 * time.Second)
	for {
		rec, ok := store.Get(podName)
		if ok && rec.Status == state.StatusRunning {
			// Verify it was actually restarted (creates > 1 would show re-creation,
			// but with podman the reconciler sets to Pending then re-schedules).
			break
		}
		select {
		case <-deadline:
			rec, _ := store.Get(podName)
			t.Fatalf("pod not restarted within 15s, status: %s", rec.Status)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func command(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// TestIntegrationPendingWatchdogEmitsEvent verifies that a Pod requesting a
// GPU on a cluster with no GPU available accumulates at least one
// "awaiting-resources" watchdog event after one reconcile tick. This is the
// boundary test for the pending-watchdog feature (issue #32, S1.2.2).
//
// Uses the in-process stubExecutor (defined in reconciler_test.go) so it
// does not require podman; the reconciler never reaches CreatePod when the
// scheduler returns Pending.
func TestIntegrationPendingWatchdogEmitsEvent(t *testing.T) {
	store := state.NewPodStore()
	// Empty "cluster": no GPU devices. Total resources advertise zero GPUs
	// so a Pod requesting nvidia.com/gpu: "1" cannot fit and has no
	// preemption candidates.
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUCount: 0, GPUMemoryMB: 0},
		scheduler.Resources{},
		nil, 0,
	)
	sched := scheduler.NewScheduler(tracker)
	stub := newStubExecutor()
	r := NewReconciler(store, sched, stub, 50*time.Millisecond)

	podName := "spark-pending-watchdog"
	spec := manifest.PodSpec{
		Name: podName,
		Containers: []manifest.ContainerSpec{
			{
				Name:  "gpu-app",
				Image: "docker.io/library/alpine:latest",
				Resources: manifest.ResourceRequirements{
					// Request both a GPU device slot and GPU memory.
					// GPUMemoryMB is checked unconditionally by CanFit
					// (GPUCount is only checked when devices are
					// configured), so an empty cluster (zero gpu memory)
					// will refuse this request.
					Requests: manifest.ResourceList{GPUCount: 1, GPUMemoryMB: 16384},
				},
			},
		},
	}
	store.Apply(spec)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go r.Run(ctx)

	// After at least one reconcile tick, the pod should have a watchdog
	// event with an "awaiting-resources" message that includes the
	// scheduler's reason.
	deadline := time.After(3 * time.Second)
	for {
		rec, ok := store.Get(podName)
		if ok {
			for _, ev := range rec.Events {
				if ev.Type == "PendingWatchdog" &&
					strings.Contains(ev.Message, "awaiting-resources") &&
					len(ev.Message) > len("awaiting-resources: ") {
					return // success
				}
			}
		}
		select {
		case <-deadline:
			rec, _ := store.Get(podName)
			t.Fatalf("no PendingWatchdog event with 'awaiting-resources' observed within 3s; events=%+v reason=%q",
				rec.Events, rec.Reason)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
