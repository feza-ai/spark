# Changelog

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
