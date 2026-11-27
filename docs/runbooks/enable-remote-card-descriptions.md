# 内测期间把卡片描述交给外部接口

状态：draft；只用于实验 NAS 上的剪辑中心内测（[#648](https://github.com/zhongwater123/A-NAS/issues/648)），待执行
更新时间：2026-11-27

## 目的

实验 NAS 本机给一张卡片写描述平均要 74 秒，30 分钟素材要几个小时。内测期间改由一个 OpenAI 兼容的外部接口写描述（#648 的实测：每张约 7 秒）。

成功结果：AI Worker 的日志写明卡片描述交给了哪台主机，相册给新素材写描述明显变快；读照片里的文字、向量、人脸等其他 AI 工作仍在本机。

**这是临时改动，不是产品功能**：
- 关键帧会发到设备外，AI Worker 要联网，与 [ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)“本地 AI 不联网”相反；
- 代码里的开关（`ai/anas_ai/providers.py` 的 `RemoteDescriber`、`ai/anas_ai/worker.py` 的 `remote_describer`）由一个提交加入，`videocut-center` 合进 `main` 之前整笔回退，本手册随它删除；
- 软件包里的 `anas-ai.service` 不变，仍不联网，系统测试照常检查。联网、接口地址与密钥只写在这台设备的两个文件里。

## 前提与风险

- **权限**：NAS 上的 root。
- **测试者同意**：测试者视频的关键帧会发到外部接口。开始前告诉测试者并征得同意，测试结束后按“回滚”关掉，并请接口方删除记录（#648）。
- **版本**：设备上装的版本包含加入开关的提交。看日志确认：没有这个提交时，第 4 步之后日志里不会出现 `cards are described`。
- **接口**：接口地址（完整的 `.../v1/chat/completions`）、模型名与密钥由接口方提供，不写进仓库、issue 或聊天记录。接口要接受 JSON Schema 的 `response_format`、`repetition_penalty` 与 base64 图片，并在流式回答末尾给出 `usage`（vLLM 都支持）。
- **描述的身份**：相册只收“读文字的模型”写的描述，所以外部接口写的描述记在本机读文字模型的名下。关掉开关后，这些描述不会自动重写；内测数据可以丢弃，不处理。
- **服务影响**：第 4 步停掉 AI Worker，正在做的相册 AI 工作中断，下一次请求时自动重启并接着做。
- **停止条件**：
  - 第 1 步连不上接口：停下，先解决网络，不要放开 AI Worker 的联网；
  - 第 5 步日志里是 `cards are described here`：按日志写的原因改第 2 步的文件。

## 步骤

1. **确认这台 NAS 连得上接口**（以普通账号即可）：
   ```bash
   read -rp 'URL: ' URL; read -rsp 'Key: ' KEY; echo
   curl -sS -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
     -d '{"model":"<模型名>","messages":[{"role":"user","content":"hi"}],"max_tokens":1}' "$URL"
   unset KEY
   ```
   - 预期：`200`。
   - 完成标准：是 `200`。连不上、超时或 `401` 时停下。
2. **root：写接口地址、模型名与密钥**，只有 root 能读：
   ```bash
   read -rp 'URL: ' URL; read -rp 'Model: ' MODEL; read -rsp 'Key: ' KEY; echo
   (umask 077; printf 'ANAS_AI_DESCRIBE_URL=%s\nANAS_AI_DESCRIBE_MODEL=%s\nANAS_AI_DESCRIBE_KEY=%s\n' "$URL" "$MODEL" "$KEY" \
     > /etc/a-nas/describe-remote.env)
   unset KEY
   stat -c '%U %a' /etc/a-nas/describe-remote.env
   ```
   - 预期：`root 600`。
   - 接口用公司内部的证书时，再加一行 `SSL_CERT_FILE=<证书文件>`，证书文件要放在 AI Worker 读得到的位置（例如 `/etc/ssl/certs/` 下）。
3. **root：放开 AI Worker 的联网**，只用一个 drop-in：
   ```bash
   install -d -m 0755 /etc/systemd/system/anas-ai.service.d
   printf '%s\n' '[Service]' \
     '# 内测临时（#648）：卡片描述交给外部接口；内测结束即删' \
     'PrivateNetwork=false' 'RestrictAddressFamilies=' 'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6' \
     'EnvironmentFile=/etc/a-nas/describe-remote.env' \
     > /etc/systemd/system/anas-ai.service.d/describe-remote.conf
   systemctl daemon-reload
   systemctl show anas-ai.service -p PrivateNetwork -p RestrictAddressFamilies
   ```
   - 预期：`PrivateNetwork=no`，`RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`。
4. **root：让 AI Worker 带着新设置重启**：
   ```bash
   systemctl stop anas-ai.service
   ```
   - 预期：`anas-ai.socket` 仍是 `active`，下一次相册请求时 AI Worker 自动启动。
5. **看 AI Worker 用了哪种方式**：在剪辑中心用一段新视频建项目，等相册开始写描述后：
   ```bash
   journalctl -u anas-ai.service --since '-30 min' -o cat | grep -E 'cards are described|card describer'
   ```
   - 预期：`cards are described by <接口主机>, off this device (#648)`。接口忙时会有 `the card describer answered 429 ...; asking again in 5 s`，属正常。
   - 完成标准：有上面这一行，且没有 `cards are described here`。

## 验证

- `journalctl -u anas-ai.service -o cat | grep 'card description from'` 的时间间隔明显短于本机的 74 秒一张。
- 相册里读照片文字仍在本机：`journalctl -u anas-ai.service -o cat | grep 'reading text with'` 照常出现。
- `a-nas-check` 没有 `FAIL`。

## 回滚或恢复

内测结束、或任何一步不符合预期时，以 root：

```bash
rm /etc/systemd/system/anas-ai.service.d/describe-remote.conf /etc/a-nas/describe-remote.env
systemctl daemon-reload
systemctl stop anas-ai.service
systemctl show anas-ai.service -p PrivateNetwork
```

- 预期：`PrivateNetwork=yes`；之后的卡片描述回到本机。
- 请接口方删除内测期间的请求记录。

## 关联

- Issue：[#648 剪辑中心内测的交付方式](https://github.com/zhongwater123/A-NAS/issues/648)
- ADR：[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- 代码：`ai/anas_ai/providers.py` 的 `RemoteDescriber`，`ai/anas_ai/worker.py` 的 `remote_describer`；测试 `ai/tests/test_describe_remote.py`
- 演示：[在实验 NAS 上演示剪辑中心](demo-videocut-center.md)
