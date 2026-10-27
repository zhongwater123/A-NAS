# Apple Live Photo 配对标识被 EXIF 库遗漏

状态：resolved；配对标识只取决于文件内容，构造的 JPEG EXIF 与 HEIF Exif item 回归已覆盖，实验 NAS 上没有可补充的证据。不同设备导出的匿名样本见后续工作
更新时间：2026-10-27

## 症状与影响

带正确配对标识的照片与 MOV 被作为不匹配的 Live Photo 拒绝。没有标识的同名组合能够导入，因此只测试无元数据文件不能发现该问题。

## 最小复现

1. 使用 `TestAppleContentIdentifiersPairAndRejectMismatch` 构造 JPEG EXIF，包含 Apple MakerNote 的 ContentIdentifier。
2. MOV 探测结果返回相同的 `com.apple.quicktime.content.identifier`。
3. 直接依赖 imagemeta v1.1.0 时，读取到 Make=Apple，MakerNote.Apple 存在，但 ContentIdentifier 为空；导入返回 ErrInvalidMotion。

## 观察事实

- 当前依赖的 `meta/exif/makernote/apple/apple.go` 把 MakerNoteHeaderLength 设为 26，`reader_makernote.go` 按此跳过头部。
- [ExifTool 的 MakerNotes 解析定义](https://github.com/exiftool/exiftool/blob/master/lib/Image/ExifTool/MakerNotes.pm) 对 Apple iOS 使用 14 字节头部，并以头部起点为值偏移基准；[Apple 标签定义](https://github.com/exiftool/exiftool/blob/master/lib/Image/ExifTool/Apple.pm) 将 0x0011 识别为 ContentIdentifier。
- 回归样本在修复前正向配对失败、错配拒绝通过；该差异排除了视频探测和图库权限。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| MOV 缺少标识 | 固定探测结果提供匹配标识 | ruled out |
| MakerNote 定位错误 | 查看解析结果、核对前导长度与相对偏移，使用同一输入复测 | confirmed |

## 根因

依赖使用错误的 MakerNote 前导长度，越过目录开头；返回部分元数据且没有错误，调用方不能凭 Decode 成功判断标识已提取。

## 修复与回归证据

使用窄范围的 Apple 标识读取器：JPEG 只读 APP1 EXIF，HEIF 使用已有 BMFF 元数据读取器给出的 Exif item 边界；按 TIFF 的 ExifIFD → MakerNote → 0x0011 链读取，只处理该字段，所有目录、值偏移与长度都做边界检查。保留现有拍摄时间等通用元数据实现，不放宽标识不匹配时的拒绝规则。

回归覆盖内联字符串、相对 MakerNote 偏移的 UUID、HEIF Exif item、匹配和不匹配两种结果。其他格式、生命周期及真实编解码证据见动态照片规格。

## 后续工作

依赖修复后可用同一组回归评估是否移除此窄读取器；发布前继续补充不同设备实际导出的匿名样本。

## 关联

- 规格：[动态照片](../specs/photo-motion.md)
- 代码：[Apple 标识读取器](../../internal/photos/applemetadata.go)
- 测试：[配对与生命周期](../../internal/photos/motion_test.go)
- ADR：[相册服务身份](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)
- 运行手册：不涉及
