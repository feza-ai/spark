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
