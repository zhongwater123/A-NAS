# 更新单元退出时遗漏已受理的请求

状态：open，等待软件包设备验收
更新时间：2026-11-16

## 症状与影响

在定时更新开始前检查到单元空闲的请求可能收到 HTTP 202，但之后一直留在状态目录。下一次定时运行通常超过请求有效期，请求被丢弃。

## 根因

Host Agent 查询单元状态和写入请求之间，定时器可以启动更新服务。即使服务在退出前再次读取请求，文件仍可能在最后一次读取之后才到达。此时 oneshot 启动作业尚未结束，Host Agent 的 `systemctl start --no-block` 可以并入旧作业。

## 修复与回归证据

- [Host Agent](../../internal/hostops/linux/update.go)先启动[路径单元](../../deploy/systemd/system/anas-update.path)，再发布请求文件。systemd 在服务退出后重新检查请求路径，[更新服务](../../internal/systemupdate/updater/main.go)在领取请求时核对有效期。
- [Debian 系统测试](../../scripts/system-test.sh)用 `ExecStartPost` 让单元停在最后一次读取与退出之间，此时发布检查请求。放行旧运行后验证状态的检查时间改变，且请求已领取。

## 关联

- [系统更新规格](../specs/system-update.md#边界与失败)
- [Issue #485](https://github.com/zhongwater123/A-NAS/issues/485)、[PR #491](https://github.com/zhongwater123/A-NAS/pull/491)
