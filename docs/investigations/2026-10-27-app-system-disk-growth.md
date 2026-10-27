# 应用日志与卸载残留持续占用系统盘

状态：fixed，待实机验收
更新时间：2026-10-27

## 症状与影响

[#186](https://github.com/zhongwater123/A-NAS/issues/186) 的源码审查发现：服务日志没有轮转限制，卸载保留镜像和镜像声明的匿名卷，重复安装后系统盘占用持续增加。A-NAS 的数据库也在系统盘上；本次没有访问实验 NAS，不能据此判断其当前占用。

## 最小复现

在开发机的可丢弃 Docker 测试环境中，使用声明 `VOLUME` 的本地镜像，持续输出超过 40 MB 日志，并挂载一个命名卷和应用数据目录。完成安装、卸载、重新安装与安装失败回滚后，比较日志文件大小、匿名卷、镜像、命名卷和数据文件。

```bash
ANAS_DOCKER_INTEGRATION=1 ANAS_TEST_IMAGE=<带-Python-3-的本地镜像> \
  go test -tags dockerintegration -count=1 -run TestRealDockerBoundsLogsAndCleansAppResources -v ./internal/appstore/engine
```

该测试需能读取 Docker 日志文件的权限。它创建唯一命名的镜像、容器、网络和卷，只清理自己的资源；不使用全局 prune，也不应在实验 NAS 上运行。

## 根因与修复

- 日志策略依赖 Docker 默认设置。[渲染器](../../internal/appstore/render.go)现在为每个服务覆盖日志配置；旧容器的配置不会因更新代理而改变，用运行规则版本标出重装需求。
- Compose 的 `down --volumes` 会同时删除命名卷，不能直接加上这个参数。[清理流程](../../internal/appstore/engine/cleanup.go)先通过 Docker Engine 删除容器和匿名卷，再由 Compose 清理项目网络，保留命名卷和宿主数据。
- 容器一旦删除，单靠容器标签无法再找回待清理镜像。安装开始写入空的清理标记，成功后记录容器实际使用的镜像 ID；回滚和恢复同样只从容器与已有 ID 记录取得目标，清理前原子写入并同步记录。容器删除后的失败和重启仍可重试。损坏记录或可变标签会阻止清理，不猜测目标。若拉取在创建任何容器前中断，无法证明归属的下载不自动删除；保存计划中的标签不能作为删除授权，因为停机期间标签可能改指其他镜像。
- [镜像清理](../../internal/appstore/engine/docker_cleanup.go)检查全部容器，包括已停止的容器，并始终按不可变 ID 非强制删除。多标签或其他 Docker 依赖冲突会保留清理记录并显示失败；不按标签删除，因为标签在核对后仍可能被其他 Docker 操作改指向另一个镜像。管理员处理冲突后可重试，即使原容器已全部移除。
- [安装空间预检](../../internal/appstore/engine/storage.go)在生成计划和确认安装时检查系统状态所在文件系统与实际 Docker data-root；不足最低余量或无法观测时拒绝。产品接口在预建应用目录前先生成计划。余量是门槛，不是拉取镜像大小的估计，也不是运行时配额。

## 回归证据

- 渲染日志限制的回归在旧代码失败，修复后通过。
- 应用中心后端测试与 race 测试通过：安装/回滚、清理失败、容器删除后再次卸载、代理重启恢复、损坏记录保护。
- 前端应用中心 17 个测试通过，包含“卸载未完成”后确认重试，以及既有安装和卸载流程。
- WSL 本地 Docker 首轮实际写入约 45 MB 日志后保留 3 个文件，共 29,267,465 字节；卸载和回滚后匿名卷消失，命名卷及应用数据内容保留。被另一个已停止容器使用的镜像保留，独占镜像删除。
- 系统盘观测、代理编解码、两条产品 API 和设置概览的相关回归通过。未知用量不显示为零或正常。

## 后续工作

在实验 NAS 更新产品服务、Host Agent 与容器代理后，按运行手册核对概览空间和新容器日志配置。旧容器需逐个备份、确认后重装；历史上已孤立且无法证明归属的卷和镜像不自动删除。

## 关联

- [应用中心规格](../specs/app-center.md)
- [清理回归](../../internal/appstore/engine/cleanup_test.go)
- [真实 Docker 回归](../../internal/appstore/engine/cleanup_docker_integration_test.go)
- [安装与升级容器代理](../runbooks/install-container-agent.md)
- [实机验收](../runbooks/accept-features-on-experimental-nas.md#应用中心与-docker)
- [Docker 镜像删除语义](https://docs.docker.com/reference/cli/docker/image/rm/)
