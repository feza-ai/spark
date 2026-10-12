package manifest

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OnDemandSpec configures a service whose containers may be unloaded.
// Durations are persisted with the pod spec; request activity is process-local.
type OnDemandSpec struct {
	Mode               string // http (default), tcp, or github
	GatewayPort        int    // stable TCP listener, distinct from the backend hostPort
	GitHubRepo         string
	RunnerName         string
	RunnerLabels       []string
	WorkerContainer    string
	ReadinessContainer string
	ReadinessCommand   []string
	HostPort           int
	ReadinessPath      string
	IdleTimeout        time.Duration
	StartupTimeout     time.Duration
	MaxRequests        int
	AlwaysWarm         bool
}

func (c OnDemandSpec) ActivationMode() string {
	if c.Mode == "" {
		return "http"
	}
	return c.Mode
}

func (c OnDemandSpec) BackendURL() string {
	return "http://127.0.0.1:" + strconv.Itoa(c.HostPort)
}

func configureOnDemand(p *PodSpec) error {
	const prefix = "spark.feza.ai/"
	a := p.Annotations
	enabled := a[prefix+"on-demand"]
	if enabled == "" || enabled == "false" {
		return nil
	}
	if enabled != "true" {
		return fmt.Errorf("%son-demand must be true or false", prefix)
	}
	if p.SourceKind == "Job" || p.SourceKind == "CronJob" {
		return fmt.Errorf("on-demand is only supported for services (Pod, Deployment, StatefulSet)")
	}
	c := &OnDemandSpec{Mode: a[prefix+"activation"], IdleTimeout: 5 * time.Minute, StartupTimeout: 5 * time.Minute, MaxRequests: 64}
	switch c.ActivationMode() {
	case "http", "tcp":
		port, err := strconv.Atoi(a[prefix+"backend-port"])
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("on-demand backend-port must be a host port between 1 and 65535")
		}
		mapped := false
		for _, container := range p.Containers {
			for _, cp := range container.Ports {
				if cp.HostPort == port && (cp.Protocol == "" || strings.EqualFold(cp.Protocol, "tcp")) {
					mapped = true
				}
			}
		}
		if !mapped {
			return fmt.Errorf("on-demand backend-port %d must match a TCP hostPort in the pod", port)
		}
		c.HostPort = port
		if c.ActivationMode() == "http" {
			c.ReadinessPath = a[prefix+"readiness-path"]
			u, err := url.ParseRequestURI(c.ReadinessPath)
			if err != nil || !strings.HasPrefix(c.ReadinessPath, "/") || strings.HasPrefix(c.ReadinessPath, "//") || u.IsAbs() || u.Host != "" || u.Fragment != "" {
				return fmt.Errorf("on-demand readiness-path must be an absolute HTTP path, e.g. /health")
			}
		} else {
			c.GatewayPort, err = strconv.Atoi(a[prefix+"gateway-port"])
			if err != nil || c.GatewayPort < 1 || c.GatewayPort > 65535 || c.GatewayPort == c.HostPort {
				return fmt.Errorf("TCP gateway-port must be a valid port distinct from backend-port")
			}
		}
	case "github":
		c.GitHubRepo = a[prefix+"github-repository"]
		parts := strings.Split(c.GitHubRepo, "/")
		if len(parts) != 2 || !validName(parts[0]) || !validName(parts[1]) {
			return fmt.Errorf("github-repository must be owner/repository")
		}
		c.RunnerName = a[prefix+"github-runner-name"]
		c.WorkerContainer = a[prefix+"worker-container"]
		if c.RunnerName == "" || c.WorkerContainer == "" {
			return fmt.Errorf("GitHub activation requires github-runner-name and worker-container")
		}
		found := false
		for _, container := range p.Containers {
			if container.Name == c.WorkerContainer {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("worker-container must name a container in the pod")
		}
		for _, label := range strings.Split(a[prefix+"github-labels"], ",") {
			if label = strings.TrimSpace(label); label != "" {
				c.RunnerLabels = append(c.RunnerLabels, strings.ToLower(label))
			}
		}
		if len(c.RunnerLabels) == 0 {
			return fmt.Errorf("GitHub activation requires github-labels")
		}
	default:
		return fmt.Errorf("activation must be http, tcp, or github")
	}
	if v := a[prefix+"readiness-command"]; v != "" {
		if err := json.Unmarshal([]byte(v), &c.ReadinessCommand); err != nil || len(c.ReadinessCommand) == 0 || c.ReadinessCommand[0] == "" {
			return fmt.Errorf("readiness-command must be a nonempty JSON string array")
		}
		c.ReadinessContainer = a[prefix+"readiness-container"]
		found := false
		for _, container := range p.Containers {
			if container.Name == c.ReadinessContainer {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("readiness-container must name a container in the pod")
		}
	}
	for key, dst := range map[string]*time.Duration{"idle-timeout": &c.IdleTimeout, "startup-timeout": &c.StartupTimeout} {
		if v, ok := a[prefix+key]; ok {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return fmt.Errorf("on-demand %s must be a positive duration", key)
			}
			*dst = d
		}
	}
	if v, ok := a[prefix+"max-requests"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 10000 {
			return fmt.Errorf("on-demand max-requests must be between 1 and 10000")
		}
		c.MaxRequests = n
	}
	if v, ok := a[prefix+"always-warm"]; ok {
		if v != "true" && v != "false" {
			return fmt.Errorf("on-demand always-warm must be true or false")
		}
		c.AlwaysWarm = v == "true"
	}
	if p.RestartPolicy == "" {
		p.RestartPolicy = "Always"
	}
	if p.RestartPolicy != "Always" {
		return fmt.Errorf("on-demand services require restartPolicy Always")
	}
	p.OnDemand = c
	return nil
}

func validName(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
