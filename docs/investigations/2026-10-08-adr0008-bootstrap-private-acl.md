# ADR 0008 首次升级时 Host Agent 无法启动

状态：resolved；三项修复已在实验 NAS 完成实机回归
更新时间：2026-10-08

## 症状与影响

从旧身份模型升级到 ADR 0008 后，旧 Linux/Samba 账号已经按运行手册删除，系统服务 release 已切换到 `4bfbd9377f42`，但 `anas-host-agent.service` 持续自动重启。Product Service 能启动，却因 Host Agent socket 被重置而无法同步统一身份，新的 `admin` Linux 账号和空间 ACL 因而无法建立。

Host Agent 的稳定错误为：

```text
protect /srv/a-nas/data/spaces/private/admin: exit status 2:
setfacl: Option -s: invalid argument near character 48
```

安装脚本在系统服务状态检查处停止，未继续切换 Kiosk。升级前数据库、账号文件、Samba 配置和数据卷 ACL 已保存到 root 专用备份目录。

## 最小复现

回归测试在已有个人空间注册项、尚未同步任何 Linux 身份的状态下调用真实的空间物化路径，并让命令 runner 按 `setfacl` 的实际行为拒绝不存在的命名用户：

```bash
go test ./internal/hostops/linux \
  -run '^TestMaterializeRegisteredSpacesProtectsPrivateSpaceBeforeIdentitySync$' \
  -count=1
```

修复前稳定失败：

```text
materialize before identity sync: protect .../spaces/private/bootstrap-admin:
exit status 2: setfacl: Option -s: invalid argument near character 48
```

该用例只保留三个必要条件：个人空间注册项存在、账号身份尚不存在、Host Agent 在启动阶段修复 ACL。

## 假设与证据

| 假设 | 可证伪预测 | 结果 |
|---|---|---|
| 启动修复在身份同步前引用了不存在的个人空间所有者 | 相同 ACL 使用不存在账号失败，改用已存在账号成功 | confirmed |
| Debian 13 `setfacl` 不接受当前 ACL 条目顺序 | 改用已存在账号后仍在相同字符失败 | rejected |
| `setfacl` 不接受逗号拼接的 access/default ACL | 已存在账号的相同 ACL 结构仍失败 | rejected |
| `/proc/self/fd` 目标形式不受支持 | 容器目录 ACL 会先失败，或错误指向目标而非 ACL 字符位置 | rejected |

NAS 上的无写入对照使用 `setfacl --test`：`user:admin:rwx` 在字符 48 失败，而结构相同的 `user:anas-dev:rwx` 被完整解析并打印预期 ACL。

## 根因

Host Agent 启动时先执行 `ReconcileDataVolume`，再开放身份同步 API。旧版本的空间注册表已经包含 `/srv/a-nas/data/spaces/private/admin`，但首次 ADR 0008 升级尚无 `identity-registry.json`，旧 `admin` 又已按运行手册删除。

空间物化代码直接从个人空间路径 basename 构造 `user:admin:rwx`，并在账号重建前交给 `setfacl --set`。`setfacl` 必须解析命名用户，因账号不存在而失败；Host Agent 在开放 socket 前退出，Product Service 因而永远没有机会调用 `SyncIdentities` 重建该账号，形成启动死锁。

## 修复与回归证据

- 身份未知时，个人空间及其回收站临时应用 root-only ACL；不会授予 Product Service、固定用户组或任何路径 basename 对应的未知账号。
- `SyncIdentities` 创建并记录 Linux 身份后仍调用同一个权限修复入口，将 root-only ACL 收敛为所有者 ACL，并建立个人回收站。
- 管理员查看模式的 viewer 条目也只从已知统一身份生成，避免陈旧查看记录触发相同的命名用户解析失败。
- `TestMaterializeRegisteredSpacesProtectsPrivateSpaceBeforeIdentitySync` 修复前以同一错误失败，修复后通过；已有完整身份的空间 ACL 布局测试继续覆盖正常稳态。
- 实验 NAS 的最终修复 release 为 `ae4b642fe89d`：Host Agent、API、Samba 与 Kiosk 均 active，Host Agent 和 File Broker socket 均为 `root:a-nas 0660`，系统盘与数据卷身份镜像均为 `root:root 0600`。

第一次修复制品只运行了 `make build-binaries`。该目标原先未依赖 `web-build`，因此 Go 编译成功时 `internal/webui/dist` 仍只有 `.keep`；修复后的 Host Agent 已在实验 NAS 正常监听并成功应用启动期 root-only ACL，但同 release 的 API 持续以 `embedded web UI is not built` 退出。现有 `TestHandlerServesEmbeddedDesktopAndImmutableAssets` 在该源码/产物状态下稳定复现错误。

