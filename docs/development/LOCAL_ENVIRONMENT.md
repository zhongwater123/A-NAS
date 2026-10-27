# 本地开发环境

更新时间：2026-10-23

项目有两台 Windows 开发机。[检出换行](#windows-检出换行)、[Root 集成测试](#root-集成测试)、[系统测试](#系统测试)和[实验 NAS 接入](#实验-nas-接入)适用于两台；两台的差异分列在各自的小节。

## Windows 检出换行

仓库依赖 LF（`gofmt` 检查会拒绝 CRLF）。在 Windows 全局启用 `core.autocrlf=true` 的机器上克隆后，先在仓库内覆盖再重新检出：

```bash
git config core.autocrlf false
git config core.eol lf
```

`.ps1` 等脚本仍由 `.gitattributes` 保持 CRLF。

## 开发机 A：`E:\A-NAS`

| 项目 | 状态 |
|---|---|
| Windows 工作区 | `E:\A-NAS` |
| WSL | WSL2，Ubuntu 24.04 LTS，systemd 已启用 |
| WSL 开发用户 | `anas-dev`，独立 home：`/home/anas-dev` |
| WSL 工作区入口 | `/home/anas-dev/workspace/A-NAS` → `/mnt/e/A-NAS` |
| Git | 仓库默认分支为 `main`，WSL 使用 LF |
| 通用工具 | Git、C/C++ 构建工具、CMake、Ninja、Python、jq、ripgrep、ShellCheck、SQLite、Ansible |
| 产品工具链 | Go 1.27.1；Node.js 26.9.0；npm 11.19.1 |

`scripts/deploy-dev.ps1` 的默认参数（WSL `Ubuntu-24.04`、用户 `anas-dev`、`~/workspace/A-NAS`、密钥 `a-nas-dev_ed25519`）按这台机器设置。

### 隔离边界

- `anas-dev` 不复用现有的 `docker-dev` home、Git 配置、SSH 密钥或语言缓存。
- `anas-dev` 不属于 `sudo` 或 `docker` 组，日常开发默认非特权运行。
- Windows 已为实验 NAS 创建项目专用 ED25519 密钥，默认路径为 `%USERPROFILE%\.ssh\a-nas-dev_ed25519`；私钥内容和口令不进入仓库。
- 容器运行时已按 [ADR 0009](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md) 选定 Docker Engine；`anas-dev` 仍不加入 `docker` 组。本地联调可在具备 Docker 权限的账号下以 `ANAS_CONTAINER_AGENT_SOCKET=/tmp/...` 运行 `build/anas-container-agent`，再以 `ANAS_CONTAINERS_MODE=agent` 启动产品服务；只对专用测试容器执行启停。
- Git 用户名和邮箱需要由开发者在 `anas-dev` 下自行设置，仓库不记录个人身份。
- Go 安装在 `/opt/go/1.27.1`，`anas-dev` 使用自己的模块、构建和工具缓存。产品要求的版本由 `.go-version` 与 `go.mod` 的 `toolchain` 行固定；本机 Go 较旧时，`go` 命令按默认的 `GOTOOLCHAIN=auto` 从 `proxy.golang.org` 下载该版本。
- 前端依赖由 `web/package-lock.json` 固定；不要同时从 Windows 与 WSL 对同一个 `web/node_modules` 执行安装。

### 日常使用

从 Windows 进入专用开发环境：

```powershell
wsl -d Ubuntu-24.04
```

进入项目并检查环境：

```bash
cd ~/workspace/A-NAS
bash scripts/check-dev-env.sh
```

需要安装或维护 WSL 系统包时，显式使用 root，而不是提升日常开发账号：

```powershell
wsl -d Ubuntu-24.04 -u root
```

## 开发机 B：`D:\A-NAS`

| 项目 | 状态 |
|---|---|
| Windows | Windows 11 专业版（build 26200）；源码 `D:\A-NAS`，Claude Code 的工作树位于 `D:\A-NAS\.claude\worktrees\` |
| WSL | WSL2，发行版名 `Ubuntu`（Ubuntu 26.04 LTS），systemd 已启用 |
| WSL 用户 | 日常用户，属于 `docker` 组，没有免密 `sudo`；没有 `anas-dev` |
| WSL 工作区入口 | 直接使用 `/mnt/d/A-NAS` 或对应工作树 |
| Git | Git for Windows；仓库内已设置 `core.autocrlf=false`、`core.eol=lf` |
| 产品工具链 | Go 1.27.1、Node.js 26.9.0、npm 11.19.1、ShellCheck 0.11.0、jq 与 Ruff（见[静态检查](#静态检查)），均以用户身份装在 `~/.local/opt`，`. ~/.anas-env` 把它们加入 `PATH`；`go.mod` 要求更新的 Go 时，`go` 命令自动下载；gcc 15.2 来自 Ubuntu 软件包（`/usr/bin/gcc`），cgo 可直接在 WSL 中编译 |
| Docker | WSL 内 Docker Engine 29.1.3，cgroup v2；Docker Hub 不可达，`deb.debian.org` 与 `proxy.golang.org` 可达 |
| 本地镜像 | `anas-acl-matrix:bookworm`：Debian 12，加装 gcc、acl、btrfs-progs、samba、samba-vfs-modules 与 smbclient，用于 root 集成测试，也可在 Debian 12 中复现 cgo 测试；`anas-systemd:trixie`：由 `scripts/build-system-test-image.sh` 以前者为构建器生成，用于系统测试 |

### 运行检查

文档与运维检查可以直接在 `/mnt/d` 上运行：

```bash
. ~/.anas-env
cd /mnt/d/A-NAS
make docs-check ops-check
```

完整门禁按 [check-in-wsl](../../.claude/skills/check-in-wsl/SKILL.md) 在 WSL 的 ext4 副本中运行，依赖 cgo（SQLite）的 Go 测试与构建直接用 WSL 的 gcc。要在 Debian 12 中复现 cgo 测试时，在 `anas-acl-matrix:bookworm` 中以当前用户运行；以 root 运行会改变依赖权限检查的文件代理与文件测试。模块缓存先在 WSL 中用 `go mod download` 填好：

```bash
docker run --rm -u "$(id -u):$(id -g)" -v "$PWD:/src" -w /src \
  -v "$HOME/.local/opt/go:/usr/local/go:ro" -v "$HOME/go/pkg/mod:/gomod:ro" \
  -e GOMODCACHE=/gomod -e GOCACHE=/tmp/gocache -e HOME=/tmp -e GOPROXY=off -e GOTOOLCHAIN=local \
  -e GOFLAGS="-mod=readonly -buildvcs=false" -e CGO_ENABLED=1 \
  anas-acl-matrix:bookworm /usr/local/go/bin/go test ./...
```

向量索引的测试（`internal/vectorindex` 与相册的同步）运行 [`deploy/qdrant/qdrant.json`](../../deploy/qdrant/qdrant.json) 固定的 Qdrant 二进制。先在 WSL 中运行一次 `scripts/fetch-qdrant.sh`：它下载官方压缩包，核对大小与 SHA-256，把二进制留在 `build/qdrant/<sha256>/`。测试默认取这个位置，也可以用 `ANAS_QDRANT_BINARY` 指定。缺少二进制时这些测试跳过；设 `ANAS_QDRANT_REQUIRED=1` 时改为失败，CI 就是这样设的。ext4 副本不带 `build/`，复制后再把 `build/qdrant` 一并复制过去，或在副本中重新获取。

把最后的 `go test ./...` 换成 `go vet ./...` 或 `go build -o build/ ./cmd/anas-api ./cmd/anas-host-agent` 即得到对应检查。root 集成测试使用同一镜像，去掉 `-u`，加上 `--privileged -e ANAS_ROOT_INTEGRATION=1`，运行 `go test -tags rootintegration -count=1 ./internal/hostops/linux`。

系统测试按 [system-test](../../.claude/skills/system-test/SKILL.md) 在同一个 ext4 副本中运行；构建系统测试镜像时以 `anas-acl-matrix:bookworm` 为构建器：

```bash
scripts/build-system-test-image.sh anas-systemd:trixie anas-acl-matrix:bookworm
```

注意事项：

- 从 PowerShell 5.1 向 `wsl ... bash -c '...'` 传带引号的命令会丢失引号；多行命令先写成脚本，再运行 `wsl -d Ubuntu -- bash <脚本>`。
- 不要用 PowerShell 5.1 的 `Set-Content -Encoding utf8` 改仓库文件，它会写入 BOM。
- 在 `/mnt/d` 上运行 Vite 开发服务器时设置 `CHOKIDAR_USEPOLLING=1`，否则看不到 Windows 侧的修改。
- 本机 Docker 上还运行着与 A-NAS 无关的容器；测试脚本只启动并删除自己命名的容器，不要清理其他容器。

### 与实验 NAS 联调

- SSH 别名 `a-nas` 写在 `%USERPROFILE%\.ssh\config`，以 Debian 安装时创建的普通账号 `guoyi` 登录，密钥为 `%USERPROFILE%\.ssh\a-nas-guoyi_ed25519`（无口令，只授权给本机）。该账号不属于 `sudo`，只用于只读检查与联调；任何 root 操作仍由管理员按运行手册执行。
- SSH 别名 `a-nas-dev` 以 `anas-dev` 登录，密钥为 `%USERPROFILE%\.ssh\a-nas-dev_ed25519`（无口令，只授权给本机），用于暂存 release。
- `deploy-dev.ps1` 只有在 `build/.validated-build` 与提交、产品版本和两个二进制哈希都一致时才跳过 WSL 中的 `make check`。2026-10-09 暂存 `64f0e26d8349` 时本机还缺 gcc，所以按[运行检查](#运行检查)逐项运行 `check-steps` 的全部目标（cgo 部分在容器中，`go build` 加 `-trimpath -ldflags "-X main.version=<12 位提交>"`），全部通过后按 Makefile `check` 目标的格式写入该文件，再运行 `scripts/deploy-dev.ps1 -NasHost a-nas-dev -StageOnly -WslDistribution Ubuntu -WslUser <WSL 用户>`。同日 WSL 已有 gcc，但在 `/mnt/d` 上 vitest worker 启动超时：把工作树复制到 WSL 自身的 ext4 目录（排除 `.git`、`web/node_modules` 与 `build`），在那里 `npm ci` 后运行 `make check-steps VERSION=<版本>` 与 `bash scripts/system-test.sh`。
- 屏保视频不随 release 暂存。只有更换视频时，才用 `deploy-screensavers.ps1` 上传 NAS 还没有的文件，见[本地控制台运行手册](../runbooks/operate-local-kiosk.md#更换屏保视频)。系统安装器随 release 一起暂存，所以基于 [ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md) 之前主干的分支仍使用旧安装器，会在不带视频的升级后停播屏保；这类分支应先合入主干再部署。
- 在 PowerShell 5.1 中运行 `deploy-dev.ps1` 不要加 `2>&1`：Windows OpenSSH 的 `close - IO is still pending` 会变成错误记录，使脚本在 `ErrorActionPreference = 'Stop'` 下中止。
- 撤销本机访问：删除 NAS 上 `/home/guoyi/.ssh/authorized_keys` 中注释为 `claude-code@SKZ00011303->guoyi@a-nas` 的一行。

## Root 集成测试

账号、ACL 与 Samba 行为需要真实 root 工具验证。`internal/hostops/linux/root_integration_test.go` 使用 `rootintegration` 构建标签，只在 root 且 `ANAS_ROOT_INTEGRATION=1` 时运行，并会创建账号、组和 Samba 配置。只在可丢弃的特权 Debian 容器中运行，绝不在开发机或实验 NAS 上直接执行：

```bash
docker run --rm --privileged -e ANAS_ROOT_INTEGRATION=1 -e CGO_ENABLED=1 \
  -v "$PWD:/src" -w /src <装有 Go、gcc、acl、btrfs-progs、samba、smbclient 的 Debian 镜像> \
  make root-integration-test
```

权限矩阵会挂载 loop 设备上的 Btrfs，因此需要 `--privileged`。bookworm 镜像还需 `samba-vfs-modules`（trixie 的 `samba` 已自带 `recycle.so`）。

相册服务的端到端冒烟在同一类容器中进行：

- 运行真实 Host Agent（root）、以 `a-nas-photos` 运行的相册服务和以 `a-nas` 运行的产品服务。
- 经产品服务完成启用管理员、上传、缩略图和原图读取。
- 检查存储区与套接字的属主、权限和隔离。
- 注入数据卷故障：把卷建在 device-mapper 设备上，换成 `error` 目标使 Btrfs 转为只读，再懒卸载与重新挂载，并在上传中途强制终止相册服务，确认相册暂停服务、不写系统盘并在卷恢复后自行重新打开。

相册 AI Worker（`ai/`）的协议测试只需 Python 标准库：`make ai-test`。使用真实 EmbeddingGemma 2 与 D-FINE 检测器的测试需要 MediaPipe 与 ONNX Runtime，在 Debian 13 开发镜像中运行（镜像已按锁文件的版本装好 ONNX Runtime），模型从开发机预留位置只读挂载（见 [M2 实施方案](../architecture/photo-ai.md#模型与运行环境的交付)）；检测器是用官方权重导出的 `D:\A-NAS-models\dfine-obj365\official\dfine_m_obj365.onnx`（[研究](../research/photo-ai-object-detection.md)）：

```bash
docker build -t anas-ai-dev:trixie -f ai/dev.Dockerfile ai      # 基于 anas-systemd:trixie
docker run --rm --cpuset-cpus=0-3 -v "$PWD:/src:ro" -w /src/ai \
  -v /mnt/d/A-NAS-models/embeddinggemma-2-740m:/models:ro -v /mnt/d/A-NAS-models/dfine-obj365/official:/detector:ro \
  -e ANAS_AI_MODEL=/models/embeddinggemma-2-740m.litertlm -e ANAS_AI_DETECTOR=/detector/dfine_m_obj365.onnx \
  -e PYTHONDONTWRITEBYTECODE=1 anas-ai-dev:trixie /opt/anas-ai/bin/python -m unittest discover -s tests -t .
```

发行用的 Worker 运行环境（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）也在开发机上构建：`scripts/build-ai-runtime.sh` 在同一开发镜像中按 [`ai/requirements.lock`](../../ai/requirements.lock) 的哈希安装 wheel，打包为 `build/ai-runtime/<ID>.tar.gz`，并写出 `deploy-dev.ps1` 读取的 `build/ai-runtime.json`。同一 ID 的包已存在时直接复用，因此 NAS 上看到的哈希不变；锁文件改了版本后先运行 `scripts/build-ai-runtime.sh --lock` 更新哈希。两者都从 PyPI 下载，NAS 不需要。2026-10-10 的包 `ccd886271b067afa` 压缩后 163 MB，解开约 460 MB；2026-10-13 加入 ONNX Runtime 后为 `3eca142df9a765c2`，压缩后 189 MB，解开约 542 MB。

AI 的评估（`ai/eval/`：标签阈值校准、搜索取舍与分块向量）使用放在 `D:\A-NAS-datasets`（WSL 中为 `/mnt/d/A-NAS-datasets`）的公开数据集，只在开发机使用，不进入仓库、NAS 或发行包；数据集来源、许可与结论见[标签校准报告](../research/photo-ai-label-calibration.md)。COCO 与 Open Images 元数据按报告中的地址下载并解压后，依次逐张下载其余图片、计算向量、校准阈值：

```bash
AI="docker run --rm -v $PWD:/src -w /src/ai -v /mnt/d/A-NAS-datasets:/datasets \
  -v /mnt/d/A-NAS-models/embeddinggemma-2-740m:/models:ro -v $HOME/.cache/anas-ai-xnnpack:/xnnpack \
  -e PYTHONDONTWRITEBYTECODE=1 anas-ai-dev:trixie /opt/anas-ai/bin/python"
$AI -m eval.fetch --budget-gb 4                       # 先用 HEAD 计算体积，超出预算不下载
$AI -m eval.embed --model /models/embeddinggemma-2-740m.litertlm --weight-cache /xnnpack --tokens 70
$AI -m eval.calibrate --cache "/datasets/cache/<模型 ID>-e512" --report /datasets/report.json --write
```

`eval.embed` 可中断，重跑时只补算缺少的向量；权重缓存放在 WSL 自己的磁盘上，加载比放在 `/mnt/d` 快。`--write` 不带路径时更新 `internal/photos/labels/v1.calibration.json`；`--compare-with` 在两份缓存共有的图片上比较视觉 token 或输入尺寸。

AI 聚合的阈值按“宁缺毋滥”校准，场景标签的真值来自 COCO-Stuff：只取 `stuff_annotations_trainval2017.zip` 里的 `stuff_val2017.json`（22 MB），用 [remotezip](https://pypi.org/project/remotezip/) 按 HTTP Range 读出，不下载 1.1 GB 的整个压缩包，放在 `/mnt/d/A-NAS-datasets/coco-stuff/`：

```bash
$AI -m eval.calibrate --cache "/datasets/cache/<模型 ID>-e512" --min-precision 0.9 --min-label-precision 0.8 --min-recall 0.1 --write
```

校准集还可以加入 COCO 2017 train 的固定子集（2026-10-12 起）。验证集的 5,000 张让稀有标签和多数场景的照片太少，无法判断某组对不对；子集只用于校准，测试仍只用验证集的测试半。

[`eval.coco_train`](../../ai/eval/coco_train.py) 在开发机宿主上运行，需要 ijson 与 remotezip，不在评估镜像里。它按图片 ID 的哈希取 2 万张，把本机 `annotations_trainval2017.zip` 中的实例标注和远端 stuff 压缩包中的 `stuff_train2017.json` 以流式读取、压缩成每张图的类别与面积（约 11 MB），再逐张下载图片（约 3.1 GB）：

```bash
cd ai && ANAS_DATASETS=D:/A-NAS-datasets python -m eval.coco_train --count 20000
$AI -m eval.embed --model /models/embeddinggemma-2-740m.litertlm --weight-cache /xnnpack --tokens 70 --sets coco-train --no-texts
```

缓存里有子集的向量后，`eval.calibrate` 自动把它们并入校准半。v1 阈值还用 `--hold-back` 手动留下 8 组不启用（原因见[校准报告](../research/photo-ai-label-calibration.md#ai-聚合的严格校准)），写入的阈值文件记下这份名单：

```bash
$AI -m eval.calibrate --cache "/datasets/cache/<模型 ID>-e512" --min-precision 0.9 --min-label-precision 0.8 --min-recall 0.1 \
  --hold-back hot_dog,carrot,wine_glass,scissors,microwave,ball,bridge,sofa --report /datasets/report.json --write
```

分块向量（[M2 实施方案“AI 聚合”](../architecture/photo-ai.md#ai-聚合)）先为 COCO 测试半与 COCO-CN 测试集算每块的向量，再与整图向量比较；中途也可以运行 `eval.tile_search` 查看已算部分的结果：

```bash
$AI -m eval.embed --model /models/embeddinggemma-2-740m.litertlm --weight-cache /xnnpack --tokens 70 --tiles 2 --split test --sets coco,coco-cn
$AI -m eval.tile_search --cache "/datasets/cache/<模型 ID>-e512" --tiles "/datasets/cache/<模型 ID>-e512-tiles2"
```

物体检测与混合检索（[研究](../research/photo-ai-object-detection.md)）用另一个评估镜像：在 `anas-ai-dev:trixie` 上加装 ONNX Runtime（`FROM anas-ai-dev:trixie` 后 `RUN /opt/anas-ai/bin/pip install onnxruntime`，标记为 `anas-ai-dev-ort:trixie`）。D-FINE 的 ONNX 转换放在 `D:\A-NAS-models\dfine-obj365\dfine_m_obj365\`（`config.json`、`onnx/model.onnx` 与记下 revision 的 `hub.json`）。`eval.detect` 为 COCO、COCO-CN 测试集与 Open Images 缓存检测框，可中断续跑；`eval.hybrid_search` 把检测框与已缓存的照片向量组合比较，`--embedder` 第一次运行时编码中文类名，`--half calibration` 在 COCO 另一半上复核：

```bash
DET="docker run --rm -v $PWD:/src -w /src/ai -v /mnt/d/A-NAS-datasets:/datasets \
  -v /mnt/d/A-NAS-models/dfine-obj365:/models:ro -v /mnt/d/A-NAS-models/embeddinggemma-2-740m:/models-eg:ro \
  -v $HOME/.cache/anas-ai-xnnpack:/xnnpack -e PYTHONDONTWRITEBYTECODE=1 anas-ai-dev-ort:trixie /opt/anas-ai/bin/python"
$DET -m eval.detect --model /models/dfine_m_obj365 --threads 4 --sets coco,coco-cn,openimages
$DET -m eval.detect --model /models/dfine_m_obj365 --threads 4 --bench 30 --embedder /models-eg/embeddinggemma-2-740m.litertlm --weight-cache /xnnpack
$DET -m eval.hybrid_search --vectors "/datasets/cache/<模型 ID>-e512" --detections "/datasets/cache/<检测模型 ID>" \
  --embedder /models-eg/embeddinggemma-2-740m.litertlm --weight-cache /xnnpack --report /datasets/runs/detector/report.json
```

相册的规模基线（4 名成员、20,000 张，约 5 分钟）与 12 MP 缩略图计时默认跳过，在同一 cgo 容器中加 `-e ANAS_PHOTO_SCALE=1` 运行 `go test -count=1 -timeout 60m -run 'TestScale' -v ./internal/photos/`；结果的解读见[相册技术设计](../architecture/photo-library.md)切片 7。

device-mapper 与 loop 设备属于宿主内核而不是容器，脚本退出时会停止服务并删除自己的 `anas-smoke-<容器名>` 设备。若脚本被强行中断，用 `ls /dev/mapper` 检查残留，再在特权容器中执行 `dmsetup remove <名称>`。容器缺少 `curl` 或 `dmsetup` 时脚本会临时安装：

```bash
docker run --rm --privileged -e CGO_ENABLED=1 \
  -v "$PWD:/src" <同上镜像> bash /src/scripts/smoke-photo-service.sh
```

## 系统测试

root 集成测试与相册冒烟都由测试进程直接启动服务，覆盖不到正式 systemd unit 的沙箱。[ADR 0008 升级后 Web 文件无法使用](../investigations/2026-10-08-host-agent-loses-setuid-under-systemd.md)正是这类缺口。系统测试在以 systemd 为 PID 1 的 Debian 13 容器中，用真实安装器、unit 与发布二进制安装 A-NAS，再按用户检查：

- Host Agent 保留 `CAP_SETUID`/`CAP_SETGID` 并通过启动自检；
- Web 文件的读写、下载、删除与恢复以登录用户 UID 落盘，成员与管理员互相不可见；
- SMB 与 Web 互相可见对方写入的文件，使用同一组 ACL、同一个回收站和同一个密码；
- 禁用账号会断开它连着的 SMB 会话，之后产品服务启动时的身份同步照常成功。Host Agent 的沙箱让 Samba 命令在标准错误打印告警，root 集成测试碰不到这一点（[调查](../investigations/2026-10-26-smbstatus-warning-fails-identity-sync.md)）；
- 产品服务账号与相册服务进不了任何空间，相册上传在专用身份下工作；
- 安装器配置的 Caddy 局域网入口能转发登录与上传，经入口的登录拿不到本机长期会话；
- 活动记录为经入口的登录记下客户端地址、为本机屏幕登录记下来源；Host Agent 在正式单元的沙箱中以 `journalctl` 读到产品服务与 systemd 关于白名单单元的日志，成员读不到系统日志，白名单外的单元被拒绝（[ADR 0020](../adr/0020-read-service-logs-through-the-host-agent.md)）；
- 重启 Host Agent 后以上行为保持；
- 屏保视频的独立通道（[ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)）：暂存只上传缺少的视频并复用旧 release 暂存过的视频；安装后的池成为当前池，release 升级保留它；共有的视频只存一份；重复安装同一个池不产生变化；切回已安装的池不需要暂存区；视频哈希不符或池 ID 与清单不符时拒绝且不留改动；从旧安装器首次升级时以硬链接导入它留下的池；
- 本地 AI 随系统安装（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）：套接字只对相册服务的组开放，搜索经套接字按需启动 Worker，Worker 以动态分配的身份运行；单元的 CPU 上限是本机逻辑核数减 2（安装器写入的 drop-in），内存先回收后上限；上传的照片在 Worker 沙箱中得到向量与检测结果（相册服务的空闲期经 drop-in 缩短为 2 秒）；放在 Worker 自己单元中的探针打不开网络套接字、列不出数据卷，而同一探针在沙箱外可以；暂存的模型或检测器与清单不符时拒绝安装且不留改动；升级不重复复制模型、检测器与运行环境，并启动新 release 的 Worker。CI 使用替身模型与检测器、只含 Debian Python 的运行环境与 Worker 的假 Provider；
- 向量索引（[ADR 0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)）：
  - 套接字只属于相册服务的账号，产品服务的账号连不上；
  - 连接时拉起实例，实例与代理同在一个只有 `lo` 的网络命名空间，以每个实例自己的动态身份运行；
  - 代理空闲（经 drop-in 缩短为 3 秒）后实例停止；
  - 换了 Qdrant 二进制时存储被清空；
  - 放在实例单元中的探针看不到网络、读不到数据卷与产品状态；
  - 篡改的压缩包被拒绝；
  - 相册服务建好集合、同步照片，并记下搜索的影子比对。

  运行前需要 `scripts/fetch-qdrant.sh` 取得的压缩包。
- 最后卸载数据卷，相册服务在自己的挂载命名空间中察觉并停止服务。容器的挂载默认是 private，脚本先改为与 Debian 主机一致的 shared，否则宿主机的卸载传不进服务的命名空间。

```bash
scripts/build-system-test-image.sh anas-systemd:trixie <本地任一 Debian 镜像>
make system-test
```

- 镜像用 debootstrap 从 `deb.debian.org` 组装，因此不依赖 Docker Hub；镜像内 systemd 版本与实验 NAS 一致。
- 容器没有真实磁盘，脚本在 `lsblk` 边界把 loop 卷呈现为 SATA 数据盘，并写入与存储初始化相同的卷记录；`lsblk` 之上的 Host Agent、存储状态和卷校验均为正式代码。
- CI 的 `System test (Debian 13, systemd)` 作业运行同一流程。
- 部署前在开发机用真实运行环境与模型再跑一遍，同样的检查改由真实模型编码查询与检测照片，并打印 Worker 的内存峰值：

  ```bash
  scripts/build-ai-runtime.sh
  ANAS_SYSTEM_TEST_AI=real ANAS_AI_MODEL=/mnt/d/A-NAS-models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm \
    ANAS_AI_DETECTOR=/mnt/d/A-NAS-models/dfine-obj365/official/dfine_m_obj365.onnx bash scripts/system-test.sh anas-systemd:trixie
  ```

## 实验 NAS 接入

| 项目 | 当前状态 |
|---|---|
| 主机名 | `a-nas-dev` |
| 系统 | Debian 13 trixie amd64 |
| SSH | OpenSSH 已启用；开发机 A 以 `anas-dev`、开发机 B 以 `guoyi` 的密钥登录已验证 |
| 开发账号 | `anas-dev`（暂存制品）与 `guoyi`（只读联调），均无 `sudo` 权限 |
| 地址 | DHCP 地址，不写入仓库；每次使用前按需确认 |

首次配置、指纹核对、密钥轮换和恢复步骤见[实验 NAS SSH 运行手册](../runbooks/bootstrap-experimental-nas-ssh.md)。尚未关闭密码登录；完成恢复路径与第二把管理员密钥设计前不做 SSH 全局加固。

## Web 桌面

完整检查在依赖锁变化时执行 `npm ci`，随后运行类型检查、[静态检查](#静态检查)、React 测试和 Vite 构建：

```bash
make check
```

日常迭代先运行受影响的现有目标，例如 `go test ./internal/storage`、`make web-test` 或 `make ops-check`；RC 提交只运行一次完整 `make check VERSION=<RC>`。完整检查会把提交、产品版本和两个二进制哈希写入可丢弃的 `build/.validated-build`，`deploy-dev.ps1` 只有在四者全部一致时才复用结果，否则自动重跑完整门禁。这样不会用“跳过测试”换速度，也不会为同一制品重复执行全量测试。

完整检查中的 `make vuln-check` 用 `go.mod` 的 `tool` 指令固定的 govulncheck 扫描代码可达的已知漏洞，需要能访问 `vuln.go.dev`。它报告可达漏洞时检查失败：升级对应模块，或一起修改 `.go-version` 与 `go.mod` 的 `toolchain` 行来升级 Go。CI 每周一还在 `main` 上单独运行一次（`.github/workflows/vuln.yml`，也可手动触发），这样没有人推送时，新公布的漏洞也会报出；修改这个工作流的 PR 也会运行它，其他 PR 不运行，所以它不能设为必需检查。

只开发界面时，先在 `127.0.0.1:8080` 上启动 `ANAS_HOSTSTATE_MODE=fake` 的产品服务（见 [README](../../README.md)），再：

```bash
cd web
npm ci
npm run dev
```

生成资源位于被 Git 忽略的 `internal/webui/dist/`，随后由 Go `embed` 写入产品二进制；NAS 不需要 Node.js。

## 接口契约

产品 API 的契约是 OpenAPI 3.1，按领域分文件（#220）：

- [`api/openapi.yaml`](../../api/openapi.yaml) 是根，逐条以 `$ref` 列出每个路径与组件，按领域分组；
- `api/paths/<领域>.yaml` 写路径与操作，`api/components/<领域>.yaml` 写参数、响应与 schema，跨领域共用的错误响应、CSRF 头与两个公共类型在 `api/components/common.yaml`；
- 新增路径或组件时写进它所属领域的文件，再在根文件同一领域的分组中加一行引用。根文件列出组件，打包后的组件名就是根文件中的键。

改了 `api/` 下的任一文件，运行 `make api`（或在 `web/` 中 `npm run generate`），把它写出的三个文件一起提交：

| 生成文件 | 来源与用途 |
|---|---|
| `api/openapi.bundle.yaml` | Redocly CLI 打包的单个文档；Go 契约测试经 `internal/openapitest` 加载它（`api` 包嵌入） |
| `web/src/api.generated.ts` | openapi-typescript 从打包结果生成的路径、操作与 schema 类型，每个 schema 以自己的名字导出 |
| `web/src/api.validators.generated.ts` | Ajv 预编译的响应校验函数，只覆盖 `tools/openapi/validators.mjs` 列出的操作 |

`make check` 的第一步 `make api-check` 重新生成这三个文件，与已提交的不一致即失败。Go 契约测试读的是打包结果，改了契约先运行 `make api` 再跑 `go test`。两个分支都改了契约时，生成文件冲突不必手工合并：取任一边，解决源文件的冲突后重新运行 `make api`。

生成工具在 [`tools/openapi`](../../tools/openapi/package.json) 中有独立的 npm 包，与 `tools/golangci-lint` 同理：openapi-typescript 依赖 TypeScript 5 的编译器 API，而 Web 客户端用 TypeScript 7。Makefile 以 `REDOCLY_TELEMETRY=off` 运行 Redocly CLI，并关闭更新检查，生成过程不联网。

Web 客户端只经 [`api.ts`](../../web/src/api.ts) 的 `client`（openapi-fetch）调用产品 API，类型从 `api.generated.ts` 取，不再手写接口：

- `call()` 把结果展开为数据，失败时抛出带服务端错误码的 `APIError`；二进制与 multipart 请求体经 `rawBody()` 原样发送。
- CSRF 令牌由客户端按方法添加：除 GET 与 HEAD 外的请求都带 `X-CSRF-Token`，与服务端 `withMutation` 的划分一致。带进度的上传（XHR）与 tus 也从 `csrfHeader(method)` 取同一规则，调用方不再传参。
- 桌面外壳在窗口错误边界之外读取的响应（登录、状态栏、通知、头像、壁纸、应用快捷方式、VNAS、分享提醒、传输提示与问题反馈），由客户端中间件按契约校验，不符合的响应让这次调用以 `malformed_response` 失败，而不是让桌面空白（[10-07 的空白桌面](../investigations/2026-10-07-blank-disk-plan-ui-crash.md)）。校验只管形状（类型、必有字段、枚举与 `null`），格式与字符串长度留给服务端：服务端按字符数限制昵称，生成的校验只能按 UTF-16 单元计数。外壳新读取一个接口时，把操作加进 `validators.mjs` 的列表并运行 `make api`；这些响应在 Go 侧也要有契约测试，用 `openapitest.Check` 校验请求与响应（如 `internal/httpapi/shell_contract_test.go`）。

Web 测试用 [`test/api.ts`](../../web/src/test/api.ts) 的 `stubAPI` 替换 `fetch`：桩函数仍以路径与 init 看到每个请求，返回真实的 `Response`。共用的测试数据在 [`test/fixtures.ts`](../../web/src/test/fixtures.ts)，以生成的类型声明；漏掉服务端总会返回的字段时无法通过类型检查，外壳的响应在运行时也会被拒绝。

## 静态检查

`make check` 包含以下目标，也可单独运行：

| 目标 | 检查 |
|---|---|
| `make api-check` | 由接口契约重新生成打包文档、Web 类型与响应校验，与已提交的不一致即失败，见[接口契约](#接口契约) |
| `make web-lint` | `web/` 中的 `npm run lint`：Oxlint 与 eslint-plugin-react-hooks（含 React Compiler 的规则），再由 Biome 检查格式；规则列在 `web/.oxlintrc.json`，所有文件都须通过 |
| `make go-lint` | golangci-lint，版本由 `tools/golangci-lint/go.mod` 固定，首次运行经 `go tool` 从 `proxy.golang.org` 构建；规则与理由见 `.golangci.yml` |
| `make ai-lint` | Ruff 检查 `ai/`；`ai/requirements-dev.lock` 按哈希固定版本，`PATH` 上的 `ruff` 版本不同即失败。Ruff 在 WSL 中不检查可执行位（EXE001/EXE002），这两条只在 CI 报出：Python 文件在 Git 中是 0644，不写 shebang |
| `make ops-check` | 对每个受 Git 跟踪的 `*.sh` 运行 ShellCheck（`.shellcheckrc`）与 `bash -n`，由 `tools/depcheck` 确认 Dependabot 不把 0.x 的 Web 包放进分组，再核对单元与安装器的约定；ShellCheck 的版本由 Makefile 的 `SHELLCHECK_VERSION` 固定（不同版本判断不同），CI 按哈希安装同一版本 |

Ruff 不随 npm 或 Go 安装。有 `ensurepip` 的机器装进独立的 venv，再把其 `bin` 加入 `PATH`：

```bash
python3 -m venv ~/.local/opt/ruff
~/.local/opt/ruff/bin/pip install --no-deps --require-hashes --only-binary=:all: -r ai/requirements-dev.lock
```

开发机 B 的 Python 3.14 没有 `ensurepip`：从 PyPI 下载本机平台的 wheel，核对 SHA-256 与锁文件中的一项相同，再解出其中的 `ruff-<版本>.data/scripts/ruff`。

`make race-test` 以竞态检测器、每次 4 个包运行全部 Go 测试，需要 gcc；CI 另开作业运行，修改并发代码时本地也运行。测试注入的时钟与 ID 生成器也会被后台 goroutine 调用，要像 `time.Now` 一样可以并发调用：时钟用 `internal/clocktest`，不要在闭包里改写捕获的变量，否则这个作业会偶发失败。

Web 代码由 Biome 格式化：提交前运行 `cd web && npm run format`。2026-10-22 对全部 Web 代码的一次格式化提交列在 `.git-blame-ignore-revs`，GitHub 的 blame 跳过它，本地运行 `git config blame.ignoreRevsFile .git-blame-ignore-revs`。在它之前分出的分支先合并它的父提交，在分支上运行 `npm run format` 并提交，再合并 `main`，这样只剩真正的冲突。

## CI

`.github/workflows/ci.yml` 对每个 PR 和每次合入 `main` 运行三个作业：`Project checks`（`make check`）、`Go tests with the race detector`（`make race-test`）与 `System test (Debian 13, systemd)`（`make system-test`）。`main` 的规则集按作业名要求三者通过才能合并，改作业名时同时改规则集。新的强制检查加进这三个作业，通常是 Makefile 的 `check-steps`，规则集不用改。

PR 只改文档时，`Project checks` 只运行 `make docs-check`，另两个作业按条件跳过，GitHub 把条件跳过的作业记为通过。文档指 `docs/` 下的文件、根目录的 `*.md` 和 `.claude/skills/` 中的 `*.md`；其他目录中的 Markdown 随产品发布（助手技能、应用目录的说明），按代码处理。判定见 `scripts/ci-scope.sh`，判定失败时三个作业照常完整运行。合入 `main` 后的运行总是完整的。

## 尚待决定后再安装

以下项目应在对应 ADR 或技术原型完成后再固定版本：

- PostgreSQL 等常驻数据库；
- OpenAPI、Protobuf 等代码生成器；
- AI Provider SDK 和本地模型 Runtime。
