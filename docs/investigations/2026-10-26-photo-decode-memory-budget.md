# 相册进程内解码超过服务内存上限

状态：fixed，待实机验收
更新时间：2026-10-27

## 证据与复现

[#187](https://github.com/zhongwater123/A-NAS/issues/187) 在 `8f3c0c4` 仍成立：导入只检查 `1 << 28` 像素，16384 × 16384 的 16 位 RGBA PNG 因此可以通过；Go PNG 解码器生成的 NRGBA64 像素本身需要 2 GiB，已等于相册服务硬上限，还没有计算缩放、索引和数据库。该路径没有与助手看图、AI 输入预取及搜索共用的并发限制。

`TestImportRejectsPhotoDecodeBudgetBeforeAllocatingPixels` 只提交合法 PNG 头部，不构造真实大图。修复前四例均返回成功：2 GiB RGBA16、512 MiB RGBA16、9000 × 9000 RGB8 和极端宽的单行 PNG。用例同时要求拒绝后没有 staging 和对象文件。

## 根因与边界

像素数不能代表字节数：16 位颜色需要更宽的像素，灰度或调色板图片在缩放时也会使用 NRGBA 中间图。`imaging` 的 Lanczos 缩放还按源图边长预计算权重，所以只限制总像素会漏掉极端长宽比的输入。

所有完整解码收敛到 `withDecodedImage`：使用标准库读取配置，按[规格预算](../specs/photo-library.md)拒绝超限，在已有 `x/sync/semaphore` 的进程级槽位内执行解码及全部像素处理。没有自建调度器、图片解析器或新的依赖。该限制与 `Service` 实例数量无关；两个实例也不能并发持有大型栅格。原有解码器 panic 处理保持在业务入口，槽位以 defer 释放。

导入前检查阻止新超限对象进入库；解码前重复检查覆盖历史对象及 Worker 返回内容。旧对象保留原图访问，不删除用户文件；超限派生被归为永久 `undecodable`，不因任务恢复而重新领取。FFmpeg Worker 自身的资源策略保持独立，调用 Worker 时不占本机图片槽位。

Go 的 [GOMEMLIMIT 是软限制](https://go.dev/doc/gc-guide#Memory_limit)，不能作为输入校验或并发限制的替代。服务的软、硬限制及余量理由统一记录在[相册技术设计](../architecture/photo-library.md#后台任务与资源隔离)。实际整机峰值还受图库规模、并发请求和其他状态影响；本次用确定性预算与槽位测试验证解码边界，没有通过向实验 NAS 投入压缩炸弹测量 OOM。

## 验证方法

```sh
go test ./internal/photos ./internal/photoservice ./internal/photosapi -count=1
make check
make race-test
```

Windows 开发机按 [WSL 流程](../../.claude/skills/check-in-wsl/SKILL.md)在 ext4 副本运行 `make check-steps`，与 `make check` 相同的门禁，不写部署清单。

回归覆盖颜色模型的预算边界、无效及溢出尺寸、极端长边、导入无残留、历史图片与图片搜索、等待中的取消、解码器 panic 后释放，以及像素处理未结束时四类入口共用槽位。`make ops-check` 同时检查单元的 Go 内存软限制与 systemd 硬限制。

## 关联

- [相册规格](../specs/photo-library.md)、[相册技术设计](../architecture/photo-library.md)
- [解码预算与共享槽位](../../internal/photos/imagedecode.go)、[单元测试](../../internal/photos/imagedecode_test.go)
- [导入回归](../../internal/photos/service_test.go)、[任务失败分类](../../internal/photos/jobs.go)
- [历史图片视图与任务恢复回归](../../internal/photosapi/decodebudget_test.go)、[413 响应契约](../../api/paths/photos.yaml)
- [服务单元](../../deploy/systemd/system/anas-photos.service)、[运维检查](../../Makefile)
