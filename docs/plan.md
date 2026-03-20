# Spark: Container Operations and GPU Device Management (v1.4.0)

## Status: In Progress

## Context

### Problem Statement

Spark v1.3.0 is a production-ready pod orchestrator with 31 wired use cases: NATS messaging, HTTP REST API (9 endpoints + auth + metrics), SQLite persistence, resource-aware scheduling, graceful shutdown, Prometheus metrics, and structured logging. However, it has five operational gaps that limit its utility for ML workloads on the DGX:

1. **No pod exec.** Operators cannot execute commands inside running containers. When an ML training job stalls or produces unexpected output, there is no way to inspect the container without SSH into the DGX and using podman directly. An HTTP exec endpoint (`POST /api/v1/pods/{name}/exec`) would enable remote debugging without direct host access.

2. **No container port mapping.** Pods cannot expose ports to the host. Jupyter notebooks, TensorBoard dashboards, and model serving endpoints running inside pods are inaccessible. The K8s manifest `ports` field is ignored by the parser and executor.

3. **No init containers.** ML workflows often require setup steps before training starts: downloading model weights, preparing datasets, generating configuration files. K8s init containers run sequentially to completion before main containers. Spark parses only the `containers` field and ignores `initContainers`.

4. **No GPU device assignment.** The `--gpu-max` flag is declared but unused (`_ = gpuMax`). All GPU-requesting pods receive `--device nvidia.com/gpu=all`, giving every pod access to all GPUs. On multi-GPU hosts, this prevents proper isolation. Spark should assign specific GPU device IDs via `NVIDIA_VISIBLE_DEVICES` and limit concurrent GPU pods.

5. **No image management API.** Container images can only be pulled implicitly when a pod starts. There is no way to pre-load images, check if an image exists, or list available images via the HTTP API. Pre-pulling large ML model images (often 10-50 GB) avoids cold-start delays.

### Objectives

1. Add HTTP pod exec endpoint for running commands inside containers.
2. Parse container port declarations from manifests and map them via podman `--publish`.
3. Support init containers that run sequentially before main containers.
4. Implement GPU device assignment with `NVIDIA_VISIBLE_DEVICES` and `--gpu-max` enforcement.
5. Add HTTP endpoints for listing and pulling container images.

### Non-Goals

- Interactive TTY/shell exec (single command execution only for v1.4.0).
- Service discovery or DNS-based routing to exposed ports.
- Sidecar containers (init containers only, no concurrent sidecars).
- GPU time-slicing or MIG (Multi-Instance GPU) partitioning.
- Image build or push (pull and list only).

### Constraints

- Go standard library plus `nats.go` and `modernc.org/sqlite` (CGO_ENABLED=0). See ADR-001.
- HTTP routing via `net/http.ServeMux` with Go 1.22+ method-aware patterns. See ADR-009.
- Standard `flag` package for CLI flags.
- Podman CLI only (no podman API socket).
- GPU device assignment via `NVIDIA_VISIBLE_DEVICES` environment variable (standard NVIDIA Container Toolkit mechanism).

### Success Metrics

- `curl -X POST localhost:8080/api/v1/pods/NAME/exec -d '{"command":["ls","-la"]}'` returns stdout/stderr JSON.
- Pod with `containerPort: 8888` and `hostPort: 8888` is accessible on `localhost:8888`.
- Pod with init containers: init containers run sequentially, main containers start only after all init containers succeed.
- With `--gpu-max 1`, a second GPU-requesting pod is queued (not scheduled) while the first is running.
- `curl localhost:8080/api/v1/images` returns JSON list of available images.
- `go test ./... -race -timeout 120s` passes with all new tests.

## Discovery Summary

**Current architecture** (from docs/design.md):
- 13 packages, 31 use cases (all WIRED).
- HTTP API: 9 endpoints + auth middleware + Prometheus metrics on :8080.
- Executor interface: CreatePod, StopPod, PodStatus, RemovePod, ListPods, PodStats, PodLogs, StreamPodLogs. No Exec, no port mapping, no init container orchestration.
- Manifest types: PodSpec has Containers but no InitContainers or Ports. ContainerSpec has no Ports field.
- Scheduler: ResourceTracker tracks CPUMillis, MemoryMB, GPUMemoryMB. No GPU device ID tracking. `--gpu-max` flag unused.
- GPU detection: `gpu.GPUInfo` has GPUCount field. Device IDs not enumerated.
- CI/CD: GitHub Actions with build/test/vet/staticcheck + release-please + goreleaser. Already operational.

