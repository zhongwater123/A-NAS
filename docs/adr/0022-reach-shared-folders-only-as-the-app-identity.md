# 0022：应用容器只以应用身份接触共享空间

状态：accepted（2026-10-14）；行为见[应用中心规格](../specs/app-center.md)，证据见[调查](../investigations/2026-10-14-app-containers-and-the-shared-acl.md)。本决定修订 [ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md) 第 3 条“应用容器”的实现方式，并取代 [ADR 0010](0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md) 关于运行身份的第 3 段。

ADR 0008 要求应用以 `app-<id>` 身份、经共享空间的 ACL 访问共享空间。原实现是在宿主机上把 `app-<id>` 加入 `a-nas-users`；清单既不声明 `user` 也不声明 `PUID`/`PGID` 的服务，则保留镜像自带的用户。2026-10-14 的实验证明这两点都达不到 ADR 0008 的要求：

- Docker 只从镜像自己的 `/etc/group` 和 Compose 的 `group_add` 取附加组，宿主机上的组成员关系从不进入容器。所以以应用身份运行的服务读写不了共享空间。
- 保留镜像用户的服务多数以 root 运行。rootful Docker 默认保留 `CAP_DAC_OVERRIDE`、`CAP_FOWNER`，这样的进程不受 ACL 约束，还能修改成员文件的权限。

安装计划却对这两种情况都写着“以独立账号运行”“与成员相同”。

## 决定

1. **每个服务恰好属于以下一种运行方式**，由渲染器根据清单和审查表决定，并写入计划：
   - `app`：Compose `user` 设为 `app-<id>`，`cap_drop: [ALL]`。清单声明了 `user`，或审查表 `reviewedRunAs` 确认镜像能以任意 UID 运行时，采用这种方式。
   - `app-after-init`：清单声明了 `PUID`/`PGID`。容器以 root 启动，入口程序完成准备后切换到计划设定的 `PUID`/`PGID`。入口程序的监管进程在容器的整个生命周期内保持 root，并保留 Docker 默认能力。
   - `image`：其余服务，保留镜像自带的用户（可能是 root），能力不变。
2. **共享空间只挂载给 `app` 与 `app-after-init` 服务。** `image` 服务挂载共享空间时渲染失败；清单测试保证内置目录中没有这样的应用。
3. **共享空间权限写进服务自身，通过 `a-nas-users`（GID 20000）组授予。**
   - `app` 服务：`group_add: ["20000"]`。
   - `app-after-init` 服务：入口程序降权时会按镜像的 `/etc/group` 重新计算附加组，丢掉 `group_add` 加的组，所以改为把 `PGID` 设为 20000，作为进程的主组。
   - 宿主机上的 `app-<id>` 不属于任何附加组。Host Agent 在准备和释放应用时，清除此前写入的组成员关系。
4. **共享空间默认只读。** 只有被审查表 `sharedWrites` 列为该应用用途的挂载（下载、同步、备份与恢复、上传，以及会修改库中文件的整理工具）才可读写，其余挂载一律以 `read_only` 渲染。在只读挂载上，写入、`chmod`、`setfacl` 都返回 `EROFS`，与进程以什么身份运行无关。
5. **所有服务设置 `no-new-privileges`。**
6. **安装后核对实际运行情况。** 容器代理按计划逐项核对每个容器：`User`（仅 `app` 服务）、`GroupAdd`、`CapDrop`、`no-new-privileges`，以及只读挂载是否真的只读。任何一项不符就执行 `compose down` 回滚。
7. **安装计划如实显示**每个服务的运行方式，以及它对共享空间是读还是写。
8. **旧安装不自动迁移。** 容器带 `io.a-nas.runtime` 标签，记录渲染时依据的规则版本。按旧规则安装的应用照原样继续运行，详情页提示管理员卸载（保留应用数据）后重新安装；系统不会替管理员自动重装。

## 后果

- 以应用身份读写共享空间的应用这才真正可用；在此之前，按推断它们读写不了共享空间（见调查）。
- 以 root 运行的镜像不再能挂载共享空间。Audiobookshelf、Immich、Kavita、Navidrome、PhotoPrism、Swing Music 因此改为 `app` 方式，依据各不相同：
  - Audiobookshelf、Swing Music：在本机实测通过；
  - Navidrome、Immich、PhotoPrism：上游文档给出了非 root 运行方式；
  - Kavita：没有找到相关文档。

  以受限身份运行是一种“失败即回滚”的选择：镜像不支持时，安装会在启动检查中失败并回滚，不会因此多出任何权限。
- `app-after-init` 方式的初始化窗口和常驻的 root 监管进程，是接受的风险：它们出自经过审查的入口程序，不处理用户数据；应用主进程以应用身份运行，有效能力为零。
- 应用写入共享空间的文件属主是应用 UID，成员经继承的 ACL 可以读写。`app-after-init` 应用的入口程序可能把自己数据目录的主组改为 20000；数据卷上的 `apps` 目录只有 root 能进入，成员看不到这些目录。
- `image` 服务仍可能以 root 写入自己的应用数据目录。容器一旦被逃逸，root 容器的后果仍是宿主机 root，这是 rootful Docker 本身的风险，本决定不改变它。
- 计划内容变了，计划摘要也随之改变；已安装的应用不受影响，直到重新安装。从 root 改为应用账号的应用，旧安装在自己数据目录里留下的 root 属主文件会让它写不了数据，重装前需要按[容器代理手册](../runbooks/install-container-agent.md#验证)把这些子目录归还给应用账号；目前只有尚未发布的实验 NAS 受影响，正式升级前再决定是否由 Host Agent 自动完成。

## 未选择

- **宿主机组成员关系（原实现）**：组成员关系不会进入容器。
- **全程 root 但去掉绕过权限的能力**：能让 root 镜像受 ACL 约束，但它写出的文件属主是 UID 0，容器被逃逸后仍是 UID 0。用户选择的是不让 root 镜像挂载共享空间。
- **Docker `userns-remap`**：会把所有 UID 整体平移，`app-<id>` 的 ACL 和文件属主全部对不上；而且它是守护进程级设置，影响宿主机上的所有容器。
- **`app-after-init` 服务用 `group_add`**：实测证明降权后这个组会丢失。
- **保持读写（与成员相同）**：媒体浏览类应用不需要修改共享空间。改为只读后，即使应用被攻破，它对共享空间也只能读取。

## 关联

- 规格：[应用中心](../specs/app-center.md)、[统一身份与文件授权](../specs/unified-identity-and-file-acl.md)
- 调查：[应用容器拿不到共享空间的组权限，root 镜像绕过 ACL](../investigations/2026-10-14-app-containers-and-the-shared-acl.md)
- 研究：[应用目录扩充评审](../research/app-catalog-review-2026-10.md)
- 代码：[`internal/appstore/runtime.go`](../../internal/appstore/runtime.go)、[`internal/appstore/render.go`](../../internal/appstore/render.go)、[`internal/appstore/engine/engine.go`](../../internal/appstore/engine/engine.go)、[`internal/hostops/linux/apps.go`](../../internal/hostops/linux/apps.go)
- 测试：[`runtime_test.go`](../../internal/appstore/runtime_test.go)、[`render_test.go`](../../internal/appstore/render_test.go)、[`engine_test.go`](../../internal/appstore/engine/engine_test.go)、[`root_apps_integration_test.go`](../../internal/hostops/linux/root_apps_integration_test.go)
