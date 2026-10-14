# 应用容器拿不到共享空间的组权限，root 镜像绕过 ACL

状态：fixed locally；Experimental NAS verification pending
更新时间：2026-10-14

## 症状与影响

这个问题没有用户报告。它是在讨论 AI 助手运行时隔离时，审查应用中心发现的。涉及两类应用：

- **以应用身份运行、又挂载共享空间的应用**：qBittorrent、Deluge、Syncthing、Transmission、PyLoad、Resilio Sync、Duplicati、Calibre-Web。按推断，它们在真实 Docker 中读写不了共享空间。文档中没有这些应用在实机上安装过的记录，所以这一点没有在 NAS 上观察到。
- **镜像默认以 root 运行、又挂载共享空间的应用**：Audiobookshelf、Immich、Kavita、Navidrome、PhotoPrism、Swing Music。它们不受 ACL 约束，还能修改成员文件的权限。实验 NAS 上安装过 Immich（见[当前状态](../status/CURRENT.md)），宿主机上的 `app-immich` 属于 `a-nas-users`。

对这两类应用，安装计划都显示“以独立账号运行”“将获得共享空间的读写权限（与成员相同）”，与实际情况不符。

## 最小复现

1. 环境：WSL2 中的 Docker 29.1.3（rootful，未启用 userns）。准备一个用完即删的卷，在里面建一个与共享空间相同的目录：属主 20100，ACL 为 `g::---,o::---,g:20000:rwx`（含默认项），里面放一个成员文件。
2. 用不同身份的容器访问这个目录：

   ```bash
   docker run --rm --user 30002:30002 -v VOL:/s anas-acl-matrix:bookworm sh -c 'id; touch /s/shared/a'
   docker run --rm --user 30002:30002 --group-add 20000 -v VOL:/s anas-acl-matrix:bookworm sh -c 'id; touch /s/shared/b'
   docker run --rm -v VOL:/s anas-acl-matrix:bookworm sh -c 'touch /s/shared/c; chmod 000 /s/shared/member.txt'
   ```

3. 实际结果：
   - 第一条命令：`groups=30002`，写入被拒绝（`Permission denied`）。
   - 第二条命令：写入成功。
   - 第三条命令（root）：写入成功，还能对成员的文件执行 `chmod`。

   期望结果：应用身份能按计划访问共享空间，root 进程不应绕过 ACL。

## 观察事实

- **容器身份对照**：

  | 容器身份 | 结果 |
  |---|---|
  | `--user 30002:30002` | 被拒绝 |
  | 加 `--group-add 20000` | 可写，文件属主为 30002 |
  | root，Docker 默认能力 | 可写，能 `chmod` 成员文件 |
  | root，去掉 `DAC_OVERRIDE`、`FOWNER`、`CHOWN`、`FSETID` | 被 ACL 拒绝 |
  | 上一行再加 `--group-add 20000` | 可写，但 `chmod` 成员文件时报 `Operation not permitted` |
  | root，默认能力，只读挂载 | 写入、`chmod`、`setfacl` 都返回 `Read-only file system` |

- **hotio qBittorrent 5.0.4**（`PUID`/`PGID` 类镜像，以 root 启动后经 s6 降权）：
  - 设 `PUID=30030`、`PGID=30030` 并加 `--group-add 20000`：`qbittorrent-nox` 的附加组为 `100,30030`，`group_add` 加的组在降权时丢失。
  - 设 `PGID=20000`：`qbittorrent-nox` 以 `uid 30030 gid 20000`、附加组 `100,20000` 运行，`CapEff` 为 0，`NoNewPrivs` 为 1。
  - 两种情况下，s6 的监管进程都一直以 root 运行，`CapEff` 为 `a80425fb`。
- **Audiobookshelf 2.35.0 与 Swing Music 2.1.4**：镜像默认以 root 运行。改以 `--user 3002x --group-add 20000 --cap-drop ALL --security-opt no-new-privileges:true` 运行，各观察 45 秒：
  - 持续运行，没有重启；
  - 写入自己的配置目录（文件属主为应用 UID）；
  - 能读只读挂载的媒体目录，写入返回 `EROFS`；Audiobookshelf 的播客目录可写，新文件继承共享空间的 ACL。
