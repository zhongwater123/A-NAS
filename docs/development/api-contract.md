# API 契约开发

更新时间：2026-10-27

修改产品 API、重新生成客户端或补充响应校验时阅读本文。

## 接口契约

产品 API 的契约是 OpenAPI 3.1，按领域分文件（#220）：

- [`api/openapi.yaml`](../../api/openapi.yaml) 是根，逐条以 `$ref` 列出每个路径与组件，按领域分组；
- `api/paths/<领域>.yaml` 写路径与操作，`api/components/<领域>.yaml` 写参数、响应与 schema，跨领域共用的错误响应、CSRF 头与两个公共类型在 `api/components/common.yaml`；
- 新增路径或组件时写进它所属领域的文件，再在根文件同一领域的分组中加一行引用。根文件列出组件，打包后的组件名就是根文件中的键。

改了 `api/` 下的任一文件，运行 `make api`（或在 `web/` 中 `npm run generate`），把它写出的三个文件一起提交：

| 生成文件 | 来源与用途 |
|---|---|
| `api/openapi.bundle.yaml` | Redocly CLI 打包的单个文档；Go 契约测试经 `internal/openapitest` 加载它（`api` 包嵌入） |
| `web/src/api.generated.ts` | openapi-typescript 从打包结果生成的路径、操作与 schema 类型，每个 schema 以自己的名字导出 |
| `web/src/api.validators.generated.ts` | Ajv 预编译的响应校验函数，只覆盖 `tools/openapi/validators.mjs` 列出的操作 |

`make check` 的第一步 `make api-check` 先用 Redocly CLI 检查契约（`redocly lint`，规则在 [`api/redocly.yaml`](../../api/redocly.yaml)，逐条核对过的例外在 `api/.redocly.lint-ignore.yaml`），再重新生成这三个文件，与已提交的不一致即失败。Go 契约测试读的是打包结果，改了契约先运行 `make api` 再跑 `go test`。两个分支都改了契约时，生成文件冲突不必手工合并：取任一边，解决源文件的冲突后重新运行 `make api`。

生成工具在 [`tools/openapi`](../../tools/openapi/package.json) 中有独立的 npm 包，与 `tools/golangci-lint` 同理：openapi-typescript 依赖 TypeScript 5 的编译器 API，而 Web 客户端用 TypeScript 7。Makefile 以 `REDOCLY_TELEMETRY=off` 运行 Redocly CLI，并关闭更新检查，生成过程不联网。

Web 客户端只经 [`api.ts`](../../web/src/api/client.ts) 的 `client`（openapi-fetch）调用产品 API，类型从 `api.generated.ts` 取，不再手写接口：

- `call()` 把结果展开为数据，失败时抛出带服务端错误码的 `APIError`；二进制与 multipart 请求体经 `rawBody()` 原样发送。
- CSRF 令牌由客户端按方法添加：除 GET 与 HEAD 外的请求都带 `X-CSRF-Token`，与服务端 `withMutation` 的划分一致。带进度的上传（XHR）与 tus 也从 `csrfHeader(method)` 取同一规则，调用方不再传参。
- 桌面外壳在窗口错误边界之外读取的响应（登录、状态栏、通知、头像、壁纸、应用快捷方式、VNAS、分享提醒、传输提示与问题反馈），由客户端中间件按契约校验，不符合的响应让这次调用以 `malformed_response` 失败，而不是让桌面空白（[10-07 的空白桌面](../investigations/2026-10-07-blank-disk-plan-ui-crash.md)）。校验只管形状（类型、必有字段、枚举与 `null`），格式与字符串长度留给服务端：服务端按字符数限制昵称，生成的校验只能按 UTF-16 单元计数。外壳新读取一个接口时，把操作加进 `validators.mjs` 的列表并运行 `make api`；这些响应在 Go 侧也要有契约测试，用 `openapitest.Check` 校验请求与响应（如 `internal/httpapi/shell_contract_test.go`）。

Web 测试由 [MSW](https://mswjs.io/) 应答产品 API（[`test/server.ts`](../../web/src/test/server.ts)）。处理器用 openapi-msw 按 `api.generated.ts` 的路径与类型编写，按领域放在 [`test/handlers/`](../../web/src/test/handlers/shell.ts)，测试数据也以生成的类型声明：路径不在契约中、状态码或响应形状不对时无法通过类型检查，外壳的响应在运行时还会被客户端拒绝。没有处理器应答的请求让发出它的测试失败（[`test/setup.ts`](../../web/src/test/setup.ts)）。jsdom 的 `FormData` 进不了 Node 的 `Request`，[`test/forms.ts`](../../web/src/test/forms.ts) 为处理器留下原表单（`formOf`）；带进度的上传与 tus 在测试中替换 `XMLHttpRequest` 或 tus-js-client。

## 关联

- [开发环境入口](LOCAL_ENVIRONMENT.md)
- [测试与验证](testing.md)
- [OpenAPI 根文件](../../api/openapi.yaml)