**Key interfaces to extend**:
- `manifest.ContainerSpec`: add `Ports []ContainerPort` field.
- `manifest.PodSpec`: add `InitContainers []ContainerSpec` field.
- `executor.Executor`: add `ExecPod(ctx, name, command) (stdout, stderr, exitCode, error)`.
- `executor.PodmanExecutor.CreatePod`: handle init containers (sequential), port mapping (`--publish`), GPU device env var.
- `scheduler.ResourceTracker`: add GPU device slot tracking alongside memory tracking.
- `gpu.Detect()`: enumerate device IDs (0, 1, ..., N-1).
- `internal/api`: add POST /exec, GET /images, POST /images/pull endpoints.

**Use cases affected**:
- UC-032 (pod exec): new operational use case.
- UC-033 (port mapping): new networking use case.
- UC-034 (init containers): new pod lifecycle use case.
- UC-035 (GPU device assignment): extends UC-005 scheduling and UC-014 GPU detection.
- UC-036 (image management): new operational use case.

## Scope and Deliverables

### In Scope

| ID | Deliverable | Acceptance Criteria |
|----|-------------|---------------------|
| D1 | Pod exec endpoint | POST /api/v1/pods/{name}/exec runs a command, returns stdout/stderr/exitCode JSON |
| D2 | Container port mapping | Parser reads ports, executor maps with --publish, ports visible in pod detail |
| D3 | Init containers | Parser reads initContainers, executor runs them sequentially before main containers |
| D4 | GPU device assignment | Scheduler assigns device IDs, executor sets NVIDIA_VISIBLE_DEVICES, --gpu-max enforced |
| D5 | Image management API | GET /api/v1/images lists images, POST /api/v1/images/pull pulls by name:tag |

### Out of Scope

- Interactive TTY exec or WebSocket shell
- Service discovery or port-based routing
- Sidecar containers
- GPU MIG or time-slicing
- Image build or push

## Checkable Work Breakdown

### E34: Pod Exec

- [x] T34.1 Add ExecPod method to executor  Owner: TBD  Est: 45m  verifies: [UC-032]
  - Add to `executor.Executor` interface: `ExecPod(ctx context.Context, podName string, containerName string, command []string) ([]byte, []byte, int, error)` returning (stdout, stderr, exitCode, err).
  - Implement in `PodmanExecutor`: run `podman exec <podName>-<containerName> <command...>`.
  - Capture stdout and stderr separately using `cmd.StdoutPipe()` and `cmd.StderrPipe()`.
  - Return exit code from `cmd.ProcessState.ExitCode()`.
  - If containerName is empty, default to the first container: `<podName>-<spec.Containers[0].Name>`.
  - Create tests with stub command runner.
  - Acceptance: `go test -race ./internal/executor/` passes.

- [x] T34.2 Add pod exec HTTP endpoint in internal/api  Owner: TBD  Est: 45m  verifies: [UC-032]
  - Depends on: T34.1.
  - Create `internal/api/pods_exec.go`:
    - `POST /api/v1/pods/{name}/exec` handler.
    - Request body JSON: `{"command":["ls","-la"],"container":"worker"}` (container optional).
    - Verify pod exists and is Running (404 if not found, 400 if not running).
    - Call `executor.ExecPod(ctx, name, container, command)`.
    - Response JSON: `{"stdout":"...","stderr":"...","exit_code":0}`.
    - 400 if command is empty.
  - Register route in server.go.
  - Create tests: exec success, pod not found, pod not running, empty command.
  - Acceptance: `go test -race ./internal/api/` passes.

### E35: Container Port Mapping

- [x] T35.1 Add port types to manifest and parse from YAML  Owner: TBD  Est: 45m  verifies: [UC-033]
  - Add to `manifest.ContainerSpec`:
    - `Ports []ContainerPort`
  - Define `type ContainerPort struct { ContainerPort int; HostPort int; Protocol string }`.
  - Protocol defaults to "tcp" if not specified.
  - Update the container parser (in parse.go or the relevant parser functions) to extract `ports` from each container in the YAML:
    ```yaml
    ports:
      - containerPort: 8888
        hostPort: 8888
        protocol: TCP
    ```
  - Create tests: parse ports, default protocol, missing hostPort (use containerPort).
  - Acceptance: `go test -race ./internal/manifest/` passes. Ports are parsed correctly.

- [ ] T35.2 Wire port mapping into executor CreatePod  Owner: TBD  Est: 30m  verifies: [UC-033]
  - Depends on: T35.1.
  - Update `executor.buildRunArgs`: for each port in container.Ports:
    - Add `--publish <hostPort>:<containerPort>/<protocol>` to podman args.
    - If HostPort is 0, omit the host port (podman assigns a random port).
  - Actually, in podman, ports must be added during `pod create`, not `run`. Update `CreatePod` to add `--publish` args to the pod create command (not the container run command).
  - Create tests: verify --publish args in pod create command.
  - Acceptance: `go test -race ./internal/executor/` passes.

