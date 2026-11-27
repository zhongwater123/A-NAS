# Web 桌面（web/）

在本目录改代码前要知道的约定；根目录的 [AGENTS.md](../AGENTS.md) 仍然适用。结构与约定的权威说明在 [Web 桌面结构与约定](../docs/architecture/web-desktop.md)。

- 产品 API 的类型与响应校验从契约生成（`src/api.generated.ts`、`src/api.validators.generated.ts`），不手改、不手写接口类型；改契约见 [api/AGENTS.md](../api/AGENTS.md)。调用只经 `src/api/client.ts` 的 `client`。
- 对话框、菜单、提示与悬浮卡片用 `radix-ui`，封装在 `src/ui/`；容量、时间与错误文案用 `src/lib/` 和 `src/api/errorMessages.ts`，不在功能里另写。新代码不手写缓存、去重与轮询（迁移到 TanStack Query，#271）。
- 样式用 CSS Modules 与 `src/styles/tokens.css` 的令牌；窗口一律经 `src/ui/lazyCode.ts` 懒加载。首屏体积有上限（`.size-limit.js`），超出即构建失败。
- 界面设计与文案按[界面设计与文案](../docs/architecture/web-desktop.md#界面设计与文案)：一次到位、文案只写事实与操作、不动 `.desktop-shell` 的尺寸与溢出。
- 测试：Vitest 与 Testing Library，请求由 MSW 按契约应答（`src/test/handlers/`），按角色与可访问名称查元素。单个文件：`npx vitest run src/<路径>`；类型检查：`npm run typecheck`。
- 组件里的 `setTimeout` 与 `setInterval` 在卸载时清掉：测试结束、jsdom 拆掉后才触发的计时器报 `window is not defined`，整轮 Vitest 失败（提示框的计时器先后让 #253、#683 的 CI 偶发失败）。
- 格式化：提交前在仓库根运行 `make fmt`（编辑后的钩子也会运行）；整个目录可用 `npm run format`。
