# Use Docker Engine through a dedicated container agent

A-NAS runs user applications on the rootful Docker Engine (Moby) with Compose v2, and the Web desktop manages them through a self-built Docker app rather than an embedded third-party panel. Access to `docker.sock` is equivalent to root, so only a separate `anas-container-agent` process, running as the system user `anas-container` in the `docker` group, may open it. The agent exposes a small typed API over HTTP/JSON on a Unix socket (`/run/a-nas-container/agent.sock`, directory `0750`, socket `0660`, group `anas-container`): a snapshot of containers and images, `start`/`stop`/`restart`, and bounded log reads. The Product Service user joins only the `anas-container` group and never the `docker` group; the agent binary and unit are root-owned so the Product Service user cannot replace them.

Docker rather than Podman because Compose-based app catalogs such as CasaOS-AppStore (Apache-2.0) and most NAS apps assume it. Rootful rather than rootless because containers must write files with the same UIDs that SMB shares and the file manager see; rootless user-namespace mapping would make shared-folder permissions diverge. A self-built UI rather than Portainer CE or Dockge keeps one desktop experience, one future authentication model and an API that can refuse dangerous options; Portainer would hold the full Docker API behind its own login.

The agent reuses the official Moby Go client (`github.com/moby/moby/client`, Apache-2.0) instead of a hand-written Engine API client. Future capabilities such as installing Compose apps must be added as new typed, validated operations following plan, confirm, execute, and must keep rejecting privileged containers and bind mounts outside approved data paths; the agent will not offer generic `docker run`, `exec` or Engine API passthrough.

## 关联

- [容器管理规格](../specs/container-management.md)
- [安装容器代理运行手册](../runbooks/install-container-agent.md)
- [Host Agent IPC 决策](0004-use-http-json-over-unix-socket-for-host-state.md)
- 代码：[`internal/containers`](../../internal/containers/containers.go)、[`cmd/anas-container-agent`](../../cmd/anas-container-agent/main.go)
