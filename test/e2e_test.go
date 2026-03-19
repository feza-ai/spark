//go:build e2e

package test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/cron"
	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/reconciler"
	"github.com/feza-ai/spark/internal/scheduler"
	"github.com/feza-ai/spark/internal/state"
)

const testImage = "alpine:latest"

// requirePodman skips the test if podman is not available.
func requirePodman(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not available, skipping e2e test")
	}
}

// ensureNetwork creates the spark-net network if it does not exist.
func ensureNetwork(t *testing.T) {
	t.Helper()
	out, err := exec.Command("podman", "network", "exists", "spark-net").CombinedOutput()
	if err != nil {
		// Network does not exist, create it.
		out, err = exec.Command("podman", "network", "create", "spark-net").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to create spark-net: %s: %s", err, out)
		}
	}
}

// cleanupPod forcefully removes a pod by name, ignoring errors.
func cleanupPod(t *testing.T, name string) {
	t.Helper()
	_ = exec.Command("podman", "pod", "rm", "-f", name).Run()
}

// waitForStatus polls the store until the pod reaches the desired status or timeout.
func waitForStatus(t *testing.T, store *state.PodStore, name string, desired state.PodStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rec, ok := store.Get(name)
		if ok && rec.Status == desired {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	rec, ok := store.Get(name)
	if ok {
		t.Fatalf("pod %q did not reach status %q within %v (current: %q)", name, desired, timeout, rec.Status)
	} else {
		t.Fatalf("pod %q not found in store while waiting for status %q", name, desired)
	}
}

// newTestInfra creates a shared set of components for E2E tests.
func newTestInfra() (*state.PodStore, *scheduler.Scheduler, *executor.PodmanExecutor) {
	store := state.NewPodStore()
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 49152},
		scheduler.Resources{CPUMillis: 500, MemoryMB: 512, GPUMemoryMB: 0},
	)
	sched := scheduler.NewScheduler(tracker)
	exec := executor.NewPodmanExecutor("spark-net")
	return store, sched, exec
}

