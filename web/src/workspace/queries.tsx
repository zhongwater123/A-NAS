import { QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type ReactNode, useEffect, useState } from "react";

import { APIError } from "../api/client";
import { Failed } from "./parts";

// WorkspaceQueries is the query cache of 共享工作区's window or of one page's
// tab. An account an administrator takes out keeps nothing it had read: the
// first answer that says so empties the view and says why.
export function WorkspaceQueries({ className, children }: { className: string; children: ReactNode }) {
  const [forbidden, setForbidden] = useState<APIError>();
  const [client] = useState(() => {
    const onError = (error: unknown) => {
      if (error instanceof APIError && error.code === "workspace_forbidden") setForbidden(error);
    };
    return new QueryClient({
      queryCache: new QueryCache({ onError }),
      // A request the service refused (4xx) is not tried again; one it could
      // not answer is, once.
      defaultOptions: {
        queries: {
          retry: (failures, error) => failures < 1 && !(error instanceof APIError && error.status < 500),
          staleTime: 2_000,
          refetchOnWindowFocus: false,
        },
      },
    });
  });
  useEffect(() => {
    if (forbidden) client.clear();
  }, [forbidden, client]);
  return (
    <QueryClientProvider client={client}>
      {forbidden ? (
        <div className={className}>
          <Failed error={forbidden} onRetry={() => setForbidden(undefined)} />
        </div>
      ) : (
        children
      )}
    </QueryClientProvider>
  );
}