- [x] T35.3 Expose ports in pod detail API response  Owner: TBD  Est: 15m  verifies: [UC-033]
  - Depends on: T35.1.
  - Update the pod detail JSON response in `pods_query.go` to include ports from the pod spec.
  - No new endpoint -- extend existing GET /api/v1/pods/{name} response.
  - Create test: apply pod with ports, GET detail, verify ports in response.
  - Acceptance: `go test -race ./internal/api/` passes.

### E36: Init Containers

- [x] T36.1 Add InitContainers field to PodSpec and parse from YAML  Owner: TBD  Est: 30m  verifies: [UC-034]
  - Add to `manifest.PodSpec`: `InitContainers []ContainerSpec`.
  - Update Pod parser to extract `initContainers` from the YAML spec (same structure as `containers`).
  - Create tests: parse init containers, empty init containers, init + main containers.
  - Acceptance: `go test -race ./internal/manifest/` passes.

- [ ] T36.2 Execute init containers sequentially in CreatePod  Owner: TBD  Est: 45m  verifies: [UC-034]
  - Depends on: T36.1.
  - Update `PodmanExecutor.CreatePod`:
    1. After pod create, run each init container sequentially (not in background, use `podman run` without `-d`).
    2. Wait for each init container to complete (`cmd.Wait()`).
    3. If any init container exits non-zero, return error immediately (do not start main containers).
    4. After all init containers succeed, start main containers as before (with `-d`).
  - Init containers use the same volume mounts, env vars, and network as main containers.
  - Init containers are named `<podName>-init-<index>-<name>`.
  - Create tests with stub: verify init containers run before main, verify failure stops execution.
  - Acceptance: `go test -race ./internal/executor/` passes.

### E37: GPU Device Assignment

- [x] T37.1 Add GPU device enumeration to gpu package  Owner: TBD  Est: 30m  verifies: [UC-035]
  - Update `gpu.GPUInfo` to include `DeviceIDs []int` field (e.g., [0, 1, 2, 3]).
  - Update `gpu.Detect()` / `gpu.DetectWithFallback()` to populate device IDs by parsing nvidia-smi output.
  - For single-GPU hosts like DGX Spark, DeviceIDs = [0].
  - For multi-GPU: parse `nvidia-smi -L` to count devices, assign IDs 0 through N-1.
  - Create tests with stub nvidia-smi output.
  - Acceptance: `go test -race ./internal/gpu/` passes.

- [x] T37.2 Add GPU device slot tracking to scheduler  Owner: TBD  Est: 45m  verifies: [UC-035]
  - Add to `ResourceTracker`:
    - `gpuDevices []int`: list of all GPU device IDs.
    - `gpuMax int`: max concurrent GPU pods (from --gpu-max flag).
    - `gpuAssignments map[string][]int`: pod name -> assigned device IDs.
  - Update `NewResourceTracker` to accept device IDs and gpuMax.
  - Update `Allocate`: if pod requests GPUMemoryMB > 0, assign a free device ID. If no device available (all assigned or gpuMax reached), return error.
  - Update `Release`: free the device IDs for the released pod.
  - Add `AssignedGPUs(name string) []int` getter.
  - Create tests: allocate GPU, release GPU, exceed gpuMax, multi-device assignment.
  - Acceptance: `go test -race ./internal/scheduler/` passes.

- [ ] T37.3 Set NVIDIA_VISIBLE_DEVICES in executor  Owner: TBD  Est: 30m  verifies: [UC-035]
  - Depends on: T37.2.
  - Update `executor.CreatePod` to accept an optional `gpuDevices []int` parameter (or read from a context/spec).
  - In `buildRunArgs`: if GPU devices are assigned, add `--env NVIDIA_VISIBLE_DEVICES=<comma-separated IDs>` instead of `--device nvidia.com/gpu=all`.
  - If no GPU devices assigned (non-GPU pod), do not add GPU flags.
  - Update reconciler and main.go to pass GPU assignments from scheduler to executor.
  - Create tests: verify NVIDIA_VISIBLE_DEVICES env var in args.
  - Acceptance: `go test -race ./internal/executor/` passes.

### E38: Image Management API

