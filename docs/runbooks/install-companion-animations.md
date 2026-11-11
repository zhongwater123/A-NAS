# 安装伙伴动画

状态：draft（系统测试已验证；2026-11-10 在实验 NAS 执行第 1–2 步，池 `36492963e4112f55`，HTTP 核对通过）
更新时间：2026-11-10

## 目的

把虚拟伙伴（大肥鱼）播放的动画装到 NAS 上。动画来自 dsh-pet 的固定提交，不进 Git、不随 release 发布；与屏保视频一样是系统盘上的长期媒体（[ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)），两者共用同一套暂存与安装脚本。成功后产品服务的 `/companion/animations` 列出全部动画，各动画支持 Range 请求，本地控制台显示伙伴。

## 前提与风险

- 权限：开发机能以 `anas-dev` 用 SSH 登录 NAS；NAS 上有 root。
- 目标：已安装含 `install-media-pool.sh` 的 release 的 NAS（`/opt/a-nas/current/install-media-pool.sh` 存在）。
- 素材：本地 dsh-pet 检出位于 [`web/src/companion/dsh-pet/README.md`](../../web/src/companion/dsh-pet/README.md) 记录的提交；Safari 用的 HEVC 动画是 `dsh-pet-mov-assets-mov.zip`，SHA-256 为 `3e6be25298d0abbcc6e38e910d2d1726a4787c1f6c189c60cd17fa88abb4e21d`（[`vendor-dsh-pet.py`](../../scripts/vendor-dsh-pet.py) 中的 `MOV_ZIP`）。从私有仓库 A-NAS-assets 取：`gh release download sha256-3e6be25298d0abbcc6e38e910d2d1726a4787c1f6c189c60cd17fa88abb4e21d --repo zhongwater123/A-NAS-assets --dir <目录>`。不要从 dsh-pet 的 `assets-mov` 发布下载：上游每次构建都同名覆盖这个文件，编码结果也每次不同，与固定的哈希对不上（2026-11-10 的情况）。动画仅限非商业试验，许可与署名见同一 README。
- 验证用电脑：与 NAS 同一局域网、能打开 `http://<NAS 地址>` 的另一台电脑（[启用局域网 Web 访问](enable-lan-web-access.md)）。NAS 默认没有 `curl`，HTTP 检查不在 NAS 上做。
- 风险：只写 `/var/lib/a-nas/companion` 与暂存账号的 `~/apps/a-nas/companion`，不涉及数据盘。本地控制台在运行时会被重启一次，本机屏幕重新打开桌面。
- 停止条件：任一脚本报错退出，包括素材包不是固定的那一份、本地文件与清单不符、池 ID 不是 release 固定的池，或 root 安装报错。池 ID 由脚本核对，不再人工比对。

## 步骤

1. 在开发机暂存动画。脚本先核对 zip 的 SHA-256，不是固定的那一份就在打开它之前停止；再由 [`animations.sha256`](../../web/src/companion/dsh-pet/animations.sha256) 算出池 ID，与 [`install-media-pool.sh`](../../scripts/install-media-pool.sh) 中的 `companion_pool` 不同即停止；然后把 zip 解到临时目录，逐个核对 webm 与 mov，只上传 NAS 暂存区还没有的文件，结束时删除临时目录。池 ID 随清单交给 NAS（`remote-stage-media-pool.sh companion commit <池 ID>`），上传的清单不是这个池时 NAS 拒绝、不建档；脚本再核对 NAS 报回的池 ID。`-WhatIf` 只做本地校验，不联网：

   ```powershell
   .\scripts\deploy-media-pool.ps1 -Kind companion -NasHost <NAS_HOST> -DshPet <dsh-pet 检出目录> -MovZip <dsh-pet-mov-assets-mov.zip>
   ```

   - 预期：输出上传数量与池 ID；`-WhatIf` 输出 `All 228 animations match …, pool 36492963e4112f55.`。
   - 完成标准：脚本没有报错，输出 `The companion animation pool 36492963e4112f55 is staged on <NAS_HOST>.`。
2. 以 root 安装：

   ```bash
   /opt/a-nas/current/install-media-pool.sh companion 36492963e4112f55
   ```

   - 预期：脚本只接受这个 release 固定的池 `36492963e4112f55`，其他池在改动前拒绝；`--unpinned` 只给系统测试的替身用，不在真 NAS 上使用。脚本复核池 ID 与每个文件的 SHA-256，任一不符即在改动前停止；全部通过后建池并原子切换 `current`。暂存区由暂存账号所有，脚本只读其中的普通文件，不经末端的符号链接，不等 FIFO 的写入方，清单最大 1 MiB、每段动画最大 256 MiB；清单含 NUL 字节或最后一行没有换行也拒绝。产品服务每次请求重新读取目录，无需重启。本地控制台的页面只在打开桌面时读一次动画清单，所以本地控制台（`anas-kiosk@tty1`）在运行时由脚本重启；没有运行时不启动它。其他电脑与手机上已打开的页面继续播放原来的池，刷新后换成新池。
   - 完成标准：输出 `companion animation pool <池 ID> is current`；本地控制台运行时，重新打开的桌面上出现伙伴。

