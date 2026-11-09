# 安装伙伴动画

状态：draft（系统测试已验证；实验 NAS 待执行）
更新时间：2026-11-09

## 目的

把虚拟伙伴（大肥鱼）播放的动画装到 NAS 上。动画来自 dsh-pet 的固定提交，不进 Git、不随 release 发布；与屏保视频一样是系统盘上的长期媒体（[ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)），两者共用同一套暂存与安装脚本。成功后产品服务的 `/companion/animations` 列出全部动画，各动画支持 Range 请求，本地控制台显示伙伴。

## 前提与风险

- 权限：开发机能以 `anas-dev` 用 SSH 登录 NAS；NAS 上有 root。
- 目标：已安装含 `install-media-pool.sh` 的 release 的 NAS（`/opt/a-nas/current/install-media-pool.sh` 存在）。
- 素材：本地 dsh-pet 检出位于 [`web/src/companion/dsh-pet/README.md`](../../web/src/companion/dsh-pet/README.md) 记录的提交；Safari 用的 HEVC 动画是 dsh-pet 的 `assets-mov` 发布中的 `dsh-pet-mov-assets-mov.zip`，SHA-256 为 `654e42f14b647a1e59a4c6f05694273dcf23edb063a5e49b14fc05a1fad3b467`（[`vendor-dsh-pet.py`](../../scripts/vendor-dsh-pet.py) 中的 `MOV_ZIP`）。动画仅限非商业试验，许可与署名见同一 README。
- 验证用电脑：与 NAS 同一局域网、能打开 `http://<NAS 地址>` 的另一台电脑（[启用局域网 Web 访问](enable-lan-web-access.md)）。NAS 默认没有 `curl`，HTTP 检查不在 NAS 上做。
- 风险：只写 `/var/lib/a-nas/companion` 与暂存账号的 `~/apps/a-nas/companion`，不涉及数据盘。本地控制台在运行时会被重启一次，本机屏幕重新打开桌面。
- 停止条件：本地校验报告与清单不符、暂存输出的池 ID 与 README 记录的不同，或 root 安装报错。

## 步骤

1. 在开发机暂存动画。脚本把 zip 解到临时目录，按 [`animations.sha256`](../../web/src/companion/dsh-pet/animations.sha256) 逐个核对 webm 与 mov，只上传 NAS 暂存区还没有的文件，结束时删除临时目录；`-WhatIf` 只做本地校验，不联网：

   ```powershell
   .\scripts\deploy-media-pool.ps1 -Kind companion -NasHost <NAS_HOST> -DshPet <dsh-pet 检出目录> -MovZip <dsh-pet-mov-assets-mov.zip>
   ```

   - 预期：输出上传数量与池 ID。
   - 完成标准：池 ID 与 README 记录的一致。
2. 以 root 安装：

   ```bash
   /opt/a-nas/current/install-media-pool.sh companion <池 ID>
   ```

   - 预期：脚本复核池 ID 与每个文件的 SHA-256，任一不符即在改动前停止；全部通过后建池并原子切换 `current`。产品服务每次请求重新读取目录，无需重启。本地控制台的页面只在打开桌面时读一次动画清单，所以本地控制台（`anas-kiosk@tty1`）在运行时由脚本重启；没有运行时不启动它。其他电脑与手机上已打开的页面继续播放原来的池，刷新后换成新池。
   - 完成标准：输出 `companion animation pool <池 ID> is current`；本地控制台运行时，重新打开的桌面上出现伙伴。

## 验证

在另一台电脑上检查，`<NAS 地址>` 换成 NAS 的局域网地址。Windows PowerShell：

```powershell
$nas = 'http://<NAS 地址>'
$manifest = Invoke-RestMethod "$nas/companion/animations"
$animations = @($manifest.animations.PSObject.Properties)
$manifest.pool                                               # 第 2 步的池 ID
$animations.Count                                            # 清单中 webm 的行数
@($animations | Where-Object { -not $_.Value.mov }).Count    # 5：吃白饭的 5 段没有 mov
curl.exe -s -o NUL -w '%{http_code}\n' -r 0-1023 "$nas$($animations[0].Value.webm)"   # 206
```

macOS 或 Linux 的终端（只用系统自带的 `curl` 与 `python3`）：

```bash
nas=http://<NAS 地址>
curl -fsS "$nas/companion/animations" | python3 -c 'import json, sys
m = json.load(sys.stdin); a = m["animations"]
print(m["pool"], len(a), sum("mov" not in f for f in a.values()))
print(next(iter(a.values()))["webm"])'
curl -s -o /dev/null -w '%{http_code}\n' -r 0-1023 "$nas<上一行打印的 URL>"
```

- 池 ID 与第 2 步一致；动画数等于清单中 webm 的行数，其中除吃白饭的 5 段外都有 `mov`。
- Range 请求返回 `206`。
- 本地控制台运行时，本机屏幕上出现伙伴。

## 回滚或恢复

- 指定已安装的池 ID 再运行第 2 步即切回该池，无需暂存；本地控制台同样在运行时被重启。产品服务保留所有已安装的池，动画地址带着池 ID，所以其他已打开的页面在刷新前继续播放切换前的池，刷新后才换成切回的池。
- 删除 `/var/lib/a-nas/companion/current` 后，刷新或重新打开的页面不再显示伙伴，其余功能不受影响；本地控制台要 `systemctl restart anas-kiosk@tty1.service` 后才隐藏伙伴。

## 关联

- 规格：[虚拟伙伴（大肥鱼）](../specs/virtual-companion.md)
- ADR：[0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)
- 运行手册：[运行实验 NAS 本地控制台](operate-local-kiosk.md#屏保视频)（屏保视频用同一套脚本）
- 代码：[`install-media-pool.sh`](../../scripts/install-media-pool.sh)、[`remote-stage-media-pool.sh`](../../scripts/remote-stage-media-pool.sh)、[`deploy-media-pool.ps1`](../../scripts/deploy-media-pool.ps1)、[`vendor-dsh-pet.py`](../../scripts/vendor-dsh-pet.py)、[`handler.go`](../../internal/webui/handler.go)
- 测试：[`handler_test.go`](../../internal/webui/handler_test.go)、[`system-test.sh`](../../scripts/system-test.sh)