- [x] T38.1 Add image list and pull methods to executor  Owner: TBD  Est: 30m  verifies: [UC-036]
  - Add to `executor.Executor` interface:
    - `ListImages(ctx context.Context) ([]ImageInfo, error)`.
    - `PullImage(ctx context.Context, name string) error`.
  - Define `type ImageInfo struct { Name string; Tag string; Size string; Created string }`.
  - Implement in `PodmanExecutor`:
    - `ListImages`: run `podman images --format json`, parse JSON output.
    - `PullImage`: run `podman pull <name>`. Return error if pull fails.
  - Note: `ImageExists` and `PullIfNeeded` already exist in `internal/executor/image.go`. Extend or reuse.
  - Create tests with stub.
  - Acceptance: `go test -race ./internal/executor/` passes.

- [ ] T38.2 Add image management HTTP endpoints  Owner: TBD  Est: 30m  verifies: [UC-036]
  - Depends on: T38.1.
  - Create `internal/api/images.go`:
    - `GET /api/v1/images` handler: calls `executor.ListImages()`, returns JSON array.
    - `POST /api/v1/images/pull` handler: reads body `{"image":"name:tag"}`, calls `executor.PullImage()`, returns 200 on success, 400 on failure.
  - Register routes in server.go.
  - Create tests: list images, pull image, pull invalid image.
  - Acceptance: `go test -race ./internal/api/` passes.

### E39: Integration Wiring

- [ ] T39.1 Wire GPU device assignment into main.go and reconciler  Owner: TBD  Est: 45m  verifies: [UC-032, UC-033, UC-034, UC-035, UC-036]
  - Depends on: T34.1, T34.2, T35.1, T35.2, T36.1, T36.2, T37.1, T37.2, T37.3, T38.1, T38.2.
  - Update `cmd/spark/main.go`:
    - Pass GPU device IDs and `*gpuMax` to `NewResourceTracker`.
    - Pass GPU assignments from scheduler to executor in reconciler loop.
  - Update `reconciler.Reconciler` to retrieve GPU assignments when creating pods.
  - Ensure all new HTTP routes are registered.
  - Acceptance: `go build ./...` and `go vet ./...` pass.

- [ ] T39.2 Run full test suite and lint  Owner: TBD  Est: 15m  verifies: [infrastructure]
  - Depends on: T39.1.
  - Run `go test ./... -race -timeout 120s`. Zero failures.
  - Run `go vet ./...`. Zero warnings.
  - Acceptance: All tests pass, zero lint warnings.

- [ ] T39.3 Update README and design docs for v1.4.0  Owner: TBD  Est: 30m  verifies: [infrastructure]
  - Depends on: T39.2.
  - Update README.md: add exec, ports, init containers, GPU device assignment, image management sections.
  - Update docs/design.md: add new interfaces and invariants.
  - Acceptance: README accurately describes all v1.4.0 features.

## Parallel Work

### Tracks

| Track | Tasks | Description |
|-------|-------|-------------|
| A: Pod Exec | T34.1, T34.2 | Executor exec + HTTP endpoint |
| B: Port Mapping | T35.1, T35.2, T35.3 | Manifest ports + executor + API |
| C: Init Containers | T36.1, T36.2 | Manifest init + executor orchestration |
| D: GPU Devices | T37.1, T37.2, T37.3 | GPU enumeration + scheduler + executor |
| E: Image Management | T38.1, T38.2 | Executor list/pull + HTTP endpoints |

Sync point: T39.1 requires all tracks to complete before wiring.

### Waves

### Wave 1: Independent Components (8 agents)
- [x] T34.1 Add ExecPod method to executor  verifies: [UC-032]
- [x] T35.1 Add port types to manifest and parse from YAML  verifies: [UC-033]
- [x] T36.1 Add InitContainers field to PodSpec and parse from YAML  verifies: [UC-034]
- [x] T37.1 Add GPU device enumeration to gpu package  verifies: [UC-035]
- [x] T37.2 Add GPU device slot tracking to scheduler  verifies: [UC-035]
- [x] T38.1 Add image list and pull methods to executor  verifies: [UC-036]
- [x] T35.3 Expose ports in pod detail API response  verifies: [UC-033]
- [x] T34.2 Add pod exec HTTP endpoint in internal/api  verifies: [UC-032]

### Wave 2: Dependent Components (4 agents)
- [ ] T35.2 Wire port mapping into executor CreatePod  verifies: [UC-033]
- [ ] T36.2 Execute init containers sequentially in CreatePod  verifies: [UC-034]
- [ ] T37.3 Set NVIDIA_VISIBLE_DEVICES in executor  verifies: [UC-035]
- [ ] T38.2 Add image management HTTP endpoints  verifies: [UC-036]

