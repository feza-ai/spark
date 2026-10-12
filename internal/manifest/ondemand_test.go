package manifest

import (
	"encoding/json"
	"testing"
	"time"
)

func TestOnDemandParsing(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "StatefulSet", "Job", "CronJob"} {
		t.Run(kind, func(t *testing.T) {
			a := map[string]string{"spark.feza.ai/on-demand": "true", "spark.feza.ai/backend-port": "18080", "spark.feza.ai/readiness-path": "/ready", "spark.feza.ai/idle-timeout": "30s"}
			template := map[string]any{"metadata": map[string]any{"name": "model", "annotations": a}, "spec": map[string]any{"containers": []any{map[string]any{"name": "server", "image": "test", "ports": []any{map[string]any{"containerPort": 8080, "hostPort": 18080}}}}}}
			doc := map[string]any{"kind": kind, "metadata": map[string]any{"name": "model"}, "spec": map[string]any{"template": template}}
			if kind == "Pod" {
				doc = template
				doc["kind"] = kind
			}
			if kind == "CronJob" {
				doc["spec"] = map[string]any{"schedule": "* * * * *", "jobTemplate": map[string]any{"spec": map[string]any{"template": template}}}
			}
			b, _ := json.Marshal(doc)
			parsed, err := Parse(b, nil)
			if kind == "Job" || kind == "CronJob" {
				if err == nil {
					t.Fatal("batch on-demand must be rejected")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c := parsed.Pods[0].OnDemand
			if c == nil || c.HostPort != 18080 || c.IdleTimeout != 30*time.Second || c.StartupTimeout != 5*time.Minute || c.MaxRequests != 64 {
				t.Fatalf("config = %+v", c)
			}
		})
	}
}

func TestOnDemandInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"on-demand", "yes"}, {"backend-port", "0"}, {"backend-port", "1234"}, {"readiness-path", ""}, {"readiness-path", "http://example.com/"}, {"readiness-path", "//example.com/"}, {"idle-timeout", "0s"}, {"startup-timeout", "-1s"}, {"max-requests", "0"}, {"always-warm", "yes"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			p := PodSpec{SourceKind: "Pod", Containers: []ContainerSpec{{Ports: []ContainerPort{{HostPort: 8080, Protocol: "tcp"}}}}, Annotations: map[string]string{"spark.feza.ai/on-demand": "true", "spark.feza.ai/backend-port": "8080", "spark.feza.ai/readiness-path": "/ready"}}
			p.Annotations["spark.feza.ai/"+tc.key] = tc.value
			if err := configureOnDemand(&p); err == nil {
				t.Fatal("invalid annotation accepted")
			}
		})
	}
}

func TestTCPAndGitHubConfiguration(t *testing.T) {
	p := PodSpec{SourceKind: "Pod", Containers: []ContainerSpec{{Name: "worker", Ports: []ContainerPort{{HostPort: 18080}}}}, Annotations: map[string]string{"spark.feza.ai/on-demand": "true", "spark.feza.ai/activation": "tcp", "spark.feza.ai/backend-port": "18080", "spark.feza.ai/gateway-port": "8080"}}
	if err := configureOnDemand(&p); err != nil {
		t.Fatal(err)
	}
	if p.OnDemand.ActivationMode() != "tcp" || p.OnDemand.GatewayPort != 8080 {
		t.Fatal("bad TCP config")
	}
	p.Annotations["spark.feza.ai/gateway-port"] = "18080"
	if err := configureOnDemand(&p); err == nil {
		t.Fatal("gateway/backend port collision accepted")
	}
	p.Annotations = map[string]string{"spark.feza.ai/on-demand": "true", "spark.feza.ai/activation": "github", "spark.feza.ai/github-repository": "owner/repo", "spark.feza.ai/github-runner-name": "runner", "spark.feza.ai/github-labels": "self-hosted,Linux,ARM64,dgx", "spark.feza.ai/worker-container": "worker"}
	if err := configureOnDemand(&p); err != nil {
		t.Fatal(err)
	}
	if p.OnDemand.ActivationMode() != "github" || len(p.OnDemand.RunnerLabels) != 4 {
		t.Fatal("bad GitHub config")
	}
}

func TestPersistentPodmanVolumeParsing(t *testing.T) {
	p, err := parseVolume(map[string]interface{}{"name": "data", "persistentVolumeClaim": map[string]interface{}{"claimName": "retained-db"}})
	if err != nil || p.PodmanVolume != "retained-db" {
		t.Fatalf("volume=%+v err=%v", p, err)
	}
	_, err = parseVolume(map[string]interface{}{"name": "data", "persistentVolumeClaim": map[string]interface{}{"claimName": "../../escape"}})
	if err == nil {
		t.Fatal("invalid local volume name accepted")
	}
}
