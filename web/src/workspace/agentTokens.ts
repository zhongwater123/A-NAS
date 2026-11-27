// The account's agent tokens (ADR 0040) in one cache for the whole page:
// 连接 agent in 共享工作区 and 设置 → 我的账号 → 已连接的 agent read and change
// the same list, so a token made or revoked in one window shows in the
// other. The key carries the account: a page signed in as someone else never
// shows the last account's agents.
import { QueryClient, useQuery } from "@tanstack/react-query";

import { type AgentToken, listAgentTokens } from "./api";

export const agentTokenClient = new QueryClient({ defaultOptions: { queries: { retry: 1 } } });
// Mounted for the page's life, so it reads again when the page comes back
// into view, as todos/queries.ts does.
agentTokenClient.mount();

export const agentTokensKey = (account: string) => ["agent-tokens", account] as const;

// useAgentTokens is the account's tokens; poll reads again that often while
// it answers a number.
export function useAgentTokens(account: string, poll?: (tokens?: AgentToken[]) => number | false) {
  return useQuery(
    {
      queryKey: agentTokensKey(account),
      queryFn: ({ signal }) => listAgentTokens(signal),
      refetchInterval: poll && ((query) => poll(query.state.data)),
    },
    agentTokenClient,
  );
}
