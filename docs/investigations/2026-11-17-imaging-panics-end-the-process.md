# imaging 在自己的 goroutine 里 panic，服务进程随之退出

状态：resolved；问题只取决于图片内容，回归测试用像素超出调色板的图片完整复现，实机没有可补充的证据
更新时间：2026-11-17

## 症状与影响

`github.com/disintegration/imaging` 的最新版本 1.6.2 有 CVE-2023-36308（GHSA-q7pp-wcgr-pffx，[上游 issue](https://github.com/disintegration/imaging/issues/165) 未修）：按调色板读像素时不检查下标，像素超出调色板就 panic。Dependabot 告警 #2 报出这个 CVE，没有可升级的版本。

相册、照片打分、影视海报、音乐封面、头像、壁纸和任务图标都用 imaging 缩放图片。这个 panic 发生在 imaging 自己起的 goroutine 里，[不变量 18](../architecture/OVERVIEW.md) 的解码保护恢复不了，服务进程退出；后台扫描下次遇到同一张图片又会退出。

## 根因

imaging 的 `scanner.scan` 对 `*image.Paletted` 执行 `s.palette[img.Pix[i]]`，不检查下标。缩放、裁剪、粘贴、叠加、`Clone` 与各个翻转、旋转函数都经 `parallel` 把扫描分给新起的 goroutine，调用方的 `recover` 管不到那里。

不变量 18 原来假定解码保护能接住解码与像素处理中的所有 panic，这对 imaging 的变换不成立。

目前注册的解码器不交出这样的图片：x/image 0.47.0 的 TIFF 与 BMP 解码器分别返回 “invalid color index” 与 “invalid palette index”，`image/gif` 返回 “invalid pixel value”，`image/png` 把调色板延长到覆盖该像素，WebP 与 JPEG 不产生调色板图片。但这取决于第三方解码器，新增或更换解码器就可能失效。

## 修复与回归证据

- 修复：交给 imaging 的图片都经 [`imagerender.Decode`](../../internal/imagerender/imagerender.go) 解码。它在 imaging 处理之前把像素超出调色板的图片当作 `ErrUnsupported`，与解码器读不了的图片一样处理。`WithDecoded`（相册与照片打分）以及头像、壁纸、影视海报、音乐封面和任务图标都改用它。
- 测试：[`imagetest.OutsidePalettePicture`](../../internal/imagetest/imagetest.go) 注册的解码器交出两色调色板中的像素 70，即公告中的例子。[imagerender](../../internal/imagerender/imagerender_test.go)、相册、头像、壁纸、影视、音乐与任务图标的测试都用它，要求结果为无法解码且没有记录 panic。去掉检查后，头像、壁纸、音乐与任务图标的测试进程以 `index out of range [70] with length 2` 退出，栈在 `imaging.(*scanner).scan`；其余三组测试失败。

## 后续工作

- imaging 其他缺陷造成的 panic 仍会结束进程。要彻底避免，需要把缩放与转向换成在调用方 goroutine 里运行的实现。目前没有已知的其他触发。
- GitHub 不会自动关闭告警 #2，可以按 “Vulnerable code is not actually used” 关闭并链接本文。

## 关联

- 规格：不涉及
- ADR：不涉及
- 运行手册：不涉及
- [不变量 18](../architecture/OVERVIEW.md)、[测试指南的安全扫描](../development/testing.md#安全扫描)
- [`imagerender.Decode`](../../internal/imagerender/imagerender.go) 与[测试](../../internal/imagerender/imagerender_test.go)、[测试图片](../../internal/imagetest/imagetest.go)
