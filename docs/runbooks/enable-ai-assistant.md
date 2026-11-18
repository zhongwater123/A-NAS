# 启用 AI 助手（VNAS）

状态：draft（系统测试已验证保存密钥、准备成员与模型网关；实验 NAS 上的接管与验收待执行）
更新时间：2026-11-18

## 目的

管理员在“设置 → VNAS”中填写全家的 DeepSeek 密钥，全部成员就都有 VNAS（[决定](../decisions/2026-11-17-assistant-v1-cloud-model.md)）。删除密钥即整台设备关闭 VNAS，成员的对话、记忆、定时任务与助手目录保留。

成功结果：成员在桌面打开“VNAS”，能对话、读取本人个人空间与 Shared，在自己的目录里运行命令与代码；请它改文件或复制图片时出现确认卡片，确认后由 Host Agent 执行，旧版本进回收站；有风险的命令与每段代码先出命令卡片。

已在实验 NAS 上试用过的设备，升级到带本手册的版本时由 `a-nas` 一次接管试用，见“从试用接管”。

## 前提与风险

- 权限：填写、替换、删除密钥只有管理员能做，在 Web 中完成；核对步骤里的命令要 root，由用户执行，只读。
- 版本：已装的 `a-nas` 带 `/usr/lib/a-nas/assistant`（[部署 A-NAS 软件包](deploy-a-nas-packages.md)）。
- 隐私：保存密钥前页面写明并要管理员确认：成员与 VNAS 的对话、VNAS 为回答读取的文件内容与照片信息、它查看的照片画面与桌面画面，都会发到 DeepSeek 的服务器处理（[规格](../specs/ai-assistant.md#管理员填写密钥)）。
- 密钥：由 Host Agent 用 `systemd-creds`（本机的凭据密钥 `/var/lib/systemd/credential.secret`，由 `a-nas` 的 postinst 生成）加密为 `/etc/credstore.encrypted/a-nas-assistant-deepseek`，只有模型网关的单元读取，页面不再显示，日志不记。重装系统盘或丢失 `credential.secret` 后要重新填写。
- 数据卷写入：每个成员一个 Btrfs 子卷 `assistant/<账号>`，以及执行方的 `assistant/.changes`；Shared 中的 `Agent空间/<账号>` 由 Host Agent 照常维护。助手目录还没有 10 GiB 上限：ADR 0024 要求先在实验 NAS 上验证 simple quota 对快照与删除的影响（[#116](https://github.com/zhongwater123/A-NAS/issues/116)）。
- 内存：每个实例上限 2 GiB，没有连接约 45 秒后退出；2026-11-18 实验 NAS 上两个在用的实例各约 400 MB（16 GB 内存）。
- 停止条件：保存后页面写着“模型网关出错”或“模型网关未运行”，或成员仍看到“需管理员设置”。出现时不要反复保存，按“回滚或恢复”处理并查看下面的核对命令。

## 步骤

1. 管理员在 DeepSeek 开放平台创建 API 密钥（按量计费，费用由这一户的 DeepSeek 账号承担）。
2. 在 Web 中打开“设置 → VNAS”，点“填写”，粘贴密钥，读完发往 DeepSeek 的内容后勾选同意，点“保存”。
   - 预期：页面显示“已填写”，提示“已保存，全家成员都能使用 VNAS”。
   - 完成标准：成员刷新桌面后，“VNAS”不再标着“需管理员设置”，可以打开。
3. NAS（root，只读）核对：

   ```bash
   stat -c '%a %U %n' /etc/credstore.encrypted/a-nas-assistant-deepseek
   systemctl is-active anas-assistant-gateway.service anas-assistant-cron-waker.service
   systemctl list-units --no-legend 'anas-assistant-api@*.socket'
   ```

   - 预期：凭据为 `600 root`；两个服务都是 `active`；每个已启用的成员一个 `anas-assistant-api@<账号>.socket`，停用的账号没有。

## 从试用接管

升级到本版本时，`a-nas` 的 postinst 发现试用（`/spike` 链接或 `/etc/systemd/system/anas-assistant@.service`）就接管一次，不需要另外的命令：

- 停下试用的实例、定时任务唤醒器与网关；
- 把试用写在 `/etc` 与 `/opt` 的文件（单元、drop-in、tmpfiles、`/etc/hermes`、`/etc/a-nas-assistant`、`/opt/a-nas/assistant-skills`、网关日志）移到 `/var/backups/a-nas/assistant-trial`；
- 把 `/etc/anas-spike/model.key` 加密为上面的凭据，成功后删除 `/etc/anas-spike`；
- 删除 `/spike`。

成员的 `assistant/<账号>` 子卷（对话、记忆、定时任务与目录里的文件）留在数据卷上。产品服务重启后的身份同步让 Host Agent 为每个已启用的成员重新生成实例的密钥与配置，并启用入口单元。`a-nas-assistant-trial` 这次更新为一个空的过渡包，下一个版本的系统更新会把它卸掉。

核对（root，只读）：

```bash
ls /var/backups/a-nas/assistant-trial/etc
test ! -e /etc/anas-spike && test ! -e /spike && echo trial-files-gone
dpkg-query -W -f='${Package} ${Version}\n' a-nas a-nas-assistant-trial
```

- 预期：备份里有 `systemd`、`hermes`、`a-nas-assistant`；打印 `trial-files-gone`；两个包同一版本。
- 预期之外：postinst 打印 `the trial's key stays in /etc/anas-spike` 时，加密没有成功，`/etc/anas-spike` 保留：在“设置 → VNAS”中重新填写密钥，之后由用户删除这个目录。
- 再按上面第 3 步核对，并按“验证”试用一遍。

## 验证

- 桌面的“VNAS”可以打开；登录后实例在后台启动（`systemctl is-active anas-assistant@<账号>` 几秒后为 `active`）；全部退出登录约 45 秒后实例退出。
- 问“我的个人空间里有哪些文件？”，助手列出文件。
- 请它在个人空间新建或改写一个文件：出现确认卡片；确认后文件出现或改变，被改写的旧版本在回收站中；拒绝后什么都不变。
- 请它把共享空间里的一张图片复制到个人空间：卡片写明来源、目标与大小，确认后图片出现在个人空间。
- 请它删除自己目录里的一个文件夹：出现命令卡片，拒绝后文件夹还在。
- 请它找照片并看看照片里有什么：回答列出照片并显示在下方，描述的是照片里的实际内容；没有别人私有图库里的照片。
- 退出登录后 30 秒内，助手窗口的连接断开。
- “记忆”页能打开，删除一条后不再出现；“定时任务”能新建、立即运行、删除。
- 闲时整理：`systemctl list-timers 'anas-assistant-curator@*'` 列出每个成员下一次运行的时间。
- 网关日志（root）：`/var/log/anas-assistant-gateway/requests.jsonl` 每次请求一行，记成员、用时、工具名与 token 用量，不记消息。

## 回滚或恢复

- 关闭 VNAS：管理员在“设置 → VNAS”中点“删除”。所有成员的实例停止、入口单元关闭；助手目录、对话、记忆与定时任务保留，重新填写密钥后恢复。
- 回滚到试用时的版本（系统更新的“回到原版本”）：那个版本的 `a-nas` 没有助手的单元，VNAS 不可用，数据保留；试用的文件在 `/var/backups/a-nas/assistant-trial`，不自动恢复。再次升级后填写密钥即可。
- 删除一个账号时，Host Agent 删除它的助手目录、实例密钥与配置（[重新启用与删除账号](../specs/account-enable-and-delete.md)）。

## 关联

- 规格：[AI 助手](../specs/ai-assistant.md)；决定：[V1 的模型、开启范围、密钥与告知](../decisions/2026-11-17-assistant-v1-cloud-model.md)
- ADR：[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)、[0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)
- 研究：[Hermes Agent 作为 AI 助手运行时](../research/hermes-agent-runtime.md)
- Issue：[#116](https://github.com/zhongwater123/A-NAS/issues/116)、[#179](https://github.com/zhongwater123/A-NAS/issues/179)
- 代码：[密钥与成员准备（Host Agent）](../../internal/hostops/linux/assistant.go)、[设置页](../../web/src/settings/AssistantSection.tsx)、[模型网关](../../ai/assistant/gateway.py)、[单元](../../deploy/systemd/system/anas-assistant@.service)、[接管试用（postinst）](../../deploy/debian/a-nas/DEBIAN/postinst)、[系统测试](../../ai/assistant-spike/system-test.sh)
