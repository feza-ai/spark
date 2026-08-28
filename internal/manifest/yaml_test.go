package manifest

import (
	"reflect"
	"testing"
)

func TestFindMapSeparator(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"simple map", "key: value", 3},
		{"trailing colon", "key:", 3},
		{"no space after colon", "key:value", -1},
		{"url scalar", "nats://10.88.0.1:4222", -1},
		{"http url", "http://x/y", -1},
		{"quoted url double", `"nats://10.88.0.1:4222"`, -1},
		{"quoted url single", `'nats://10.88.0.1:4222'`, -1},
		{"quoted path single", `'path:/a/b'`, -1},
		{"tab after colon", "key:\tvalue", 3},
		{"empty", "", -1},
		{"colon inside quotes, real map after", `name: "nats://x:4222"`, 4},
		{"double quote inside single", `'he said "hi: there"'`, -1},
		{"no colon at all", "plain value", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findMapSeparator(tt.input)
			if got != tt.want {
				t.Errorf("findMapSeparator(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseMemoryKiAndK(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		// Ki suffix (divide by 1024)
		{"1024Ki", 1},
		{"512Ki", 0},
		{"2048Ki", 2},
		// K suffix (divide by 1000)
		{"1000K", 1},
		{"500K", 0},
		{"2000K", 2},
		// Existing suffixes still work
		{"1Gi", 1024},
		{"2Mi", 2},
		{"1G", 1000},
		{"512M", 512},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseMemory(tt.input)
			if err != nil {
				t.Fatalf("parseMemory(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("parseMemory(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseFlowList(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []interface{}
	}{
		{"simple", `["a", "b"]`, []interface{}{"a", "b"}},
		{"unquoted", `[a, b, c]`, []interface{}{"a", "b", "c"}},
		{"single quotes", `['sh', '-c']`, []interface{}{"sh", "-c"}},
		{"empty", `[]`, []interface{}{}},
		{"with spaces and numbers", `[1, 2, "three"]`, []interface{}{"1", "2", "three"}},
		{"comma in quoted", `["a,b", "c"]`, []interface{}{"a,b", "c"}},
		{"nsys profile", `["nsys", "profile", "-o", "/out"]`, []interface{}{"nsys", "profile", "-o", "/out"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFlowList(tt.input)
			if err != nil {
				t.Fatalf("parseFlowList(%q) err = %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseFlowList(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseFlowList_Errors(t *testing.T) {
	cases := []string{
		`["unterminated, x]`,
		`[a, [b]`, // unbalanced nesting
	}
	for _, c := range cases {
		if _, err := parseFlowList(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestParseYAML_FlowListInMap(t *testing.T) {
	doc := `command: ["nsys", "profile", "-o", "/out"]
args: [a, b, c]
`
	got, err := ParseYAML([]byte(doc))
	if err != nil {
		t.Fatalf("ParseYAML err = %v", err)
	}
	wantCmd := []interface{}{"nsys", "profile", "-o", "/out"}
	if !reflect.DeepEqual(got["command"], wantCmd) {
		t.Errorf("command = %v, want %v", got["command"], wantCmd)
	}
	wantArgs := []interface{}{"a", "b", "c"}
	if !reflect.DeepEqual(got["args"], wantArgs) {
		t.Errorf("args = %v, want %v", got["args"], wantArgs)
	}
}

func TestParseYAML_LiteralBlockScalar(t *testing.T) {
	doc := `script: |
  echo hello
  echo world
next: tail
`
	got, err := ParseYAML([]byte(doc))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := "echo hello\necho world\n"
	if got["script"] != want {
		t.Errorf("script = %q, want %q", got["script"], want)
	}
	if got["next"] != "tail" {
		t.Errorf("next = %v, want tail", got["next"])
	}
}

func TestParseYAML_LiteralStripBlockScalar(t *testing.T) {
	doc := `script: |-
  line1
  line2
`
	got, err := ParseYAML([]byte(doc))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := "line1\nline2"
	if got["script"] != want {
		t.Errorf("script = %q, want %q", got["script"], want)
	}
}

func TestParseYAML_FoldedBlockScalar(t *testing.T) {
	doc := `text: >
  one two
  three
`
	got, err := ParseYAML([]byte(doc))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := "one two three\n"
	if got["text"] != want {
		t.Errorf("text = %q, want %q", got["text"], want)
	}
}

func TestParseYAML_BlockListRegression(t *testing.T) {
	doc := `items:
  - foo
  - bar
  - baz
`
	got, err := ParseYAML([]byte(doc))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := []interface{}{"foo", "bar", "baz"}
	if !reflect.DeepEqual(got["items"], want) {
		t.Errorf("items = %v, want %v", got["items"], want)
	}
}

// TestParseYAML_SameIndentBlockList covers a block sequence whose "-"
// markers sit at the SAME indentation as their parent key, rather than
// nested one level deeper (the style TestParseYAML_BlockListRegression
// covers). Both are valid YAML; K8s manifests commonly use the same-indent
// style for `containers:`. Before the fix, a key whose next line was at
// nextIndent == baseIndent was always treated as "key has no value",
// silently dropping the entire list (issue #77).
func TestParseYAML_SameIndentBlockList(t *testing.T) {
	doc := `items:
- foo
- bar
- baz
sibling: ok
`
	got, err := ParseYAML([]byte(doc))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := []interface{}{"foo", "bar", "baz"}
	if !reflect.DeepEqual(got["items"], want) {
		t.Errorf("items = %v, want %v", got["items"], want)
	}
	if got["sibling"] != "ok" {
		t.Errorf("sibling = %v, want %q (parser must resume at the correct line after the list)", got["sibling"], "ok")
	}
}

// TestParseYAML_SameIndentBlockList_Nested covers the exact shape from
// issue #77: a nested map key ("spec") whose own child key ("containers")
// has a same-indent block sequence, with map-valued items (not bare
// scalars) so the item's own sub-fields must also parse correctly.
func TestParseYAML_SameIndentBlockList_Nested(t *testing.T) {
	doc := `spec:
  restartPolicy: Always
  containers:
  - name: runner
    image: alpine:latest
`
	got, err := ParseYAML([]byte(doc))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	spec, ok := got["spec"].(map[string]interface{})
	if !ok {
		t.Fatalf("spec = %v (%T), want map[string]interface{}", got["spec"], got["spec"])
	}
	if spec["restartPolicy"] != "Always" {
		t.Errorf("spec.restartPolicy = %v, want %q", spec["restartPolicy"], "Always")
	}
	containers, ok := spec["containers"].([]interface{})
	if !ok || len(containers) != 1 {
		t.Fatalf("spec.containers = %v (%T), want a 1-element list", spec["containers"], spec["containers"])
	}
	item, ok := containers[0].(map[string]interface{})
	if !ok {
		t.Fatalf("containers[0] = %v (%T), want map[string]interface{}", containers[0], containers[0])
	}
	if item["name"] != "runner" || item["image"] != "alpine:latest" {
		t.Errorf("containers[0] = %v, want name=runner image=alpine:latest", item)
	}
}
