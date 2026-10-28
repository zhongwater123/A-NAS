# macOS 与 Colima 开发环境

更新时间：2026-10-27

开发机 C 的配置与操作步骤。公共检查要求见[测试与验证](../testing.md)。

| 项目 | 状态 |
|---|---|
| 系统 | macOS 27.0.1，Apple 芯片（arm64） |
| 检出 | `~/Desktop/A-NAS`，在 iCloud“桌面与文稿”的同步范围内，见[注意事项](#注意事项) |
| 本机工具 | Homebrew 安装的 Go 1.27.3、ShellCheck 0.11.0、bash 5.3、Colima 0.10.3 与 Docker CLI；Node.js 26.11.1、npm 11.20.0；Ruff 0.17.1 按[静态检查](../testing.md#静态检查)装在 `~/.local/opt/ruff` |
| `~/.anas-env` | 把 Ruff 与 `/opt/homebrew/bin` 放在 `PATH` 最前面：macOS 自带的 `/bin/bash` 是 3.2，`ops-check` 需要 bash 4 以上 |
| Linux 虚拟机 | Colima 配置 `anas`：vz、6 核、8 GiB，Ubuntu 24.04 arm64，只供 A-NAS 使用；本机的 `default` 配置留给其他项目 |
| 虚拟机工具链 | 装在虚拟机用户的 `~/.local/opt`：Go 1.27.3、Node.js 26.9.0（与 CI 相同）、ShellCheck 0.11.0、Ruff 0.17.1；gcc 13.3 来自 apt。由虚拟机的 `~/.anas-env` 加入 `PATH` |
| 虚拟机中的镜像 | `golang:1.27.3-trixie`、`debian:trixie`；系统测试镜像尚未构建 |
| 模型 | `~/A-NAS-models`，目录结构与 `deploy-dev.ps1` 的默认目录相同（`embeddinggemma-2-740m`、`faces/yunet`、`faces/auraface`、`pets/dinov2-small`、`dfine-obj365`），另有 `faces/sface`；文件按 `deploy/models/*.json` 中的地址下载，并核对 SHA-256。D-FINE 只有官方权重 `dfine_m_obj365.pth`：在 arm64 上按清单导出的 ONNX 大小相同但 SHA-256 不同，Worker 与暂存都会拒绝，放在 `dfine-obj365/arm64-export`，只供本机评估；`dfine-obj365/official` 还需从已有清单所列文件的开发机复制 |
| 网络 | Docker Hub 时通时断（`registry-1.docker.io` 有时被解析到错误地址），拉取失败时重试；`proxy.golang.org`、`vuln.go.dev`、npm、PyPI、GitHub 与 `deb.debian.org` 可达 |

## 为什么用虚拟机

产品的 Go 代码只面向 Linux（例如 `internal/shareservice` 带 `//go:build linux`），所以在 macOS 上 `go vet` 和 `anas-api` 的构建都会失败。本机可以运行 `api-check`、`web-*`、`fmt-check`、`docs-check`、`ops-check`、`ai-lint` 与 `ai-test`；Linux 检查和 Fake 预览在 `anas` 虚拟机中运行；完整门禁还需要可运行的 Qdrant，限制见[运行检查](#运行检查)。与 [check-in-wsl](../../../.claude/skills/check-in-wsl/SKILL.md) 一样，先把检出复制到虚拟机自己的 ext4 目录再运行，因为本机的 `node_modules` 装的是 darwin 平台的二进制，不能共用。

## 配置

只需创建一次虚拟机：

```bash
colima start anas --cpu 6 --memory 8 --disk 60 --vm-type vz --mount-type virtiofs
docker context use colima    # colima start 会把 Docker 上下文切到 colima-anas；本机其他项目要用 default 时切回
```

虚拟机中的工具链：

- 用 apt 安装 `build-essential`、`jq`、`rsync` 与 `python3-venv`。
- Go 与 Node.js 从官方地址下载 linux-arm64 包，按官方公布的 SHA-256 核对。
- ShellCheck 用 GitHub 发布的 `shellcheck-v0.11.0.linux.aarch64.tar.xz`，SHA-256 为 `12b331c1d2db6b9eb13cfca64306b1b157a86eb69db83023e261eaa7e7c14588`。
- Ruff、pip-audit 与 uv 按[静态检查](../testing.md#静态检查)的命令安装：`make check` 的 `vuln-check` 需要 pip-audit。

镜像拉不下来时，可以从已有该镜像的虚拟机复制：`docker --context <来源> save <镜像> | docker --context colima-anas load`。

## 运行检查

当前清单固定的是 Linux x86_64 Qdrant，不能直接作为这台 ARM64 虚拟机的原生二进制运行。这里尚无经过验证的兼容运行方案；完整门禁可交给 Windows/WSL 开发机或 CI 执行。Qdrant 的版本、路径和强制检查规则见[测试依赖](../testing.md#qdrant-测试依赖)。

下面的完整检查入口会强制要求 Qdrant，缺少时失败。只有在虚拟机中准备好同版本、确认能够运行的 Qdrant，并在 `~/.anas-env` 中导出绝对路径 `ANAS_QDRANT_BINARY` 后，才能用它验证全部目标。不要通过取消 `ANAS_QDRANT_REQUIRED` 来报告完整门禁通过。

在检出根目录运行；再次运行只同步改动：

```bash
colima ssh -p anas -- bash -s -- "$PWD" <<'EOF'
set -euo pipefail
. ~/.anas-env
src=$1 dst=~/anas-check/$(basename "$1")
mkdir -p "$dst"
rsync -a --delete --exclude .git --exclude web/node_modules --exclude tools/openapi/node_modules --exclude build \
  --include internal/webui/dist/.keep --exclude 'internal/webui/dist/*' "$src"/ "$dst"/
cd "$dst"
export ANAS_QDRANT_REQUIRED=1
if make check-steps > "$dst.log" 2>&1; then echo "PASS (log: $dst.log)"; else tail -n 80 "$dst.log"; echo "FAIL (log: $dst.log)"; exit 1; fi
EOF
```

尚未准备好 Qdrant 时，可将上面的 `make check-steps` 换成 `make docs-check` 等独立目标；记录实际执行的目标，不将部分检查记为完整通过。

## Fake 预览

与 [fake-preview](../../../.claude/skills/fake-preview/SKILL.md) 相同，只是构建和运行都在虚拟机中。Colima 会把虚拟机里监听 `127.0.0.1` 的端口转发到本机，所以本机浏览器可以直接打开 `http://<名称>.localhost:<端口>`：

```bash
colima ssh -p anas -- bash -s -- "$PWD" <名称> <端口> <<'EOF'
set -euo pipefail
. ~/.anas-env
src=$1 dst=~/anas-check/$(basename "$1") state=~/anas-preview/$2
mkdir -p "$dst" "$state"
rsync -a --delete --exclude .git --exclude web/node_modules --exclude tools/openapi/node_modules --exclude build \
  --include internal/webui/dist/.keep --exclude 'internal/webui/dist/*' "$src"/ "$dst"/
cd "$dst"
make build-binaries > "$state/build.log" 2>&1
cp build/anas-api "$state/"
cd "$state"
ANAS_HOSTSTATE_MODE=fake ANAS_HTTP_ADDR=127.0.0.1:$3 ANAS_STATE_DIR=$state setsid nohup ./anas-api > api.log 2>&1 < /dev/null &
echo $! > pid
EOF
```

改动代码后先停止原预览，再运行构建命令；构建失败立即退出，查看 `~/anas-preview/<名称>/build.log`。检查和预览共用副本，勿同时运行。

首次启用管理员时，要在虚拟机里请求回环地址；使用 12–64 个字符的测试密码：

```bash
colima ssh -p anas -- bash -s <<'EOF'
curl -fsS -X POST -H 'Content-Type: application/json' \
  -d '{"username":"<账号>","password":"<测试密码>"}' http://127.0.0.1:<端口>/api/v1/setup/admin
EOF
```

停止预览：`colima ssh -p anas -- bash -c 'kill "$(cat ~/anas-preview/<名称>/pid)"'`。

## 注意事项

- iCloud 同步的目录中，iCloud 可能生成“空格加数字”的冲突副本（如 `index 3.html`），也会把不常用的文件移到云端、本地只留占位，rsync 读到占位文件时报 `Resource deadlock avoided`。因此上面的 rsync 跳过 `internal/webui/dist` 的构建产物（副本中会重新构建）。新的检出、模型与数据集都应放在不同步的目录。
- 虚拟机与 Docker 镜像只供 A-NAS 使用；`colima stop anas` 释放它占用的内存。

## 关联

- [开发环境入口](../LOCAL_ENVIRONMENT.md)
- [Fake 预览的会话隔离与功能限制](../../../.claude/skills/fake-preview/SKILL.md)
- [构建目标](../../../Makefile)
