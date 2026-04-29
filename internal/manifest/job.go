package manifest

import (
	"fmt"
)

// parseJob extracts a PodSpec from a parsed Job manifest node.
// The node represents the top-level YAML document already parsed into a map structure.
// Returns a single PodSpec with SourceKind="Job" and BackoffLimit from the job spec.
func parseJob(root map[string]interface{}, priorityClasses map[string]int) ([]PodSpec, error) {
	templateSpec := getMap(root, "spec", "template", "spec")
	if templateSpec == nil {
		return nil, fmt.Errorf("job %q: missing spec.template.spec", getString(root, "metadata", "name"))
	}

	pod, err := parsePodFromMap(templateSpec, priorityClasses)
	if err != nil {
		return nil, fmt.Errorf("job %q: %w", getString(root, "metadata", "name"), err)
	}

	pod.Name = getString(root, "metadata", "name")
	pod.SourceKind = "Job"
	pod.SourceName = getString(root, "metadata", "name")

	// Labels and annotations from the pod template metadata.
	pod.Labels = getStringMap(root, "spec", "template", "metadata", "labels")
	pod.Annotations = getStringMap(root, "spec", "template", "metadata", "annotations")

	// BackoffLimit defaults to 2 for jobs.
	bl := getInt(root, "spec", "backoffLimit")
	if bl == 0 {
		if getString(root, "spec", "backoffLimit") == "0" {
			pod.BackoffLimit = 0
		} else {
			pod.BackoffLimit = 2
		}
	} else {
		pod.BackoffLimit = bl
	}

	// RestartPolicy defaults to "Never" for jobs.
	if pod.RestartPolicy == "" {
		pod.RestartPolicy = "Never"
	}

	return []PodSpec{pod}, nil
}

// parsePodFromMap converts a pod spec map into a PodSpec.
// This handles the contents of spec: for a Pod, or spec.template.spec for Job/Deployment/etc.
func parsePodFromMap(specMap map[string]interface{}, priorityClasses map[string]int) (PodSpec, error) {
	var pod PodSpec

	pod.RestartPolicy = getString(specMap, "restartPolicy")

	pod.PriorityClassName = getString(specMap, "priorityClassName")
	if pod.PriorityClassName != "" {
		pod.Priority = ResolvePriority(priorityClasses, pod.PriorityClassName)
	} else {
		pod.Priority = ResolvePriority(priorityClasses, "normal")
	}

	pod.TerminationGracePeriodSeconds = getInt(specMap, "terminationGracePeriodSeconds")

	for _, item := range getList(specMap, "initContainers") {
		cm, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		pod.InitContainers = append(pod.InitContainers, parseContainer(cm))
	}

	for _, item := range getList(specMap, "containers") {
		cm, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		pod.Containers = append(pod.Containers, parseContainer(cm))
	}

	for _, item := range getList(specMap, "volumes") {
		vm, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		vol, err := parseVolume(vm)
		if err != nil {
			return PodSpec{}, err
		}
		pod.Volumes = append(pod.Volumes, vol)
	}

	return pod, nil
}

func parseContainer(cm map[string]interface{}) ContainerSpec {
	var c ContainerSpec
	c.Name = getString(cm, "name")
	c.Image = getString(cm, "image")

	for _, v := range getList(cm, "command") {
		if s, ok := v.(string); ok {
			c.Command = append(c.Command, s)
		}
	}

	for _, v := range getList(cm, "args") {
		if s, ok := v.(string); ok {
			c.Args = append(c.Args, s)
		}
	}

	for _, v := range getList(cm, "env") {
		em, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		c.Env = append(c.Env, EnvVar{
			Name:  getString(em, "name"),
			Value: getString(em, "value"),
		})
	}

	for _, v := range getList(cm, "ports") {
		pm, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		cp := getInt(pm, "containerPort")
		hp := getInt(pm, "hostPort")
		if hp == 0 {
			hp = cp
		}
		proto := getString(pm, "protocol")
		if proto == "" {
			proto = "tcp"
		}
		c.Ports = append(c.Ports, ContainerPort{
			ContainerPort: cp,
			HostPort:      hp,
			Protocol:      proto,
		})
	}

	for _, v := range getList(cm, "volumeMounts") {
		vmm, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		c.VolumeMounts = append(c.VolumeMounts, VolumeMount{
			Name:      getString(vmm, "name"),
			MountPath: getString(vmm, "mountPath"),
			ReadOnly:  getString(vmm, "readOnly") == "true",
		})
	}

	c.Resources = parseResources(getMap(cm, "resources"))
	c.SecurityContext = parseSecurityContext(getMap(cm, "securityContext"))
	c.LivenessProbe = parseProbe(getMap(cm, "livenessProbe"))
	return c
}

