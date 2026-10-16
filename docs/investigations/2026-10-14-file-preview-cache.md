# 视频缩略图重访重复解码与队列阻塞

状态：fixed locally；Experimental NAS verification pending
更新时间：2026-10-14

## 症状与最小复现

用户反馈文件管理的视频缩略图加载慢，每次打开目录重新加载。使用真实预览服务与计数 Worker 替身，先后请求 70 个视频两次，并在解码槽被占用时请求已有缓存：

```sh
go test ./internal/filepreview -run 'TestDirectoryRevisit|TestCachedPreview' -count=1
```

修复前结果：`two visits decoded 140 videos; want 70`；`cached preview waited behind decode: context deadline exceeded`。最小回归耗时约 0.13 秒，不依赖设备或网络速度。

## 假设与证据

| 假设 | 预测及验证 | 结果 |
|---|---|---|
| 64 项内存缓存循环淘汰 | 按相同顺序重访 70 个文件会全部重新解码 | confirmed：140 次解码；进程重启也会清空缓存 |
| 缓存读取受解码队列阻塞 | 占住解码槽时，已有缓存也无法返回 | confirmed：100 ms 上下文超时 |
| 响应无法在浏览器复用 | 已有图片仍返回完整正文 | confirmed：原响应为 `private, no-store`，没有 ETag |

## 修复边界

增加产品服务私有状态目录内的有界磁盘缓存，缓存容量、权限和失效规则以[基础存储与共享规格](../specs/basic-storage-and-sharing.md)为准。它沿用 [ADR 0017](../adr/0017-run-the-media-center-through-the-file-broker.md) 的系统盘派生数据边界：产品服务可读派生画面，但仍不能直接读取普通文件；缓存可以独立删除，原文件和用户目录不写入。

命中缓存前必须打开源文件检查权限与文件身份。排队只用于未命中请求，出队再次验证，以免等待期间的删除、修改或撤权被忽略；同一图片的并发请求复用前一个请求的结果。解码前后检查文件身份，避免把写入中的图片存到旧版本键。缓存损坏或写入失败降级为重新生成。

成功响应允许浏览器私有存储，但每次复用都必须验证，校验通过后才返回 304；错误不缓存。依据 [RFC 9111 §5.2.2.4](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.4)，`no-cache` 要求复用前成功验证，并不禁止存储。首次浏览仍需抽帧；缓存容量耗尽后，最久未用的项目会重新生成。本修复不预扫描整个数据卷，也不改变媒体 Worker 的解码隔离。

## 验证

- 回归覆盖目录重访、缓存不等待解码、服务重启、磁盘容量与项数淘汰、临时文件清理、缓存损坏、写入失败、并发合并、SMB 保持 mtime 的写入、账号隔离、撤权与删除。
- HTTP 回归验证 304 无正文、旧 ETag 返回 200、无会话和无权用户携带 ETag 仍被拒绝、删除后携带 ETag 返回 404。
- 验证结果：`make check` 完整通过（前端 34 个文件 / 300 项测试）；上述竞态回归通过；一次性 Debian 13 systemd 容器系统测试 144 项 PASS、`failures=0`。新增系统用例在生成视频缩略图后停止媒体 Worker socket、重启产品服务，验证缓存仍返回相同图片及 304。首次系统测试在安装器身份探针处未获取到启动日志，尚未执行预览用例；未修改安装器或服务配置，原配置重跑通过。本次未操作实验 NAS。
- 可复现：`go test -race ./internal/filepreview ./internal/httpapi -run 'Preview|DirectoryRevisit|DiskCache' -count=1`；完整门禁 `make check`。

## 关联

- [预览服务与回归](../../internal/filepreview)
- [HTTP 预览接口](../../internal/httpapi/filepreview.go)、[HTTP 回归](../../internal/httpapi/filepreview_test.go)
- [系统沙箱验收](../../scripts/system-test.sh)
- [启动配置](../../cmd/anas-api/main.go)
- [原视频与 HEIC 适配调查](2026-10-14-file-media-previews.md)
