import { ScrollText, ServerCog } from "lucide-react";
import { useEffect } from "react";

import type { Session } from "../api.generated";
import { PageHeader } from "../settings/parts";
import { ActivityLog, type LogsPreset } from "./ActivityLog";
import { useRemembered } from "./common";
import { SystemLog } from "./SystemLog";
import "./logs.css";

export type { LogsPreset } from "./ActivityLog";

type Tab = "activity" | "system";

// LogsSection is Settings → 日志: everyone's activity and the service logs for
// administrators, only their own activity for members.
export function LogsSection({ session, preset }: { session: Session; preset?: LogsPreset }) {
  const isAdmin = session.user.role === "admin";
  const [tab, setTab] = useRemembered<Tab>(`${session.user.id}:logs-tab`, "activity");
  const [shownPreset, setShownPreset] = useRemembered(`${session.user.id}:logs-preset`, 0);
  useEffect(() => {
    if (!preset || preset.id === shownPreset) return;
    setShownPreset(preset.id);
    setTab("activity");
  }, [preset, shownPreset, setShownPreset, setTab]);
  const current = isAdmin ? tab : "activity";
  return (
    <>
      <PageHeader
        title={isAdmin ? "日志" : "我的活动"}
        subtitle={
          isAdmin
            ? "谁在什么时候登录、做了什么，以及 A-NAS 服务的运行日志"
            : "你的登录与操作，以及管理员对你账号所做的事"
        }
      />
      {isAdmin && (
        <div className="lg-tabs" role="tablist" aria-label="日志类型">
          {(
            [
              ["activity", "活动记录", ScrollText],
              ["system", "系统日志", ServerCog],
            ] as const
          ).map(([id, label, Icon]) => (
            <button
              key={id}
              role="tab"
              id={`logs-tab-${id}`}
              aria-selected={current === id}
              aria-controls={`logs-panel-${id}`}
              className={current === id ? "active" : ""}
              onClick={() => setTab(id)}
            >
              <Icon />
              {label}
            </button>
          ))}
        </div>
      )}
      <div
        role={isAdmin ? "tabpanel" : undefined}
        id={`logs-panel-${current}`}
        aria-labelledby={isAdmin ? `logs-tab-${current}` : undefined}
      >
        {current === "activity" ? <ActivityLog session={session} preset={preset} /> : <SystemLog />}
      </div>
    </>
  );
}
