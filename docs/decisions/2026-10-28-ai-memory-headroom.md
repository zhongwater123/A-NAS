# AI 模型不设固定内存预算，加载前留出余量

状态：decided（2026-10-28，用户）
更新时间：2026-11-05

## 问题

CPU 已按闲时有多少用多少处理（[决定](2026-10-13-ai-uses-idle-capacity.md)），AI 模型的内存仍有固定预算。

## 选项

未记录当时比较过的选项。

## 决定

用户 2026-10-28 决定内存与 CPU 同一原则：不设固定预算；后台任务加载模型前确认加载后仍余 1 GiB，有人在等的请求不受限制；安装器写入的 `MemoryMax`（总内存减 2.5 GiB）只防失控（[#321](https://github.com/zhongwater123/A-NAS/pull/321)）。2026-10-30 用户说会把余量调整为 500 MB；本记录更新时代码中仍是 1 GiB（[`gate.go`](../../internal/photoservice/gate.go) 的 `MemoryReserve`）。

## 关联

- [物体检测的资源策略](../architecture/photo-objects.md#资源策略)、[OCR 实施方案](../architecture/photo-ocr.md)
