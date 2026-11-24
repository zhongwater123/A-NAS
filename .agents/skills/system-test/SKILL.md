---
name: system-test
description: 运行 A-NAS 的 systemd 系统测试（scripts/system-test.sh）。它在以 systemd 为 PID 1 的一次性 Debian 13 容器中，从签名软件源用 apt 安装 A-NAS 的软件包，再按用户检查 Web 文件、SMB、相册、FFmpeg Worker、终端、局域网入口与 AI 助手沙箱的权限边界，最后升级、回滚、再升级。修改服务单元、软件包、Host Agent、文件代理、文件权限或助手沙箱后，以及部署前使用。make release 只发布系统测试通过的软件包。
---

# 运行 systemd 系统测试

系统测试覆盖的内容与原因见[测试与验证 · 系统测试](../../../docs/development/testing.md#系统测试)，入口在 [`scripts/system-test.sh`](../../../scripts/system-test.sh) 与 Makefile 的 `system-test` 目标。CI 的 `System test (Debian 13, systemd)` 作业对每次 push 与 PR 运行同一流程；以下情况才需要在本机运行：推送前就要结果、调试失败，或部署前用真实模型复测。

## 前提

- WSL 中的 Docker；测试只启动并删除自己命名为 `anas-system-test-<PID>` 的容器。本机还运行着与 A-NAS 无关的容器，不要清理它们。
- 镜像 `anas-systemd:trixie`，其中的 aptly 与 GnuPG 为测试做签名软件源。镜像带 label 记录构建输入（`anas.image.inputs`）、ISO 周与来源提交；`scripts/system-test.sh` 启动前用 `scripts/check-system-test-env.sh` 自动核对：镜像的 inputs 必须与本检出的输入一致，否则失败并提示重建；ISO 周不同只警告（提醒重建以带上 Debian 的安全更新）。共享开发服务器上有管理员构建的镜像，先看 `anas-images`；过渡期可用 `ANAS_SYSTEM_TEST_SKIP_ENV_CHECK=1` 跳过检查。

  缺少镜像或自己改了镜像输入文件时，自行构建（自己的分支用私有标签，测试时把标签传给脚本，用完删除；共享标签不要改、不要删）：

  ```bash
  scripts/build-system-test-image.sh anas-systemd:trixie-<你>-<主题> <本地任一 Debian 镜像>
  scripts/system-test.sh anas-systemd:trixie-<你>-<主题>
  ```

  镜像用 debootstrap 从 `deb.debian.org` 组装，不需要 Docker Hub。`ANAS_SYSTEM_TEST_ALLOW_ENV_MISMATCH=1` 可把不一致降级为警告（不推荐）。
- 固定版本的 Qdrant 与 llama.cpp 压缩包：在副本中运行 `scripts/fetch-qdrant.sh` 与 `scripts/fetch-llama.sh`。测试自己用 `scripts/fetch-upstream-packages.sh` 取固定版本的 Docker 与 Caddy 软件包（约 92 MB），放在 `build/upstream`，以后重复使用。
- 软件包 `build/packages`：`make system-test` 会先运行 `packages`，它构建二进制，需要 gcc（cgo SQLite）。外层脚本还用 Go 编译“下一 release”的 `anas-api`（`-tags rollbacktest`）。WSL 中没有 gcc 时，按 [Windows 环境的运行检查](../../../docs/development/environments/windows.md#运行检查)在 `anas-acl-matrix:bookworm` 中构建，再直接运行 `bash scripts/system-test.sh`。

## 步骤

在 Windows 开发机上，测试在 [check-in-wsl](../check-in-wsl/SKILL.md) 的 ext4 副本中运行最快：先在那里跑完 `make check-steps`，它已经构建好 Web 资源与二进制。测试本身需要几分钟，用后台任务运行，写法与 check-in-wsl 相同，只把 `make check-steps` 一行换成：

```bash
status=0; bash scripts/system-test.sh > "$dst.system-test.log" 2>&1 || status=$?
echo "PASS: $(grep -c '^PASS' "$dst.system-test.log" || true)"; grep '^FAIL' "$dst.system-test.log" || true; exit "$status"
```

在 Linux 机器或 CI 中直接运行 `make system-test`。

## 变体

- 真实内容：`ANAS_SYSTEM_TEST_CONTENT` 指向发布的内容库（如 `/mnt/d/A-NAS-releases/content`，目录结构见 [`scripts/build-content-packages.sh`](../../../scripts/build-content-packages.sh)）时，装真实的模型与两个运行环境，SearXNG 也真实启动；否则用替身内容包。模型位置见 [AI 开发与评估](../../../docs/development/ai-development.md)。
- AI 助手：`ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME` 指向 `ai/assistant/runtime.json` 固定的 `runtime.tar.gz`（`ai/assistant-spike/run.sh build` 构建）时，会把它解到内容包的替身上，加上策略检查与一个对接脚本化模型的真实助手。剪辑中心的规划链路也在这里核对：给一个任务写进目录与歌的分析后，任务自己走到规划步骤，root 的规划发起方把一轮交给成员的规划进程，脚本化模型调四个剪辑工具交两个方案（第一次调用超过 2 分钟），任务越过规划步骤、对话留在成员的历史里、复盘写出技能、模型只拿到剪辑、记忆与技能工具（[规划会话](../../../docs/architecture/montage-planning-session.md#测试)）。

## 结果

- 脚本逐项打印 `PASS <名称>` 或 `FAIL <名称>`，有任何失败即以非零退出。在报告或 PR 中写明 PASS 的项数与每一条 FAIL。
- `FAIL` 下一行的 `ran:` 是展开参数后执行的命令，`test` 比较的实际值就在其中；`quiet`、`fails` 与 `denied` 等辅助函数隐藏的输出，失败时以 `|` 开头打印最后 20 行。有失败时，脚本最后打印失败的单元与日志中的警告，并把整个 journal、单元列表、apt 与各服务的日志放进检出中的 `build/system-test-logs/`。通过时，脚本把软件包的哈希写进 `build/.system-tested`，`make release` 据此只发布测过的软件包。CI 失败时这个目录作为 `system-test-logs` 制品上传。
- 测试被强行中断时，用 `docker ps -a --filter name=anas-system-test-` 找出残留的容器，只删除这些。