构建门禁现要求 `build-binaries` 自身依赖 `web-build`，而不是只依赖调用者记住执行顺序。重新生成的不可变 release 必须先让该 Web UI 嵌入测试通过，再用于续接现场；已经安装的不完整 release 不原位覆盖。

完整构建的 `67f6ccd17ff0` 随后成功启动 API、Host Agent、File Broker 与 Kiosk，并重建 `admin` 为 UID/GID 20100；最终权限验收又暴露数据卷入口遗漏：个人空间已含 `user:admin:rwx`，`admin` 也已属于 `a-nas-users`，但以该用户访问仍被拒绝。`namei` 将不可达层级定位到挂载点 `/srv/a-nas/data`。对账此前只给父目录 `/srv/a-nas` 添加了 `a-nas-users:--x`，没有给挂载点本身添加；每一级路径分量都要求执行权限，因此空间根 ACL 正确也不可达。

`TestMaterializeRegisteredSpacesAppliesTheACLLayout` 现同时要求父目录与数据卷挂载点获得仅遍历 ACL。修复让启动对账对两级目录都执行同一幂等操作，不授予列目录权限。最终实机验收证明：`admin` 可以穿过卷根并进入自己的个人空间、不能列出卷根，Product Service 账号 `a-nas` 继续被内核拒绝；`healthz`、嵌入式桌面和屏保 byte-range 请求均成功。当时认为 ADR 0008 只剩管理员在 Web 改密以重新生成已按手册删除的 Samba 凭据；后来发现正式服务单元下 Web 文件从未可用，见 [issue #38 调查](2026-10-08-host-agent-loses-setuid-under-systemd.md)。

## 可复用的升级经验

- 首次升级必须把“身份尚不存在但空间注册表已经存在”作为正常中间状态。启动期对账先 fail closed，再由身份同步收敛到最终 ACL；不能让服务启动依赖尚未开放的同步 API。
- 不可变 release 的验证对象必须是最终制品而不只是 Go 源码。`build-binaries` 自身负责生成嵌入式 Web UI，避免调用者漏掉隐含构建顺序；每次切换前同时核对版本文件和所有二进制/媒体 SHA-256。
- systemd 服务刚重启时的瞬时 `activating` 或一次 socket reset 不是最终判据。恢复脚本应等待服务和健康端点稳定，再验证身份、ACL 与 Kiosk；失败时保留备份和现场，不重复执行已经完成的破坏性步骤。
- 高风险 root 操作使用上传后经过 `bash -n`/`shellcheck` 和哈希核对的脚本，操作者只执行一个短命令。避免把长脚本通过富文本终端粘贴，因为转义、换行和 HTML 实体会使现场不可审计。
- 运行手册必须随服务边界迁移一起更新。ADR 0008 把产品从 `anas-dev` 用户级服务迁到 `a-nas` 系统服务；任何仍修改 `~/.config/a-nas` 或重启 `systemctl --user` 的后续能力手册都需要重新路由。
- 删除旧 Samba 身份后，升级完成标准必须包含管理员可见的自助改密入口。后端存在改密 API 不等于用户能够完成凭据重建；UI、API、Samba 三层需要同一条验收路径。
- 使用 `apt-get --no-install-recommends` 时必须显式枚举运行时二进制所在的软件包，并在目标 Debian 版本验证。Debian 13 将 Docker CLI 拆为 `docker-cli` 推荐包；只安装 `docker.io` 会启动 daemon，却没有脚本所需的 `docker` 命令。
- 恢复脚本的健康探针必须引用产品真实契约并逐项报告。把屏保路由凭记忆写成 `/screensaver.mp4`，会在 API 已以 `containers=true` 稳定运行时制造假失败；事故当时的权威路由是 `/local-console/screensaver.mp4`。视频池实现后，探针由 Handler 测试和前端常量共同约束为先读取 `/local-console/screensavers`，再请求清单中的媒体 URL；旧路由只保留兼容。

## 关联

- 决策：[ADR 0008：统一 Linux 身份，以文件系统 ACL 作为唯一授权来源](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)
- 规格：[统一身份与文件授权](../specs/unified-identity-and-file-acl.md)
- 运行手册：[v1.0.1 实验存储部署](../runbooks/provision-v1.0.1-experimental-storage.md)
- 实现：`internal/hostops/linux/acl.go`、`internal/hostops/linux/identity.go`
- 回归：`internal/hostops/linux/space_permissions_test.go`
