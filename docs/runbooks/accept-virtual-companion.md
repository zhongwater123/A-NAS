# 在公司内网试玩奶蛙任务、提醒与 VNAS 文本纪要

状态：draft
更新时间：2026-11-03

## 目的

由能访问公司开发 NAS 的同事验证：电脑奶蛙与网页小窝接续同一条 VNAS 对话，整理文本会议记录，保存前使用原确认卡，确认后能预览真正落盘的文件。此手册记录实机操作；本地模拟 Agent 的结果不能代替这一项。

## 前提与风险

- 操作人具有开发仓库与测试 NAS 的访问权。系统服务升级沿用[实验存储主手册](provision-v1.0.1-experimental-storage.md)，root 步骤仍由 NAS 管理员执行。
- 目标必须是公司内网的开发 NAS；先按[SSH 手册](bootstrap-experimental-nas-ssh.md)核对身份、主机指纹和当前 release。不要在保存真实家庭资料的设备上启用本轮云端模型测试。
- 已有测试成员能打开 VNAS，模型和文件工具按[助手试用手册](try-ai-assistant-on-experimental-nas.md)配置。桌宠不配置第二套模型密钥。
- 制品未签名，只作内部试玩；角色来源与发布使用边界见[素材说明](../../web/src/companion/assets/NOTICE.md)。
- 停止条件：目标身份不符、当前提交 CI 未通过、现有 VNAS 本身不可用、测试前已有未处理的确认卡，或文件权限与预期不符。记录现象并停止本轮操作，不绕过确认、鉴权或模型配置。

## 准备代码与制品

1. 在干净的开发检出中获取分支。先运行 `git status --short`，有自己的改动时先保留，不执行切换。

   ```sh
   git fetch origin codex/companion-review-20261102
   git switch --detach origin/codex/companion-review-20261102
   git rev-parse HEAD
   ```

2. 在该 PR 的 Checks 查看同一提交的 `Project checks`、`Go tests with the race detector`、`System test (Debian 13, systemd)` 和 `Companion protocol (Linux)`。全部通过后再进入 NAS 更新。CI 不会连接公司 NAS，也不运行真实云端模型。
3. CI 的 `Companion preview` 页面提供以下 artifacts，保留 7 天：
   - `companion-desktop-windows-x64`：Windows 桌宠 ZIP；
   - `companion-desktop-macos-arm64`：Apple 芯片 Mac 桌宠 ZIP；
   - `companion-protocol-evidence`：两个入口、原确认卡和保存预览的模拟协议证据。
   主 CI 的 `companion-linux-amd64` 包含嵌入 Web 的产品二进制和构建校验记录，只供核对候选制品；它不是包含模型、运行环境和 systemd 单元的完整安装包，不能手工替换在线服务。