func parseSecurityContext(sc map[string]interface{}) *SecurityContext {
	if sc == nil {
		return nil
	}
	ctx := &SecurityContext{
		RunAsUser:    getInt(sc, "runAsUser"),
		RunAsNonRoot: getString(sc, "runAsNonRoot") == "true",
		Privileged:   getString(sc, "privileged") == "true",
	}
	for _, v := range getList(sc, "capabilities", "add") {
		if s, ok := v.(string); ok {
			ctx.AddCaps = append(ctx.AddCaps, s)
		}
	}
	for _, v := range getList(sc, "capabilities", "drop") {
		if s, ok := v.(string); ok {
			ctx.DropCaps = append(ctx.DropCaps, s)
		}
	}
	return ctx
}

func parseResources(rm map[string]interface{}) ResourceRequirements {
	if rm == nil {
		return ResourceRequirements{}
	}
	return ResourceRequirements{
		Requests: parseResourceList(getMap(rm, "requests")),
		Limits:   parseResourceList(getMap(rm, "limits")),
	}
}

func parseResourceList(rm map[string]interface{}) ResourceList {
	if rm == nil {
		return ResourceList{}
	}
	return ResourceList{
		CPUMillis:   parseCPU(getString(rm, "cpu")),
		MemoryMB:    parseMemory(getString(rm, "memory")),
		GPUCount:  parseGPU(getString(rm, "nvidia.com/gpu")),
	}
}

// parseGPU converts a GPU count string (e.g. "2") to an integer.
func parseGPU(s string) int {
	if s == "" {
		return 0
	}
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

func parseProbe(pm map[string]interface{}) *ProbeSpec {
	if pm == nil {
		return nil
	}
	probe := &ProbeSpec{
		InitialDelaySeconds: getInt(pm, "initialDelaySeconds"),
		PeriodSeconds:       getInt(pm, "periodSeconds"),
		FailureThreshold:    getInt(pm, "failureThreshold"),
		TimeoutSeconds:      getInt(pm, "timeoutSeconds"),
	}
	// Apply defaults.
	if probe.PeriodSeconds == 0 {
		probe.PeriodSeconds = 10
	}
	if probe.FailureThreshold == 0 {
		probe.FailureThreshold = 3
	}
	if probe.TimeoutSeconds == 0 {
		probe.TimeoutSeconds = 1
	}
	// Parse exec probe.
	if execMap := getMap(pm, "exec"); execMap != nil {
		ep := &ExecProbe{}
		for _, v := range getList(execMap, "command") {
			if s, ok := v.(string); ok {
				ep.Command = append(ep.Command, s)
			}
		}
		probe.Exec = ep
	}
	// Parse httpGet probe.
	if httpMap := getMap(pm, "httpGet"); httpMap != nil {
		probe.HTTPGet = &HTTPGetProbe{
			Path: getString(httpMap, "path"),
			Port: getInt(httpMap, "port"),
		}
	}
	return probe
}

func parseVolume(vm map[string]interface{}) (VolumeSpec, error) {
	v := VolumeSpec{
		Name: getString(vm, "name"),
	}
	_, hostPathPresent := vm["hostPath"]
	if hp := getMap(vm, "hostPath"); hp != nil {
		v.HostPath = getString(hp, "path")
	}
	if getMap(vm, "emptyDir") != nil {
		v.EmptyDir = true
	}
	if s, ok := vm["emptyDir"]; ok {
		if _, isStr := s.(string); isStr {
			v.EmptyDir = true
		}
	}
	if hostPathPresent && v.HostPath == "" {
		return VolumeSpec{}, fmt.Errorf("volume %q: hostPath.path is empty", v.Name)
	}
	if !hostPathPresent && !v.EmptyDir {
		return VolumeSpec{}, fmt.Errorf("volume %q: must set hostPath or emptyDir", v.Name)
	}
	return v, nil
}
