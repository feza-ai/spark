package manifest

// ResourceList represents compute resource quantities.
type ResourceList struct {
	CPUMillis   int
	MemoryMB    int
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

// ContainerSpec describes a single container within a pod.
type ContainerSpec struct {
	Name         string
	Image        string
	Command      []string
	Args         []string
	Env          []EnvVar
	Ports        []ContainerPort
	VolumeMounts []VolumeMount
	Resources    ResourceRequirements
}

// PodSpec is the internal representation of a schedulable pod.
type PodSpec struct {
	Name                          string
	Labels                        map[string]string
	Annotations                   map[string]string
	Containers                    []ContainerSpec
	Volumes                       []VolumeSpec
	RestartPolicy                 string
	PriorityClassName             string
	Priority                      int
	TerminationGracePeriodSeconds int
	SourceKind                    string // Pod, Job, Deployment, StatefulSet, CronJob
	SourceName                    string
	BackoffLimit                  int
}

// TotalRequests sums resource requests across all containers in the pod.
func (p *PodSpec) TotalRequests() ResourceList {
	var total ResourceList
	for _, c := range p.Containers {
		total.CPUMillis += c.Resources.Requests.CPUMillis
		total.MemoryMB += c.Resources.Requests.MemoryMB
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
