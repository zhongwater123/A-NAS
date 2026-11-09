# 安装伙伴动画

状态：draft（系统测试已验证；实验 NAS 待执行）
更新时间：2026-11-06

## 目的

把虚拟伙伴（大肥鱼）播放的动画装到 NAS 上。动画来自 dsh-pet 的固定提交，不进 Git、不随 release 发布；与屏保视频一样是系统盘上的长期媒体（[ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)）。成功后产品服务的 `/companion/animations` 列出全部动画，各动画支持 Range 请求。

## 前提与风险

- 权限：开发机能以 `anas-dev` 用 SSH 登录 NAS；NAS 上有 root。
- 目标：已安装含 `install-media-pool.sh` 的 release 的 NAS（`/opt/a-nas/current/install-media-pool.sh` 存在）。
- 素材：本地 dsh-pet 检出位于 [`web/src/companion/dsh-pet/README.md`](../../web/src/companion/dsh-pet/README.md) 记录的提交；Safari 用的 HEVC 动画是 dsh-pet 的 `assets-mov` 发布中的 `dsh-pet-mov-assets-mov.zip`，SHA-256 为 `654e42f14b647a1e59a4c6f05694273dcf23edb063a5e49b14fc05a1fad3b467`（[`vendor-dsh-pet.py`](../../scripts/vendor-dsh-pet.py) 中的 `MOV_ZIP`）。动画仅限非商业试验，许可与署名见同一 README。
- 风险：只写 `/var/lib/a-nas/companion` 与暂存账号的 `~/apps/a-nas/companion`，不涉及数据盘。
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

   - 预期：脚本复核池 ID 与每个文件的 SHA-256，任一不符即在改动前停止；全部通过后建池并原子切换 `current`。产品服务每次请求重新读取目录，无需重启。
   - 完成标准：输出 `companion animation pool <池 ID> is current`。

## 验证

- `curl -fsS http://127.0.0.1:8080/companion/animations | jq '.animations | length'` 等于清单中的动画数（webm 的行数），其中除吃白饭的 5 段外都有 `mov`。
- 取清单中任一 URL，`curl -s -o /dev/null -w '%{http_code}' -r 0-1023 http://127.0.0.1:8080<URL>` 返回 `206`。

## 回滚或恢复

- 指定已安装的池 ID 再运行第 2 步即切回该池，无需暂存。
- 删除 `/var/lib/a-nas/companion/current` 后伙伴不再显示，其余功能不受影响。

## 关联

- 规格：[虚拟伙伴（大肥鱼）](../specs/virtual-companion.md)
- ADR：[0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)
- 代码：[`install-media-pool.sh`](../../scripts/install-media-pool.sh)、[`remote-stage-media-pool.sh`](../../scripts/remote-stage-media-pool.sh)、[`deploy-media-pool.ps1`](../../scripts/deploy-media-pool.ps1)、[`vendor-dsh-pet.py`](../../scripts/vendor-dsh-pet.py)、[`handler.go`](../../internal/webui/handler.go)
- 测试：[`handler_test.go`](../../internal/webui/handler_test.go)、[`system-test.sh`](../../scripts/system-test.sh)