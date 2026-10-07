# ADR 0008 首次升级时 Host Agent 无法启动

状态：启动 ACL 与构建修复已实机验证；数据卷入口遍历修复待实机回归
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
- 实验 NAS 仍需用修复制品完成 Host Agent 启动、身份同步、ACL、File Broker、Samba 与 Kiosk 的实机验证。

第一次修复制品只运行了 `make build-binaries`。该目标原先未依赖 `web-build`，因此 Go 编译成功时 `internal/webui/dist` 仍只有 `.keep`；修复后的 Host Agent 已在实验 NAS 正常监听并成功应用启动期 root-only ACL，但同 release 的 API 持续以 `embedded web UI is not built` 退出。现有 `TestHandlerServesEmbeddedDesktopAndImmutableAssets` 在该源码/产物状态下稳定复现错误。

构建门禁现要求 `build-binaries` 自身依赖 `web-build`，而不是只依赖调用者记住执行顺序。重新生成的不可变 release 必须先让该 Web UI 嵌入测试通过，再用于续接现场；已经安装的不完整 release 不原位覆盖。

完整构建的 `67f6ccd17ff0` 随后成功启动 API、Host Agent、File Broker 与 Kiosk，并重建 `admin` 为 UID/GID 20100；最终权限验收又暴露数据卷入口遗漏：个人空间已含 `user:admin:rwx`，`admin` 也已属于 `a-nas-users`，但以该用户访问仍被拒绝。`namei` 将不可达层级定位到挂载点 `/srv/a-nas/data`。对账此前只给父目录 `/srv/a-nas` 添加了 `a-nas-users:--x`，没有给挂载点本身添加；每一级路径分量都要求执行权限，因此空间根 ACL 正确也不可达。

`TestMaterializeRegisteredSpacesAppliesTheACLLayout` 现同时要求父目录与数据卷挂载点获得仅遍历 ACL。修复让启动对账对两级目录都执行同一幂等操作，不授予列目录权限；实机仍需证明 `admin` 可以进入自己的个人空间，而 Product Service 账号 `a-nas` 继续被内核拒绝。

## 关联

- 决策：[ADR 0008：统一 Linux 身份，以文件系统 ACL 作为唯一授权来源](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)
- 规格：[统一身份与文件授权](../specs/unified-identity-and-file-acl.md)
- 运行手册：[v1.0.1 实验存储部署](../runbooks/provision-v1.0.1-experimental-storage.md)
- 实现：`internal/hostops/linux/acl.go`、`internal/hostops/linux/identity.go`
- 回归：`internal/hostops/linux/space_permissions_test.go`
