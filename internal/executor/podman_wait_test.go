package executor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// writeFakePodman drops an executable script named "podman" into dir and
// points PATH at dir for the duration of the test, so exec.Command("podman",
// ...) in production code resolves to it instead of touching real podman or
// the DGX. Unix-only (matches the project's linux/arm64 DGX target and
// darwin dev hosts); skips on other platforms.
func writeFakePodman(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake podman script requires a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "podman")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake podman script: %v", err)
	}
	// Prepend rather than replace: the fake script's own shebang still
	// needs to resolve real coreutils (sh, sleep, ...) from the rest of PATH.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestRunPodmanBounded_OrphanedGrandchildHangsWithoutWaitDelay reproduces
// issue #88's exact mechanism without touching real podman or the DGX: a
// child process that backgrounds a long-lived grandchild inheriting its
// stdout/stderr pipe, then exits immediately itself. Go's os/exec
// Wait()/CombinedOutput() cannot tell that apart from "still running" --
// it reads stdout/stderr until EOF, which doesn't arrive until every
// process holding the pipe's write end closes it, including the orphaned
// grandchild. That leaves the calling goroutine blocked long after the
// direct child has already exited and become a zombie under the caller's
// PID -- exactly what issue #88 observed on the DGX (`[podman] <defunct>`
// parented by spark's own PID, while CombinedOutput() never returned).
//
// This is the RED half of the red/green pair: with waitDelay=0 (the
// behavior of every podman.go call site before this fix), the call blocks
// for the grandchild's full sleep, not the direct child's near-instant
// exit -- proving a context timeout alone (podmanStopTimeout) is not
// sufficient; WaitDelay is the other required half of the fix.
func TestRunPodmanBounded_OrphanedGrandchildHangsWithoutWaitDelay(t *testing.T) {
	writeFakePodman(t, "#!/bin/sh\n(sleep 5 &)\nexit 0\n")

	// A generous timeout here is deliberate: this test isolates the
	// pipe/EOF half of the gap from the context-cancellation half. Under a
	// loaded, parallel `-race` run, a tight timeout can fire before the
	// shell even gets scheduled to fork and detach its background sleep,
	// killing it pre-fork and returning fast for an unrelated reason (a
	// scheduling race, not evidence the gap doesn't exist). 20s keeps the
	// context out of the way so only the orphaned-pipe mechanism governs
	// the result; the call still returns in ~5s in practice, bounded by
	// the grandchild's own sleep, not by this timeout.
	start := time.Now()
	_, err := runPodmanBounded(context.Background(), 20*time.Second, 0, "pod", "stop", "--time", "1", "irrelevant")
	elapsed := time.Since(start)

	// The direct child (the shell) exits in milliseconds. If the call
	// returned anywhere near that, WaitDelay isn't needed and this test's
	// premise is wrong. It should instead track the grandchild's ~5s sleep.
	if elapsed < 4*time.Second {
		t.Fatalf("expected the call to block for the orphaned grandchild's sleep (~5s) with waitDelay=0, returned after %s (err=%v) -- the gap this test exists to demonstrate did not reproduce", elapsed, err)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("expected the call to unblock once the grandchild's sleep finishes (~5s), took %s -- did the fake script change?", elapsed)
	}
}

// TestRunPodmanBounded_WaitDelayBoundsTheHang is the GREEN half: the same
// orphaned-grandchild scenario, but with WaitDelay set (as StopPod/RemovePod
// now do). Cmd.Wait force-closes the pipes once waitDelay elapses after the
// direct child is observed to have exited, unblocking the call long before
// the grandchild's sleep finishes.
func TestRunPodmanBounded_WaitDelayBoundsTheHang(t *testing.T) {
	writeFakePodman(t, "#!/bin/sh\n(sleep 5 &)\nexit 0\n")

	start := time.Now()
	_, err := runPodmanBounded(context.Background(), time.Second, 300*time.Millisecond, "pod", "stop", "--time", "1", "irrelevant")
	elapsed := time.Since(start)

	if elapsed >= 4*time.Second {
		t.Fatalf("expected WaitDelay to bound the wait to well under the grandchild's 5s sleep, took %s (err=%v)", elapsed, err)
	}
	if err == nil {
		t.Fatalf("expected an error once WaitDelay force-closes the pipes on the still-open grandchild, got nil")
	}
}

// TestStopPod_BoundedDespiteOrphanedGrandchild exercises the real
// production method (not just the helper) end-to-end via a fake podman on
// PATH, proving the actual stop/delete code path -- not just the isolated
// mechanism above -- is bounded. Uses the production podmanStopTimeout /
// podmanWaitDelay constants, so this also documents their real-world
// worst-case wall-clock bound.
func TestStopPod_BoundedDespiteOrphanedGrandchild(t *testing.T) {
	writeFakePodman(t, `#!/bin/sh
case "$*" in
  *"pod stop"*) (sleep 30 &) ; exit 0 ;;
  *) exit 0 ;;
esac
`)

	p := NewPodmanExecutor("test-net")
	start := time.Now()
	err := p.StopPod(context.Background(), "irrelevant-pod", 1)
	elapsed := time.Since(start)

	// Worst case for a single call: podmanStopTimeout to notice the direct
	// child has nothing left to signal, then podmanWaitDelay to force the
	// pipes closed. Generous slack for scheduling jitter.
	maxBound := podmanStopTimeout + podmanWaitDelay + 3*time.Second
	if elapsed > maxBound {
		t.Fatalf("StopPod took %s against a wedged (orphaned-grandchild) podman invocation, expected it bounded near %s (issue #88)", elapsed, maxBound)
	}
	if err == nil {
		t.Fatalf("expected StopPod to surface an error once the wedged podman invocation is force-timed-out, got nil")
	}
}

// TestRemovePod_BoundedDespiteOrphanedGrandchild is RemovePod's counterpart
// to the StopPod test above -- exercised separately since it's a distinct
// call site (used directly by the reconciler, housekeeper, and shutdown
// drain path, not only via StopPod).
func TestRemovePod_BoundedDespiteOrphanedGrandchild(t *testing.T) {
	writeFakePodman(t, `#!/bin/sh
case "$*" in
  *"pod rm"*) (sleep 30 &) ; exit 0 ;;
  *) exit 0 ;;
esac
`)

	p := NewPodmanExecutor("test-net")
	start := time.Now()
	err := p.RemovePod(context.Background(), "irrelevant-pod")
	elapsed := time.Since(start)

	maxBound := podmanStopTimeout + podmanWaitDelay + 3*time.Second
	if elapsed > maxBound {
		t.Fatalf("RemovePod took %s against a wedged (orphaned-grandchild) podman invocation, expected it bounded near %s (issue #88)", elapsed, maxBound)
	}
	if err == nil {
		t.Fatalf("expected RemovePod to surface an error once the wedged podman invocation is force-timed-out, got nil")
	}
}
