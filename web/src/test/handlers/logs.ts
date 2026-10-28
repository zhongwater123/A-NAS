// Logs (设置 → 日志): the activity log, the service logs and the accounts the
// activity log's member filter offers.
import type { ActivityPage, SystemLogPage, User } from "../../api.generated";
import { apiError, http } from "../server";

export interface LogsOptions {
  // activity answers each activity read by its query; nothing by default.
  activity?: (query: URLSearchParams) => ActivityPage;
  // systemLogs answers each service log read by its query; an Error stands for
  // a Host Agent that cannot be reached (logs_unavailable, with its message).
  systemLogs?: (query: URLSearchParams) => SystemLogPage | Error;
  users?: User[];
}

// logsHandlers answers both logs and the account list, and keeps the query of
// every read in the order they came, for a test to see what was asked.
export function logsHandlers(options: LogsOptions = {}) {
  const sent = { activity: [] as URLSearchParams[], systemLogs: [] as URLSearchParams[], users: 0 };
  const handlers = [
    http.get("/api/v1/users", ({ response }) => {
      sent.users += 1;
      return response(200).json({ items: options.users ?? [] });
    }),
    http.get("/api/v1/activity", ({ request, response }) => {
      const query = new URL(request.url).searchParams;
      sent.activity.push(query);
      return response(200).json(options.activity?.(query) ?? { items: [], scope: "all" });
    }),
    http.get("/api/v1/system-logs", ({ request, response }) => {
      const query = new URL(request.url).searchParams;
      sent.systemLogs.push(query);
      const page = options.systemLogs?.(query) ?? { items: [], dataSource: "simulated" };
      return page instanceof Error
        ? response(503).json(apiError("logs_unavailable", page.message))
        : response(200).json(page);
    }),
  ];
  return { handlers, sent };
}
