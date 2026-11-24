# 产品 API 契约（api/）

在本目录改契约前要知道的约定；根目录的 [AGENTS.md](../AGENTS.md) 仍然适用。分文件布局、生成工具与客户端的权威说明在 [API 契约开发](../docs/development/api-contract.md)。

- 契约是 OpenAPI 3.1，按领域分文件：路径写在 `paths/<领域>.yaml`，参数、响应与 schema 写在 `components/<领域>.yaml`，再在根文件 `openapi.yaml` 同一领域的分组里加一行引用。
- 改了本目录的任一文件，在仓库根运行 `make api`（或在 `web/` 中 `npm run generate`），把它写出的 `api/openapi.bundle.yaml`、`web/src/api.generated.ts`、`web/src/api.validators.generated.ts` 一起提交；三个生成文件不手改。`make api-check` 在它们过期时失败。
- Go 的契约测试经 `internal/openapitest` 加载打包文档：改契约后先 `make api` 再 `go test`。产品服务新增的路由没有写进契约时，`internal/httpapi` 的路由测试失败。
- 错误响应、CSRF 头等共用部分在 `components/common.yaml`；契约检查的规则与逐条核对过的例外见 `redocly.yaml` 与 `.redocly.lint-ignore.yaml`。
