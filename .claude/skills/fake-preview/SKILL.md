---
name: fake-preview
description: 在开发机上启动一套 Fake 模式的 A-NAS 预览栈：模拟磁盘、容器与应用，不碰真实宿主机，用自己的 http://<名称>.localhost:<端口> 打开。用于在浏览器里检查界面改动、截图，或与其他会话的预览栈并行而互不登出。
---

# 启动 Fake 模式预览栈

产品服务 `anas-api` 在 `ANAS_HOSTSTATE_MODE=fake` 时使用模拟的宿主机：两块确定性磁盘加一块空闲盘、模拟的存储执行、Fake 容器与应用目录（[`cmd/anas-api/config.go`](../../../cmd/anas-api/config.go)）。这个变量没有默认值，不设置时服务拒绝启动。Web 桌面嵌入在同一个二进制中，所以一个进程就是一整套栈。

## 为什么每套栈用自己的主机名

- 会话 Cookie `anas_session` 按主机名保存，不区分端口。几套栈都用 `localhost` 或 `127.0.0.1` 时，在一套里登录会顶掉另一套的会话。`<名称>.localhost` 在 Chromium 中解析到回环地址，每个名称有自己的 Cookie。
- 代价：只认 `localhost` 或 IP 的请求在 `<名称>.localhost` 上被拒绝，见 [`internal/localorigin`](../../../internal/localorigin/localorigin.go)。这些请求包括首次启用管理员、本机屏幕会话、Docker 与应用中心的写操作和终端。首次启用用下面的 curl 完成；要测这几项时，改从 `http://127.0.0.1:<端口>` 打开。

## 步骤

1. 选名称与端口：名称取功能的简写（如 `music`），端口在 18000–18999 中选一个空闲的（在 WSL 中用 `ss -ltn` 查看）。
2. 构建并启动。Windows 开发机上从 Claude Code 的 Bash（Git Bash）在检出根目录运行，作为后台任务。它复用 [check-in-wsl](../check-in-wsl/SKILL.md) 的 ext4 副本，二进制与状态都放在 `~/anas-preview/<名称>`：

   ```bash
   MSYS_NO_PATHCONV=1 wsl.exe -e bash -s -- "$(cygpath -w "$PWD")" <名称> <端口> <<'EOF'
   set -euo pipefail
   if [ -f ~/.anas-env ]; then . ~/.anas-env; fi
   src=$(wslpath -a "$1") name=$2 port=$3
   dst=~/anas-check/$(basename "$src") state=~/anas-preview/$2
   mkdir -p "$dst" "$state"
   rsync -a --delete --exclude .git --exclude web/node_modules --exclude build "$src"/ "$dst"/
   cd "$dst"
   make web-build > "$state/build.log" 2>&1
   go build -o "$state/anas-api" ./cmd/anas-api >> "$state/build.log" 2>&1
   echo $$ > "$state/pid"
   ANAS_HOSTSTATE_MODE=fake ANAS_HTTP_ADDR=127.0.0.1:$port ANAS_STATE_DIR=$state exec "$state/anas-api" > "$state/api.log" 2>&1
   EOF
   ```

   - `ANAS_STATE_DIR` 必须是绝对路径，每套栈各用一个。账号、文件目录与各模块的 SQLite 都在其中，模拟数据卷在其下的 `development-volume`。不设时所有栈共用 `~/.config/a-nas/state`。
   - 构建需要 Node（`make web-build` 在副本中按锁文件安装依赖）与 gcc（cgo SQLite）。
   - 改了代码就停掉这套栈，再运行同一命令。
   - 副本与 check-in-wsl 共用，不要在同一检出的门禁运行期间构建，否则两边同时改写 `internal/webui/dist`。
3. 首次启用管理员。只接受直接的回环请求，所以在 WSL 中对 `127.0.0.1` 发请求。用测试账号与 12–64 个字符的测试密码，不要用真实密码：

   ```bash
   MSYS_NO_PATHCONV=1 wsl.exe -e curl -fsS -X POST -H 'Content-Type: application/json' \
     -d '{"username":"<账号>","password":"<测试密码>"}' http://127.0.0.1:<端口>/api/v1/setup/admin
   ```

4. 在浏览器中打开 `http://<名称>.localhost:<端口>` 并登录（Claude Code 桌面版的浏览器面板可以直接打开这个地址）。页面显示“请在 NAS 本机启用”，说明第 3 步还没有完成。成员账号在“设置 → 用户与权限”中创建。
5. 用完后停止：`wsl.exe -e bash -c 'kill "$(cat ~/anas-preview/<名称>/pid)"'`。要从空白状态重来，就删除 `~/anas-preview/<名称>`。

## 预览覆盖不到的

- 影视、音乐、视频与 HEIC 需要 `ffmpeg`、`ffprobe` 与 `heif-thumbnailer`：开发模式在进程内运行媒体 Worker，WSL 中没有这些命令时，相关功能报错。
- 相册本地 AI 与 AI 助手默认不可用；文件权限、Samba 与 systemd 沙箱只能由 [system-test](../system-test/SKILL.md) 或实机验证。

## 只改界面时

端口 8080 空闲时，可以在 `web/` 中运行 `npm run dev`。Vite 把 `/api` 代理到固定的 `127.0.0.1:8080`，所以同一时间只能有一套这样的栈；在 `/mnt` 下运行时加 `CHOKIDAR_USEPOLLING=1`，见[本地环境](../../../docs/development/environments/windows.md#运行检查)。
