# FFmpeg Worker 的槽位、分层与内存上限

状态：pending
更新时间：2026-11-05

## 问题

产品、相册与分享服务共用 FFmpeg Worker 的 8 个槽位：交互请求可用全部 8 个，流可用 6 个，后台工作只用 2 个；所有 Worker 实例共用 `MemoryMax=3G`。这些数值是修复 [#168](https://github.com/zhongwater123/A-NAS/issues/168) 时按现有上限与常见负载估计的，没有实测（[调查](../investigations/2026-10-21-media-worker-shared-budget.md)）。槽位太少时上传、播放与缩略图要排队；内存上限太低时多路转码会被 OOM 终止，太高时挤占其他服务。

## 选项

- **沿用估计值**：不再改动，出现排队或 OOM 时再调。
- **实测后再定**：按调查中的方法，在实验 NAS 上高负载（多路转码加批量导入）时观察 slice 的内存峰值与 OOM 记录，再定数值。

## 决定

待定。

## 关联

- [调查](../investigations/2026-10-21-media-worker-shared-budget.md)、[#168](https://github.com/zhongwater123/A-NAS/issues/168)
- [准入代码](../../internal/ffmpegworker/admission.go)、[slice 单元](../../deploy/systemd/system/system-anasffmpeg.slice)
