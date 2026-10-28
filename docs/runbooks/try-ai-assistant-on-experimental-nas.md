# 在实验 NAS 上试用 AI 助手（云端模型）

状态：draft
更新时间：2026-10-27

## 目的

在实验 NAS 上为 `a-nas-users` 的全部成员开启 AI 助手，用云端 OpenAI 兼容模型（默认 DeepSeek `deepseek-flash`）验证它在设备上的可用性与资源占用（[#116](https://github.com/zhongwater123/A-NAS/issues/116)）。只用于开发测试，结束后按“回滚或恢复”移除。

成功结果：成员在桌面打开“VNAS”（AI 助手），能对话、读取本人个人空间与 Shared，在自己的目录里运行命令与代码；请它改文件或复制图片时出现确认卡片，确认后由文件代理执行，旧版本进回收站；有风险的命令与每段代码先出命令卡片。

正式产品中这些由 Host Agent 的类型化操作与安装器完成（[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)），尚未实现；本手册用 spike 的脚本代替，限制见下。

## 前提与风险

- 权限：NAS 上的 root 步骤由用户执行；开发机上的准备与暂存经 `ssh a-nas-dev`。
- 目标：主机名 `a-nas-dev`；`/opt/a-nas/current` 指向含助手代码的 release（`df2a9837dc2d` 或更新）；数据卷 `/srv/a-nas/data` 为 `/dev/sda1`，Btrfs，UUID `09e275fe-794a-458d-8200-b6e67c55cc22`（与[数据卷手册](provision-v1.0.1-experimental-storage.md)一致）。
- 隐私：成员使用助手时，它读到的个人空间与 Shared 文件内容会发往云端模型。规格要求本地模型接入前不在保存真实家庭数据的设备上开启；实验 NAS 只存可丢弃测试数据。搜索结果里的照片名称、拍摄时间、标签与相册名会发给模型；它查看照片详情时，原图记录的元数据也会，包括相机记下的拍摄位置；成员要它看照片的画面时，那几张照片的 1600 像素视图也会。模型网关只记 token 用量与工具名，不记消息，日志目录只有 root 可读。
- 数据卷写入：每个账号一个 Btrfs 子卷 `assistant/<账号>`，Shared 中的 `Agent空间/<账号>`，以及执行方的 `assistant/.changes`。不开启 simple quota：ADR 0024 要求先在测试卷上验证它对快照与删除的影响（#116），因此试用期间助手目录没有 10 GiB 上限。
- 服务影响：安装、更新与移除各重启一次 Host Agent、相册服务与产品服务，几秒内进行中的文件操作、终端、上传与相册整理会断开后重连。
- 内存：每个实例上限 2 GiB，没有连接约 45 秒后退出；NAS 共 7.7 GB，多人同时使用时留意（#116 记录实测）。
- 用 spike 脚本代替 Host Agent 的限制：
  - `Agent空间` 及账号目录由 Host Agent 管理；先更新并重启 Host Agent，确认这些目录归 root 所有且不是链接，再安装新版试用包。安装脚本只检查并复用它们，不按路径重写其 ACL；它们已存在不算旧试用残留，移除试用也保留。助手自身目录仍由 spike 按路径创建，因此只在信任现有成员、没有并发目录改动的全新试用状态执行。Shared 中名为 `Agent空间` 的条目不通过 SMB 访问，发布内容使用 Web 或助手读取。
  - 实例套接字、模型网关与 `/run` 下的挂载点在重启后自动恢复（已启用的单元与 tmpfiles）。Host Agent 重启后，已在运行的实例里绑定的执行方套接字失效，提交更改会失败，直到实例空闲退出后重开。
  - 相册接给助手的方式（ADR 0024 决定 6）：相册服务多开一个只给助手的套接字（`anas-photos-assistant.socket`，由 systemd 持有，只绑定进每个助手的沙箱），按连接者的 UID 认出成员，只读，只看本人私有图库与共享图库，拿不到原图与缩略图；Host Agent 为它提供按 UID 查成员（`ANAS_PHOTO_ASSISTANT_LOOKUP=1`）。窗口里的照片由成员本人的会话加载。
  - 这些设置与会话闸门一起写成 systemd drop-in（`/etc/systemd/system/<服务>.service.d/assistant-trial.conf`，由 [`product-settings.sh`](../../ai/assistant-spike/product-settings.sh) 写入）：正式部署会重写 `/etc/a-nas/*.env`，写在那里的会话闸门每次部署后都要重新加；drop-in 不受部署影响。
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
   - 预期：依次输出 packages、files、the assistant for N accounts、the session gate, and photos for the assistants，最后 11 行 `PASS` 与 `installed`。
   - 完成标准：没有 `STOP` 或 `FAIL`。
5. NAS（root，可选）：测冷启动与它能看到的相册，不调用模型。
   ```bash
   bash /root/assistant-trial-<提交>/spike/nas-trial.sh smoke <账号> 钓鱼
   ```
   - 预期：`ready after N s` 与实例内存；第一次启动要把 Hermes 自带的技能写进主目录，最长约一分钟。之后 `photo libraries: 200 [...]` 列出本人的私有图库（`private:`）与共享图库（`shared:`），`photos for "钓鱼"` 列出这个账号搜到的照片名称，与在相册里搜索的结果一致。

## 更新

包含 [#178 修复](../investigations/2026-10-27-assistant-cron-waker-member-isolation.md) 的版本，先更新 Host Agent，再更新助手 bundle。安装器和定时任务唤醒器现在通过 Host Agent 的私有 socket 查询活动账号；旧 Host Agent 不提供该查询，检查失败时不会启用或唤醒成员助手。停用账号会停止并 mask 其助手、API、定时任务与复盘单元，更新保留这些 mask；不要为排查连接故障手动 unmask 停用账号。回滚到缺少此修复的版本会失去这项保证。

新的 bundle 暂存后，校验并解包（第 2 步），然后：

```bash
bash /root/assistant-trial-<提交>/spike/nas-trial.sh update
```

- 换上新的脚本、单元、Hermes 策略与运行环境，重新生成会话令牌与成员配置（都在 root 自己的 `/etc` 中），停止正在运行的实例；成员的助手目录、对话与记忆保留，成员可写的文件夹不碰。
- 完成标准：最后 11 行 `PASS`。安装之后才加入的成员不在其中，`update` 会列出；要包括他们，`remove` 后重新 `install`。
- 已装早先试用（bundle `c6f6993`）的设备同样直接 `update`：会话令牌按新格式重新生成，成员的助手目录保留（2026-10-16 在系统测试容器中预演）。
- 2026-10-16 之后的 bundle 带上了后台复盘工具白名单的上游修复，并为每个账号启用每周的闲时整理（`anas-assistant-curator@<账号>.timer`，`check` 的最后一行 `PASS weekly curator`）；`update` 一并换上（[研究](../research/hermes-agent-runtime.md#后台复盘与闲时整理)）。
- 2026-10-19 之后的 bundle 接上相册（`check` 的最后两行 `PASS photo service ...`），并把会话闸门从 `/etc/a-nas/*.env` 移到 drop-in；`update` 删掉环境文件里旧的那一行。之后的正式部署不再需要重新加它（[研究](../research/hermes-agent-runtime.md#接入相册服务)）。
- 2026-10-20 之后的 bundle 开启定时任务：装上唤醒服务 `anas-assistant-cron-waker.service` 与运行单元 `anas-assistant-cron@.service`（`check` 多一行 `PASS scheduled tasks are started when due`），以及系统技能 `anas-scheduled-tasks`。任务列表、立即运行与桌面提示在产品服务中，要正式部署带上它们才有（[研究](../research/hermes-agent-runtime.md#定时任务)）。
- 带“VNAS 操作 WebOS”的 bundle 把实例换成 Hermes 自己的登录，旧的产品服务连不上它，带登录的产品服务也登不进旧实例，所以**同一次维护里先正式部署产品服务（含 Host Agent 的文件代理），再立即 `update`**；中间这一会儿 VNAS 不可用。它带上 `hermes-patches/0002` 与系统技能 `anas-desktop`，删掉旧的 `anas-assistant-token@.service`（[研究](../research/hermes-agent-runtime.md#让-vnas-操作-webos)）。

## 验证

- 用自己的账号登录 Web，桌面的“VNAS”可以打开，不再是“规划中”。
- 登录后实例在后台启动（`systemctl is-active anas-assistant@<账号>` 几秒后为 `active`，不用打开窗口），之后打开 VNAS 不再等待；全部退出登录约 45 秒后实例退出。这需要产品服务带上 2026-10-19 的“登录即启动”，由正式部署带来，试用的 `update` 不换产品服务。
- 问“我的个人空间里有哪些文件？”，助手列出文件。
- 请它在个人空间新建或改写一个文件：出现确认卡片；确认后文件出现或改变，被改写的旧版本在回收站中；拒绝后什么都不变。
- 请它把共享空间里的一张图片复制到个人空间：卡片写明来源、目标与大小，确认后图片出现在个人空间。
- 请它删除自己目录里的一个文件夹：出现命令卡片，拒绝后文件夹还在。
- 请它看照片，例如“找到我钓鱼的照片，看看里面都有什么”：处理过程里出现“取出照片画面”与“看图”，回答说的是照片里的实际内容。
- 请它找照片，例如“帮我在相册里找钓鱼的照片”：回答列出照片的名称与拍摄时间，下方显示照片本身（一张时是大图，多张时排成网格），点开在相册中打开；结果与在相册里搜索一致，没有别人私有图库里的照片。
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

- 关闭会话闸门与相册的助手套接字（删除三个 drop-in、`anas-photos-assistant.socket` 与环境文件里早先试用留下的那一行），重启 Host Agent、相册服务与产品服务。
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
