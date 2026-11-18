# 适配 RK3588 的五寸触控显示

状态：draft
更新时间：2026-11-18

公开记录中的 `REDACTED` 值是实例标识的占位符。执行前须从本地封存清单恢复真实值并重新核对目标，不直接安装脱敏的参考配置。

## 目标与边界

仅适用于 eMMC CID `BOARD_EMMC_CID_REDACTED` 的 ATK-DLRK3588。用户要求五寸屏实机适配；本机为 DSI-1/DSI-2 的 1080×1920 模式，实际触控设备为 `dsi0_ts_gt9xx`（event1），DSI-1 为首选单屏目标。不能套用实验 NAS 的 DP-2、1536×2048、旋转 90°、缩放 1.5 或 Intel VA-API 判断。

先按现有原生竖向准备临时验证：DSI-1 开、DSI-2 关、scale=3、transform=normal，对应 360×640 逻辑视口。方向和是否纯触控输入仍待用户回复；该默认值不是用户已确认的最终安装方向。

## 识别与准备

1. 再查 CID、DRM connector/mode、触控名称和已登录图形会话。当前厂商 GNOME 将两个 DSI 拼接，缩放约 2.3077；GPU 为 `/dev/mali0` 的专有 Mali-G610，DRM card0 是 rockchip-drm、card1 是 RKNPU，不能把 NPU render node 当作 GPU。
2. 使用新建 root 专属 `/root/anas-fiveinch-before-20261118` 保存 `site.conf`、现有 kiosk drop-in、默认 target、GDM/kiosk 的启用与运行状态、输入和 GPU 节点权限。已有备份目录则停止，不覆盖。保留 ADB/UART 恢复通道。
3. 只做临时图形会话切换：先停止 GDM，再启动专用账号的 `anas-kiosk@tty1`，不同时运行两个图形会话，不在验证前永久停用厂商桌面。Chromium 保持 sandbox，账号不得加入 input/video 全局组。

## 显示与验证

本机包装启动器只调用现有 `wlr-randr` 关闭第二个 DSI，然后运行软件包的 kiosk-launcher；URL 仍固定为本机 `http://127.0.0.1:8080/?local-console=1`。仅本机 systemd drop-in 使用该包装器和 rockchip DRM by-path，不改实验 NAS 配置。

检查 Cage/Chromium 的真实日志、单屏 mode/transform/scale、Mali 节点访问、屏幕方向与触控坐标；不能用关闭浏览器 sandbox 或全局放宽设备权限绕过失败。如确认 Mali 节点缺活动会话授权，只为该节点添加标准 seat/uaccess 标签并核对 ACL，失败回滚本次规则。

实测 GBM 初始化还需要 `/dev/dma_heap/system-uncached`；仅有 `/dev/mali0` 的 ACL 不足。备份原始 ACL 后临时只给该节点授权即可使 Cage 和 Chromium 启动；撤销临时 ACL，移除 `WLR_RENDER_DRM_DEVICE` / `MALI_DEFAULT_DISPLAY` 探针后，以下两条本机规则也可在重新启动的活动会话中授予 ACL（各存为 `70-anas-rk3588-mali-seat.rules`、`70-anas-rk3588-dma-seat.rules`）：

```udev
SUBSYSTEM=="misc", KERNEL=="mali0", KERNELS=="fb000000.gpu", MODE:="0660", TAG+="seat", TAG+="uaccess"
SUBSYSTEM=="dma_heap", KERNEL=="system-uncached", MODE:="0660", TAG+="seat", TAG+="uaccess"
```

不授权其他 dma_heap，不把账号加入 video/input 组。规则重载后仅 trigger 对应 sysfs 节点，再核对 `getfacl` 和实际浏览器进程；Wayland socket 出现早于应用启动，须继续轮询到 Chromium 运行且 DSI-1 scale=3、DSI-2 disabled 才算显示启动成功。回滚时停止 kiosk、移除本次规则、重新加载规则并恢复备份 ACL。

浏览器验证使用独立 Fake 实例与测试账号；真实板端画面另行核验，不能把模拟视口截图当作物理屏幕验收。验证逻辑视口、无页面横向裁切、窗口可关闭返回、列表可滚动、主要触控目标至少 44 CSS px，并检查登录文本输入方案。先保持现有 A-NAS 视觉语言；发现尺寸问题再按规格改布局。

本次使用独立 Fake 账号实测，修复前 360×640 的文件窗口仅宽 248 px，768×432 时右边控件越界。响应式规则扩大到短横屏，并清除继承的桌面 max-width 后，同一浏览器断言通过：窗口左右各留 8 px、底部快捷栏不被覆盖，关闭按钮为 46×48 px。格式、lint、类型检查、Web 构建及 ARM `internal/webui` 包通过；本次 CSS 改动未重新跑完整 Go/AI 门禁，固定基准的完整门禁证据见研究记录。`output/playwright/rk5-physical-screen.png` 为 DRM 实机登录页截帧；带 `files-fixed` 的两图来自 Fake 浏览器，不能混称板端已登录验收。

