---
name: check-on-shared-server
description: 在共享开发服务器上运行 A-NAS 的完整门禁（make check 的全部检查），也用于 race 测试、系统测试和只跑其中几项。从编码工具经 SSH 把要测的提交拷过去，脱离会话运行，等待、取回结果并清理。Go 代码、构建、CI 或跨模块契约变更后，或用户要求跑 make check 时默认用它；服务器连不上时改用 check-in-wsl。
---

# 在共享开发服务器上运行门禁

什么变更跑什么检查以 [AGENTS.md](../../../AGENTS.md#验证) 为准。机器情况、为什么这样做以及踩过的坑见[共享开发服务器](../../../docs/development/environments/shared-dev-server.md)；账号与连接见那一页的“申请账号”。服务器连不上时，用 [check-in-wsl](../check-in-wsl/SKILL.md) 在本机 WSL 跑。

## 前提

- `~/.ssh/config` 里有服务器的别名，`ssh <别名> true` 能直接连上，不问密码。
- 要测的改动已经提交：副本只含提交的内容。

## 步骤

从 Claude Code 的 Bash（Windows 上是 Git Bash）或 Codex 的 shell，在检出根目录执行。全程约 4 到 8 分钟，几个门禁同时跑时更久；第 2 步用后台任务运行，不要在前台等。

**1. 拷过去并启动。** 把 `host` 换成自己的别名：

```bash
host=shared-dev
rev=$(git rev-parse --short=12 HEAD)
dir=anas-check/gate-$rev
git archive --format=tar HEAD | ssh "$host" "rm -rf ~/$dir && mkdir -p ~/$dir/src && tar -C ~/$dir/src -xf -"
ssh "$host" "cat > ~/$dir/run.sh" <<'EOF'
#!/usr/bin/env bash
cd "$(dirname "$0")/src" || exit
. /etc/profile.d/anas-toolchain.sh
bash scripts/fetch-qdrant.sh > ../fetch.log 2>&1
ANAS_QDRANT_REQUIRED=1 ANAS_MONTAGE_PYTHON=python3 timeout 90m \
  anas-gate -k check-steps PIP_AUDIT="timeout 600 pip-audit" > ../gate.log 2>&1
echo "$?" > ../status
EOF
ssh "$host" "cd ~/$dir && { setsid nohup bash run.sh > /dev/null 2>&1 < /dev/null & echo \$! > pid; }"
```

- 提交解到 `src/`，运行脚本、进程号、退出码与日志放在它外面：`ops-check` 对副本里所有 `.sh` 跑 shellcheck，放在里面的运行脚本会让门禁失败（2026-11-26）。
- 写脚本与启动分成两次 SSH 调用：写在一起时，`cat > f && … &` 会把 `cat` 也放到后台，脚本传过去是空的。
- 启动命令报 255（连接被重置）时，先看 `ssh "$host" "cat ~/$dir/pid"` 与进程还在不在，不要直接再启动一遍。

**2. 等它结束（后台任务）。** 退出码文件出现或进程消失就返回；只有断线（ssh 退出码 255）才重连再等：

```bash
until ssh -o ServerAliveInterval=30 "$host" \
  "cd ~/$dir; while [ ! -f status ] && kill -0 \"\$(cat pid)\" 2>/dev/null; do sleep 20; done; exit 0"; do sleep 30; done
```

不要用 `pgrep -f` 判断任务是否还在：远端的 `bash -c` 自己的命令行里就带着那个名字，循环永远不会结束。

要中途停掉，杀整个进程组：`ssh "$host" "kill -- -\$(cat ~/$dir/pid)"`。`setsid` 让这个进程号同时是进程组号；只杀 `bash run.sh` 一个进程，make 与测试进程会成为孤儿留下来（2026-11-26 试过）。

**3. 取结果并清理：**

```bash
ssh "$host" "cd ~/$dir && echo exit=\$(cat status 2>/dev/null || echo cut-off) && tail -n 80 gate.log"
mkdir -p build && scp -q "$host:$dir/gate.log" "build/shared-gate-$rev.log"
ssh "$host" "rm -rf ~/$dir; ps -u \"\$USER\" -o pid,ppid,etime,args | grep -v -e sshd: -e 'ps -u' -e 'grep -v'"
```

- `exit=0` 是通过；`exit=124` 是 90 分钟超时；`cut-off` 是进程没写退出码就没了。
- 失败的是哪一步：在日志里找 `make[1]: ***` 开头的行。
- 最后一行列出自己在服务器上的进程。PPID 为 1 的 `qdrant` 是被打断的测试留下的孤儿，会一直占着一个门禁槽位，杀掉它。

## 变体

- **只跑其中几项**：把 `check-steps` 换成对应目标，例如 `anas-gate -k docs-check ops-check vet test`。纯文档变更只要 `docs-check`。
- **race 测试**：`anas-gate race-test`，和门禁一样占一个槽位。
- **系统测试**：`anas-gate system-test` 独占全部槽位，排队期间别人的门禁也排在它后面。先看 `cat /opt/anas/lock/anas-gate.holder` 有没有长时间的占用，再用 `anas-images` 看共享镜像 `anas-systemd:trixie` 是否跟得上当前检出（[system-test](../system-test/SKILL.md) 的前提）。共享镜像不要改、不要删。
- **长时间的评估、覆盖率或变异测试**：同样脱离会话运行，命令前加 `anas-batch`，并限制并行数。临时虚拟环境要自己装 NumPy 与 Pillow，见[共享开发服务器 · Python 环境](../../../docs/development/environments/shared-dev-server.md#python-环境)。

## 结果

- 在最终报告或 PR 中写明 `PASS` 或失败的目标、跑的是哪个提交，以及本机日志路径 `build/shared-gate-<提交>.log`。
- 失败时先读日志，修复当前变更引起的问题后重跑。与当前变更无关的失败如实报告，不要绕过。
- `ANAS_MONTAGE_PYTHON` 与 `ANAS_QDRANT_REQUIRED` 让缺依赖的测试失败而不是跳过。日志里仍有跳过的测试时，看原因：缺依赖跳过的不能算完整门禁（[测试与验证](../../../docs/development/testing.md)）。
