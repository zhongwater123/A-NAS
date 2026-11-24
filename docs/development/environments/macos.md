# macOS 与 Colima 开发环境

更新时间：2026-11-19

开发机 C 的配置与操作步骤。公共检查要求见[测试与验证](../testing.md)。

| 项目 | 状态 |
|---|---|
| 系统 | macOS 27.0.1，Apple 芯片（arm64） |
| 检出 | `~/code/A-NAS`，不在 iCloud 的同步范围内，见[注意事项](#注意事项) |
| 本机工具 | Homebrew 安装的 Go 1.27.3、ShellCheck 0.11.0、bash 5.3、Colima 0.10.3 与 Docker CLI；Node.js 26.11.1、npm 11.20.0；Ruff 0.17.1 按[静态检查](../testing.md#静态检查)装在 `~/.local/opt/ruff` |
| `~/.anas-env` | 把 Ruff 与 `/opt/homebrew/bin` 放在 `PATH` 最前面：macOS 自带的 `/bin/bash` 是 3.2，`ops-check` 需要 bash 4 以上 |
| Linux 虚拟机 | Colima 配置 `anas`：vz、6 核、8 GiB，Ubuntu 24.04 arm64，只供 A-NAS 使用；本机的 `default` 配置留给其他项目 |
| 虚拟机工具链 | 装在虚拟机用户的 `~/.local/opt`：Go 1.27.3、Node.js 26.9.0（与 CI 相同）、ShellCheck 0.11.0、lychee 0.24.2（见[静态检查](../testing.md#静态检查)）、Ruff 0.17.1，zizmor 1.32.0 与 pip-audit 2.10.1 在 venv `~/.local/opt/zizmor` 中；gcc 13.3、`file` 与 qemu-user-static 来自 apt。由虚拟机的 `~/.anas-env` 加入 `PATH` |
| 虚拟机中的镜像 | `golang:1.27.3-trixie`、`debian:trixie`、系统测试镜像 `anas-systemd:trixie` |
| 模型 | `~/A-NAS-models`，目录结构与 `scripts/fetch-models.sh` 的 `layout` 相同（`embeddinggemma-2-740m`、`faces/yunet`、`faces/auraface`、`pets/dinov2-small`、`dfine-obj365`），另有 `faces/sface`；文件按 `deploy/models/*.json` 中的地址下载，并核对 SHA-256。D-FINE 只有官方权重 `dfine_m_obj365.pth`：在 arm64 上按清单导出的 ONNX 大小相同但 SHA-256 不同，Worker 与打包都会拒绝，放在 `dfine-obj365/arm64-export`，只供本机评估；`dfine-obj365/official` 用 `scripts/fetch-models.sh ~/A-NAS-models` 拉取（[拉取模型](../ai-development.md#拉取模型)） |
| 测试照片 | `~/A-NAS-datasets/shared-gallery/<月份>/`：实验 NAS 相册共享图库的原图（JPEG、PNG 与一段 MP4），每月附 NAS 记录的清单 `manifest-*.json`，`SHA256SUMS` 为本机计算值。相册没有打包下载，按月经 Web 的相册接口取原图 |
| 测试视频 | `~/A-NAS-datasets/videos/`：12 段可署名使用的视频，共 8.5 GiB。11 段是 Wikimedia Commons 上 CC BY / CC BY-SA 或公有领域的 WebM，SHA-1 与 Commons 公布的一致，其中两段是 4K、10 分钟以上；另有 Netflix Open Content 的 Meridian（4K 59.94p HDR PQ 的 MP4，CC BY 4.0），按其 S3 分片 ETag 核对。最长一段是 67 分钟的 1080p 爱丁堡街景；`CREDITS.md` 记录作者、许可与原始页面，展示时按许可署名 |
| 网络 | Docker Hub 时通时断（`registry-1.docker.io` 有时被解析到错误地址），拉取失败时重试；`proxy.golang.org`、`vuln.go.dev`、npm、PyPI、GitHub 与 `deb.debian.org` 可达 |

## 为什么用虚拟机

产品的 Go 代码只面向 Linux（例如 `internal/shareservice` 带 `//go:build linux`），所以在 macOS 上 `go vet` 和 `anas-api` 的构建都会失败。本机可以运行 `api-check`、`web-*`、`fmt-check`、`docs-check`、`ops-check`、`ai-lint` 与 `ai-test`；Linux 检查和 Fake 预览在 `anas` 虚拟机中运行；完整门禁还需要可运行的 Qdrant，限制见[运行检查](#运行检查)。与 [check-in-wsl](../../../.agents/skills/check-in-wsl/SKILL.md) 一样，先把检出复制到虚拟机自己的 ext4 目录再运行，因为本机的 `node_modules` 装的是 darwin 平台的二进制，不能共用。

## 配置

只需创建一次虚拟机：

```bash
colima start anas --cpu 6 --memory 8 --disk 60 --vm-type vz --mount-type virtiofs
```

启动后 Docker 上下文会切到 `colima-anas`。下面的 A-NAS 步骤用 `colima ssh -p anas` 指定虚拟机。处理其他项目时，若要切回本机原有的 Colima 默认实例，再运行 `docker context use colima`。

虚拟机中的工具链：

- 用 apt 安装 `build-essential`、`jq`、`rsync`、`python3-venv`、`file` 与 `qemu-user-static`。`file` 是[环境检查](../../../scripts/check-dev-env.sh)要求的工具；qemu-user-static 注册 x86_64 的 binfmt，见[运行检查](#运行检查)。
- Go 与 Node.js 从官方地址下载 linux-arm64 包，按官方公布的 SHA-256 核对。
- ShellCheck 用 GitHub 发布的 `shellcheck-v0.11.0.linux.aarch64.tar.xz`，SHA-256 为 `12b331c1d2db6b9eb13cfca64306b1b157a86eb69db83023e261eaa7e7c14588`。
- Ruff、pip-audit、zizmor 与 uv 按[静态检查](../testing.md#静态检查)的命令装进 `~/.local/opt` 下的 venv：`ops-check` 需要 zizmor，`vuln-check` 需要 pip-audit，版本都以 `ai/requirements-dev.txt` 为准。
- lychee 按[静态检查](../testing.md#静态检查)中虚拟机的那一句安装，按其中的 SHA-256 核对。
- 以上各目录（venv 的是其 `bin`）都写进虚拟机的 `~/.anas-env`，加入 `PATH`。在副本中运行 `bash scripts/check-dev-env.sh` 确认。

镜像拉不下来时，可以从已有该镜像的虚拟机复制：`docker --context <来源> save <镜像> | docker --context colima-anas load`。

## 运行检查

`deploy/qdrant/qdrant.json` 只固定了 Linux x86_64 的静态链接版。虚拟机装了 qemu-user-static，经 binfmt 以用户态模拟运行它，测试用的仍是清单所列的同一个二进制（2026-11-19 在虚拟机中：`qdrant --version` 输出 1.20.0，`internal/vectorindex` 的测试在 `ANAS_QDRANT_REQUIRED=1` 下通过）。下面的入口在副本中用 `scripts/fetch-qdrant.sh` 取得它，并像 CI 一样设 `ANAS_QDRANT_REQUIRED=1`，缺少时失败。Qdrant 的版本、路径和强制检查规则见[测试依赖](../testing.md#qdrant-测试依赖)；不要通过取消 `ANAS_QDRANT_REQUIRED` 来报告完整门禁通过。

虚拟机的 60 GiB 数据盘只挂在 `/var/lib/docker`；`~/anas-check` 所在的根分区约 19 GiB，其他会话的副本也在那里。空闲不够完整门禁时，环境检查会警告；用完的副本及时删除。

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
# The vector index tests run the pinned Qdrant, as in CI.
bash scripts/fetch-qdrant.sh >/dev/null
export ANAS_QDRANT_REQUIRED=1
if make -k check-steps > "$dst.log" 2>&1; then echo "PASS (log: $dst.log)"; else tail -n 80 "$dst.log"; echo "FAIL (log: $dst.log)"; exit 1; fi
EOF
```

`-k` 让一次运行报出全部失败的步骤。只跑部分检查时，换成 `make docs-check` 等独立目标；记录实际执行的目标，不将部分检查记为完整通过。

## Fake 预览

先按[配置](#配置)启动 `anas` 虚拟机并准备工具。在 Mac 终端进入检出根目录。下面用会话名 `anas-preview` 和端口 `18080` 演示；同时运行多个预览时，每个会话使用自己的名称和空闲端口，并在下列命令和地址中同步替换。构建和服务都在虚拟机中运行。

```bash
colima ssh -p anas -- bash -s -- "$PWD" anas-preview 18080 <<'EOF'
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

首次创建管理员时，在 Mac 终端运行下段命令，由虚拟机请求回环地址。把引号内的账号和密码替换为自己的测试值，密码长度为 12–64 个字符：

```bash
colima ssh -p anas -- bash -s <<'EOF'
curl -fsS -X POST -H 'Content-Type: application/json' \
  -d '{"username":"<账号>","password":"<测试密码>"}' http://127.0.0.1:18080/api/v1/setup/admin
EOF
```

Colima 把端口转发到 Mac。在本机浏览器打开 `http://anas-preview.localhost:18080`，用刚创建的测试账号登录，看到桌面即预览成功。

停止这个预览：`colima ssh -p anas -- bash -c 'kill "$(cat ~/anas-preview/anas-preview/pid)"'`。

## 注意事项

- 检出、模型与数据集不要放在 iCloud 同步的目录（如桌面、文稿）。切换分支等短时间内的大量改写会让 iCloud 生成“空格加数字”的冲突副本（如 `config 2.go`），Go 会把这些 `.go` 副本一起编译，报重复定义；iCloud 还会把不常用的文件移到云端、本地只留占位，rsync 读到时报 `Resource deadlock avoided`。旧检出 `~/Desktop/A-NAS` 仍在同步范围内，不再使用。上面的 rsync 跳过 `internal/webui/dist` 的构建产物（副本中会重新构建）。
- 虚拟机与 Docker 镜像只供 A-NAS 使用；`colima stop anas` 释放它占用的内存。

## 关联

- [开发环境入口](../LOCAL_ENVIRONMENT.md)
- [环境检查脚本](../../../scripts/check-dev-env.sh)
- [Fake 预览的会话隔离与功能限制](../../../.agents/skills/fake-preview/SKILL.md)
- [构建目标](../../../Makefile)
