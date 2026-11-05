# A-NAS 移动端

Android 与 iPhone 的 React Native / Expo 客户端。底部只有聊天、传输，使用局域网 NAS 上的账号、VNAS 和传输接口。

## 开发

在本目录执行 `npm ci`。把 `.env.example` 复制为 `.env.local`，将 `ANAS_LAN_HOSTS` 改为测试 NAS 的 IP（不带协议和端口）；多个地址用逗号分隔。原生网络例外随构建生成，修改地址后需要重新构建。应用中填写完整地址，如 `http://192.168.1.10`。此版本不支持公网地址。

- Android：安装 JDK 21、Android SDK，设置 `ANDROID_HOME`，连接开启 USB 调试的手机，执行 `npm run android -- --variant release`。首次构建需要下载所用版本的 SDK/NDK；这里的 release 使用本地测试签名，不是商店发布包。
- iPhone（iOS 17 或更新）：在 macOS 安装 Xcode 与 CocoaPods，执行 `npm run ios -- --device --configuration Release`，按 Xcode 提示选择开发团队签名。首次使用允许局域网访问。此最低版本用于支持按 NAS IP 设置的 HTTP 例外。
- 上述测试构建内置 JS，不依赖电脑上的 Metro。开发时可用 `npm start` 启动 Metro，但需把实际开发服务器 IP 加入构建地址名单并重新生成原生工程；Expo Go 不能代替最终原生验收。
- `npm run typecheck`、`npm run lint`、`npm test` 检查应用。
- `npm run bundle` 分别生成 Android 与 iOS 的 JS/资源包，不能代替 APK、IPA 构建或真机验证。

密码只在登录过程中使用。会话 Cookie 保存在系统安全存储；传输记录不含 Cookie、CSRF 或密码。切换账号和 NAS 会暂停传输；每个账号分别保存记录与本机文件。下载保存在应用私有目录，通过“打开 / 保存到手机”调用系统分享导出。

上传使用 tus-js-client；下载使用 Expo 流式网络及文件接口按块落盘，以 Range/If-Range 校验续传。后台暂停，回到前台手动继续；不承诺系统终止进程后继续传输。无 ETag 的下载重试从头开始。会话随 NAS 的过期和客户端绑定规则失效，不在客户端绕过。

`glob` 与 `minimatch` 的 overrides 修复 Expo 构建/检查依赖树中的已公布漏洞；升级 Expo 时重新审查，不能用 npm 建议的降级 Expo 主版本来掩盖它们。

## GitHub 云构建

[Mobile builds](../.github/workflows/mobile.yml) 在开发分支推送移动端改动时运行，也可手动指定 NAS IP。先检查协议、类型、代码和两端资源包，再分别构建 Android ARM64 测试 APK 与 iPhone 模拟器应用，并保留 7 天构建产物。云构建不会连接实际 NAS。

iOS 任务使用 GitHub 的 macOS/Xcode 环境，无需本地 Mac；启动模拟器后保存登录页截图。模拟器应用不能安装到真实 iPhone。真机 IPA 需要另行配置 Apple 开发证书和匹配的描述文件，目前工作流没有签名或发布步骤。

## 验证记录

原生设备验收清单见规格。Windows 本地的协议测试用独立回环 HTTP/WebSocket 服务验证真实客户端逻辑，不访问实验 NAS；其回答是协议夹具，不代表真实 VNAS 推理已经通过。

测试覆盖登录和 CSRF、会话恢复、跨来源地址拒绝、VNAS JSON-RPC 流式回答、重连不重复提交、对话审批隔离、tus 上传暂停续传后的 SHA-256 一致性，以及下载的 Range、ETag、截断、版本变化与空文件。

2026-11-05：21 项协议测试、TypeScript 和 ESLint 检查通过；Expo Doctor 20/20 通过；`npm audit --omit=dev` 无漏洞；Android 与 iOS 的 JS/资源构建通过；`make docs-check` 通过。NAS `http://172.18.45.48` 的会话入口可达，未登录返回 401；尚未完成真实账号、手机与 VNAS 联调。Windows 不能本地编译 iOS，iPhone 测试仍需 macOS/Xcode 构建及开发签名。

## 关联

- [范围与双端验收](../docs/specs/mobile-app.md)
- [模块与技术栈](../docs/architecture/mobile-app.md)
- [NAS 连接](src/nas/client.ts)
- [聊天模块](src/chat/controller.ts)
- [传输模块](src/transfers/engine.ts)
- [协议测试](tests/client.test.ts)
