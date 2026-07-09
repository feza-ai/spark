package manifest

import "testing"

// Issue #44: a block scalar as a list item (`- |`) was parsed as the literal
// string "|" and the indented block lines were silently dropped, so a
// multi-line container command reached podman as `bash -c '|'`.

func TestParseYAML_ListBlockScalarLiteral(t *testing.T) {
	yaml := `command:
  - /bin/bash
  - -c
  - |
    set -e
    (echo a && echo b) \
      || (echo c && echo d)
    echo done
`
	root, err := ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	list, ok := root["command"].([]interface{})
	if !ok {
		t.Fatalf("command is %T, want list", root["command"])
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 items, got %d: %#v", len(list), list)
	}
	want := "set -e\n(echo a && echo b) \\\n  || (echo c && echo d)\necho done\n"
	if got := list[2]; got != want {
		t.Errorf("block scalar item = %q, want %q", got, want)
	}
}

func TestParseYAML_ListBlockScalarStrip(t *testing.T) {
	yaml := `args:
  - |-
    line1
    line2
`
	root, err := ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	list := root["args"].([]interface{})
	if got, want := list[0], "line1\nline2"; got != want {
		t.Errorf("strip scalar = %q, want %q", got, want)
	}
}

func TestParseYAML_ListBlockScalarFolded(t *testing.T) {
	yaml := `args:
  - >-
    one
    two
`
	root, err := ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	list := root["args"].([]interface{})
	if got, want := list[0], "one two"; got != want {
		t.Errorf("folded scalar = %q, want %q", got, want)
	}
}

func TestParseYAML_ListContinuesAfterBlockScalar(t *testing.T) {
	yaml := `args:
  - |
    script line
  - second
  - third
`
	root, err := ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	list := root["args"].([]interface{})
	if len(list) != 3 {
		t.Fatalf("expected 3 items, got %d: %#v", len(list), list)
	}
	if list[0] != "script line\n" || list[1] != "second" || list[2] != "third" {
		t.Errorf("unexpected list: %#v", list)
	}
}

// End-to-end: the incident shape from issue #44 — the command must reach the
// PodSpec byte-for-byte as YAML defines it, newlines included.
func TestParse_PodCommandBlockScalar(t *testing.T) {
	yaml := `apiVersion: v1
kind: Pod
metadata:
  name: cmd-blockscalar
spec:
  containers:
    - name: main
      image: docker.io/library/bash:5
      command:
        - /bin/bash
        - -c
        - |
          set -e
          echo done
`
	result, err := Parse([]byte(yaml), DefaultPriorityClasses())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cmd := result.Pods[0].Containers[0].Command
	if len(cmd) != 3 {
		t.Fatalf("expected 3 command parts, got %d: %#v", len(cmd), cmd)
	}
	if want := "set -e\necho done\n"; cmd[2] != want {
		t.Errorf("command[2] = %q, want %q", cmd[2], want)
	}
}