4. NAS 更新使用 `scripts/deploy-dev.ps1 -StageOnly` 暂存该提交，再按[实验存储主手册](provision-v1.0.1-experimental-storage.md#安装系统服务)安装、核对与回滚。不要省略 `-StageOnly`，也不要复用旧的用户级预览激活路径。核对新版本包含 `/api/v1/companion` 和“伙伴小窝”；保留上一 release 的标识与制品校验记录。

没有桌宠 artifact 时，可以在开发机使用仓库指定的 Node/npm 从源码构建：

```sh
npm ci --prefix web
npm ci --prefix desktop/companion
npm run companion:build --prefix web
npm run package:win --prefix desktop/companion
```

Mac 将最后一行换成 `npm run package:mac --prefix desktop/companion`。包里默认是离线试玩；真实任务入口来自同一提交的 NAS Web/API。

## 启动桌宠并登录

Windows：解压桌宠 ZIP，在包含 EXE 的目录打开 PowerShell，输入测试 NAS 的完整 URL，再启动：

```powershell
$env:ANAS_NAS_URL = Read-Host '开发 NAS 的完整 URL（HTTPS，或局域网 HTTP）'
& .\A-NAS-Companion.exe
```

Apple 芯片 Mac：解压内层 ZIP，在包含 APP 的目录执行：

```sh
read -r ANAS_NAS_URL
export ANAS_NAS_URL
./A-NAS-Companion.app/Contents/MacOS/A-NAS-Companion
```

桌宠先显示原 NAS 登录页，使用专门的测试成员登录。它使用自己的 Electron 会话，小窝窗口与它共享该会话，不复制系统浏览器 Cookie。公网地址必须 HTTPS；HTTP 仅接受本机或局域网地址。系统浏览器可独立登录同一测试成员观察接续。

## 验证

将[合成会议记录](../../desktop/companion/fixtures/meeting.md)通过文件管理上传到测试成员个人空间，不使用真实工作或家庭文件。按下表逐项记录候选提交、客户端系统、测试成员代号、预期/实际、截图与执行时间；不要将密码、Cookie 或密钥写入 issue。

| 场景 | 操作 | 完成标准 |
|---|---|---|
| 出现与基础互动 | 打开小窝，叫到这里，招呼、主动捉迷藏送花、安静、隐藏、叫回 | 奶蛙可见，安静模式不主动邀请；隐藏后可恢复 |
| 双端交接 | 电脑桌宠登录后申请接续，再从浏览器叫到小窝 | 旧端先停下，新端接续；另一端离线时等待旧租约到期，不是两端同时激活 |
| 输入纪要 | 小窝展开“整理 NAS 里的会议记录”，选择上传的 `meeting.md`，点“整理这份记录” | 出现处理状态；摘要包含两个已知负责人和日期，未指定事项标“待确认”；来源文件不被覆盖 |
| 保存前 | 点“保存到个人空间”但暂不确认 | 原 VNAS 对话出现确认卡；奶蛙显示等你决定，个人空间尚未新增结果文件 |
| 同一条对话 | 等待时切到电脑桌宠，打开这次任务 | 指向原对话与原确认卡，不新建另一条任务，不重复整理或提交更改 |
| 拒绝 | 第一轮在原确认卡点拒绝 | 没有结果文件落盘；不出现“已确认保存”的新文件记录；后续仍能另开一件事 |
| 确认与预览 | 另开一件事后重复整理与保存，在原确认卡确认 | 文件管理能找到新 Markdown；小窝只在执行器记录为 applied 后列出文件，预览与实际文件内容一致 |
| 停止 | 在新一轮处理中点“停止这次回答” | 回答停止或明确显示停止请求未送达；不能把未送达当作已停止；已确认的旧文件不被撤回 |
| 断网与恢复 | 处理中断开客户端网络，再恢复连接和刷新 | 连接中断时不能提交新任务；恢复同一对话及结果，不自动重发 prompt |
| 成员隔离 | 退出后改用另一个测试成员 | 不继承上一个成员的对话引用、确认卡、已保存文件或玩球次数 |
| 无 VNAS / 旧 NAS | 使用未开启助手的测试成员，或单独核对旧服务兼容提示 | 显示不可用，不把离线试玩当成真实任务执行 |

互动租约只控制奶蛙由哪个入口互动，不是任务超时。任务生命周期沿用 VNAS；不要将关闭小窝等同于停止任务，也不承诺退出 NAS 账号后仍无限后台执行。

## 提醒与主动回报的运行时验收

前端协议测试不替代 Hermes 运行时。当前 Mac 独立 Linux 环境的实际任务、离线唤醒和重新登录结果检查见[运行时联调记录](../research/companion-reminder-runtime-check.md)；真实 NAS 仍需按[助手试用手册](try-ai-assistant-on-experimental-nas.md#qwen-云端联调与后续本地接入)配置后执行下列用例。云端阶段只使用合成资料；本机 Fake 宿主机与 Qwen 直连检查都不替代实机验收。

| 检查 | 操作 | 通过标准 |
| --- | --- | --- |
| 自然语言安排 | 在小窝输入“几分钟后提醒我检查合成测试结果”，发送 | 模型经真实 `cronjob` 保存；展开“我的提醒与后台任务”出现实际任务与具体下次时间，NAS 时区与设备显示时间核对一致 |
| 到期与离线 | 保持 NAS 在线，关闭 VNAS 窗口或客户端，到期后重新登录 | NAS 留下运行记录；奶蛙补显示未读结果；重新载入不重复庆祝或创建任务 |
| 在线主动反馈 | 保持奶蛙在这里、非安静，等待下一次合成任务完成 | 正常结果显示“有新结果”，失败有明确说明；一分钟级轮询后出现，不承诺秒级弹出 |
| 查看与回执 | 查看原运行记录，再对另一条结果点“知道了” | 原结果与运行记录一致；已读在其他设备下次读取后消失，不把用户事项标成已完成；已读网络失败时卡片仍在 |
| 十分钟后再提醒 | 在小窝结果卡点该按钮，再点“交给奶蛙” | 先看到待发送交代；发送后真实任务列表出现一次性新提醒，原任务不会重跑、改计划或自动标已读 |
| 隔离与拒绝重复执行 | 换测试成员，另测忙、安静、断网和重复刷新 | 不显示前成员记录；忙时不插入播报；安静时不庆祝；重连不重发交代，任务失败不会被开心文案伪装为成功 |

当前原生桌宠紧凑视图只提供“查看结果”和“知道了”，重新安排从小窝发起。触屏继续列为硬件待办。

## 回滚或恢复

- 停止桌宠可从托盘退出，清除本次进程的 `ANAS_NAS_URL` 后重新启动即回离线试玩。
- 服务升级或鉴权异常时，停止联调并由管理员按[主手册的回滚步骤](provision-v1.0.1-experimental-storage.md#回滚)恢复上一 release 与单元；不要删除或格式化数据卷。
- 新服务在产品状态目录添加 `companion.db`，迁移只增加对话引用列。保留该数据库及正常备份，不手工删库或降列；旧服务不使用该功能。已确认写入的测试文件由文件管理正常删除或恢复，服务回滚不会替你撤销个人文件更改。
- 在 PR 反馈失败步骤、候选提交与现象；模型未响应、确认执行失败、客户端断线和 NAS 升级问题分开记录，不能以模拟协议通过代替实机通过。

## 关联

- [行为与接续边界](../specs/virtual-companion.md)
- [桌宠构建与本机验证](../../desktop/companion/README.md)
- [完整 Linux 门禁](../../.github/workflows/ci.yml)与[试玩包和协议验证](../../.github/workflows/companion.yml)
- [产品成员鉴权测试](../../internal/httpapi/companion_test.go)
