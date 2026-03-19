package manifest

import (
	"fmt"
	"strconv"
)

// parseStatefulSet extracts PodSpecs from a parsed StatefulSet manifest.
// Returns N PodSpecs where N = spec.replicas (default 1).
// Pod names follow pattern: {statefulset-name}-{index} (0-indexed, stable ordered names).
func parseStatefulSet(root map[string]interface{}, priorityClasses map[string]int) ([]PodSpec, error) {
	name := getString(root, "metadata", "name")
	if name == "" {
		return nil, fmt.Errorf("metadata.name is required")
	}

	spec := getMap(root, "spec")
	if spec == nil {
		return nil, fmt.Errorf("spec is required")
	}

	replicas := 1
	if v, ok := spec["replicas"]; ok {
		switch r := v.(type) {
		case string:
			n, err := strconv.Atoi(r)
			if err != nil {
				return nil, fmt.Errorf("invalid replicas value: %s", r)
			}
			replicas = n
		case int:
			replicas = r
		}
	}

	template := getMap(spec, "template")
	if template == nil {
		return nil, fmt.Errorf("spec.template is required")
	}

	templateSpec := getMap(template, "spec")
	if templateSpec == nil {
		return nil, fmt.Errorf("spec.template.spec is required")
	}

	base, err := parsePodFromMap(templateSpec, priorityClasses)
	if err != nil {
		return nil, fmt.Errorf("parsing pod template: %w", err)
	}

	// Apply template-level metadata labels.
	if labels := getStringMap(template, "metadata", "labels"); labels != nil {
		base.Labels = labels
	}

	// Apply template-level metadata annotations.
	if annotations := getStringMap(template, "metadata", "annotations"); annotations != nil {
		base.Annotations = annotations
	}

	base.RestartPolicy = "Always"
	base.SourceKind = "StatefulSet"
	base.SourceName = name

	pods := make([]PodSpec, replicas)
	for i := 0; i < replicas; i++ {
		p := base
		p.Name = fmt.Sprintf("%s-%d", name, i)
		// Deep-copy containers slice so each pod owns its own.
		p.Containers = make([]ContainerSpec, len(base.Containers))
		copy(p.Containers, base.Containers)
		// Deep-copy volumes slice.
		p.Volumes = make([]VolumeSpec, len(base.Volumes))
		copy(p.Volumes, base.Volumes)
		// Deep-copy labels.
		if base.Labels != nil {
			p.Labels = make(map[string]string, len(base.Labels))
			for k, v := range base.Labels {
				p.Labels[k] = v
			}
		}
		// Deep-copy annotations.
		if base.Annotations != nil {
			p.Annotations = make(map[string]string, len(base.Annotations))
			for k, v := range base.Annotations {
				p.Annotations[k] = v
			}
		}
		pods[i] = p
	}

	return pods, nil
}
