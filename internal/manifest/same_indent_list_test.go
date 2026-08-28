package manifest

import "testing"

// TestIssue77_SameIndentContainersList reproduces
// github.com/feza-ai/spark/issues/77 using the exact manifest text from the
// issue report (metadata.name and the container's command/args/resources
// changed only to strip the placeholder github token/url, which are
// irrelevant to the parse path under test).
func TestIssue77_SameIndentContainersList(t *testing.T) {
	yaml := []byte(`apiVersion: v1
kind: Pod
metadata:
  name: sire-dgx-runner-v2
spec:
  restartPolicy: Always
  containers:
  - name: runner
    image: ghcr.io/actions/actions-runner:latest
    command: ["/bin/bash", "-c"]
    args: ["./config.sh --url https://github.com/example/example --token XXX --name sire-dgx-runner --labels self-hosted,linux,arm64,dgx --unattended --replace && ./run.sh"]
    resources:
      limits:
        cpu: "2"
        memory: 4Gi
`)

	result, err := Parse(yaml, nil)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(result.Pods) != 1 {
		t.Fatalf("got %d pods, want 1", len(result.Pods))
	}
	pod := result.Pods[0]
	t.Logf("parsed pod: name=%q restartPolicy=%q containers=%d", pod.Name, pod.RestartPolicy, len(pod.Containers))
	if pod.Name != "sire-dgx-runner-v2" {
		t.Errorf("pod.Name = %q, want %q", pod.Name, "sire-dgx-runner-v2")
	}
	if len(pod.Containers) != 1 {
		t.Errorf("len(pod.Containers) = %d, want 1 (container spec silently dropped -- issue #77)", len(pod.Containers))
	}
	if pod.RestartPolicy != "Always" {
		t.Errorf("pod.RestartPolicy = %q, want %q", pod.RestartPolicy, "Always")
	}
}
