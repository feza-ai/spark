package manifest

import (
	"fmt"
	"strconv"
)

// parseDeployment extracts PodSpecs from a parsed Deployment manifest.
// Returns N PodSpecs where N = spec.replicas (default 1).
// Pod names follow pattern: {deployment-name}-{index} (0-indexed).
func parseDeployment(root map[string]interface{}, priorityClasses map[string]int) ([]PodSpec, error) {
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

	base, err := parsePodSpec(templateSpec, priorityClasses)
	if err != nil {
		return nil, fmt.Errorf("parsing pod template: %w", err)
	}

	// Apply template-level metadata labels.
	if labels := getStringMap(template, "metadata", "labels"); labels != nil {
		base.Labels = labels
	}

	// Deployment pods always restart.
	base.RestartPolicy = "Always"
	base.SourceKind = "Deployment"
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
		pods[i] = p
	}

	return pods, nil
}

// parsePodSpec extracts a PodSpec from a spec map (shared by Deployment, Job, etc).
func parsePodSpec(specMap map[string]interface{}, priorityClasses map[string]int) (PodSpec, error) {
	pod := PodSpec{
		RestartPolicy:                 getString(specMap, "restartPolicy"),
		PriorityClassName:             getString(specMap, "priorityClassName"),
		TerminationGracePeriodSeconds: getInt(specMap, "terminationGracePeriodSeconds"),
	}

	if pod.PriorityClassName != "" {
		pod.Priority = ResolvePriority(priorityClasses, pod.PriorityClassName)
	}

	containers := getList(specMap, "containers")
	for _, c := range containers {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		cs, err := parseContainerSpec(cm)
		if err != nil {
			return PodSpec{}, err
		}
		pod.Containers = append(pod.Containers, cs)
	}

	volumes := getList(specMap, "volumes")
	for _, v := range volumes {
		vm, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		pod.Volumes = append(pod.Volumes, parseVolumeSpec(vm))
	}

	return pod, nil
}

// parseContainerSpec extracts a ContainerSpec from a container map.
func parseContainerSpec(m map[string]interface{}) (ContainerSpec, error) {
	c := ContainerSpec{
		Name:  getString(m, "name"),
		Image: getString(m, "image"),
	}

	// Parse command list.
	if cmdList := getList(m, "command"); cmdList != nil {
		for _, item := range cmdList {
			if s, ok := item.(string); ok {
				c.Command = append(c.Command, s)
			}
		}
	}

	// Parse args list.
	if argList := getList(m, "args"); argList != nil {
		for _, item := range argList {
			if s, ok := item.(string); ok {
				c.Args = append(c.Args, s)
			}
		}
	}

	// Parse env.
	if envList := getList(m, "env"); envList != nil {
		for _, item := range envList {
			em, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			c.Env = append(c.Env, EnvVar{
				Name:  getString(em, "name"),
				Value: getString(em, "value"),
			})
		}
	}

	// Parse volumeMounts.
	if vmList := getList(m, "volumeMounts"); vmList != nil {
		for _, item := range vmList {
			vm, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			c.VolumeMounts = append(c.VolumeMounts, VolumeMount{
				Name:      getString(vm, "name"),
				MountPath: getString(vm, "mountPath"),
				ReadOnly:  getString(vm, "readOnly") == "true",
			})
		}
	}

	// Parse resources.
	if res := getMap(m, "resources"); res != nil {
		c.Resources.Requests = parseResourceList(getMap(res, "requests"))
		c.Resources.Limits = parseResourceList(getMap(res, "limits"))
	}

	return c, nil
}

// parseResourceList parses cpu, memory, and gpu resource values from a map.
func parseResourceList(m map[string]interface{}) ResourceList {
	if m == nil {
		return ResourceList{}
	}
	rl := ResourceList{}
	if cpu := getString(m, "cpu"); cpu != "" {
		rl.CPUMillis = parseCPU(cpu)
	}
	if mem := getString(m, "memory"); mem != "" {
		rl.MemoryMB = parseMemory(mem)
	}
	if gpu := getString(m, "nvidia.com/gpu"); gpu != "" {
		v, _ := strconv.Atoi(gpu)
		rl.GPUMemoryMB = v
	}
	return rl
}

// parseVolumeSpec extracts a VolumeSpec from a volume map.
func parseVolumeSpec(m map[string]interface{}) VolumeSpec {
	v := VolumeSpec{
		Name: getString(m, "name"),
	}
	if hp := getMap(m, "hostPath"); hp != nil {
		v.HostPath = getString(hp, "path")
	}
	if _, ok := m["emptyDir"]; ok {
		v.EmptyDir = true
	}
	return v
}
