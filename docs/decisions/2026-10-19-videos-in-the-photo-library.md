# 相册放视频，视频不参与 AI

状态：decided（2026-10-19，用户）
更新时间：2026-11-17

## 问题

此前相册只收照片（实况照片的视频部分除外），单独的视频不能放进相册。

## 选项

未记录当时比较过的选项。

## 决定

用户 2026-10-19 要求“相册可以往里面放视频”，并且“视频暂不参与任何 AI 功能，只是可以通过相册承载”。实现见 [PR #146](https://github.com/zhongwater123/A-NAS/pull/146)；实现时另选的默认做法等用户确认，见[相册视频的默认做法](2026-10-20-photo-video-defaults.md)。

（2026-11-17 用户为剪辑中心定了例外：相册为剪辑中心请求的视频计算基础分析，见[决定记录](2026-11-17-montage-center-first-version.md)第 8、9 项与 [ADR 0038](../adr/0038-keep-montage-media-in-the-photo-library.md) 决定 4。）

## 关联

- [相册视频规格](../specs/photo-video.md)
- [剪辑中心首版的数据归属与几项做法](2026-11-17-montage-center-first-version.md)
