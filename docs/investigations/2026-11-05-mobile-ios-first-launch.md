# iOS 模拟器首次启动显示连接错误

状态：resolved，2026-11-05 在云端 iOS 模拟器证实正常首次启动，回归断言通过。

## 现象与边界

[首轮构建](https://github.com/zhongwater123/A-NAS/actions/runs/41961148652)成功编译 Release 模拟器应用，安装与启动命令也返回成功。但 `ios-login.png` 中，未输入账号的登录页已经显示“连接未完成”。因此，构建成功不能作为首次启动正常的证据。

首次启动会先从 SecureStore 恢复会话；没有已存会话时应直接进入空白登录页，不发送 NAS 请求。云端不能访问实验 NAS 不是这条路径预期的失败原因。当前通用错误映射不足以区分本机凭据存储失败与网络失败，不能从提示文字断定网络有问题。

## 可复现检查

从构建产物提取 `ios-login.png`，在 macOS 执行：

```sh
swift scripts/mobile-assert-login.swift ios-login.png
```

原截图的输出包含登录页、配置地址和连接错误，退出码为 1；缺少登录页或地址时为 2。该断言已在云端实际捕获原问题。

## 调查

第一项假设是 `CODE_SIGNING_ALLOWED=NO` 产生的模拟器应用缺少 Keychain 权限。使用原构建产物，只补模拟器临时签名与 Keychain entitlement，避免同时改变应用代码。该签名不使用开发者账号，也不用于 iPhone 真机安装。

[首次对照](https://github.com/zhongwater123/A-NAS/actions/runs/41965504967)在启动应用时被模拟器的 `SBMainWorkspace` 拒绝，尚未执行到登录页断言，不能据此确认或排除 Keychain 假设。[重试对照](https://github.com/zhongwater123/A-NAS/actions/runs/41966228127)仍被拒绝；日志记录 `NSPOSIXErrorDomain code 162`，安装器同时认为可执行文件没有 entitlement。

模拟器的 entitlement 需要进入 Mach-O 段，不能仅按真机方式写进代码签名（[构建工具的说明](https://github.com/bazelbuild/rules_apple/blob/main/doc/common_info.md#codesign-bundles-for-the-simulator-appleskip_codesign_simulator_bundles)）。移除手工补签名的实验脚本，改由 Xcode 正常生成模拟器 entitlement 与 ad-hoc 签名；保留登录页断言作为回归检查。

[修正后的构建](https://github.com/zhongwater123/A-NAS/actions/runs/41966920493)在应用运行代码不变的情况下通过编译、模拟器安装、启动及登录页断言。截图与 OCR 输出显示 NAS 地址和空白账号、密码输入，没有连接错误。因此问题在云端禁用模拟器签名与 entitlement 生成的构建方式，而非 NAS 网络；没有取得原始 Keychain 错误码，不推断具体系统错误码。Android 构建和协议检查也通过。

## 关联

- [原生构建与启动检查](../../.github/workflows/mobile.yml)
- [登录页断言](../../scripts/mobile-assert-login.swift)
- [凭据存储](../../mobile/src/nas/native.ts)
- [会话恢复](../../mobile/src/runtime.tsx)
- [移动端验收](../specs/mobile-app.md)
