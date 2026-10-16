# 在实验 NAS 上试用 AI 助手（云端模型）

状态：draft
更新时间：2026-10-16

## 目的

在实验 NAS 上为 `a-nas-users` 的全部成员开启 AI 助手，用云端 OpenAI 兼容模型（默认 DeepSeek `deepseek-flash`）验证它在设备上的可用性与资源占用（[#116](https://github.com/zhongwater123/A-NAS/issues/116)）。只用于开发测试，结束后按“回滚或恢复”移除。

成功结果：成员在桌面打开“AI 助手”，能对话、读取本人个人空间与 Shared，在自己的目录里运行命令与代码；请它改文件或复制图片时出现确认卡片，确认后由文件代理执行，旧版本进回收站；有风险的命令与每段代码先出命令卡片。

正式产品中这些由 Host Agent 的类型化操作与安装器完成（[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)），尚未实现；本手册用 spike 的脚本代替，限制见下。

## 前提与风险

- 权限：NAS 上的 root 步骤由用户执行；开发机上的准备与暂存经 `ssh a-nas-dev`。
- 目标：主机名 `a-nas-dev`；`/opt/a-nas/current` 指向含助手代码的 release（`df2a9837dc2d` 或更新）；数据卷 `/srv/a-nas/data` 为 `/dev/sda1`，Btrfs，UUID `09e275fe-794a-458d-8200-b6e67c55cc22`（与[数据卷手册](provision-v1.0.1-experimental-storage.md)一致）。
- 隐私：成员使用助手时，它读到的个人空间与 Shared 文件内容会发往云端模型。规格要求本地模型接入前不在保存真实家庭数据的设备上开启；实验 NAS 只存可丢弃测试数据。模型网关只记 token 用量与工具名，不记消息，日志目录只有 root 可读。
- 数据卷写入：每个账号一个 Btrfs 子卷 `assistant/<账号>`，Shared 中的 `Agent空间/<账号>`，以及执行方的 `assistant/.changes`。不开启 simple quota：ADR 0024 要求先在测试卷上验证它对快照与删除的影响（#116），因此试用期间助手目录没有 10 GiB 上限。
- 服务影响：安装与移除各重启一次 Host Agent 与产品服务，几秒内进行中的文件操作、终端与上传会断开后重连。
- 内存：每个实例上限 2 GiB，没有连接约 45 秒后退出；NAS 共 7.7 GB，多人同时使用时留意（#116 记录实测）。
- 用 spike 脚本代替 Host Agent 的限制：
  - 安装按路径在成员可写的 Shared 中建立 `Agent空间` 并设置属主与 ACL（Host Agent 会以不跟随链接的方式操作）。因此安装只在全新状态运行，拒绝覆盖已有内容；安装进行的几秒内，若有成员恰好用符号链接替换这些文件夹，仍可能让 root 改到别处。只在信任现有成员时执行。
  - 实例套接字、模型网关与 `/run` 下的挂载点在重启后自动恢复（已启用的单元与 tmpfiles）。Host Agent 重启后，已在运行的实例里绑定的执行方套接字失效，提交更改会失败，直到实例空闲退出后重开。
  - 照片工具没有接上（相册的助手套接字需另行配置），问到照片时助手回答“相册暂时连不上”。
  - `video_analyze` 把整段视频交给模型，DeepSeek 不收视频，这个工具会报错；看图（`vision_analyze`）可用。
  - 模型可能提到 Hermes 的 `/help` 与上手引导（待定）。
- 停止条件：`check` 报告不是全新状态、数据卷或 release 不符、bundle 校验失败；`install` 输出 `STOP` 或 `FAIL`。出现时不要继续，按“回滚或恢复”处理。

## 步骤

1. 开发机：生成并暂存 bundle。
   ```bash
   ai/assistant-spike/nas-trial-bundle.sh assistant-trial-<提交>
   ```
   在 WSL 中运行（需要 `run.sh build` 做好的运行环境与本地 Debian 13 镜像），再 `scp` 到 `a-nas-dev:~/`。
   - 预期：打印 socat、ripgrep 的依赖与压缩包的 SHA-256。
   - 完成标准：依赖都已在 NAS 上（`dpkg-query -W`），压缩包已在 `/home/anas-dev/`。
2. NAS（root）：核对、解包、检查。
   ```bash
   sha256sum /home/anas-dev/assistant-trial-<提交>.tar.gz
   tar -C /root -xzf /home/anas-dev/assistant-trial-<提交>.tar.gz
   bash /root/assistant-trial-<提交>/spike/nas-trial.sh check
   ```
   - 预期：SHA-256 与第 1 步一致；`check` 列出主机、release、数据卷（UUID 如上）、账号与 `fresh`，`key` 为 `MISSING`。
   - 完成标准：以上都符合。
3. NAS（root）：写入模型密钥。不要把密钥写进命令行，它会留在 shell 历史里。
   ```bash
   install -d -m 0700 /etc/anas-spike
   (umask 077; cat > /etc/anas-spike/model.key)
   ```
   粘贴密钥，回车，再按 Ctrl-D。
   - 完成标准：再次 `check` 显示 `key: /etc/anas-spike/model.key (600 root)`。
   - 密钥只写这一次：`install`、`update` 与 `remove` 都不动它，只有 `remove --purge-key` 删除它。
4. NAS（root）：安装。
   ```bash
   bash /root/assistant-trial-<提交>/spike/nas-trial.sh install
   ```
   - 预期：依次输出 packages、files、the assistant for N accounts、the session gate，最后 8 行 `PASS` 与 `installed`。
   - 完成标准：没有 `STOP` 或 `FAIL`。
5. NAS（root，可选）：测冷启动，不调用模型。
   ```bash
   bash /root/assistant-trial-<提交>/spike/nas-trial.sh smoke <账号>
   ```
   - 预期：`ready after N s` 与实例内存；第一次启动要把 Hermes 自带的技能写进主目录，最长约一分钟。

## 更新

新的 bundle 暂存后，校验并解包（第 2 步），然后：

```bash
bash /root/assistant-trial-<提交>/spike/nas-trial.sh update
```

- 换上新的脚本、单元、Hermes 策略与运行环境，重新生成会话令牌与成员配置（都在 root 自己的 `/etc` 中），停止正在运行的实例；成员的助手目录、对话与记忆保留，成员可写的文件夹不碰。
- 完成标准：最后 8 行 `PASS`。安装之后才加入的成员不在其中，`update` 会列出；要包括他们，`remove` 后重新 `install`。
- 已装早先试用（bundle `c6f6993`）的设备同样直接 `update`：会话令牌按新格式重新生成，成员的助手目录保留（2026-10-16 在系统测试容器中预演）。
- 2026-10-16 之后的 bundle 带上了后台复盘工具白名单的上游修复，并为每个账号启用每周的闲时整理（`anas-assistant-curator@<账号>.timer`，`check` 的最后一行 `PASS weekly curator`）；`update` 一并换上（[研究](../research/hermes-agent-runtime.md#后台复盘与闲时整理2026-10-16开发机)）。

## 验证

- 用自己的账号登录 Web，桌面的“AI 助手”可以打开，不再是“规划中”。
- 问“我的个人空间里有哪些文件？”，助手列出文件。
- 请它在个人空间新建或改写一个文件：出现确认卡片；确认后文件出现或改变，被改写的旧版本在回收站中；拒绝后什么都不变。
- 请它把共享空间里的一张图片复制到个人空间：卡片写明来源、目标与大小，确认后图片出现在个人空间。
- 请它删除自己目录里的一个文件夹：出现命令卡片，拒绝后文件夹还在。
- 打开两个窗口，卡片在两个窗口都出现，在任一窗口确认即可。
- 退出登录后 30 秒内，助手窗口的连接断开。
- “记忆”页能打开，删除一条后不再出现。
- 后台复盘改了记忆或技能时，那一轮回答下方出现“自我改进回顾：……”与“查看记忆”（Hermes 默认每 10 条消息复盘一次记忆）。
- 闲时整理：`systemctl list-timers 'anas-assistant-curator@*'` 列出每个账号下一次运行的时间；运行过后 `journalctl -u anas-assistant-curator@<账号>.service` 有一行 `auto: checked=…`。
- 资源：`systemctl status 'anas-assistant@*'` 看实例内存；`/var/log/anas-assistant-gateway/requests.jsonl` 每次请求一行，记用时、工具名与 token 用量。结果写进[研究文档](../research/hermes-agent-runtime.md)与 #116。

## 回滚或恢复

```bash
bash /root/assistant-trial-<提交>/spike/nas-trial.sh remove
```

- 关闭会话闸门（两份环境文件各去掉一行），重启 Host Agent 与产品服务。
- 停止并删除每个账号的实例、令牌、成员配置与 `assistant/<账号>` 子卷：助手目录里的文件、对话与记忆一并删除。
- 删除空的 `Agent空间` 文件夹；有内容的保留并列出，由用户决定。
- 删除单元、tmpfiles、`/etc/hermes`、`/etc/a-nas-assistant`、运行环境、`/spike` 与网关日志。密钥保留，测试结束时用 `remove --purge-key` 一并删除。socat 与 ripgrep 保留，`dpkg -r socat ripgrep` 可卸载。
- 完成后 `check` 显示 `fresh`。安装中途失败时同样运行 `remove`。

## 关联

- 规格：[AI 助手](../specs/ai-assistant.md)
- ADR：[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
- 研究：[Hermes Agent 作为 AI 助手运行时](../research/hermes-agent-runtime.md)
- Issue：[#116](https://github.com/zhongwater123/A-NAS/issues/116)
- 代码：[`nas-trial.sh`](../../ai/assistant-spike/nas-trial.sh)、[`nas-trial-bundle.sh`](../../ai/assistant-spike/nas-trial-bundle.sh)、[网关单元](../../ai/assistant-spike/anas-assistant-trial-gateway.service)、[tmpfiles](../../ai/assistant-spike/anas-assistant-trial.tmpfiles)、[`assistant-install.sh`](../../ai/assistant-spike/assistant-install.sh)、[`assistant-layout.sh`](../../ai/assistant-spike/assistant-layout.sh)
