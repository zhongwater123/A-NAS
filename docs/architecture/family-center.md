# 家庭中心技术设计

状态：M1 implemented；M2（认领、隐藏、从图库加入频道）未实现
更新时间：2026-10-29

产品行为以[家庭中心规格](../specs/family-center.md)为准；身份、照片与记录的归属见 [ADR 0029](../adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)。

## 组件与数据流

```text
Web 家庭中心窗口 / 桌面提示
    │ 会话 + CSRF
    ▼
产品服务 ── /api/v1/family 转发（HTTP over UDS，只带会话令牌）──► 家庭服务 a-nas-family
    │                                                            ├── Host Agent 家庭会话查询（确认身份、列出在用账号）
    │                                                            ├── 数据卷 family/：family.db 与 recordings/
    │                                                            ├── 相册服务 /internal/photos/lookup（以读者会话，只读）
    │                                                            └── FFmpeg Worker（AMR、3GPP 录音转成 FLAC 播放）
    └── /api/v1/photos 转发 ──► 相册服务（照片视频以上传者身份存入共享图库）
```

- 频道里的照片视频由浏览器先以上传者的会话传进共享图库，再把照片资产 ID 交给家庭服务；家庭服务从不接收或保存照片字节。
- 家庭服务读取频道时，把记录里的照片资产 ID 交给相册服务的 `/internal/photos/lookup`，带上读者自己的会话。相册服务按它的 Policy 只回答读者现在能看到的照片（在回收站中、已删除、以后被隐藏的都不在答案里），家庭服务据此过滤记录和云相册。这个路径在 `/api/v1/photos` 之外，浏览器经产品服务到不了。
- 开发模式在产品服务进程内装配同一个 `family.Store`，数据放在开发卷的 `family/` 下，查询照片时直接调用进程内的相册接口（[`cmd/anas-api/family.go`](../../cmd/anas-api/family.go)）。

## 模块与身份

| 入口 | 责任 |
|---|---|
| [`internal/family`](../../internal/family/family.go) | 家人与家长、宝宝档案、各类记录与留言、最近删除、录音文件、云相册、生长标准、疫苗程序、大事记、桌面提示 |
| [`internal/familyapi`](../../internal/familyapi/handler.go) | `/api/v1/family` 的 HTTP 接口、错误码、录音的流式上传与播放 |
| [`internal/familyservice`](../../internal/familyservice/service.go) | 独立进程：会话确认、数据卷上存储的检查与重新打开、每小时清理到期的最近删除；[相册查询客户端](../../internal/familyservice/photos.go) |
| [`internal/photos/lookup.go`](../../internal/photos/lookup.go) | 相册服务按读者 Policy 批量回答照片是否可见 |
| `web/src/family` | 窗口、各板块、记录对话框、桌面提示 |

- 家庭服务 `anas-api family-service` 以 `a-nas-family`（UID/GID 31002）运行，单元见 [`anas-family.service`](../../deploy/systemd/system/anas-family.service)：无网络、`ProtectSystem=strict`，只写数据卷。它带 `a-nas-photos` 附加组，以连接相册服务的接口套接字与 FFmpeg Worker；和分享服务一样，这个组也让它能连上 AI Worker 的套接字，但相册接口的每个请求仍要读者的会话。
- `family/` 由 Host Agent 在数据卷就绪时创建为子卷，属主 `a-nas-family`、`0700`，每 15 分钟修复漂移（[`EnsureFamilyStore`](../../internal/hostops/linux/photos.go)）。家庭服务只在它是本身份所有、`0700`、位于可写 Btrfs 数据卷且卷标记存在时打开；数据卷离开后整体回答 `family_unavailable`，不在系统盘暂存。
- Host Agent 在 `/run/a-nas-family-sessions/family.sock` 为它提供会话查询，目录交给 `a-nas-family` 组；产品服务经 `a-nas-family` 附加组连接 `/run/a-nas-family/family.sock`。

## 数据

`family.db`（SQLite，WAL，`synchronous=FULL`，`PRAGMA user_version` 记迁移）：

- `family`、`members`、`join_requests`：一台 NAS 一个家庭。成员的账号停用或删除后不再算家人，但内容保留；家长全部不在时，最早加入的在用成员自动成为家长。
- `babies`：宝宝档案，头像是裁好的小 JPEG，删除后保留 15 天。
- `records`：六种记录共用一张表，测量值以毫米和克存整数；`record_photos` 记照片资产 ID、角色（云相册或接种凭证）与拍摄时间；`comments` 是记录下的留言与频道留言墙；`reactions` 记每位家人在一条记录上留下的表情回应，一种一行，随记录永久删除而删除。
- `vaccine_plans`、`hints_seen`、`audit_events`（与其他模块同样的列，供活动记录读取）。

录音存在 `recordings/<记录 ID>`：先写成同目录的隐藏临时文件、`fsync` 后改名，再提交数据库行；失败或崩溃留下的临时文件在清理时删除。

## 参考数据

[`internal/family/standards`](../../internal/family/standards/README.md) 随程序发布 WS/T 423—2022 的百分位表和 2026 年版免疫程序，来源、校验与转录核对写在那里。

- 生长曲线：标准没有 LMS 参数，两个取样年龄之间线性插值；超出标准的年龄只画宝宝自己的数据。
- 疫苗：`covers` 让非免疫规划疫苗的第 N 剂视为所替代疫苗的第 N 剂（`coversUpTo` 限制只替代前几剂）；乙脑、甲肝两种程序以 `group` 表示，默认减毒活疫苗，家庭可以改选，记过另一种的剂次也会自动切换。

## 验证

- [`internal/family/family_test.go`](../../internal/family/family_test.go)：加入与家长、宝宝档案的并发修改与删除、照片可见性与回收站、云相册的月龄分组、作者与家长的权限、生长标准查值与插值、2026 年版程序与替代、桌面提示、年龄计算、大事记、录音与表情回应。
- [`internal/httpapi/family_test.go`](../../internal/httpapi/family_test.go)：经产品服务、按 OpenAPI 契约走通全部接口，照片由进程内的相册服务以读者会话回答。
- [`web/src/family/FamilyPanel.test.tsx`](../../web/src/family/FamilyPanel.test.tsx)：加入、频道、写日志、添加宝宝与切换。
- 实机：家庭服务的单元、身份与 `family/` 子卷由安装器与 Host Agent 建立，尚未在实验 NAS 上部署验证。

## 关联

- [规格](../specs/family-center.md)、[ADR 0029](../adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)、[成员分享技术设计](member-sharing.md)（同样的独立服务结构）