- **测试为什么没发现**：root 集成测试用 `setpriv --init-groups` 模拟容器，这个参数按宿主机的组初始化附加组，Docker 不会这样做。所以测试一直能通过。
- **实验 NAS（只读检查）**：`a-nas-users` 的成员为 `admin,app-immich,a12,adminfrank`。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 宿主机上 `usermod --groups a-nas-users app-<id>` 能让容器获得共享空间权限 | 用不在任何组的 UID 加 `--user` 运行，对比加 `--group-add` | rejected：容器的附加组只来自镜像的 `/etc/group` 和 `group_add` |
| `PUID` 类镜像会保留 `group_add` 加的组 | 查看 s6 降权后主进程的 `supgid` | rejected：降权时按镜像的 `/etc/group` 重新计算 |
| 以 root 运行的镜像同样受 ACL 约束 | 用 Docker 默认能力的 root 写入并 `chmod` 成员文件 | rejected：`DAC_OVERRIDE`、`FOWNER` 绕过 ACL |
| 只读挂载能挡住 root | 默认能力的 root 写入、`chmod`、`setfacl` | confirmed：全部返回 `EROFS` |

## 根因

问题出在两处，而这两处在当时的测试和界面上都看不出来：

1. **授权方式**：应用访问共享空间的授权，是在宿主机上把 `app-<id>` 加入 `a-nas-users`。Docker 不读宿主机的 `/etc/group`，这个组成员关系对容器无效；模拟容器的集成测试却用了宿主机的组，所以测出来是好的。
2. **身份规则**：渲染器对既没有 `user` 也没有 `PUID`/`PGID` 的服务，保留镜像自带的用户，而这类镜像多数是 root。共享空间是否安全，就只取决于“挂载了哪些文件夹”，ACL 不再起作用。安装计划界面则不论哪种情况，都一律显示应用账号。

## 修复与回归证据

- 决定：[ADR 0022](../adr/0022-reach-shared-folders-only-as-the-app-identity.md)。
- 修复：
  - [`runtime.go`](../../internal/appstore/runtime.go)：定义三种运行方式、`group_add`/`PGID`、`cap_drop`、`no-new-privileges` 和共享空间默认只读；
  - [`render.go`](../../internal/appstore/render.go)：拒绝保留镜像用户的服务挂载共享空间，拒绝清单自带 `group_add`；
  - [`engine.go`](../../internal/appstore/engine/engine.go)：安装后核对容器的身份、能力与只读挂载，识别按旧规则安装的应用；
  - [`apps.go`](../../internal/hostops/linux/apps.go)：清除应用账号的附加组；
  - 安装计划与详情页如实显示（[`InstallSheet.tsx`](../../web/src/appcenter/InstallSheet.tsx)、[`AppDetail.tsx`](../../web/src/appcenter/AppDetail.tsx)）。
- 测试：
  - 渲染与目录：`TestCatalogServicesReachSharedOnlyAsTheAppIdentity`、`TestCatalogWritesSharedOnlyWhereListed`、`TestReviewedServicesRunAsTheAppIdentity`、`TestSharedGIDIsTheMembersGroup`、`TestRenderRefusesSharedForServicesThatKeepTheImageUser`、`TestRenderSwitchesPUIDImagesToTheAppIdentity`、`TestRenderMountsSharedReadOnlyUnlessTheAppWritesIt`；
  - 安装引擎：`TestInstallRollsBackAContainerThatDoesNotRunAsPlanned`、`TestInstallRollsBackAWritableReadOnlyFolder`、`TestAppsReportContainersFromOlderRuntimeRules`；
  - root 集成测试 `TestRootAppIdentityAndFolders`：不再使用宿主机的组，验证没有计划中的组时不能写共享空间、有组时可以写，并验证会清除旧的组成员关系；
  - 前端：应用中心测试中的服务运行方式、只读说明和旧规则提示。
- 本机实测：上述 Audiobookshelf、Swing Music、qBittorrent 的运行结果，见“观察事实”。

## 后续工作

- 在 Experimental NAS 部署后重新安装 Immich，确认它以应用身份运行、上传可用，宿主机的 `app-immich` 不再属于 `a-nas-users`。
- Navidrome、Immich、PhotoPrism、Kavita 的镜像在 Docker Hub 上，本机无法拉取，因此没有实测。前三个依据上游的非 root 文档，Kavita 没有找到文档；在 NAS 上安装时，由启动检查兜底。
- 其余 31 个应用的镜像身份逐个审查，用户决定暂缓。
- 可以评估给 `app-after-init` 与 `image` 服务去掉 `NET_RAW` 等用不到的默认能力。

## 关联

- 规格：[应用中心](../specs/app-center.md)、[统一身份与文件授权](../specs/unified-identity-and-file-acl.md)
- ADR：[0022](../adr/0022-reach-shared-folders-only-as-the-app-identity.md)、[0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)、[0010](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)
- 调查：[OpenList 运行身份](2026-10-08-openlist-runtime-identity.md)
- 运行手册：[安装 Docker 与容器代理](../runbooks/install-container-agent.md)
