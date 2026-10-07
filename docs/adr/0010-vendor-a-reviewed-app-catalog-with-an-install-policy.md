# Vendor a reviewed app catalog and enforce an install policy

The App Center installs apps only from a catalog compiled into the binaries: unmodified Compose manifests and icons copied from CasaOS-AppStore (Apache-2.0) at a pinned commit, kept in `internal/appstore/catalog` with a NOTICE and the upstream LICENSE. The store never downloads manifests at runtime, so what can run on a NAS is exactly what was reviewed in this repository, and updating the catalog is an ordinary, testable change.

Every manifest is rendered by the container agent against an install policy before it can run: no `privileged`, `cap_add`, devices or GPUs, host network/PID/IPC/UTS or user namespaces, Docker API socket, `security_opt`, `sysctls`, `env_file`, `build`, external networks or custom volume drivers; published ports must be single ports at or above 1024 and not reserved by A-NAS; bind mounts must come from CasaOS `/DATA` paths, which are rewritten to `ANAS_APP_DATA_ROOT/<app>` for the app's own data and `ANAS_SHARED_DATA_ROOT` for shared folders, with `/etc/localtime` allowed read-only. A test renders every vendored manifest, so the store never lists an app the policy would refuse; apps that need devices or host networking (for example Jellyfin hardware transcoding or Home Assistant) stay out until a later decision defines safe exceptions.

Installing follows plan, confirm, execute: the owner reviews the rendered plan, and the install request carries the plan's SHA-256 digest; the agent re-renders and refuses a mismatch, so the executed Compose file is the one shown. The agent runs the Docker Compose CLI as a separate process (`docker compose -p a-nas-<app>`), one job at a time, and labels every container with `io.a-nas.app`. Uninstalling runs `compose down` and keeps app data.

## 关联

- [应用中心规格](../specs/app-center.md)
- [0009 Docker Engine 与专用容器代理](0009-use-docker-engine-through-a-dedicated-container-agent.md)
- 代码：[`internal/appstore`](../../internal/appstore/appstore.go)、[清单来源](../../internal/appstore/catalog/NOTICE.md)
