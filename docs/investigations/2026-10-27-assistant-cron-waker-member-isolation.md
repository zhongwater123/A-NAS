# 助手定时任务被成员文件阻塞，停用账号仍被唤醒

状态：resolved，修复已通过本地完整门禁、竞态检测与 systemd 验证
更新时间：2026-10-27

## 症状与复现

[#178](https://github.com/zhongwater123/A-NAS/issues/178) 在 `cb62d69` 的唤醒器中复现：将一个测试账号的 `hermes/cron/jobs.json` 换成 FIFO，读取超过两秒仍不返回；换成目录得到未捕获的 `IsADirectoryError`；20 万字节的深度嵌套 JSON 得到 `RecursionError`。这些都发生在同一顺序循环，后面的账号不能被处理。

```bash
python3 -m unittest discover -s ai/tests -p test_cron_waker.py
go test ./internal/hostops/linux -run TestDisabledAccountsStopAndMaskAssistantUnits -count=1
```

第一条在旧实现有一项失败和两项异常。第二条记录实际 Linux Adapter 发出的命令：账号停用和禁用身份同步都只处理 Samba/账号，未停止任何助手单元；Samba 失败时直接返回。

## 根因与修复边界

- 令牌文件只证明助手曾被配置，不能证明账号仍活动。唤醒前经 Host Agent 私有 socket 复用按 UID 查询活动账号的协议，并核对返回的账号名；数据库、代理或协议不可用时不启动。
- 只读普通文件：`O_NOFOLLOW | O_NONBLOCK` 打开后用 `fstat` 检查类型和大小，最多读取 4 MiB 加一字节。解析和读取错误拒绝该文件。每个账号在独立的短生命周期进程中处理，父进程使用标准库的十秒超时，单个账号异常不结束整轮检查。
- 停用时同时停止并禁用助手、API 服务/socket、定时任务及复盘服务/定时器，并 mask 实例，防止在活动状态检查之后排队的启动请求重新激活它。Samba 操作失败仍执行助手停用。身份同步会补做禁用；试用包更新保留 mask，不重新启用这些实例。
- 不删除成员的助手目录、令牌或历史记录，不读取模型密钥。尚无重新启用账号的产品操作；这些 mask 不由更新脚本自动撤销。

## 验证与关联

- 2026-10-27，WSL Ubuntu 24.04 的独立 ext4 副本：`make check-steps` 的全部目标分阶段通过（Web 共 64 个文件、513 个测试）；`make race-test` 通过。修正后单独重跑账号错误文案测试与 `make fmt-check ops-check docs-check`，均通过。
- 使用当前构建脚本生成的 Debian 13/systemd 镜像运行 `bash scripts/system-test.sh anas-systemd:issue178`：353 项 PASS，0 项失败。真实产品 API 停用运行中的任务所有者，确认进程结束、活动身份查询被拒绝、六个实例不能再启动，更新成员配置后仍被屏蔽。首轮新夹具遗漏前序测试删除的助手目录，补齐准备步骤后完整重跑通过。
- [账号停用系统测试](../../ai/assistant-spike/disable-system-test.sh)
- [唤醒器文件与账号隔离回归](../../ai/tests/test_cron_waker.py)
- [账号停用回归](../../internal/hostops/linux/assistant_test.go)
- [既有活动身份查询](../../internal/accounts/session_identity_test.go)
- [AI 助手规格](../specs/ai-assistant.md)
- [试用与升级手册](../runbooks/try-ai-assistant-on-experimental-nas.md)
- [ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
