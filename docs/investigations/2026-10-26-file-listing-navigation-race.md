# 旧目录响应覆盖当前文件列表，重命名变成移动

状态：fixed
更新时间：2026-10-26

## 症状与复现

[#188](https://github.com/zhongwater123/A-NAS/issues/188) 在 `6f25568` 仍可复现：进入加载较慢的目录后返回根目录，晚到的响应会在根目录面包屑下显示旧目录条目。粘贴进行期间进入另一个目录，粘贴结束也会把列表覆盖回原目录。此时重命名旧条目，会把它移动到面包屑所在的目录。

回归使用可手动兑现的 Promise 控制响应顺序，不依赖真实网络延迟：

1. 挂起 A 的列目录请求，导航 B 并返回 B 的条目，再返回 A 的条目。
2. 在 A 发起复制，等待期间进入 B，最后返回复制结果，检查随后的列目录请求和显示内容。
3. 给文件管理一个父目录不同于当前导航位置的条目，提交重命名，检查 PATCH 的 `parentId`。

修复前 `useFileListing.test.tsx` 的 8 个用例全部失败；交互测试中复制完成后的请求序列为根目录、B、根目录，重命名将 `folder:original` 错写为空字符串。根目录重命名的对照用例通过。

## 根因与处理边界

`useFileListing` 的所有请求直接写同一组状态，没有随导航或刷新失效。仅比较空间和目录还不够：A → B → A 时，第一次访问 A 的响应仍可能晚于第二次访问。旧请求的 catch/finally 也会污染新目录的错误和加载状态。

现有 React Effect 负责列目录，其依赖包含空间、父目录和刷新次数；清理时让该次请求的所有状态写入失效。采用 [React 的 Effect 清理模式](https://react.dev/reference/react/useEffect#fetching-data)，与现有文本预览及快照版本列表一致。这里没有缓存、分页或共享查询需求，沿用生成的 API 客户端及 React 生命周期即可，无需引入数据获取库或自建请求调度器。失效只忽略响应，不取消服务端已经开始的读取。

`reload` 只递增刷新次数，不捕获目录。异步操作保留的旧回调仍会刷新调用时打开的目录；写操作的实际目标仍是发起操作时选择的位置。切换目录立即清空旧条目，同目录刷新保留条目但显示加载状态；没有可见空间时清空列表并结束加载。

重命名沿用现有 PATCH 接口，以条目的 `parentId`（根目录为空）为目标，不传新空间。这独立于列表保护，避免导航位置被当作移动意图；不改变跨空间剪切、复制的语义。服务端并发移动或删除造成的冲突仍由现有接口处理。

## 验证

在 WSL ext4 检出副本中执行：

```sh
cd web
npm run format
npm test -- src/files/useFileListing.test.tsx src/files/FilePanel.navigation.test.tsx
npm run typecheck
npm run lint
npm test
npm run build
cd ..
make docs-check
```

回归覆盖跨目录、跨空间、A → B → A、同目录刷新乱序、旧响应的成功和错误、当前请求的失败、空空间、旧刷新回调、粘贴期间导航，以及根目录和子目录条目重命名。

## 关联

- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [`useFileListing.ts`](../../web/src/files/useFileListing.ts) 与[请求生命周期测试](../../web/src/files/useFileListing.test.tsx)
- [`FilePanel.tsx`](../../web/src/files/FilePanel.tsx) 与[导航交错交互测试](../../web/src/files/FilePanel.navigation.test.tsx)
- [WSL 检查流程](../../.claude/skills/check-in-wsl/SKILL.md)
