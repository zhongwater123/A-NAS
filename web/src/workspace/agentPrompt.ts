import type { AgentKind } from "./api";

// The MCP server's name in the member's agent.
const serverName = "a-nas";

// connectionCheck is the request a Codex sends once to see that it reaches
// the workspace with its token. The Go chain test sends the same request
// (internal/workspacemcp/testdata/connection-check.json).
export const connectionCheck = {
  body: '{"jsonrpc":"2.0","id":1,"method":"tools/list"}',
  contentType: "application/json",
  accept: "application/json, text/event-stream",
  answerHas: "workspace_context",
};

// defaultAgentName names the agent after the computer the browser runs on,
// where the member's agent usually runs too.
export function defaultAgentName(userAgent = navigator.userAgent): string {
  if (/Windows/.test(userAgent)) return "Windows 电脑";
  if (/Macintosh|Mac OS X/.test(userAgent)) return "Mac 电脑";
  if (/Linux|X11/.test(userAgent) && !/Android/.test(userAgent)) return "Linux 电脑";
  return "";
}

const intro =
  "请把我们团队 NAS 上的“A-NAS 共享工作区”接入为你的 MCP 服务器，接好后告诉我怎么用。\n\n" +
  "共享工作区是团队一起写 HTML 页面（例如 PRD）的地方：我在浏览器里打开页面、选中要改的元素，再让你改。" +
  "下面的令牌是 A-NAS 刚为你生成的，只能用于共享工作区，我可以在 A-NAS 的“设置 → 我的账号 → 已连接的 agent”里撤销它。" +
  "不要在回复里重复令牌。";

const failures =
  "401 是令牌不对或已撤销，403 是我还不能用共享工作区，连不上是这台电脑到 NAS 的网络不通：告诉我是哪一种。";

const done = (fresh: string) =>
  `告诉我接好了：${fresh}就能用；以后在共享工作区的页面里点“选取”选中元素，再对你说“把这个改小”这样的话。`;

// promptFor is what the member sends to their agent, which then connects
// itself to the workspace at endpoint with token on whatever system it runs
// on. Members paste it without a word of their own, so it says what the
// workspace is, why the token is there and what to tell the member at the end.
export function promptFor(kind: AgentKind, endpoint: string, token: string): string {
  if (kind === "codex") {
    const { body, contentType, accept, answerHas } = connectionCheck;
    return `${intro}

1. 打开 Codex 的配置文件 config.toml，没有就新建：设了 CODEX_HOME 时在那个目录里，否则 macOS 与 Linux 上是 ~/.codex/config.toml，Windows 上是 %USERPROFILE%\\.codex\\config.toml。删掉已有的 [mcp_servers.${serverName}] 表与它的子表，在文件末尾加上：

[mcp_servers.${serverName}]
url = "${endpoint}"
http_headers = { Authorization = "Bearer ${token}" }

2. 运行 codex mcp get ${serverName}，确认读到的是这个地址。

3. 试连一次，回答里有 ${answerHas} 就是连上了。
macOS 与 Linux：
curl -sS -X POST "${endpoint}" -H "Authorization: Bearer ${token}" -H "Content-Type: ${contentType}" -H "Accept: ${accept}" -d '${body}'
Windows PowerShell：
(Invoke-RestMethod -Method Post -Uri "${endpoint}" -Headers @{ Authorization = "Bearer ${token}"; Accept = "${accept}" } -ContentType "${contentType}" -Body '${body}').result.tools.name
${failures}

4. ${done("重新开一个 Codex 会话")}`;
  }
  return `${intro}

1. 删掉以前可能加过的同名服务（没有时会报错，忽略即可）：
claude mcp remove ${serverName} --scope user

2. 加上共享工作区，macOS、Linux 与 Windows 上都是这一行：
claude mcp add --transport http --scope user ${serverName} ${endpoint} --header "Authorization: Bearer ${token}"
没有 claude 命令时，用你自己的方式把同样的设置（HTTP、这个地址、这个 Authorization 请求头）加到用户级的 MCP 配置里。

3. 运行 claude mcp get ${serverName}，确认已连接。${failures}

4. ${done("开一个新对话")}`;
}
