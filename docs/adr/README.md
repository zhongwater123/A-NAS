# 架构决策记录

ADR 记录难以逆转、未来读者无法仅凭代码理解且存在真实权衡的决定。普通依赖升级、易撤销配置和实现细节不创建 ADR。

## 索引

- [0001：产品服务和 Host Agent 使用 Go](0001-use-go-for-product-services.md)
- [0002：客户端产品接口使用 REST 和 OpenAPI](0002-use-rest-openapi-for-product-clients.md)

## 格式

文件名使用 `NNNN-short-title.md`。正文首先用一至三句话说明背景、决定和原因；只有确有价值时再增加状态、备选项或后果。

决定被替代时保留原文件，在顶部标记 `superseded` 并链接新 ADR。
