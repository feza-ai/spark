package manifest

// ResourceList represents compute resource quantities.
type ResourceList struct {
	CPUMillis   int
	MemoryMB    int
	GPUCount    int
	GPUMemoryMB int
}

// ResourceRequirements describes resource requests and limits for a container.
type ResourceRequirements struct {
	Requests ResourceList
	Limits   ResourceList
	// MemoryRequestUndeclared reports that the container declared neither a
	// memory request nor a memory limit. Such a container is accounted at
	// 0MB unless Parse is given WithDefaultMemoryRequestMB, which is how a
	// node packed with undeclared containers reported a completely empty
	// memory ledger while it was full (issue #121).
	MemoryRequestUndeclared bool
}

// EnvVar represents an environment variable.
type EnvVar struct {
	Name  string
	Value string
}

// ContainerPort describes a port exposed by a container.
type ContainerPort struct {
	ContainerPort int
	HostPort      int
	Protocol      string // defaults to "tcp"
}

// VolumeMount describes a mount point for a volume in a container.
type VolumeMount struct {
	Name      string
	MountPath string
	ReadOnly  bool
}

// VolumeSpec describes a volume available to a pod.
type VolumeSpec struct {
	Name     string
	HostPath string
	EmptyDir bool
}

// SecurityContext holds security configuration for a container.
type SecurityContext struct {
	RunAsUser    int
	RunAsNonRoot bool
	Privileged   bool
	AddCaps      []string // from capabilities.add
	DropCaps     []string // from capabilities.drop
}

// ExecProbe describes a probe that runs a command inside the container.
type ExecProbe struct {
	Command []string
}

// HTTPGetProbe describes a probe that performs an HTTP GET request.
type HTTPGetProbe struct {
	Path string
	Port int
}

// ProbeSpec describes a health probe for a container.
type ProbeSpec struct {
	Exec                *ExecProbe
	HTTPGet             *HTTPGetProbe
	InitialDelaySeconds int
	PeriodSeconds       int // default 10
	FailureThreshold    int // default 3
	TimeoutSeconds      int // default 1
}

// ContainerSpec describes a single container within a pod.
type ContainerSpec struct {
	Name            string
	Image           string
	Command         []string
	Args            []string
	Env             []EnvVar
	Ports           []ContainerPort
	VolumeMounts    []VolumeMount
	Resources       ResourceRequirements
	SecurityContext *SecurityContext
	LivenessProbe   *ProbeSpec
}

// PodSpec is the internal representation of a schedulable pod.
type PodSpec struct {
	Name                          string
	Labels                        map[string]string
	Annotations                   map[string]string
	InitContainers                []ContainerSpec
	Containers                    []ContainerSpec
	Volumes                       []VolumeSpec
	RestartPolicy                 string
	PriorityClassName             string
	Priority                      int
	TerminationGracePeriodSeconds int
	SourceKind                    string // Pod, Job, Deployment, StatefulSet, CronJob
	SourceName                    string
	BackoffLimit                  int
	GPUDevices                    []int // runtime: assigned GPU device IDs (set by scheduler, not parsed from YAML)
	CpusetCores                   []int // runtime: assigned CPU core IDs for --cpuset-cpus (set by reconciler, not parsed from YAML)
}

// UndeclaredMemoryContainers returns the names of accounted containers that
// declared neither a memory request nor a memory limit, in spec order.
// Returns nil when every container declared memory. Init containers are
// excluded because TotalRequests does not account them.
func (p *PodSpec) UndeclaredMemoryContainers() []string {
	var names []string
	for _, c := range p.Containers {
		if c.Resources.MemoryRequestUndeclared {
			names = append(names, c.Name)
		}
	}
	return names
}

// TotalRequests sums resource requests across all containers in the pod.
func (p *PodSpec) TotalRequests() ResourceList {
	var total ResourceList
	for _, c := range p.Containers {
		total.CPUMillis += c.Resources.Requests.CPUMillis
		total.MemoryMB += c.Resources.Requests.MemoryMB
		total.GPUCount += c.Resources.Requests.GPUCount
		total.GPUMemoryMB += c.Resources.Requests.GPUMemoryMB
	}
	return total
}

// CronJobSpec describes a cron-scheduled job.
type CronJobSpec struct {
	Name                       string
	Schedule                   string
	ConcurrencyPolicy          string
	SuccessfulJobsHistoryLimit int
	FailedJobsHistoryLimit     int
	JobTemplate                PodSpec
	BackoffLimit               int
}
