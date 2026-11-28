# 剪辑中心的网页（web/src/videocut/）

Venus 的三个页面，几个人按 PRD 编号分头开发，各自合并后要能直接跑。根目录与 [web/](../../AGENTS.md) 的 AGENTS.md 仍然适用；结构见[技术设计](../../../docs/architecture/videocut-center.md#venus-改版的网页结构)，行为见[规格](../../../docs/specs/venus.md)。范例：M7-1 的 `screens/cut/ThisShot.tsx` 与它的测试。

- **只替换自己的插槽：** 把写着自己 PRD 编号的 `<Slot id="…" />` 换成组件，组件放在对应页面的 `screens/` 目录。不改 `screens/layout.module.css`，不挪别人的插槽；放不下就提给骨架负责人。
- **骨架不顺手改：** `VideoCutPanel.tsx`、`app/`、`ui/`、`lib/pages.ts` 大家共用，要改先提 issue 或在 PR 里说明，经骨架负责人审，不在功能 PR 里顺带重写。`app/features.ts` 里留给某功能补的 hook，由那个功能补全，签名不变。
- **分层：** 页面与 `ui/` 只用 `app/` 的 hook，不 import `api/`（Oxlint 拦，不许改规则绕过）；媒体地址从 `app/media.ts` 取。
- **镜头操作只经 `useEditor`：** 加锁、修订号、重放与提示都在里面；按钮用 `ui/ActionButton`，它自己读操作锁。成片页各块只读写 `useCutStore`，不互相调用。
- **不另做公共组件：** 提示用 `useToast`，覆盖提示用 `useBanner`，确认用 `ConfirmDialog`，空态用 `Empty`；时间与数字用 `lib/time.ts` 或 `Intl`。
- **文案写进自己页面的文件：** `ui/messages/` 按页面分文件，几页共用的放 `shared.ts`；组件里不写中文，键名不重复（测试会查）。
- **改契约：** 改 `api/` 的剪辑文件后运行 `make api`，生成文件不手改；新接口在 `mocks/handlers.ts` 加替身（测试会查）。后端实现一个操作，就删它的替身、501 路由与契约里的 501。
- **测试在替身上：** 每个功能至少一条组件测试，用 `VenusFake` 与 `videocutHandlers` 走自己的流程。看界面用 [Fake 预览](../../../.agents/skills/fake-preview/SKILL.md)，构建时加 `VITE_VIDEOCUT_STANDINS=1`。
- **资源先定级别**（技术设计的“资源分级”）：页面级的离开页面就释放，用 `app/resources.ts`；快捷键加在 `app/shortcuts.ts`。
- **验证：** `npm run format` 后跑 `npx vitest run src/videocut`、`npm run typecheck`、`npm run lint`，交 PR 前 `make check`。
