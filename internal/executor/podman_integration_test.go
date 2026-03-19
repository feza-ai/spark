//go:build integration

package executor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
)

const testNetwork = "spark-test-net"

func setupNetwork(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := EnsureNetwork(ctx, testNetwork); err != nil {
		t.Fatalf("failed to create test network: %v", err)
	}
}

func cleanupPod(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Force remove, ignore errors (pod may not exist).
	exec.CommandContext(ctx, "podman", "pod", "rm", "-f", name).Run()
}

func uniqueName(t *testing.T, suffix string) string {
	t.Helper()
	// Use nanosecond timestamp for uniqueness.
	return fmt.Sprintf("test-%s-%d", suffix, time.Now().UnixNano())
}

func TestIntegration_CreatePod_Running(t *testing.T) {
	setupNetwork(t)
	exec := NewPodmanExecutor(testNetwork)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	podName := uniqueName(t, "create")
	defer cleanupPod(t, podName)

	spec := manifest.PodSpec{
		Name: podName,
		Containers: []manifest.ContainerSpec{
			{
				Name:    "alpine",
				Image:   "alpine:latest",
				Command: []string{"sleep", "300"},
			},
		},
	}

	if err := exec.CreatePod(ctx, spec); err != nil {
		t.Fatalf("CreatePod failed: %v", err)
	}

	// Give podman a moment to start.
	time.Sleep(2 * time.Second)

	status, err := exec.PodStatus(ctx, podName)
	if err != nil {
		t.Fatalf("PodStatus failed: %v", err)
	}
	if !status.Running {
		t.Errorf("expected pod to be running, got Running=%v", status.Running)
	}
}

func TestIntegration_StopPod(t *testing.T) {
	setupNetwork(t)
	exec := NewPodmanExecutor(testNetwork)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	podName := uniqueName(t, "stop")
	defer cleanupPod(t, podName)

	spec := manifest.PodSpec{
		Name: podName,
		Containers: []manifest.ContainerSpec{
			{
				Name:    "alpine",
				Image:   "alpine:latest",
				Command: []string{"sleep", "300"},
			},
		},
	}

	if err := exec.CreatePod(ctx, spec); err != nil {
		t.Fatalf("CreatePod failed: %v", err)
	}
	time.Sleep(2 * time.Second)

	if err := exec.StopPod(ctx, podName, 5); err != nil {
		t.Fatalf("StopPod failed: %v", err)
	}

	// After StopPod, the pod is removed, so PodStatus should fail.
	_, err := exec.PodStatus(ctx, podName)
	if err == nil {
		t.Error("expected PodStatus to fail after StopPod (pod should be removed)")
	}
}

func TestIntegration_NameResolution(t *testing.T) {
	setupNetwork(t)
	e := NewPodmanExecutor(testNetwork)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	podA := uniqueName(t, "dns-a")
	podB := uniqueName(t, "dns-b")
	defer cleanupPod(t, podA)
	defer cleanupPod(t, podB)

	// Create pod A: a simple alpine sleeping container.
	specA := manifest.PodSpec{
		Name: podA,
		Containers: []manifest.ContainerSpec{
			{
				Name:    "server",
				Image:   "alpine:latest",
				Command: []string{"sleep", "300"},
			},
		},
	}
	if err := e.CreatePod(ctx, specA); err != nil {
		t.Fatalf("CreatePod A failed: %v", err)
	}

	// Create pod B: ping pod A by name.
	specB := manifest.PodSpec{
		Name: podB,
		Containers: []manifest.ContainerSpec{
			{
				Name:    "client",
				Image:   "alpine:latest",
				Command: []string{"ping", "-c", "3", "-W", "5", podA},
			},
		},
	}
	if err := e.CreatePod(ctx, specB); err != nil {
		t.Fatalf("CreatePod B failed: %v", err)
	}

	// Wait for pod B's container to finish (ping should complete).
	deadline := time.Now().Add(30 * time.Second)
	containerName := podB + "-client"
	for time.Now().Before(deadline) {
		out, err := exec.CommandContext(ctx, "podman", "inspect", containerName, "--format", "{{.State.Status}}").CombinedOutput()
		if err == nil {
			state := strings.TrimSpace(string(out))
			if state == "exited" {
				break
			}
		}
		time.Sleep(time.Second)
	}

	// Check exit code of the ping container.
	out, err := exec.CommandContext(ctx, "podman", "inspect", containerName, "--format", "{{.State.ExitCode}}").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to inspect container exit code: %v: %s", err, out)
	}
	exitCode := strings.TrimSpace(string(out))
	if exitCode != "0" {
		t.Errorf("ping from pod B to pod A failed with exit code %s; expected 0 (name resolution)", exitCode)
	}
}

func TestIntegration_MultiContainer_SharedLocalhost(t *testing.T) {
	setupNetwork(t)
	e := NewPodmanExecutor(testNetwork)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	podName := uniqueName(t, "multi")
	defer cleanupPod(t, podName)

	// Create a pod with two containers:
	// - server: listens on localhost:8080 using nc
	// - client: waits briefly, then connects to localhost:8080
	spec := manifest.PodSpec{
		Name: podName,
		Containers: []manifest.ContainerSpec{
			{
				Name:    "server",
				Image:   "alpine:latest",
				Command: []string{"sh", "-c", "echo ready | nc -l -p 8080"},
			},
			{
				Name:    "client",
				Image:   "alpine:latest",
				Command: []string{"sh", "-c", "sleep 3 && nc -w 5 127.0.0.1 8080 > /tmp/result && cat /tmp/result"},
			},
		},
	}

	if err := e.CreatePod(ctx, spec); err != nil {
		t.Fatalf("CreatePod failed: %v", err)
	}

	// Wait for client container to finish.
	clientContainer := podName + "-client"
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.CommandContext(ctx, "podman", "inspect", clientContainer, "--format", "{{.State.Status}}").CombinedOutput()
		if err == nil {
			state := strings.TrimSpace(string(out))
			if state == "exited" {
				break
			}
		}
		time.Sleep(time.Second)
	}

	// Verify client received data from server via shared localhost.
	out, err := exec.CommandContext(ctx, "podman", "logs", clientContainer).CombinedOutput()
	if err != nil {
		t.Fatalf("failed to get client logs: %v: %s", err, out)
	}
	logs := strings.TrimSpace(string(out))
	if !strings.Contains(logs, "ready") {
		t.Errorf("client did not receive 'ready' from server over shared localhost; got logs: %q", logs)
	}

	// Also check exit code.
	exitOut, err := exec.CommandContext(ctx, "podman", "inspect", clientContainer, "--format", "{{.State.ExitCode}}").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to inspect client exit code: %v: %s", err, exitOut)
	}
	exitCode := strings.TrimSpace(string(exitOut))
	if exitCode != "0" {
		t.Errorf("client container exited with code %s, expected 0", exitCode)
	}
}
