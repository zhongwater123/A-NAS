# OCR 实施方案中采纳的建议

状态：decided（2026-10-30，用户）
更新时间：2026-11-05

## 问题

[OCR 实施方案](../architecture/photo-ocr.md)起草时列出 8 项待用户决定。

## 选项

每项的建议与理由见实施方案的相应章节。

## 决定

用户 2026-10-30 按建议采纳 5 项：

- 首批文档类型与字段按[方案中的表](../architecture/photo-ocr.md#首批文档类型与字段2026-10-30-确认)；
- 敏感号码默认遮挡；
- “照片中的文字”开关默认开启，关闭即清除、保留更正；
- 共享图库所有成员都可以更正，记入活动记录；
- 手动“读取这张照片的文字”放到后续版本。

AI 助手的可见性按用户的方向另定（[决定](2026-10-30-ocr-assistant-handles.md)）。分流与核对的验收目标在数据出来后定下（[分流](2026-11-02-ocr-routing-recall.md)、[单字概率](2026-11-03-ocr-character-confidence.md)）。还有两项待定：[点开完整号码是否记入活动记录](2026-10-30-ocr-full-number-reveal-logging.md)、[敏感字段的清单](2026-10-30-ocr-sensitive-fields.md)。

## 关联

- [OCR 实施方案](../architecture/photo-ocr.md)、[相册规格](../specs/photo-library.md)