### Wave 3: Integration and Verification (3 agents)
- [ ] T39.1 Wire GPU device assignment into main.go and reconciler  verifies: [UC-032, UC-033, UC-034, UC-035, UC-036]
- [ ] T39.2 Run full test suite and lint  verifies: [infrastructure]
- [ ] T39.3 Update README and design docs for v1.4.0  verifies: [infrastructure]

Note: T34.2 depends on T34.1 but can run in Wave 1 if assigned to the same agent sequentially. T35.2 depends on T35.1. T36.2 depends on T36.1. T37.3 depends on T37.2. T38.2 depends on T38.1. Wave 3 tasks depend on all prior waves.

## Timeline and Milestones

| ID | Milestone | Dependencies | Exit Criteria |
|----|-----------|--------------|---------------|
| M1 | Pod exec functional | T34.1, T34.2 | Exec command returns stdout/stderr via HTTP |
| M2 | Port mapping functional | T35.1, T35.2, T35.3 | Pod with ports accessible on host |
| M3 | Init containers functional | T36.1, T36.2 | Init containers run sequentially before main |
| M4 | GPU device assignment functional | T37.1, T37.2, T37.3 | NVIDIA_VISIBLE_DEVICES set, --gpu-max enforced |
| M5 | Wired, tested, documented | T39.1, T39.2, T39.3 | Full integration passes, README updated |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | podman exec with separate stdout/stderr piping race conditions | Medium | Medium | Use cmd.Output() for simple cases. For large output, use goroutines with WaitGroup. |
| R2 | Port conflicts on host when multiple pods expose same port | High | Medium | Return clear error message. Do not retry with different port -- user must resolve. |
| R3 | Init container failure leaves pod in inconsistent state | High | Low | On init failure: remove all init containers, remove the pod, mark as Failed in store. |
| R4 | NVIDIA_VISIBLE_DEVICES format varies across driver versions | Medium | Low | Use comma-separated integer device IDs (0,1,2). This is the universal format. |
| R5 | Large image pulls block the HTTP handler for minutes | Medium | High | Pull in a goroutine. Return 202 Accepted with a status message. Poll for completion via image list. |
| R6 | GPU device tracking inconsistent after crash recovery | Medium | Medium | On startup recovery, query podman for running GPU pods, re-register device assignments. |

## Operating Procedure

### Definition of Done
- `go build ./...` succeeds.
- `go vet ./...` reports zero warnings.
- `go test ./... -race -timeout 120s` passes.
- All new functions have unit tests.
- Each commit touches one package directory.
- Conventional Commits format.

### Review and QA
- Rebase and merge (not squash, not merge commits).
- Each commit is a small, logical unit touching one package.
- HTTP tests use `httptest.NewServer` (no real network binding).
- Executor tests use stub command runner (no podman dependency in CI).
- Init container tests verify sequential execution and failure propagation.
- GPU tests verify device ID assignment and release.

## Progress Log

### 2026-03-20: Plan created
- Trimmed completed v1.3.0 plan. v1.3.0 knowledge preserved in docs/devlog.md (2026-03-19 entry) and docs/adr/010-011.
- Updated use case manifest: UC-026 through UC-031 marked WIRED. Added UC-032 through UC-036 as PLANNED.
- Created plan for v1.4.0: pod exec, port mapping, init containers, GPU device assignment, image management.
- 15 tasks across 3 waves. 8 agents in Wave 1, 4 in Wave 2, 3 in Wave 3.

## Hand-off Notes

- Spark is a single-binary Go pod orchestrator for GPU hosts. See docs/design.md for architecture.
- v1.3.0 is complete: Prometheus metrics, HTTP auth, pod logs/events via HTTP, JSON logging, emptyDir volumes. All 31 use cases WIRED.
- CI/CD is operational: GitHub Actions with build/test/vet/staticcheck + release-please + goreleaser.
- This plan adds five features: pod exec, port mapping, init containers, GPU device assignment, image management.
- Pod exec uses `podman exec <container> <command>`. Container naming: `<podName>-<containerName>`.
- Port mapping: ports declared in manifest, mapped via `podman pod create --publish`. Must be on pod create, not container run.
- Init containers: run sequentially without `-d` flag. Fail-fast on non-zero exit.
- GPU device assignment: `NVIDIA_VISIBLE_DEVICES` env var replaces `--device nvidia.com/gpu=all`. Device IDs from nvidia-smi. `--gpu-max` limits concurrent GPU pods.
- Image management: extends existing image.go with ListImages and HTTP endpoints.
- DGX Spark target: `ssh ndungu@192.168.86.250` (Ubuntu arm64, NVIDIA Grace CPU, 1 GPU 128GB unified memory).
