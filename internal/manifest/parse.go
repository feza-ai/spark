package manifest

import (
	"fmt"
	"log/slog"
	"strings"
)

// ParseResult holds the results of parsing a manifest.
type ParseResult struct {
	Pods     []PodSpec
	CronJobs []CronJobSpec
}

// Parse parses K8s-compatible YAML manifest bytes and returns pod specs and cron job specs.
// Supports kinds: Pod, Job. Other kinds (Deployment, StatefulSet, CronJob) return
// an unsupported error for now; they will be added by kind-specific parsers.
func Parse(data []byte, priorityClasses map[string]int) (ParseResult, error) {
	docs := splitDocuments(data)

	var result ParseResult
	for _, docData := range docs {
		root, err := ParseYAML(docData)
		if err != nil {
			return ParseResult{}, fmt.Errorf("parsing YAML: %w", err)
		}
		if len(root) == 0 {
			continue
		}

		kind := getString(root, "kind")
		switch kind {
		case "Pod":
			pod, err := parsePod(root, priorityClasses)
			if err != nil {
				return ParseResult{}, fmt.Errorf("parsing Pod: %w", err)
			}
			result.Pods = append(result.Pods, pod)
		case "Job":
			pods, err := parseJob(root, priorityClasses)
			if err != nil {
				return ParseResult{}, fmt.Errorf("parsing Job: %w", err)
			}
			result.Pods = append(result.Pods, pods...)
		case "Deployment", "StatefulSet", "CronJob":
			return ParseResult{}, fmt.Errorf("unsupported kind: %s", kind)
		case "":
			return ParseResult{}, fmt.Errorf("missing kind field")
		default:
			slog.Warn("ignoring unknown kind", "kind", kind)
		}
	}
	return result, nil
}

// splitDocuments splits multi-document YAML on "---" separators.
func splitDocuments(data []byte) [][]byte {
	lines := strings.Split(string(data), "\n")
	var docs [][]byte
	var current []string

	flush := func() {
		hasContent := false
		for _, l := range current {
			t := strings.TrimSpace(l)
			if t != "" && !strings.HasPrefix(t, "#") {
				hasContent = true
				break
			}
		}
		if hasContent {
			docs = append(docs, []byte(strings.Join(current, "\n")))
		}
		current = nil
	}

	for _, line := range lines {
		if strings.TrimSpace(line) == "---" {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	return docs
}

// parsePod extracts a PodSpec from a parsed YAML document map.
func parsePod(root map[string]interface{}, priorityClasses map[string]int) (PodSpec, error) {
	name := getString(root, "metadata", "name")
	if name == "" {
		return PodSpec{}, fmt.Errorf("metadata.name is required")
	}

	specMap := getMap(root, "spec")
	var pod PodSpec
	if specMap != nil {
		var err error
		pod, err = parsePodFromMap(specMap, priorityClasses)
		if err != nil {
			return PodSpec{}, err
		}
	}

	pod.Name = name
	pod.Labels = getStringMap(root, "metadata", "labels")
	pod.Annotations = getStringMap(root, "metadata", "annotations")
	pod.SourceKind = "Pod"
	pod.SourceName = name

	return pod, nil
}
