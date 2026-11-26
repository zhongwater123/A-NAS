# AI Worker 与评估（ai/）

相册 AI Worker（`anas_ai`）、剪辑计算与配乐分析 Worker（`anas_videocut`、`anas_montage_music`）、评估脚本（`eval/`）与 Python 锁文件；`assistant/` 与 `assistant-spike/` 见 [assistant/AGENTS.md](assistant/AGENTS.md)。根目录的 [AGENTS.md](../AGENTS.md) 仍然适用。权威说明：[AI 开发与评估](../docs/development/ai-development.md)、[相册本地 AI](../docs/architecture/photo-ai.md)。

- **Worker 只算不存：** 没有网络、看不到数据卷，只读调用方传来的描述符；结果由 Go 一侧核对后提交，不写原图、权限或用户元数据（[不变量](../docs/architecture/OVERVIEW.md#不变量) 7、11，[ADR 0016](../docs/adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）。
- **模型由清单固定：** `deploy/models/<名称>.json` 固定文件的 SHA-256、许可与实测的 `memoryBytes`；文件在私有的 A-NAS-assets Release 中，用 `scripts/fetch-models.sh` 拉取与 `--publish` 上传。只收代码与权重都允许商用与再分发的模型（[决定](../docs/decisions/2026-10-10-model-license-rule.md)）。新的模型角色同时改 `scripts/lib/content.sh` 的 `ai_roles`、`anas-ai.service` 与 `make ops-check` 的角色表。
- **锁文件只用脚本生成：** `scripts/build-ai-runtime.sh --lock` 从 `requirements.in` 解析出带哈希、排除 `requirements.excludes` 的锁；Dependabot 的锁过不了 `make ops-check`，在它的分支上重跑脚本。锁一变运行环境 ID 就变，剪辑计算 Worker 也用它：重新构建运行环境放进内容库，否则 `make release` 缺包；合并前跑真实模型的测试，部署前跑 [system-test](../.agents/skills/system-test/SKILL.md) 的真实内容变体（默认只装替身）。
- **导入保持轻：** `make ai-test` 只用标准库，MediaPipe、ONNX Runtime 与 NumPy 只在 provider 内按需导入。取消与卸载后要真的放掉模型会话（[调查](../docs/investigations/2026-10-13-ai-worker-kept-cancelled-detector-sessions.md)）。
- **接缝两边一起测：** `go test ./internal/aiworker ./internal/videocutcompute` 启动这里的 Worker 核对协议（`anas_ai` 用 `--fake`）；后者缺 NumPy 与 Pillow 时跳过，设 `ANAS_MONTAGE_PYTHON` 指向 AI 运行环境的 python。帧上限与错误码在 `anas_ai/protocol.py` 和 Go 的 `internal/aiworker`、`internal/videocutcompute`、`internal/videocut/app/workers.go` 各有一份，一起改。
- **评估：** 数据集与缓存只在开发机，不进仓库、NAS 或发行包。`eval.calibrate --write` 改写产品的 `internal/photos/labels/v1.calibration.json`，门槛按[每组正确率的决定](../docs/decisions/2026-10-12-ai-group-precision.md)。
