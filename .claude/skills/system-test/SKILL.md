---
name: system-test
description: 运行 A-NAS 的 systemd 系统测试（scripts/system-test.sh）。它在以 systemd 为 PID 1 的一次性 Debian 13 容器中，用真实安装器、unit 与发布二进制安装整套服务，再按用户检查 Web 文件、SMB、相册、FFmpeg Worker、终端、局域网入口与 AI 助手沙箱的权限边界。修改服务单元、安装器、Host Agent、文件代理、文件权限或助手沙箱后，以及部署前使用。
---

# 运行 systemd 系统测试

系统测试覆盖的内容与原因见[测试与验证 · 系统测试](../../../docs/development/testing.md#系统测试)，入口在 [`scripts/system-test.sh`](../../../scripts/system-test.sh) 与 Makefile 的 `system-test` 目标。CI 的 `System test (Debian 13, systemd)` 作业对每次 push 与 PR 运行同一流程；以下情况才需要在本机运行：推送前就要结果、调试失败，或部署前用真实模型复测。

## 前提

- WSL 中的 Docker；测试只启动并删除自己命名为 `anas-system-test-<PID>` 的容器。本机还运行着与 A-NAS 无关的容器，不要清理它们。
- 镜像 `anas-systemd:trixie`。缺少时，或比构建脚本旧时，重新构建。比较两个时间：`docker image inspect -f '{{.Created}}' anas-systemd:trixie`，以及在检出中运行 `git log -1 --format=%cI -- scripts/build-system-test-image.sh`。旧镜像会让后来加进脚本的检查失败，例如助手沙箱报 `rg: not found`。构建命令：

  ```bash
  scripts/build-system-test-image.sh anas-systemd:trixie <本地任一 Debian 镜像>
  ```

  镜像用 debootstrap 从 `deb.debian.org` 组装，不需要 Docker Hub。
- 固定版本的 Qdrant 与 llama.cpp 压缩包：在副本中运行 `scripts/fetch-qdrant.sh` 与 `scripts/fetch-llama.sh`。
- 发布二进制 `build/anas-api` 与 `build/anas-host-agent`。`make system-test` 会先运行 `build-binaries`，它需要 gcc（cgo SQLite）。WSL 中没有 gcc 时，按 [Windows 环境的运行检查](../../../docs/development/environments/windows.md#运行检查)在 `anas-acl-matrix:bookworm` 中构建，再直接运行 `bash scripts/system-test.sh`。

## 步骤

在 Windows 开发机上，测试在 [check-in-wsl](../check-in-wsl/SKILL.md) 的 ext4 副本中运行最快：先在那里跑完 `make check-steps`，它已经构建好 Web 资源与二进制。测试本身需要几分钟，用后台任务运行，写法与 check-in-wsl 相同，只把 `make check-steps` 一行换成：

```bash
status=0; bash scripts/system-test.sh > "$dst.system-test.log" 2>&1 || status=$?
echo "PASS: $(grep -c '^PASS' "$dst.system-test.log" || true)"; grep '^FAIL' "$dst.system-test.log" || true; exit "$status"
```

在 Linux 机器或 CI 中直接运行 `make system-test`。

## 变体

- 改名前的 FFmpeg Worker：将上一版本的 Linux `anas-api` 构建产物放在当前测试副本的 `build/legacy-anas-api`，测试会以旧 `media-worker` 子命令、旧单元与旧套接字验证回滚，再用真实安装器升级并验证音乐解码。没有该文件时，仍测试旧单元布局与切换清理，但解码器用本次构建；输出会明确区分。只复制可确认来源的本地构建产物，不提交二进制。
- 真实模型：先运行 `scripts/build-ai-runtime.sh`，再设 `ANAS_SYSTEM_TEST_AI=real`，并用 `ANAS_AI_MODEL`、`ANAS_AI_DETECTOR`、`ANAS_AI_FACES`、`ANAS_AI_PETS` 与 `ANAS_AI_OCR` 指向模型。这些变量的含义见脚本开头的注释，模型位置见 [AI 开发与评估](../../../docs/development/ai-development.md)。
- AI 助手：`ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME` 指向 `ai/assistant-spike` 构建的 `runtime.tar.gz` 时，会加上策略检查与一个对接脚本化模型的真实助手。

## 结果

- 脚本逐项打印 `PASS <名称>` 或 `FAIL <名称>`，有任何失败即以非零退出。在报告或 PR 中写明 PASS 的项数与每一条 FAIL。
- 测试被强行中断时，用 `docker ps -a --filter name=anas-system-test-` 找出残留的容器，只删除这些。
