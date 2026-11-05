# AI 后台任务闲时有多少用多少

状态：decided（2026-10-13，用户）
更新时间：2026-11-05

## 问题

相册 AI 的后台任务原先固定在 `CPUQuota=200%`、`MemoryHigh=2G`、`MemoryMax=3G` 的预算里，当时闲时检测只用了约 1 个核（[调查](../investigations/2026-10-13-idle-detection-used-one-core.md)）。

## 选项

未记录当时比较过的选项。

## 决定

用户 2026-10-13 确定原则：闲时有多少资源就用多少，始终留出余量，用户有突发请求时正确释放。实现为检测推理用可用逻辑核数减 2 个线程，安装器按本机核数写入 `CPUQuota=(N−2)×100%` 兜底，模型按需加载与卸载，检测任务可以取消。内存随后按同一原则处理（[决定](2026-10-28-ai-memory-headroom.md)）。

## 关联

- [物体检测的资源策略](../architecture/photo-objects.md#资源策略)、[相册本地 AI](../architecture/photo-ai.md)
