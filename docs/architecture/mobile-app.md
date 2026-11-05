# 移动端架构

更新时间：2026-11-05

## 技术栈

React Native + Expo + TypeScript，Android 和 iOS 共用应用代码。版本由 `mobile/package-lock.json` 固定；原生工程由 Expo 配置生成。Expo Router（基于 React Navigation）承担页面和底部导航；Expo 的文件选择、文件系统、安全存储和系统分享承担平台差异。

选择此栈是为了与现有 React/TypeScript 和 OpenAPI 类型衔接，同时使用原生文件接口。Web 桌面组件依赖 DOM，不直接搬到手机。纯协议代码和生成的类型直接引用现有来源，避免维护第二套协议。

## 模块

- **连接**：唯一持有 NAS 地址、Cookie、CSRF 和登录状态的位置。REST 客户端使用 OpenAPI 生成类型；所有携带凭据的 URL 必须属于当前 NAS。凭据放在系统安全存储，密码只在登录期间使用。客户端不申请本地控制台的无限期会话。
- **聊天**：复用 Hermes JSON-RPC Gateway 客户端与既有事件契约；将会话恢复、流式事件、停止和确认封装在聊天模块中。不会向 NAS 注册 WebOS 页面控制能力。
- **传输**：tus-js-client 负责上传协议；原生文件适配负责按块读文件。下载通过 Expo 流式网络与文件接口按块落盘，以 Range/If-Range 和 ETag 校验续传；任务记录按 NAS 和账号隔离。后台暂停并保存恢复信息，回到前台后由用户继续。
- **界面**：聊天和传输两个标签；连接设置、会话列表、NAS 文件选择是附属页面。界面不持有传输字节或密码副本。

NAS 仍是身份、权限、对话和文件的事实源。手机记录只用于连接恢复和本机传输展示。

## 局域网与后续远端接入

当前 NAS 入口允许 IP 地址，使用 HTTP。构建时通过 `ANAS_LAN_HOSTS` 指定测试 NAS 的局域网 IP；Android 网络安全配置和 iOS ATS 只对这些地址开放 HTTP，不全局关闭传输安全。客户端也校验相同名单，未配置的地址必须使用 HTTPS。iOS 声明局域网访问用途，首次连接由系统询问用户。不会关闭 NAS 的 CSRF、来源或会话绑定检查。

iPhone 最低 iOS 17，以使用 Apple [按 IP 设置 ATS 例外](https://developer.apple.com/documentation/BundleResources/Information-Property-List/NSAppTransportSecurity/NSExceptionDomains)的能力。Android 沿用当前 Expo 的 target SDK 36；未来升到 37 时，必须加入 [局域网运行时权限](https://developer.android.com/privacy-and-security/local-network-permission)的请求与拒绝恢复流程。

后续公网方案需要共同确定 HTTPS、设备身份、认证续期、WebSocket 转发和客户端地址绑定；不能把切换公网简单视为更改 base URL。系统推送也需要单独的服务端协议。

## 关联

- [移动端规格与验收](../specs/mobile-app.md)
- [工程与运行方法](../../mobile/README.md)
- [产品 API 契约](../development/api-contract.md)
- [系统边界](OVERVIEW.md)
