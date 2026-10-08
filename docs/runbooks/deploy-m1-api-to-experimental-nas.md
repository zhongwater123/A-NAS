# 部署 M1 API 到实验 NAS

状态：historical（一次性的 M1 冒烟部署，已被系统服务部署取代）
更新时间：2026-10-08

## 目的

把本机已通过检查的 `anas-api` Linux/amd64 制品，以可追溯、可校验且不需要 root 的方式上传到 Debian 13 实验 NAS，并临时完成三个只读 Fake API 的冒烟验收。本手册不创建常驻服务、不读取真实磁盘，也不把实验 NAS 变成源码事实源。

## 前提与风险

- 权限：本机 WSL 开发账号，以及实验 NAS 上无 `sudo` 权限的 `anas-dev`。
- 目标：已核验主机指纹的 `a-nas-dev`；地址通过本地 SSH 配置或运行时参数提供，不写入仓库。
- 风险：错误的主机、版本或哈希会造成不可追溯部署；占用已有端口可能干扰其他进程。
- 停止条件：工作树包含未经确认的代码变更、SSH 主机指纹变化、远端身份不是 `anas-dev`、制品哈希不一致或 `127.0.0.1:8080` 已被占用。

## 步骤

1. 在 WSL 仓库中确认来源并运行完整检查。

   ```bash
   cd ~/workspace/A-NAS
   git status --short
   version="$(git rev-parse --short HEAD)"
   make check VERSION="$version"
   ```

   - 预期：代码来源已经提交或明确标记为 dirty，`make check` 全部通过。
   - 完成标准：得到唯一的 Git 短哈希，且待部署二进制来自本次检查。

2. 构建 Linux/amd64 制品并生成哈希。

   ```bash
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 make build VERSION="$version"
   sha256sum build/anas-api
   file build/anas-api
   ```

   - 预期：文件为 Linux x86-64 ELF，版本参数为当前 Git 短哈希。
   - 完成标准：记录完整 SHA-256，不只记录缩写。

3. 在远端创建版本化发布目录，并把制品上传为临时文件。

   ```powershell
   $keyPath = Join-Path $env:USERPROFILE ".ssh\a-nas-dev_ed25519"
   $target = "anas-dev@<NAS_HOST>"
   $version = "<GIT_SHORT_SHA>"

   ssh -o BatchMode=yes -o IdentitiesOnly=yes -i "$keyPath" $target `
     "install -d -m 750 ~/apps/a-nas/releases/$version"

   scp -o BatchMode=yes -o IdentitiesOnly=yes -i "$keyPath" `
     "E:\A-NAS\build\anas-api" `
     "${target}:apps/a-nas/releases/$version/anas-api.incoming"
   ```

   - 预期：所有文件均属于 `anas-dev`，没有使用 `sudo`。
   - 完成标准：临时文件只存在于目标版本目录。

4. 在远端校验完整哈希后原子改名，并写入不含秘密的发布元数据。

   ```bash
   cd "$HOME/apps/a-nas/releases/<GIT_SHORT_SHA>"
   printf '%s  %s\n' '<FULL_SHA256>' 'anas-api.incoming' | sha256sum -c -
   chmod 750 anas-api.incoming
   mv anas-api.incoming anas-api
   printf 'version=%s\nsha256=%s\nsource=%s\n' \
     '<GIT_SHORT_SHA>' '<FULL_SHA256>' \
     'https://github.com/zhongwater123/A-NAS/commit/<GIT_SHORT_SHA>' > RELEASE
   chmod 640 RELEASE
   ```

   - 预期：`sha256sum` 返回成功，最终文件名只在校验后出现。
   - 完成标准：`anas-api` 和 `RELEASE` 的权限与内容符合预期。

5. 临时启动并在远端回环地址验收；完成后发送 SIGTERM。

   ```bash
   cd "$HOME/apps/a-nas/releases/<GIT_SHORT_SHA>"
   ANAS_HTTP_ADDR=127.0.0.1:8080 ./anas-api >anas-api-smoke.log 2>&1 &
   pid=$!

   # 使用远端已有的 HTTP 客户端或 Bash /dev/tcp 依次请求：
   # /healthz
   # /api/v1/system
   # /api/v1/disks

   kill -TERM "$pid"
   wait "$pid"
   ```

   - 预期：三个接口均为 `200`，系统响应中的 `productVersion` 等于 Git 短哈希，日志包含启动和关闭事件。
   - 完成标准：进程优雅退出，`ss -ltn` 不再显示 `127.0.0.1:8080` 监听。

## 验证

- 本地 `make check` 通过。
- 本地与远端的完整 SHA-256 相同。
- `GET /healthz` 返回 `{"status":"ok"}`。
- `GET /api/v1/system` 返回 Debian 13 Fake 状态和正确 `productVersion`。
- `GET /api/v1/disks` 返回按稳定资源 ID 排序的 125 GB Fake 系统盘和 512 GB Fake 数据盘。
- 停止后没有残留 `anas-api` 进程或 8080 端口监听。

2026-10-06 首次验证证据：版本 `03fc068`，SHA-256 `15d4a2b950014e722b6f35fb45d33e0714238efd572e4bd9da49d41e5dd3f9f2`，三个接口均返回 `200`，SIGTERM 后正常退出。

## 回滚或恢复

- 冒烟失败：先停止本次启动的 PID，保留日志和 `RELEASE`，不要切换任何 `current` 链接。
- 哈希不一致：删除 `.incoming` 临时文件，重新从已核验的本机构建目录上传；不得绕过校验。
- 端口已占用：停止部署并识别现有监听者，不主动终止来源不明的进程。
- 当前流程没有安装系统服务或修改系统配置，因此回滚只需停止临时进程；版本化制品可保留用于溯源。

## 关联

- 规格：[只读宿主机状态](../specs/read-only-host-state.md)
- ADR：[产品客户端使用 REST/OpenAPI](../adr/0002-use-rest-openapi-for-product-clients.md)
- 调查：不涉及
