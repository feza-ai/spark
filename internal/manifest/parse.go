package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// ParseResult holds the results of parsing a manifest.
type ParseResult struct {
	Pods     []PodSpec
	CronJobs []CronJobSpec
}

// Parse parses K8s-compatible manifest bytes and returns pod specs and cron
// job specs. Accepts either YAML (the historical format, including
// "---"-separated multi-document streams) or JSON. Supports kinds: Pod,
// Job. Other kinds (Deployment, StatefulSet, CronJob) return an unsupported
// error for now; they will be added by kind-specific parsers.
//
// JSON is detected up front rather than fed through the YAML parser: the
// YAML parser is line-based and expects one "key: value" pair per line, so
// pretty-printed JSON (opening brace on its own line, nested braces) either
// silently produced an empty document or mis-parsed -- a POST with a JSON
// body returned 201 {"pods":null} and created nothing (issue #74).
func Parse(data []byte, priorityClasses map[string]int) (ParseResult, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return parseJSONManifest(trimmed, priorityClasses)
	}

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

		if err := parseDocument(root, priorityClasses, &result); err != nil {
			return ParseResult{}, err
		}
	}
	return result, nil
}

// parseJSONManifest parses a single JSON document (object) or an array of
// JSON documents into a ParseResult. Unlike the YAML path, an empty or
// kind-less document is always an error: JSON bodies arrive as a single
// explicit request, so a document that resolves to nothing is a caller
// mistake, not incidental whitespace between "---" separators.
func parseJSONManifest(data []byte, priorityClasses map[string]int) (ParseResult, error) {
	var docs []map[string]interface{}
	if data[0] == '[' {
		if err := json.Unmarshal(data, &docs); err != nil {
			return ParseResult{}, fmt.Errorf("parsing JSON: %w", err)
		}
	} else {
		var doc map[string]interface{}
		if err := json.Unmarshal(data, &doc); err != nil {
			return ParseResult{}, fmt.Errorf("parsing JSON: %w", err)
		}
		docs = []map[string]interface{}{doc}
	}

	var result ParseResult
	for _, root := range docs {
		if len(root) == 0 {
			return ParseResult{}, fmt.Errorf("empty document")
		}
		if err := parseDocument(root, priorityClasses, &result); err != nil {
			return ParseResult{}, err
		}
	}

	if len(result.Pods) == 0 && len(result.CronJobs) == 0 {
		return ParseResult{}, fmt.Errorf("empty document: no pods or cron jobs produced")
	}
	return result, nil
}

// parseDocument dispatches a single parsed document (from either the YAML
// or JSON path) by its "kind" field, appending to result.
func parseDocument(root map[string]interface{}, priorityClasses map[string]int, result *ParseResult) error {
	kind := getString(root, "kind")
	switch kind {
	case "Pod":
		pod, err := parsePod(root, priorityClasses)
		if err != nil {
			return fmt.Errorf("parsing Pod: %w", err)
		}
		result.Pods = append(result.Pods, pod)
	case "Job":
		pods, err := parseJob(root, priorityClasses)
		if err != nil {
			return fmt.Errorf("parsing Job: %w", err)
		}
		result.Pods = append(result.Pods, pods...)
	case "Deployment":
		pods, err := parseDeployment(root, priorityClasses)
		if err != nil {
			return fmt.Errorf("parsing Deployment: %w", err)
		}
		result.Pods = append(result.Pods, pods...)
	case "StatefulSet":
		pods, err := parseStatefulSet(root, priorityClasses)
		if err != nil {
			return fmt.Errorf("parsing StatefulSet: %w", err)
		}
		result.Pods = append(result.Pods, pods...)
	case "CronJob":
		cj, err := parseCronJob(root, priorityClasses)
		if err != nil {
			return fmt.Errorf("parsing CronJob: %w", err)
		}
		result.CronJobs = append(result.CronJobs, cj)
	case "":
		return fmt.Errorf("missing kind field")
	default:
		slog.Warn("ignoring unknown kind", "kind", kind)
	}
	return nil
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

	// BackoffLimit defaults to 3 for Pods. An explicit "0" disables retries.
	if specMap != nil {
		bl := getInt(specMap, "backoffLimit")
		if bl == 0 {
			if getString(specMap, "backoffLimit") == "0" {
				pod.BackoffLimit = 0
			} else {
				pod.BackoffLimit = 3
			}
		} else {
			pod.BackoffLimit = bl
		}
	} else {
		pod.BackoffLimit = 3
	}

	return pod, nil
}