func TestE2E_DeploymentLifecycle(t *testing.T) {
	requirePodman(t)
	ensureNetwork(t)

	store, sched, ex := newTestInfra()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Parse a Deployment manifest with 2 replicas.
	deployYAML := []byte(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: e2e-deploy
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: worker
          image: ` + testImage + `
          command: ["sleep", "300"]
          resources:
            requests:
              cpu: 100m
              memory: 32Mi
`)

	result, err := manifest.Parse(deployYAML, nil)
	if err != nil {
		t.Fatalf("failed to parse deployment manifest: %v", err)
	}
	if len(result.Pods) != 2 {
		t.Fatalf("expected 2 pods from deployment, got %d", len(result.Pods))
	}

	// Apply pods to store.
	for _, pod := range result.Pods {
		store.Apply(pod)
	}

	// Verify both pods exist in store with Pending status.
	for _, pod := range result.Pods {
		rec, ok := store.Get(pod.Name)
		if !ok {
			t.Fatalf("pod %q not found in store after apply", pod.Name)
		}
		if rec.Status != state.StatusPending {
			t.Fatalf("pod %q expected status pending, got %q", pod.Name, rec.Status)
		}
	}

	// Start the reconciler to schedule and create pods.
	rec := reconciler.NewReconciler(store, sched, ex, 1*time.Second)
	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	go rec.Run(rctx)

	// Wait for both pods to reach Running.
	for _, pod := range result.Pods {
		waitForStatus(t, store, pod.Name, state.StatusRunning, 30*time.Second)
		defer cleanupPod(t, pod.Name)
	}

	// Delete deployment pods from store.
	rcancel() // Stop reconciler before deleting.
	for _, pod := range result.Pods {
		if !store.Delete(pod.Name) {
			t.Fatalf("failed to delete pod %q from store", pod.Name)
		}
		// Clean up podman pod.
		if err := ex.StopPod(ctx, pod.Name, 5); err != nil {
			t.Logf("warning: failed to stop pod %q: %v", pod.Name, err)
		}
	}

	// Verify pods are removed from store.
	for _, pod := range result.Pods {
		if _, ok := store.Get(pod.Name); ok {
			t.Fatalf("pod %q still exists in store after delete", pod.Name)
		}
	}
}

func TestE2E_PriorityPreemption(t *testing.T) {
	requirePodman(t)
	ensureNetwork(t)

	store := state.NewPodStore()
	// Deliberately constrained GPU resources so preemption is necessary.
	tracker := scheduler.NewResourceTracker(
		scheduler.Resources{CPUMillis: 8000, MemoryMB: 16384, GPUMemoryMB: 4096},
		scheduler.Resources{CPUMillis: 500, MemoryMB: 512, GPUMemoryMB: 0},
	)
	sched := scheduler.NewScheduler(tracker)
	ex := executor.NewPodmanExecutor("spark-net")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Low-priority GPU job (priority 100 = lower priority, higher number).
	lowPod := manifest.PodSpec{
		Name:              "e2e-low-prio",
		Priority:          100,
		PriorityClassName: "low",
		RestartPolicy:     "Never",
		SourceKind:        "Job",
		SourceName:        "e2e-low-prio",
		Containers: []manifest.ContainerSpec{
			{
				Name:    "gpu-worker",
				Image:   testImage,
				Command: []string{"sleep", "300"},
				Resources: manifest.ResourceRequirements{
					Requests: manifest.ResourceList{CPUMillis: 100, MemoryMB: 32, GPUMemoryMB: 4096},
				},
			},
		},
	}

	// High-priority GPU job (priority 10 = higher priority, lower number).
	highPod := manifest.PodSpec{
		Name:              "e2e-high-prio",
		Priority:          10,
		PriorityClassName: "high",
		RestartPolicy:     "Never",
		SourceKind:        "Job",
		SourceName:        "e2e-high-prio",
		Containers: []manifest.ContainerSpec{
			{
				Name:    "gpu-worker",
				Image:   testImage,
				Command: []string{"sleep", "300"},
				Resources: manifest.ResourceRequirements{
					Requests: manifest.ResourceList{CPUMillis: 100, MemoryMB: 32, GPUMemoryMB: 4096},
				},
			},
		},
	}

	// Apply the low-priority pod, schedule it, and start it.
	store.Apply(lowPod)
	rec := reconciler.NewReconciler(store, sched, ex, 1*time.Second)
	rctx, rcancel := context.WithCancel(ctx)
	go rec.Run(rctx)

	waitForStatus(t, store, "e2e-low-prio", state.StatusRunning, 30*time.Second)
	defer cleanupPod(t, "e2e-low-prio")

	// Now apply the high-priority pod. The scheduler should signal preemption.
	store.Apply(highPod)

	// Try to schedule the high-priority pod directly to verify preemption logic.
	schedResult := sched.Schedule(highPod)
	if schedResult.Action != scheduler.Preempting {
		t.Fatalf("expected preemption action, got %d", schedResult.Action)
	}

	if len(schedResult.Victims) == 0 {
		t.Fatal("expected at least one victim for preemption")
	}

	found := false
	for _, v := range schedResult.Victims {
		if v == "e2e-low-prio" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected e2e-low-prio to be a preemption victim, got %v", schedResult.Victims)
	}

	rcancel()

	// Simulate preemption: stop the low-priority pod, release resources, schedule high.
	if err := ex.StopPod(ctx, "e2e-low-prio", 5); err != nil {
		t.Logf("warning: failed to stop low-prio pod: %v", err)
	}
	sched.RemovePod("e2e-low-prio")
	tracker.Release("e2e-low-prio")
	store.UpdateStatus("e2e-low-prio", state.StatusPreempted, "preempted by high-prio")

	// Now schedule the high-priority pod.
	store.UpdateStatus("e2e-high-prio", state.StatusPending, "ready to schedule")
	rctx2, rcancel2 := context.WithCancel(ctx)
	defer rcancel2()
	go rec.Run(rctx2)

	waitForStatus(t, store, "e2e-high-prio", state.StatusRunning, 30*time.Second)
	defer cleanupPod(t, "e2e-high-prio")

	// Verify the low-priority pod was preempted.
	lowRec, ok := store.Get("e2e-low-prio")
	if !ok {
		t.Fatal("low-priority pod not found in store")
	}
	if lowRec.Status != state.StatusPreempted {
		t.Fatalf("expected low-priority pod status preempted, got %q", lowRec.Status)
	}

	rcancel2()
	cleanupPod(t, "e2e-high-prio")
}

func TestE2E_PodNetworking(t *testing.T) {
	requirePodman(t)
	ensureNetwork(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ex := executor.NewPodmanExecutor("spark-net")

	// Create two pods on spark-net.
	serverPod := manifest.PodSpec{
		Name:          "e2e-net-server",
		RestartPolicy: "Never",
		SourceKind:    "Pod",
		SourceName:    "e2e-net-server",
		Containers: []manifest.ContainerSpec{
			{
				Name:    "server",
				Image:   testImage,
				Command: []string{"sleep", "120"},
			},
		},
	}

	clientPod := manifest.PodSpec{
		Name:          "e2e-net-client",
		RestartPolicy: "Never",
		SourceKind:    "Pod",
		SourceName:    "e2e-net-client",
		Containers: []manifest.ContainerSpec{
			{
				Name:    "client",
				Image:   testImage,
				Command: []string{"sleep", "120"},
			},
		},
	}

	// Create server pod.
	if err := ex.CreatePod(ctx, serverPod); err != nil {
		t.Fatalf("failed to create server pod: %v", err)
	}
	defer cleanupPod(t, "e2e-net-server")

	// Create client pod.
	if err := ex.CreatePod(ctx, clientPod); err != nil {
		t.Fatalf("failed to create client pod: %v", err)
	}
	defer cleanupPod(t, "e2e-net-client")

	// Wait briefly for pods to start.
	time.Sleep(3 * time.Second)

	// Verify pod-to-pod name resolution: client pings server by pod name.
	// On podman with DNS enabled on the network, pod names are resolvable.
	out, err := exec.CommandContext(ctx, "podman", "exec", "e2e-net-client-client",
		"ping", "-c", "1", "-W", "5", "e2e-net-server").CombinedOutput()
	if err != nil {
		// DNS resolution via pod name may not work in all podman configurations.
		// Try resolving via container name instead.
		t.Logf("pod name resolution failed (expected in some podman configs): %s: %s", err, out)
		out, err = exec.CommandContext(ctx, "podman", "exec", "e2e-net-client-client",
			"ping", "-c", "1", "-W", "5", "e2e-net-server-server").CombinedOutput()
		if err != nil {
			t.Fatalf("pod-to-pod networking failed: %s: %s", err, out)
		}
	}
	t.Logf("pod networking successful: %s", strings.TrimSpace(string(out)))
}

func TestE2E_ReconcilerRestart(t *testing.T) {
	requirePodman(t)
	ensureNetwork(t)

	store, sched, ex := newTestInfra()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	podName := "e2e-restart"
	pod := manifest.PodSpec{
		Name:          podName,
		RestartPolicy: "Always",
		SourceKind:    "Pod",
		SourceName:    podName,
		Containers: []manifest.ContainerSpec{
			{
				Name:    "worker",
				Image:   testImage,
				Command: []string{"sleep", "300"},
				Resources: manifest.ResourceRequirements{
					Requests: manifest.ResourceList{CPUMillis: 100, MemoryMB: 32},
				},
			},
		},
	}

	store.Apply(pod)
	defer cleanupPod(t, podName)

	// Start reconciler with short interval.
	rec := reconciler.NewReconciler(store, sched, ex, 1*time.Second)
	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	go rec.Run(rctx)

	// Wait for pod to become Running.
	waitForStatus(t, store, podName, state.StatusRunning, 30*time.Second)

	// Kill the container to simulate a crash.
	out, err := exec.CommandContext(ctx, "podman", "pod", "kill", podName).CombinedOutput()
	if err != nil {
		t.Fatalf("failed to kill pod: %s: %s", err, out)
	}

	// The reconciler should detect the pod is no longer running and set it to Pending
	// (due to restartPolicy=Always), then re-create it as Running.
	// First it goes to Pending.
	waitForStatus(t, store, podName, state.StatusPending, 15*time.Second)

	// Remove the old dead pod so re-create can succeed.
	_ = exec.CommandContext(ctx, "podman", "pod", "rm", "-f", podName).Run()

	// Then it should come back to Running.
	waitForStatus(t, store, podName, state.StatusRunning, 15*time.Second)

	rcancel()
}

func TestE2E_RegistryImage(t *testing.T) {
	requirePodman(t)
	ensureNetwork(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	registryAddr := "localhost:5000"

	// Check if registry is available.
	out, err := exec.CommandContext(ctx, "podman", "search",
		fmt.Sprintf("%s/", registryAddr), "--limit", "1").CombinedOutput()
	if err != nil {
		t.Skipf("local registry not available at %s, skipping: %s", registryAddr, out)
	}

	// Tag and push alpine to local registry.
	localImage := fmt.Sprintf("%s/e2e-test-alpine:latest", registryAddr)

	// Pull alpine first.
	out, err = exec.CommandContext(ctx, "podman", "pull", testImage).CombinedOutput()
	if err != nil {
		t.Fatalf("failed to pull %s: %s: %s", testImage, err, out)
	}

	// Tag it for local registry.
	out, err = exec.CommandContext(ctx, "podman", "tag", testImage, localImage).CombinedOutput()
	if err != nil {
		t.Fatalf("failed to tag image: %s: %s", err, out)
	}

	// Push to local registry.
	out, err = exec.CommandContext(ctx, "podman", "push", "--tls-verify=false", localImage).CombinedOutput()
	if err != nil {
		t.Skipf("failed to push to local registry (skipping): %s: %s", err, out)
	}

	store, sched, ex := newTestInfra()

	podName := "e2e-registry"
	pod := manifest.PodSpec{
		Name:          podName,
		RestartPolicy: "Never",
		SourceKind:    "Job",
		SourceName:    podName,
		Containers: []manifest.ContainerSpec{
			{
				Name:    "job",
				Image:   localImage,
				Command: []string{"echo", "hello from registry"},
				Resources: manifest.ResourceRequirements{
					Requests: manifest.ResourceList{CPUMillis: 100, MemoryMB: 32},
				},
			},
		},
	}

	store.Apply(pod)
	defer cleanupPod(t, podName)

	rec := reconciler.NewReconciler(store, sched, ex, 1*time.Second)
	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	go rec.Run(rctx)

	// Wait for the pod to reach Running, then Completed.
	waitForStatus(t, store, podName, state.StatusRunning, 30*time.Second)
	waitForStatus(t, store, podName, state.StatusCompleted, 30*time.Second)

	rcancel()
}

func TestE2E_CronJobCreation(t *testing.T) {
	requirePodman(t)

	store := state.NewPodStore()
	cs := cron.NewCronScheduler(store)

	cronSpec := manifest.CronJobSpec{
		Name:                       "e2e-cron",
		Schedule:                   "*/1 * * * *",
		ConcurrencyPolicy:          "Allow",
		SuccessfulJobsHistoryLimit: 3,
		FailedJobsHistoryLimit:     1,
		JobTemplate: manifest.PodSpec{
			RestartPolicy: "Never",
			SourceKind:    "CronJob",
			SourceName:    "e2e-cron",
			Containers: []manifest.ContainerSpec{
				{
					Name:    "cron-worker",
					Image:   testImage,
					Command: []string{"echo", "cron fired"},
				},
			},
		},
	}

	if err := cs.Register(cronSpec); err != nil {
		t.Fatalf("failed to register cronjob: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Run the cron scheduler in the background.
	go cs.Run(ctx)

	// Poll the store for a job created by the CronJob.
	deadline := time.Now().Add(90 * time.Second)
	var foundJob string
	for time.Now().Before(deadline) {
		pods := store.List("")
		for _, rec := range pods {
			if rec.Spec.SourceKind == "CronJob" && rec.Spec.SourceName == "e2e-cron" {
				foundJob = rec.Spec.Name
				break
			}
		}
		if foundJob != "" {
			break
		}
		time.Sleep(1 * time.Second)
	}

	cancel()

	if foundJob == "" {
		t.Fatal("CronJob did not create a job within 90 seconds")
	}

	// Verify the job was created with expected naming convention.
	if !strings.HasPrefix(foundJob, "e2e-cron-") {
		t.Fatalf("expected job name to start with 'e2e-cron-', got %q", foundJob)
	}

	t.Logf("CronJob successfully created job: %s", foundJob)
}
