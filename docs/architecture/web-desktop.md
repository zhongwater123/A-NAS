# Web 桌面结构与约定

Web 桌面是 `web/` 下的 React + TypeScript 单页应用（[ADR 0003](../adr/0003-use-react-typescript-for-web-desktop.md)），由 Vite 构建后嵌入产品二进制。本页写代码放在哪里、样式与数据怎样组织；产品接口的契约与生成见[本地环境](../development/LOCAL_ENVIRONMENT.md#接口契约)，检查命令见[静态检查](../development/LOCAL_ENVIRONMENT.md#静态检查)。

## 目录

`web/src` 的根目录只放入口与生成文件，其余代码都在下面的目录中：

| 位置 | 内容 |
|---|---|
| `main.tsx`、`App.tsx` | 入口：根错误边界，登录与桌面之间的切换 |
| `api.generated.ts`、`api.validators.generated.ts` | `make api` 由契约生成，不手改 |
| `styles/` | 全局样式：层顺序、重置、[令牌](#令牌) |
| `api/` | `client.ts`：生成客户端的封装，包括请求、`APIError`、CSRF、写入闸门与带进度的上传；`errorMessages.ts`：错误码到文案的表（[规格](../specs/web-errors-and-time.md)） |
| `desktop/` | 桌面外壳：窗口注册表 `apps.tsx`、窗口状态、`AppWindow`、程序坞、系统快捷栏、状态栏、桌面图标、通知、壁纸与屏保；外壳读取的接口在 `desktop/api.ts` |
| 功能目录 | 每个应用窗口一个目录：`files`、`trash`、`settings`、`appcenter`、`docker`、`media`、`music`、`photos`、`backup`、`transfers`、`shares`、`assistant`；设置窗口的分区 `logs`、`taskmanager`；桌面上的 `feedback` |
| 领域目录 | 不是窗口的领域：`auth`（登录与会话）、`people`（各账号的名字与头像）、`boot`（开机画面） |
| `ui/` | 不认识任何功能的共享组件、钩子与样式 |
| `lib/` | 不依赖 React 与接口的函数：容量与时间格式化、文件名与类型、剪贴板、读取拖入的文件 |
| `test/` | 测试基础设施：按领域的请求处理器、测试数据与渲染帮助函数 |
| `assets/` | 静态图片 |

功能目录内部：

- `api.ts` 写本功能的接口调用，全部经 `api/client.ts` 的 `client`，类型取自 `api.generated.ts`；
- 窗口组件由 `desktop/apps.tsx` 懒加载；
- 组件的样式是旁边的 `*.module.css`；
- 测试与被测代码放在一起。

依赖只朝一个方向：`lib` ← `ui` ← 功能 ← `desktop` ← `App`。

- 一个功能可以使用另一个功能的 `api.ts`、模型与它为别人导出的组件，例如文件管理用备份中心的历史版本对话框；
- 不能使用另一个功能的样式与内部部件。两个功能都要用的部件移进 `ui/`。

## 窗口

- **注册：** 每个内置应用在 `desktop/apps.tsx` 中登记标题、图标、初始位置、启动器条目与窗口内容。
- **懒加载：** 窗口组件一律用 `lazy()` 加载，首次打开时才下载代码与样式。图标随外壳一起加载，桌面与程序坞不必等窗口的代码。
- **渲染：** 窗口几何与窗口内容分开。拖动与缩放时窗口自己跟随指针，松开后才告诉桌面；窗口内容是 `memo` 组件，桌面给它的上下文只在内容确实变化时才是新对象。所以拖动、缩放与切换焦点不会重新渲染其他窗口。
- **打开与跳转：** 窗口之间只通过桌面的 `open(id, intent)` 通信，意图的类型在 `WindowIntentPayloads` 中，例如文件管理的某个文件夹、相册的某张照片。窗口按对象身份比较意图：每次请求都是新对象，所以再次请求同一个地方仍会生效。窗口处理完后调用 `onIntentHandled`，桌面随即清除这个意图。
- **出错：** 错误边界来自 react-error-boundary。
  - 每个窗口有自己的边界，“重新打开”只重新挂载这一个窗口；
  - 窗口之外的桌面部件（快捷栏、图标、状态栏、程序坞、通知与提示）各有边界，出错时只有它自己消失；
  - 根边界兜底；`createRoot` 的 `onUncaughtError` 处理连兜底页都显示不了的情况。

## 样式

### 方案

- **组件样式：** 用 CSS Modules。样式写在组件旁的 `*.module.css` 中，类名在构建时加上哈希，一个功能的规则不会作用到另一个功能。TSX 中以 `styles.camelCase` 引用（`localsConvention: "camelCaseOnly"`）。
- **全局样式：** 只有 `styles/` 中的重置、令牌与第三方样式。
- **层：** 层的顺序只在 `styles/index.css` 的第一行声明一次。
  - `ui/` 的模块写在 `@layer ui` 中；
  - 功能的模块不进任何层，因此总是覆盖 `ui/` 与全局样式，结果与导入顺序无关；
  - 例外：带 `!important` 的声明按相反的层顺序比较，不要用它覆盖 `ui/` 的规则。
- **不跨功能借用：** 两个功能需要同样的外观时，把部件与样式移进 `ui/`，不要导入另一个功能的样式文件。

### 令牌

`styles/tokens.css` 的 `:root` 定义整个桌面共用的值：

- 颜色：文字、次要文字、分隔线、表面、强调色与状态色。危险色只有一个，即 `--color-danger`。
- 阴影、圆角、间距与字体。

规则：

- 样式只引用令牌；
- 应用自己的强调色写成该应用根元素上的局部变量；
- 新增一个值之前，先看已有令牌能否满足。

## 数据获取

- 接口调用写在各功能的 `api.ts` 中。
- 数据获取迁移到 TanStack Query（[#271](https://github.com/zhongwater123/A-NAS/issues/271)）：
  - 查询键按领域划分；
  - 请求用 `signal` 取消；
  - 窗口不可见时停止轮询。
- 迁移完成前，新代码不要再手写缓存、去重与轮询。

## 界面基础组件

- 对话框、菜单、提示与悬浮卡片使用 Radix（`radix-ui` 已是依赖），封装后放在 `ui/` 中（[#268](https://github.com/zhongwater123/A-NAS/issues/268)）。
- 功能不再自己实现焦点约束、Esc 与方向键。

## 格式化

- 容量：`lib/bytes.ts`（[容量单位](../specs/size-units.md)）。
- 时间：`lib/time.ts`，基于 `Intl`。
- 错误文案：`api/errorMessages.ts`（[规格](../specs/web-errors-and-time.md)）。
- 不在功能中另写格式化函数。

## 测试

- 用 Vitest 与 Testing Library。
- **请求：** 由 MSW 拦截。
  - 处理器用 openapi-msw 按契约的路径与类型编写，放在 `test/handlers/<领域>.ts`，路径或响应形状不对时类型检查失败；
  - 未处理的请求直接让测试失败（`onUnhandledRequest: "error"`）。
- **渲染：**
  - 窗口的用例直接渲染该窗口组件；
  - `App.test.tsx` 只测外壳：登录、桌面、窗口管理与跨窗口跳转。
- **查询元素：** 按角色与可访问名称，不按类名；CSS Modules 的类名带哈希。

## 包体积

`make web-build` 在构建后用 size-limit 检查首屏要下载的 JavaScript 与 CSS，上限写在 `web/.size-limit.json` 中，超过即失败。上限只在有意增加首屏内容时上调，并在 PR 中写明原因。

## 关联

- [ADR 0003：Web 桌面技术决策](../adr/0003-use-react-typescript-for-web-desktop.md)
- [架构总览](OVERVIEW.md)
- [Web 错误提示与时间格式](../specs/web-errors-and-time.md)
- [窗口注册表](../../web/src/desktop/apps.tsx)、[窗口状态](../../web/src/desktop/windows.ts)
- [接口客户端](../../web/src/api/client.ts)
- Issue [#195](https://github.com/zhongwater123/A-NAS/issues/195)、[#274](https://github.com/zhongwater123/A-NAS/issues/274)、[#275](https://github.com/zhongwater123/A-NAS/issues/275)
