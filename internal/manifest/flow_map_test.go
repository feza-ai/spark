package manifest

import (
	"strings"
	"testing"
)

// Issue #66: flow-style maps were parsed as scalar strings, so
// `limits: { cpu: "1", memory: 512Mi }` silently produced zero resource
// requests and the pod bypassed admission accounting entirely.

func TestParseFlowMap(t *testing.T) {
	m, err := parseFlowMap(`{ cpu: "1", memory: 512Mi }`)
	if err != nil {
		t.Fatalf("parseFlowMap: %v", err)
	}
	if m["cpu"] != "1" || m["memory"] != "512Mi" {
		t.Errorf("unexpected map: %#v", m)
	}
}

func TestParseFlowMap_Empty(t *testing.T) {
	m, err := parseFlowMap("{}")
	if err != nil {
		t.Fatalf("parseFlowMap: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %#v", m)
	}
}

func TestParseFlowMap_Nested(t *testing.T) {
	m, err := parseFlowMap(`{ limits: { cpu: "2" }, ports: [80, 443] }`)
	if err != nil {
		t.Fatalf("parseFlowMap: %v", err)
	}
	limits, ok := m["limits"].(map[string]interface{})
	if !ok || limits["cpu"] != "2" {
		t.Errorf("nested map wrong: %#v", m["limits"])
	}
	ports, ok := m["ports"].([]interface{})
	if !ok || len(ports) != 2 || ports[0] != "80" {
		t.Errorf("nested list wrong: %#v", m["ports"])
	}
}

func TestParseFlowMap_ValueWithColon(t *testing.T) {
	m, err := parseFlowMap(`{ image: docker.io/library/alpine:latest, note: "a: b" }`)
	if err != nil {
		t.Fatalf("parseFlowMap: %v", err)
	}
	if m["image"] != "docker.io/library/alpine:latest" {
		t.Errorf("image = %q", m["image"])
	}
	if m["note"] != "a: b" {
		t.Errorf("note = %q", m["note"])
	}
}

func TestParseFlowMap_MalformedIsError(t *testing.T) {
	for _, in := range []string{
		`{ cpu "1" }`,    // missing colon
		`{ cpu: "1"`,     // unterminated (caught by caller prefix check)
		`{ cpu: { "1" }`, // unbalanced nesting
		`{ cpu: '1 }`,    // unterminated quote
	} {
		if _, err := parseFlowMap(in); err == nil {
			t.Errorf("parseFlowMap(%q) expected error", in)
		}
	}
}

func TestParseFlowList_NestedCollections(t *testing.T) {
	list, err := parseFlowList(`[{ name: a }, [b, c], d]`)
	if err != nil {
		t.Fatalf("parseFlowList: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 items, got %#v", list)
	}
	if m, ok := list[0].(map[string]interface{}); !ok || m["name"] != "a" {
		t.Errorf("item 0 wrong: %#v", list[0])
	}
	if l, ok := list[1].([]interface{}); !ok || len(l) != 2 {
		t.Errorf("item 1 wrong: %#v", list[1])
	}
}

func TestParseYAML_FlowMapAsListItem(t *testing.T) {
	root, err := ParseYAML([]byte("env:\n  - { name: FOO, value: bar }\n"))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	list := root["env"].([]interface{})
	m, ok := list[0].(map[string]interface{})
	if !ok || m["name"] != "FOO" || m["value"] != "bar" {
		t.Errorf("flow map list item wrong: %#v", list[0])
	}
}

// End-to-end: the live repro from issue #66 — flow-style limits must be
// fully accounted, exactly like their block-style equivalent.
func TestParse_FlowStyleResourcesAreAccounted(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: flow-style
spec:
  restartPolicy: Always
  containers:
    - name: main
      image: docker.io/library/alpine:latest
      command: ["/bin/sh", "-c", "sleep 900"]
      resources:
        limits: { cpu: "1", memory: 512Mi }
`
	result, err := Parse([]byte(yaml), DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	total := result.Pods[0].TotalRequests()
	if total.CPUMillis != 1000 {
		t.Errorf("CPUMillis = %d, want 1000", total.CPUMillis)
	}
	if total.MemoryMB != 512 {
		t.Errorf("MemoryMB = %d, want 512", total.MemoryMB)
	}
}

// The #42-style runbook manifest: flow maps nested one level deeper.
func TestParse_FlowStyleRequestsAndLimits(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: runbook-style
spec:
  containers:
    - name: c
      image: docker.io/library/alpine:latest
      resources:
        requests: { cpu: "1", memory: 128Mi }
        limits:   { cpu: "1", memory: 128Mi }
`
	result, err := Parse([]byte(yaml), DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	req := result.Pods[0].Containers[0].Resources.Requests
	if req.CPUMillis != 1000 || req.MemoryMB != 128 {
		t.Errorf("requests = %+v, want 1000m/128MB", req)
	}
}

// A malformed flow map must fail the manifest, never silently zero out.
func TestParse_MalformedFlowMapIsError(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: broken
spec:
  containers:
    - name: c
      image: img
      resources:
        limits: { cpu "1" }
`
	_, err := Parse([]byte(yaml), DefaultPriorityClasses())
	if err == nil {
		t.Fatal("expected parse error for malformed flow map")
	}
	if !strings.Contains(err.Error(), "flow map") {
		t.Errorf("error should mention flow map, got: %v", err)
	}
}
