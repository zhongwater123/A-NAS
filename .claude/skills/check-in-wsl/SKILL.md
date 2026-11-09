---
name: check-in-wsl
description: 在 Windows 开发机上运行 A-NAS 的完整门禁（make check 的全部检查）。Go、Node 与 gcc 只装在 WSL 中，而在 /mnt 下的 Windows 盘上跑时 vitest worker 会超时，所以先把检出复制到 WSL 自己的 ext4 目录再运行。Go 代码、构建、CI 或跨模块契约变更后，或用户要求跑 make check 时使用。
---

# 在 WSL 的 ext4 副本中运行门禁

什么变更跑什么检查以 [AGENTS.md](../../../AGENTS.md#验证) 为准；工具链与两台开发机的差异见[Windows 与 WSL 环境](../../../docs/development/environments/windows.md)。

## 为什么用副本

- 开发机的 Go、Node、jq 与 ShellCheck 只在 WSL 中；开发机 B 由 `. ~/.anas-env` 加入 `PATH`。
- 在 `/mnt/<盘符>` 上运行时 vitest worker 启动超时，复制到 WSL 的 ext4 目录后正常。复制时排除 `.git`、`web/node_modules` 与 `build`。
- 副本没有 `.git`，所以运行 `make check-steps`。它就是 `make check` 的全部检查；`make check` 之后还要用 `git rev-parse` 写部署用的 `build/.validated-build`，只在部署前需要，见[测试与验证](../../../docs/development/testing.md)和[暂存注意事项](../../../docs/runbooks/provision-v1.0.1-experimental-storage.md#开发机暂存注意事项)。

## 一次只跑一个门禁

同一台开发机上，各会话的门禁排队运行，用 WSL 中的锁文件 `~/anas-check/.gate.lock`：

- 为什么：开发机 B 是低功耗笔记本处理器，WSL 最多约 7.7 GB 内存（[硬件](../../../docs/development/environments/windows.md#开发机-bda-nas)）。2026-11-09 三个会话的门禁同时跑时负载到 30 左右：Web 测试一轮要四十多分钟，用例因超时失败，WSL 一度无响应。排队跑，每个都更早结束。
- 下面的脚本先把检出同步到副本，再等锁，所以排队期间检出的改动不会混进这一轮。等待时打印当前持有者，`~/anas-check/.gate.holder` 记着它的副本名与开始时间。
- 持有锁的门禁限时 90 分钟，`pip-audit` 限时 10 分钟（它曾因 DNS 卡住 14 小时），一个卡住的门禁不会挡住后面的会话太久。进程结束，锁就释放，不用手动清理。
- [system-test](../system-test/SKILL.md) 沿用这段脚本，也在同一把锁下排队。单独跑的 `go test -race` 整包测试同样先取锁：把命令放在脚本里取锁那两行之后。
- 只跑一个 vitest 文件或一个 Go 包时，不用取锁。

## 步骤

全程超过 10 分钟，用后台任务运行，不要在前台等。从 Claude Code 的 Bash（Windows 上是 Git Bash）在检出根目录执行。整段脚本经标准输入交给 WSL，这样不会经过 PowerShell，引号也不会丢：

```bash
MSYS_NO_PATHCONV=1 wsl.exe -e bash -s -- "$(cygpath -w "$PWD")" <<'EOF'
set -euo pipefail
if [ -f ~/.anas-env ]; then . ~/.anas-env; fi
src=$(wslpath -a "$1")
dst=~/anas-check/$(basename "$src")
mkdir -p "$dst"
rsync -a --delete --exclude .git --exclude web/node_modules --exclude build "$src"/ "$dst"/
cd "$dst"
# One gate at a time on this machine; the lock goes with this process.
exec 9>~/anas-check/.gate.lock
if ! flock -n 9; then echo "waiting for the gate of $(cat ~/anas-check/.gate.holder 2>/dev/null)"; flock 9; fi
echo "$(basename "$dst") since $(date +%T)" > ~/anas-check/.gate.holder
# The vector index tests run the pinned Qdrant (docs/development/testing.md).
bash scripts/fetch-qdrant.sh >/dev/null
export ANAS_QDRANT_REQUIRED=1
if timeout 90m make check-steps PIP_AUDIT="timeout 600 pip-audit" > "$dst.log" 2>&1; then echo "PASS (log: $dst.log)"; else tail -n 80 "$dst.log"; echo "FAIL (log: $dst.log)"; exit 1; fi
EOF
```

- 每个检出有自己的副本，副本名取检出目录名。再次运行时，rsync 只同步改动，`web/node_modules` 留在副本中；锁文件没变时 `make web-install` 不会重新安装。
- 默认发行版不是开发用的发行版时，在 `wsl.exe` 后加 `-d <发行版>`。开发机 A 的发行版与账号见上述 Windows 与 WSL 环境。
- 只想跑其中几项时，把 `check-steps` 换成对应目标，例如 `make docs-check ops-check vet test`。

## 结果

- 在最终报告或 PR 中写明 `PASS` 或失败的目标，并给出日志路径，例如 `~/anas-check/<检出名>.log`。
- 失败时先读日志尾部，修复当前变更引起的问题后重跑同一命令。与当前变更无关的失败如实报告，不要绕过。
- Web 测试每个用例最多 20 秒、`findBy…` 与 `waitFor` 最多等 5 秒（[`vite.config.ts`](../../../web/vite.config.ts)、[`setup.ts`](../../../web/src/test/setup.ts)），给与其他会话共用 CPU 的开发机留出余量，不要在单个用例或命令行上另设。本机负载极高时仍有个别用例超时或 `findBy…` 找不到元素：先在副本的 `web/` 中运行 `npx vitest run <失败的文件>` 单独重跑；通过的话，在报告中写明是负载导致的超时，再运行 `web-test` 之后的目标：`make web-build fmt-check docs-check ops-check vet go-lint vuln-check test ai-lint ai-test build-binaries`。负载不高时单独重跑仍失败，就不是等待时间的问题，按缺陷调查，不要再放宽上限。
