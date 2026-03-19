# Spark

Single-binary Go pod orchestrator for GPU hosts. Accepts Kubernetes manifests and runs workloads via podman on a single node. Connects to NATS for messaging.

## Build / Test / Run

```bash
go build ./cmd/spark
go test ./... -race -timeout 120s
go vet ./...
./spark --nats nats://localhost:4222
```

## Local OCI Registry

Spark uses a local OCI registry at `localhost:5000` to store and serve container images on the DGX. This avoids pulling from remote registries during workload execution.

### Setting up the registry on the DGX

The setup script installs a systemd service that runs a registry container via podman.

```bash
# SSH into the DGX
ssh ndungu@192.168.86.250

# Run the setup script as root
sudo bash deploy/setup-registry.sh
```

This script does three things:

1. Installs `deploy/registry.service` into systemd — a unit that runs `docker.io/library/registry:2` on port 5000 via podman.
2. Configures podman to allow insecure access to `localhost:5000` by writing `/etc/containers/registries.conf.d/spark-localhost.conf`.
3. Validates the registry by pulling, tagging, and pushing an alpine image.

After setup, the registry starts automatically on boot.

### Verifying the registry is running

```bash
# Check the systemd service
systemctl status registry.service

# Hit the registry API
curl -sf http://localhost:5000/v2/
```

### Configuring podman for insecure registry

If you need to configure podman manually (e.g., on a dev machine that pushes to the DGX), create the registry config:

```bash
sudo mkdir -p /etc/containers/registries.conf.d
sudo tee /etc/containers/registries.conf.d/spark-localhost.conf <<'TOML'
[[registry]]
location = "localhost:5000"
insecure = true
TOML
```

When pushing from a remote dev machine, use an SSH tunnel so that `localhost:5000` on your machine maps to port 5000 on the DGX:

```bash
ssh -L 5000:localhost:5000 ndungu@192.168.86.250
```

### Pushing images to the registry

Build, tag, and push an image:

```bash
# Build the image with podman
podman build -t myapp:latest .

# Tag it for the local registry
podman tag myapp:latest localhost:5000/myapp:latest

# Push to the registry
podman push localhost:5000/myapp:latest
```

Verify the image is stored:

```bash
# List repositories
curl -s http://localhost:5000/v2/_catalog

# List tags for an image
curl -s http://localhost:5000/v2/myapp/tags/list
```

### Referencing registry images in pod manifests

Pod manifests use the `localhost:5000` prefix to pull from the local registry. For example:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: myapp
spec:
  containers:
    - name: myapp
      image: localhost:5000/myapp:latest
```

This works for all supported manifest kinds (Pod, Job, CronJob, Deployment, StatefulSet). Since the registry runs on the same host as Spark, image pulls are fast and fully offline.
