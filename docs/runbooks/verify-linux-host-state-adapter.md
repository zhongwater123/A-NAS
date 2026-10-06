# 验证 Debian Linux Adapter

状态：verified
更新时间：2026-10-06

## 目的

把 Linux Adapter 的显式集成测试作为静态 Linux/amd64 制品上传到实验 NAS，通过真实 Debian 文件和 `lsblk` 验证 `hoststate.Reader`，且不安装 Go、不使用 root、不输出原始机器 ID、WWN 或序列号。

## 前提与风险

- 权限：本机 WSL 开发账号，以及实验 NAS 上无 `sudo` 权限的 `anas-dev`。
- 目标：已核验主机指纹的 `a-nas-dev`；地址只通过本地配置提供。
- 风险：测试会读取 `/etc/machine-id` 和磁盘硬件身份以派生哈希，但不会输出原值；上传文件只写入 `anas-dev` 的 home。
- 停止条件：SSH 指纹变化、远端身份不是 `anas-dev`、本地测试失败、制品哈希不一致，或命令要求提权。

## 步骤

1. 在 WSL 中运行本地测试并构建静态测试制品。

   ```bash
   cd ~/workspace/A-NAS
   go test ./internal/hoststate/linux
   mkdir -p build
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
     go test -c -o build/hoststate-linux-integration ./internal/hoststate/linux
   sha256sum build/hoststate-linux-integration
   file build/hoststate-linux-integration
   ```

   - 预期：本地测试通过；制品为静态链接的 Linux x86-64 ELF。
   - 完成标准：记录完整 SHA-256。

2. 上传为临时文件，在远端校验哈希后再改名。

   ```text
   远端目录：~/apps/a-nas/tests/m2-linux-reader/
   临时文件：hoststate-linux-integration.incoming
   最终文件：hoststate-linux-integration
   ```

   - 预期：目录和文件均属于 `anas-dev`，不使用 `sudo`。
   - 完成标准：远端 `sha256sum -c` 成功，最终文件权限为 `750`。

3. 只对这一项测试显式启用真实主机读取。

   ```bash
   cd "$HOME/apps/a-nas/tests/m2-linux-reader"
   ANAS_LINUX_INTEGRATION=1 ./hoststate-linux-integration \
     -test.run '^TestReaderReadsLocalLinuxHost$' -test.v
   ```

   - 预期：测试通过且输出不包含机器 ID、WWN、序列号或设备路径。
   - 完成标准：进程退出码为 0，结束后没有残留进程。

## 验证

- 本地 `go test ./internal/hoststate/linux` 通过。
- 远端完整 SHA-256 与本地一致。
- `TestReaderReadsLocalLinuxHost` 在实验 NAS 上通过。
- 返回结果包含且只包含一个 `system` 磁盘，磁盘 ID 严格排序，未执行 SMART 时健康为 `unknown`、温度为空。

## 回滚或恢复

- 测试失败：保留制品哈希和失败输出用于调查，不修改系统状态后重试。
- 哈希不一致：删除 `.incoming`，重新从本地已验证构建目录上传；不得绕过校验。
- 测试制品不是常驻进程；确认无需保留后，可仅删除明确的 `~/apps/a-nas/tests/m2-linux-reader` 版本目录。

## 关联

- 规格：[Debian 只读宿主机状态](../specs/read-only-linux-host-state.md)
- ADR：Host Agent IPC 尚未决定
- 调查：不涉及
