import { CircleAlert } from "lucide-react";

import type { HostState } from "../api";
import type { HostStateView } from "../useHostState";
import { Banner } from "./parts";

// How the overview and storage sections show the shared host snapshot while
// it loads, fails or ages.

export function HostStatePending({ host, inline }: { host: HostStateView; inline?: boolean }) {
  const className = inline ? "set-state" : "set-card set-state";
  if (host.loading)
    return (
      <div className={className}>
        <span className="loader" />
        <h4>正在读取设备状态…</h4>
        <p>正在连接 A-NAS 产品服务</p>
      </div>
    );
  return (
    <div className={className}>
      <CircleAlert />
      <h4>暂时无法读取设备状态</h4>
      <p>请确认产品服务和 Host Agent 正在运行。</p>
      <button className="set-btn primary" onClick={() => void host.refresh()}>
        重新连接
      </button>
    </div>
  );
}

export function HostStateBanner({ host, snapshot }: { host: HostStateView; snapshot: HostState }) {
  if (host.disconnected) return <Banner tone="error">连接中断，正在显示上次成功读取的数据</Banner>;
  if (isStale(snapshot)) return <Banner tone="warning">实时观测时间较早，正在等待新数据</Banner>;
  return null;
}

export function isStale(state: HostState): boolean {
  return state.dataSource === "live" && Date.now() - new Date(state.observedAt).getTime() > 30_000;
}