小屏软件包以固定 `5a3ceb79` 加两处 CSS 补丁构建，版本后缀 `.rk5.1`，单独交付到 `I:\A-NAS-RK3588\product-5a3ceb79-rk5`。按[六包增量升级](prepare-rk3588-candidate.md#本板的增量升级)执行，同样核对 CID、卷 UUID、六包清单、6 升级/0 新装/0 移除、私有状态备份和健康差异；该轮旧基线为未带 `.rk5.1` 的 `5a3ceb79`。失败先用旧六包回退，显示配置按下面步骤独立恢复；不重建 NVMe。

该轮实际安装已退出 0，六包版本正确、dpkg audit 为空、卷 UUID 和 hold 不变，健康检查仍仅 `unit:smbd.service` 因无网口地址失败。六包清单 SHA-256 为 `041de5ea7ffe4fff204ecbe4793075bff675b6a87f7e7f5f73646e5ad0669804`；三份 SQLite 和系统状态备份通过校验，留在板端 root 专属 `/root/anas-before-rk5-20261118`，不向主机复制。新核心包的三个 ELF 全为 AArch64，十八个内容包与已审计候选同文件且摘要相同。开机显示与重启回归见下节。

## 回滚与完成条件

临时 Kiosk 失败时先留日志，再停止本次 kiosk、恢复保存的本机配置和 drop-in、daemon-reload，启动原 GDM。仅删除本次创建并核对过的包装脚本、规则和 drop-in，不删除已有浏览器资料。

已验证图形输出后，可先按原生竖向的临时默认值验证开机：备份并核对 `/etc/systemd/system/display-manager.service` 原来只链接 `/lib/systemd/system/gdm3.service`，再用 `/dev/null` 的链接屏蔽该入口，启用 `anas-kiosk@tty1`，避免厂商桌面与控制台争用屏幕。仅本机 drop-in 增加与 `gdm.service` 的冲突及 `TZ=Asia/Shanghai`；保持原 graphical 默认目标。回滚先停用 kiosk，核对并移除本次 `/dev/null` 链接，恢复保存的原链接、配置、udev 规则和 ACL，再启动 GDM。

此项曾因缺少明确授权被自动审批拒绝，拒绝后未执行；取得[用户授权](../decisions/2026-11-18-rk3588-boot-console.md)后重新提交并成功执行。原 display-manager 链接及切换前 drop-in 保存在 `/root/anas-fiveinch-before-20261118`；当前入口链接 `/dev/null`，专用控制台 enabled，GDM 不再与其争用屏幕。

第一次正常重启已显示登录页且数据卷自动挂载，但发现无网线时 Host Agent 的沙箱路径 `/run/samba` 不存在。对此板新建 `/etc/tmpfiles.d/anas-rk3588-runtime.conf`，内容为 `d /run/samba 0755 root root -`；创建前核对 CID、卷 UUID、配置文件和目录均不存在，使用 `systemd-tmpfiles --create` 应用后重启 Host Agent。只为现有沙箱提供运行目录，不放宽隔离、不取消 SMB 地址门禁。该配置回滚只移除本次新增规则；不递归删除正在使用的 `/run/samba`。根因、首轮失败与第二轮回归见[无网线启动调查](../investigations/2026-11-18-rk3588-offline-host-agent-start.md)。

正常重启验证管理员、数据卷 UUID、网页、显示和常开策略。最终方向、物理触控与文字输入仍等用户确认；纯触控是否需要屏幕键盘必须明确，不把外接键鼠假设当作已验收。

该轮已完成真实重启回归：修复无网线运行目录后，核心服务、五寸屏开机启动、管理员状态和数据卷自动挂载均通过，ADB 自动恢复、主机 18081 转发重建后 HTTP 200。完整结果与首轮失败边界见[调查](../investigations/2026-11-18-rk3588-offline-host-agent-start.md#修复与回归证据)。本次通过重启核对 sleep mask 仍在，不重复声称完成新的 15 分钟闲置测试；此前闲置验证见[休眠调查](../investigations/2026-11-18-rk3588-automatic-suspend.md)。

## 关联

- [本地控制台运行与安全边界](operate-local-kiosk.md)
- [ADR 0005：单应用 Wayland 控制台](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- [RK3588 实测与部署状态](../research/rk3588-deployment-feasibility-2026-11.md)
- [控制台启动器](../../scripts/run-kiosk.sh)、[systemd 单元](../../deploy/systemd/system/anas-kiosk@.service)
