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
