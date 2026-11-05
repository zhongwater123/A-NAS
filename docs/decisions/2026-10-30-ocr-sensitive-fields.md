# OCR 敏感字段的清单

状态：pending
更新时间：2026-11-05

## 问题

哪些字段算敏感：默认遮挡，交给 AI 助手时只给句柄（[决定](2026-10-30-ocr-assistant-handles.md)）。清单见[实施方案](../architecture/photo-ocr.md#ai-助手能用看不到具体的值)，实现时可以再调整。

## 选项

- **按实施方案的清单**。
- **增减字段**：指出要加入或去掉的字段。

## 决定

待定。实施方案的步骤 4 按方案的清单实现：公民身份号码、住址、卡号、付款账号、收款账号、交易流水号；全文中再按规则认出身份证号、卡号、手机号，以及其他 15 位以上的数字串（错了一位的证件号、卡号校验不过，仍是敏感的）。增减字段只改 [`internal/photos/textread/sensitive.go`](../../internal/photos/textread/sensitive.go) 中的表。

## 关联

- [OCR 实施方案](../architecture/photo-ocr.md#权限与隐私)
