# 文件管理的图片缩略图是否改用服务端生成

状态：pending
更新时间：2026-11-05

## 问题

文件管理里，HEIC/HEIF 照片与视频的缩略图由服务端生成；浏览器能直接显示的图片（JPEG、PNG 等）则直接加载原文件作为缩略图（[`preview.ts`](../../web/src/files/preview.ts)）。2026-10-13 处理问题反馈时记下：目录里的大照片越多，打开目录时要下载的数据越多。

## 选项

- **保持现状**：服务端不为这些图片生成缩略图；大照片多的目录打开慢、流量大。
- **改用服务端缩略图**：与 HEIC 和视频一致；首次显示时要在服务端解码原图并缓存结果。

## 决定

待定。

## 关联

- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [文件管理的媒体预览调查](../investigations/2026-10-14-file-media-previews.md)、[预览缓存调查](../investigations/2026-10-14-file-preview-cache.md)
