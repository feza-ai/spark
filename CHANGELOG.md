# Changelog

## [1.10.1](https://github.com/feza-ai/spark/compare/v1.10.0...v1.10.1) (2026-04-16)


### Bug Fixes

* **deploy:** use --force-confold in auto-upgrade to avoid conffile prompt ([c922357](https://github.com/feza-ai/spark/commit/c92235781535f5aadd6269cc707fe3954518f426))

## [1.10.0](https://github.com/feza-ai/spark/compare/v1.9.0...v1.10.0) (2026-04-16)


### Features

* **deploy:** wire --system-reserve-cores into systemd unit (refs [#22](https://github.com/feza-ai/spark/issues/22)) ([e1376b9](https://github.com/feza-ai/spark/commit/e1376b9fda106399ffbef83e0ec059cb407b0021))

## [1.9.0](https://github.com/feza-ai/spark/compare/v1.8.1...v1.9.0) (2026-04-16)


### Features

* **api:** expose cpu_reserved_cores and cpu_allocatable_cores in /api/v1/node (T3.1, refs [#22](https://github.com/feza-ai/spark/issues/22)) ([1f8c27e](https://github.com/feza-ai/spark/commit/1f8c27e9411359e679437fd5ebb2d73e054efab7))
* **api:** reject pods whose limits.cpu exceeds allocatable cores (T3.2, refs [#22](https://github.com/feza-ai/spark/issues/22)) ([ae1b3d2](https://github.com/feza-ai/spark/commit/ae1b3d2f3bb5292f2e28b67af261a160b602bab5))
* **cmd/spark:** add --system-reserve-cores flag (T1.2, refs [#22](https://github.com/feza-ai/spark/issues/22)) ([a644ce9](https://github.com/feza-ai/spark/commit/a644ce93b2f42b3c96221c84cc5651ce0a4a952b))
* **deploy:** add auto-upgrade script and systemd timer ([8f44338](https://github.com/feza-ai/spark/commit/8f443385260e73e9034bb1e18a46d241ab6ed8b4))
* **deploy:** wire auto-upgrade into .deb packaging ([00a9d7a](https://github.com/feza-ai/spark/commit/00a9d7a0d9910514e1e694fde39a395c7aa7e3ec))
* **gpu:** detect host core IDs in SystemInfo (T1.4, refs [#22](https://github.com/feza-ai/spark/issues/22)) ([04ea7f3](https://github.com/feza-ai/spark/commit/04ea7f30ff9395faffc62507f1635ecc6564ca71))
* **metrics:** add pod CPU throttled and host loadavg/softirq metrics (T4.1, T4.2, refs [#22](https://github.com/feza-ai/spark/issues/22)) ([df910c3](https://github.com/feza-ai/spark/commit/df910c390d53df8a11708704d1b73f2b0207e459))
* **scheduler,executor,reconciler,state:** wire cpuset pinning end-to-end (T2.1, T2.2, T2.3, refs [#22](https://github.com/feza-ai/spark/issues/22)) ([393e79b](https://github.com/feza-ai/spark/commit/393e79b92d7d97b93b5e7a026d9bc6af3afd1a99))
* **scheduler:** add ParseCoreRange helper (T1.2, refs [#22](https://github.com/feza-ai/spark/issues/22)) ([6cce378](https://github.com/feza-ai/spark/commit/6cce378ffed6cab6ac36a27d20cd1ef986ae551e))
* **scheduler:** expose ReservedCores accessor (T3.1, refs [#22](https://github.com/feza-ai/spark/issues/22)) ([64f94c3](https://github.com/feza-ai/spark/commit/64f94c39c460b2c94d892baa5af17d252fdccfd7))
* **scheduler:** track per-pod core assignments (T1.1, refs [#22](https://github.com/feza-ai/spark/issues/22)) ([60cd637](https://github.com/feza-ai/spark/commit/60cd6377e834bc0b81c9743713e0145e999b8c95))


### Bug Fixes

* **scheduler:** drop unused assignedCoreCountLocked (CI staticcheck) ([5ddd030](https://github.com/feza-ai/spark/commit/5ddd030b8941c9e6212d6e9ac69b4d57f2588b14))

## [1.8.1](https://github.com/feza-ai/spark/compare/v1.8.0...v1.8.1) (2026-04-16)


### Bug Fixes

* **reconciler:** reset scheduled pod with no events when podman pod is missing ([3caeb6a](https://github.com/feza-ai/spark/commit/3caeb6a8482d5963361305f47b7d87573ddadc27))
* **reconciler:** retry create when scheduled pod is missing in podman ([a35ac4e](https://github.com/feza-ai/spark/commit/a35ac4efe614859a4a844a63a39ea5c72a81e1bd)), closes [#13](https://github.com/feza-ai/spark/issues/13)

## [1.8.0](https://github.com/feza-ai/spark/compare/v1.7.0...v1.8.0) (2026-04-15)


### Features

* **api:** expose startAttempts and reason on GET /api/v1/pods/{name} ([2cf4522](https://github.com/feza-ai/spark/commit/2cf45227ae4021b114796dac485ceb223c19302c))
* **manifest:** parse backoffLimit on Pod spec ([5acfce9](https://github.com/feza-ai/spark/commit/5acfce9b992b0a90fb6df17b246fabcc0daa9836))
* **state:** add Reason and StartAttempts to pod record ([93871c4](https://github.com/feza-ai/spark/commit/93871c4fd1ad7081df6995efc542e140c82cdb91))
* **state:** persist LastAttemptAt for retry backoff ([a595c88](https://github.com/feza-ai/spark/commit/a595c880c0dee769f8b0b2108c767447e323c5cd))


### Bug Fixes

* **api:** make DELETE /api/v1/pods truthful about podman state ([8578c09](https://github.com/feza-ai/spark/commit/8578c095ad4919e6265ce65395b991d2e66ec033))
* **manifest:** preserve scalar list items containing colons ([50f7d7f](https://github.com/feza-ai/spark/commit/50f7d7f53fe11d25dac4435f0280c9f04da87a0a))
* **reconciler:** record container-start error and attempt count on pod record ([c3219cc](https://github.com/feza-ai/spark/commit/c3219cc4e9a6b4bc2fe913a650a716f7cc60b767))
* **reconciler:** remove orphaned podman pods instead of only logging ([4d7b669](https://github.com/feza-ai/spark/commit/4d7b6696bfca0044bfc45c36162fc06e3379d71b))
* **reconciler:** terminate pod after backoffLimit start failures ([c8bc93e](https://github.com/feza-ai/spark/commit/c8bc93e86c76ecd782eb40bf33db91160f70bac7))

## [1.7.0](https://github.com/feza-ai/spark/compare/v1.6.1...v1.7.0) (2026-04-14)


### Features

* **executor:** auto-generate NVIDIA CDI spec for GPU pods ([819529c](https://github.com/feza-ai/spark/commit/819529ca85d32d4e8c69deb3927c641375c21963))

## [1.6.1](https://github.com/feza-ai/spark/compare/v1.6.0...v1.6.1) (2026-04-08)


### Bug Fixes

* **executor:** inject GPU device when nvidia.com/gpu count is set ([dc9e010](https://github.com/feza-ai/spark/commit/dc9e010fd551b2fa388309f9b074a57cf3d296f0))
* **executor:** remove stale pod before retry on 'already exists' ([8f30f6d](https://github.com/feza-ai/spark/commit/8f30f6d25214866d99d0b8a0fa9ea4d0855533f2)), closes [#7](https://github.com/feza-ai/spark/issues/7)

## 1.6.0 (2026-03-20)

### Features

* **manifest:** add GPUCount field to ResourceList and refactor parseGPU to track device count separately from memory ([ef4c475](https://github.com/feza-ai/spark/commit/ef4c475))
* **scheduler:** track GPUCount for device slot allocation ([5c26f52](https://github.com/feza-ai/spark/commit/5c26f52))
* **reconciler:** add GPU count drift detection and liveness probe polling ([632c010](https://github.com/feza-ai/spark/commit/632c010), [adb91af](https://github.com/feza-ai/spark/commit/adb91af))
* **manifest:** add ProbeSpec parsing for liveness probes (exec, HTTP) ([a97ae91](https://github.com/feza-ai/spark/commit/a97ae91))
* **executor:** add ExecProbe and HTTPProbe methods ([fa386f6](https://github.com/feza-ai/spark/commit/fa386f6))
* **cron:** add List and Get methods to CronScheduler ([acab267](https://github.com/feza-ai/spark/commit/acab267))
* **api:** add CronJob HTTP management endpoints (list, get, delete) ([057f86c](https://github.com/feza-ai/spark/commit/057f86c))
* **api:** add node info HTTP endpoint ([8a00031](https://github.com/feza-ai/spark/commit/8a00031))
* **bus:** add gpuCount field to heartbeat payload ([454d936](https://github.com/feza-ai/spark/commit/454d936))
* **spark:** wire v1.6.0 features into main.go ([8020129](https://github.com/feza-ai/spark/commit/8020129))

## 1.5.0 (2026-03-20)

### Features

* **manifest:** add SecurityContext parsing from YAML (runAsUser, privileged, capabilities) ([83b69f0](https://github.com/feza-ai/spark/commit/83b69f0))
* **executor:** wire security context into podman container args ([34a0d7b](https://github.com/feza-ai/spark/commit/34a0d7b))
* **state:** add source path tracking to pod records ([aae0d46](https://github.com/feza-ai/spark/commit/aae0d46))
* **spark:** implement manifest removal handler in watcher ([53b1015](https://github.com/feza-ai/spark/commit/53b1015))
* **spark:** wire cronSched and scheduler into NATS/HTTP handlers ([683a095](https://github.com/feza-ai/spark/commit/683a095))
* **api:** wire CronJob registration into HTTP apply handler ([9e4386b](https://github.com/feza-ai/spark/commit/9e4386b))
* **bus:** wire CronJob registration into NATS apply handler ([6fd3fd0](https://github.com/feza-ai/spark/commit/6fd3fd0))

### Bug Fixes

* **api:** release scheduler resources on HTTP pod delete ([dd51b24](https://github.com/feza-ai/spark/commit/dd51b24))
* **bus:** release scheduler resources on NATS pod delete ([75af525](https://github.com/feza-ai/spark/commit/75af525))
* **reconciler:** increment restart counter on pod re-queue ([f1bec48](https://github.com/feza-ai/spark/commit/f1bec48))
* **reconciler:** recover pods stuck in Scheduled or Preempted status ([35229ec](https://github.com/feza-ai/spark/commit/35229ec))
* **executor:** reap zombie process in StreamPodLogs ([915d549](https://github.com/feza-ai/spark/commit/915d549))
* **manifest:** add Ki and K suffix support to parseMemory ([24460f5](https://github.com/feza-ai/spark/commit/24460f5))

## 1.4.0 (2026-03-19)

### Features

* **api:** add pod exec endpoint (POST /api/v1/pods/{name}/exec)
* **manifest:** parse container port mappings from manifests
* **executor:** forward port mappings via podman --publish
* **manifest:** parse init containers from initContainers field
* **executor:** run init containers sequentially before main containers
* **gpu:** enumerate device IDs via nvidia-smi for per-pod GPU isolation
* **scheduler:** track GPU device slot assignments via NVIDIA_VISIBLE_DEVICES
* **api:** add image management endpoints (GET /api/v1/images, POST /api/v1/images/pull)

## 1.3.0 (2026-03-19)

### Features

* **metrics:** add Prometheus metrics collector and text renderer (stdlib only)
* **api:** add /metrics endpoint in Prometheus text exposition format
* **api:** add bearer token auth middleware (--api-token-file, /healthz and /metrics exempt)
* **api:** add pod logs endpoint with tail and SSE streaming
* **api:** add pod events endpoint with time filtering
* **cmd:** add --log-format flag for structured JSON logging
* **manifest:** add emptyDir volume support (mapped to tmpfs)

## 1.2.0 (2026-03-19)

### Features

* **api:** add HTTP REST API with health, resources, pod CRUD endpoints
* **scheduler:** add priority-based preemption with anti-thrash protection
* **manifest:** add CronJob kind parser with concurrency policies
* **manifest:** add Deployment kind parser with replica management
* **manifest:** add StatefulSet kind parser with ordinal naming
* **reconciler:** add resource reconciliation with periodic sync
* **lifecycle:** add graceful shutdown coordinator with pod draining

## 1.1.0 (2026-03-19)

### Features

* **state:** add SQLite persistence with WAL mode (modernc.org/sqlite)
* **reconciler:** add pod recovery from podman after restart
* **state:** add retention pruning for completed/failed pods

## 1.0.0 (2026-03-19)


### Features

* **bus:** add Bus interface definition ([c0dd310](https://github.com/feza-ai/spark/commit/c0dd310d6f1770c23bf3fd7275580a449e385921))
* **bus:** implement apply handler for NATS ([9722ea7](https://github.com/feza-ai/spark/commit/9722ea72f567ead12815f6f3f9b527e38c95d5a1))
* **bus:** implement delete handler for NATS ([e53385d](https://github.com/feza-ai/spark/commit/e53385dc1848b9d96c7d36e5a4ffa3d327067785))
* **bus:** implement get and list handlers for NATS ([8a4296d](https://github.com/feza-ai/spark/commit/8a4296d50a491889d7cd8c6d44757fef9b286f37))
* **bus:** implement heartbeat publisher over NATS ([f44610a](https://github.com/feza-ai/spark/commit/f44610ae171d516a7eaddede615714ce5700337e))
* **bus:** implement log streaming over NATS ([91d890e](https://github.com/feza-ai/spark/commit/91d890e833d061159a1761b2305a82807b4f1a51))
* **bus:** implement NATS bus with reconnect ([a9ae3f6](https://github.com/feza-ai/spark/commit/a9ae3f6f1ee2dc9ab1853d028ad4346ce4a25290))
* **cmd:** wire main.go with flag parsing, startup sequence, and graceful shutdown ([2d6d5cf](https://github.com/feza-ai/spark/commit/2d6d5cf649dcec1bfdf27e9b6f0e2092225e4f77))
* **cron:** implement cron expression parser ([4ed1186](https://github.com/feza-ai/spark/commit/4ed1186df3fa7ea14ca9001324ba539c508c39cb))
* **cron:** implement cron trigger loop ([e81f94d](https://github.com/feza-ai/spark/commit/e81f94d33dd449644248a7d148c4e9f5ebbd1c56))
* **deploy:** add install script for DGX deployment ([3ce4cfc](https://github.com/feza-ai/spark/commit/3ce4cfc89b3f3374d39e2f09c775a2e103f76521))
* **deploy:** add local OCI registry systemd unit and setup script ([4593741](https://github.com/feza-ai/spark/commit/45937414aa064323c018762eca0652fe06945520))
* **deploy:** add multi-stage Containerfile ([0fca648](https://github.com/feza-ai/spark/commit/0fca648f192b3b66aa754a49cfec3515dbc55f20))
* **deploy:** add spark environment file template ([f1377cc](https://github.com/feza-ai/spark/commit/f1377ccb917e3149f1ad6c18c35984b3514d41ad))
* **deploy:** add systemd unit for spark service ([c92210f](https://github.com/feza-ai/spark/commit/c92210f8f5ae6f3c52e71cd60f28c972feadb160))
* **executor:** enhance pod stop with grace period and removal ([8be981d](https://github.com/feza-ai/spark/commit/8be981df5cce33a81e3808b2b792f1a68d90115a))
* **executor:** implement pod log streaming ([d797278](https://github.com/feza-ai/spark/commit/d79727893fedea555e0504a61a224b4677deee6e))
* **executor:** implement podman executor for pod lifecycle ([0a44704](https://github.com/feza-ai/spark/commit/0a44704342fc8cc7fdcfc52fae29a48b9252d82d))
* **executor:** implement spark network setup ([bd623c1](https://github.com/feza-ai/spark/commit/bd623c1a725dbac4b516557fac10d1acb693cdd5))
* **gpu:** implement GPU detection via nvidia-smi ([26a3ee8](https://github.com/feza-ai/spark/commit/26a3ee8a6b9928024fdb6430aa20bb7b9c38af68))
* **gpu:** implement system resource detection ([a6a44eb](https://github.com/feza-ai/spark/commit/a6a44eb2295b3339e3895df90a3fb9ae2e9a871b))
* **manifest:** add minimal YAML parser ([d8f49e5](https://github.com/feza-ai/spark/commit/d8f49e5bc2ba55b74a9addd5b62d8c3c78b0f2f3))
* **manifest:** add minimal YAML parser ([e2c251c](https://github.com/feza-ai/spark/commit/e2c251c598a5e765214565d24b332849aeaac64b))
* **manifest:** define PodSpec and resource types ([5df283e](https://github.com/feza-ai/spark/commit/5df283e60e78d3c63ca2e10d51d832c57f455f5c))
* **manifest:** implement CronJob kind parser ([43f61c7](https://github.com/feza-ai/spark/commit/43f61c75174dad24cc25e0f4567374ee9d0b2111))
* **manifest:** implement Deployment kind parser ([22c9249](https://github.com/feza-ai/spark/commit/22c9249a8a0a150a9ac8ad8ee0dd24e718e03a6c))
* **manifest:** implement Job kind parser ([1ae330f](https://github.com/feza-ai/spark/commit/1ae330fff13ff00c750e22b260a7b7304a06a17f))
* **manifest:** implement priority class configuration ([3c2d3ae](https://github.com/feza-ai/spark/commit/3c2d3ae470daf56a5d7c248188a2ce043f0e38c2))
* **manifest:** implement StatefulSet kind parser ([9f4f602](https://github.com/feza-ai/spark/commit/9f4f6020b532caf1e68121f5a0e2bcc72cb399e8))
* **manifest:** implement YAML parser with Pod kind support ([e2fa5ea](https://github.com/feza-ai/spark/commit/e2fa5eaa71f4c401078a07c4c9bf512206406e99))
* **reconciler:** implement reconciliation loop ([50465bf](https://github.com/feza-ai/spark/commit/50465bfb2308d427f92414aaf51ac55a0e44c4d6))
* **scaffold:** initialize Go module and directory structure ([16a405b](https://github.com/feza-ai/spark/commit/16a405b5e68ff39f6c5449cee28d1514293f57e9))
* **scheduler:** implement preemption execution ([ad5af53](https://github.com/feza-ai/spark/commit/ad5af533dd5a66e5639ec71ac4e244396efe9065))
* **scheduler:** implement priority-aware scheduler with preemption ([c2317ed](https://github.com/feza-ai/spark/commit/c2317edf56fc07df806926b8699311c0e0b64fa5))
* **scheduler:** implement resource tracker ([d925e0a](https://github.com/feza-ai/spark/commit/d925e0a70dcdf0be948bc4ae1923a3324e32951e))
* **state:** implement in-memory pod state store ([71f252c](https://github.com/feza-ai/spark/commit/71f252cd252e5b6a24dc863c6da0d009b6a69901))
* **watcher:** implement directory watcher with polling ([ddbf53a](https://github.com/feza-ai/spark/commit/ddbf53aeb712f9331938d4076170c066b5a7545e))


### Bug Fixes

* **ci:** use go-version-file instead of hardcoded Go version ([0dc50c9](https://github.com/feza-ai/spark/commit/0dc50c9aa86c909a8d7c9bcec3eb4e8b8e50ef46))
* **lint:** resolve staticcheck warnings ([5cfff02](https://github.com/feza-ai/spark/commit/5cfff0223359085fc3715ba32a01e5e3c9b6b894))
* **manifest:** consolidate duplicate helpers and wire all kinds ([727d7b5](https://github.com/feza-ai/spark/commit/727d7b51e5823cfaf34333a8314a11d59563e9a5))
