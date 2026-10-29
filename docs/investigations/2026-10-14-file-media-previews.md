# 文件管理没有视频缩略图，HEIC 照片不能预览

状态：fixed，待实机验收
更新时间：2026-10-27

## 症状与影响

普通文件管理列表与图标视图中的视频只有文件图标。HEIC/HEIF 照片不能在详情栏或双击后显示，用户只能下载。

## 最小复现

1. 在普通个人或共享空间放入 `clip.mp4`、`photo.HEIC`、`photo.heif`。
2. 打开文件管理，观察项目缩略图，选中和双击照片。
3. 修改前，三个项目均没有 `<img>`；照片双击进入下载。

## 观察事实

- 新增 `App.test.tsx` 的三个参数化回归用例先失败：`row.querySelector("img")` 为 `null`，与用户报告相符。
- 前端只为浏览器原生图片扩展名创建图片节点，HEIF 不在图片类型判断中。
- `/content` 返回原始字节，没有普通文件派生预览接口；现有 FFmpeg Worker 已有 FFmpeg 抽帧能力。
- libheif 的 `heif-thumbnailer` 接受输入路径并输出 PNG，不能直接消费传入的文件描述符。Worker 的动态身份不能重新打开用户所有的文件，因此使用有界、Worker 所有的匿名内存文件；不开放数据目录。工具行为见 [Debian 手册](https://manpages.debian.org/trixie/heif-thumbnailer/heif-thumbnailer.1.en.html)。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 前端排除了格式 | 对三种文件渲染实际文件管理并查找图片节点 | confirmed |
| 普通文件接口缺少抽帧和转换 | 检查路由、下载处理器和 Worker | confirmed |
| HEIC 运行依赖没有接入 | 检查安装脚本与 Debian 软件包 | confirmed：新增 heif-thumbnailer 与 HEVC 解码插件 |

## 根因

普通文件预览仅依赖浏览器直接解码。视频没有抽帧入口，HEIC/HEIF 既未列入预览类型，也没有转换后可供浏览器显示的内容。

## 修复与回归证据

- 文件预览服务在每次请求中重新打开源文件验证访问权，再使用有界内存缓存；视频抽帧，HEIC/HEIF 转成 PNG。
- 前端列表、图标、详情栏与照片查看器接入派生图片；原文件下载保持独立。
- 取消预览回归发现原有 IPC 读取在 recvmsg 返回负数时切片越界：读取失败先退出，再解析消息；同时在客户端断开时取消缓冲解码进程。
- 回归覆盖详情与双击、损坏图片回退、缓存后的权限拒绝与删除、同尺寸文件替换、输入和尺寸限制。
- 可复现验证：`make check`；真实解码：安装 `ffmpeg heif-thumbnailer libheif-plugin-libde265 libheif-examples libheif-plugin-x265` 后运行 `go test -v ./internal/ffmpegworker -run 'TestRealHEIF|TestRealFFmpeg'`。HEIC 测试现场生成测试图片和编码文件，不使用个人照片；编码依赖仅用于测试。

- 验证结果：`make check` 各阶段通过（前端 31 个文件 / 269 项测试）；`go test -race ./internal/filepreview ./internal/ffmpegworker` 通过；一次性 Debian 13 容器中真实 HEIC 与视频解码通过，生产同款 systemd 沙箱系统测试 136 项 PASS、`failures=0`。系统测试同时验证 HEIC 两种尺寸、视频缩略图、原文件下载不变与服务无直接目录读取权。

## 后续工作

按运行手册安装新增解码依赖后随版本发布；本次未操作实验 NAS。

## 关联

- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [文件预览服务与回归测试](../../internal/filepreview)
- [隔离 Worker 与真实解码测试](../../internal/ffmpegworker)
- [文件管理回归测试](../../web/src/App.test.tsx)
- [ADR 0017：FFmpeg Worker](../adr/0017-run-the-media-center-through-the-file-broker.md)
- [安装运行手册](../runbooks/provision-v1.0.1-experimental-storage.md)