## 验证

在另一台电脑上检查，`<NAS 地址>` 换成 NAS 的局域网地址。Windows PowerShell：

```powershell
$nas = 'http://<NAS 地址>'
$manifest = Invoke-RestMethod "$nas/companion/animations"
$animations = @($manifest.animations.PSObject.Properties)
$manifest.pool                                               # 第 2 步的池 ID
$animations.Count                                            # 清单中 webm 的行数
@($animations | Where-Object { -not $_.Value.mov }).Count    # 0：每段都有 mov
curl.exe -s -o NUL -w '%{http_code}\n' -r 0-1023 "$nas$($animations[0].Value.webm)"   # 206
```

macOS 或 Linux 的终端，需要 `curl` 与 `python3`。多数 Linux 发行版自带两者；macOS 自带 `curl`，`python3` 随 Command Line Tools 安装，未安装时首次运行只弹出安装提示，此时改用上面的 PowerShell 检查或先安装 Command Line Tools：

```bash
nas=http://<NAS 地址>
curl -fsS "$nas/companion/animations" | python3 -c 'import json, sys
m = json.load(sys.stdin); a = m["animations"]
print(m["pool"], len(a), sum("mov" not in f for f in a.values()))
print(next(iter(a.values()))["webm"])'
curl -s -o /dev/null -w '%{http_code}\n' -r 0-1023 "$nas<上一行打印的 URL>"
```

- 池 ID 与第 2 步一致；动画数等于清单中 webm 的行数，每段都有 `mov`。
- Range 请求返回 `206`。
- 本地控制台运行时，本机屏幕上出现伙伴。

## 回滚或恢复

- 已安装的池无需暂存，运行第 2 步即切回；本地控制台同样在运行时被重启。每个 release 的 `install-media-pool.sh` 只接受它固定的池，所以切回另一个池要用固定那个池的 release 的脚本，一般随回退 release 一起做。产品服务保留所有已安装的池，动画地址带着池 ID，所以其他已打开的页面在刷新前继续播放切换前的池，刷新后才换成切回的池。
- 删除 `/var/lib/a-nas/companion/current` 后，刷新或重新打开的页面不再显示伙伴，其余功能不受影响；本地控制台要 `systemctl restart anas-kiosk@tty1.service` 后才隐藏伙伴。

## 固定与升级

- dsh-pet 的代码与动画不跟随上游更新，设备上也没有更新的路径；规则见 [`dsh-pet/README.md` 的 No updates](../../web/src/companion/dsh-pet/README.md#no-updates)，其中有每次提里程碑前检查上游是否改动了复制目录的命令。
- 升级是一次有意的依赖升级，步骤见同一 README 的 [Upgrading](../../web/src/companion/dsh-pet/README.md#upgrading)：改固定的提交（素材包变了时连同 `MOV_ZIP`，并保留那一份 zip）、在干净的检出上重新引入并适配补丁、把 `install-media-pool.sh` 的 `companion_pool` 改成新池、运行 `make vendor-check DSH_PET=<检出> DSH_PET_MOV_ZIP=<zip>` 与 `make check`、在浏览器（含 Safari）里试过，再按本手册暂存并安装新池。新池的池 ID 不同，本手册的池 ID 随之更新。
- 复制脚本只接受固定提交上的干净检出：已跟踪的改动、任何未跟踪的文件，或 `dsh-pet/src`、`dsh-pet/assets/config.jsonc`、`dsh-pet/assets/webm`、`LICENSE` 下被忽略的文件都让它拒绝。

## 关联

- 规格：[虚拟伙伴（大肥鱼）](../specs/virtual-companion.md)
- ADR：[0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)
- 运行手册：[运行实验 NAS 本地控制台](operate-local-kiosk.md#屏保视频)（屏保视频用同一套脚本）
- 代码：[`install-media-pool.sh`](../../scripts/install-media-pool.sh)、[`remote-stage-media-pool.sh`](../../scripts/remote-stage-media-pool.sh)、[`deploy-media-pool.ps1`](../../scripts/deploy-media-pool.ps1)、[`vendor-dsh-pet.py`](../../scripts/vendor-dsh-pet.py)、[`check-dsh-pet-vendor.sh`](../../scripts/check-dsh-pet-vendor.sh)、[`handler.go`](../../internal/webui/handler.go)
- 测试：[`handler_test.go`](../../internal/webui/handler_test.go)、[`system-test.sh`](../../scripts/system-test.sh)
