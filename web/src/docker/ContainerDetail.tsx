import { ChevronLeft, CircleAlert, Cpu, ExternalLink, HeartPulse, MemoryStick, Play, RotateCw, ScrollText, Square } from "lucide-react";
import { ReactNode } from "react";

import { formatBytes } from "../bytes";
import { ContainerSummary } from "../containersApi";
import { ContainerLogs } from "./ContainerLogs";
import type { ContainerActions } from "./DockerPanel";
import { AppRef, Condition, describeContainer, formatDateTime, formatPercent, standaloneTitle, webPorts } from "./model";
import { ContainerGlyph, StatePill } from "./parts";

export type DetailTab = "overview" | "logs";

interface DetailProps {
  item: ContainerSummary;
  app?: AppRef;
  tab: DetailTab;
  backLabel: string;
  actions: ContainerActions;
  onTab: (tab: DetailTab) => void;
  onBack: () => void;
}

export function ContainerDetail({ item, app, tab, backLabel, actions, onTab, onBack }: DetailProps) {
  const condition = describeContainer(item);
  const pending = actions.pending[item.id];
  const failure = actions.failures[item.id];
  const ports = webPorts(item, app);
  // An app's own web port first, so 打开 goes to its interface rather than an API port.
  const primary = ports.find((port) => port.hostPort === app?.webPort) ?? ports[0];
  const alive = item.state === "running" || item.state === "paused" || item.state === "restarting";
  const busy = Boolean(pending);

  return (
    <div className={`dk-detail ${tab}`}>
      <button className="dk-back" onClick={onBack}><ChevronLeft />{backLabel}</button>
      <header className={`dk-head ${condition.tone}`}>
        <ContainerGlyph app={app} condition={condition} size="lg" />
        <div className="dk-head-text">
          <h2>{item.name}</h2>
          <div className="dk-head-meta"><StatePill condition={condition} /><span>{condition.detail}</span></div>
        </div>
        <div className="dk-head-actions">
          {primary && alive && <a className="dk-btn" href={primary.url} target="_blank" rel="noopener noreferrer" title={primary.url}><ExternalLink />打开</a>}
          {alive ? (
            <>
              <button className="dk-btn" disabled={busy} onClick={() => actions.request(item, "restart")}>{pending === "restart" ? <span className="loader small" /> : <RotateCw />}重启</button>
              <button className="dk-btn danger-soft" disabled={busy} onClick={() => actions.request(item, "stop")}>{pending === "stop" ? <span className="loader small" /> : <Square />}停止</button>
            </>
          ) : (
            <button className="dk-btn primary" disabled={busy || item.state === "removing"} onClick={() => actions.request(item, "start")}>{pending === "start" ? <span className="loader small" /> : <Play />}启动</button>
          )}
        </div>
      </header>
      {failure && <div className="dk-note error" role="alert"><CircleAlert />{failure}</div>}

      <div className="dk-tabs" role="tablist" aria-label="容器详情">
        <button role="tab" aria-selected={tab === "overview"} onClick={() => onTab("overview")}>概览</button>
        <button role="tab" aria-selected={tab === "logs"} onClick={() => onTab("logs")}><ScrollText />日志</button>
      </div>

      {tab === "overview" ? <Overview item={item} app={app} condition={condition} onLogs={() => onTab("logs")} /> : <ContainerLogs item={item} />}
    </div>
  );
}

function Overview({ item, app, condition, onLogs }: { item: ContainerSummary; app?: AppRef; condition: Condition; onLogs: () => void }) {
  const ports = webPorts(item, app);
  const unpublished = item.ports.filter((port) => !port.hostPort);
  const usage = item.usage;
  const memoryShare = usage && usage.memoryLimitBytes > 0 ? Math.min(1, usage.memoryBytes / usage.memoryLimitBytes) : 0;

  return (
    <div className="dk-overview" role="tabpanel" aria-label="概览">
      {condition.attention && <Diagnosis condition={condition} onLogs={onLogs} />}

      {usage && (
        <div className="dk-stats">
          <div className="dk-stat">
            <span className="dk-stat-label"><Cpu />CPU</span>
            <strong>{formatPercent(usage.cpuPercent)}</strong>
          </div>
          <div className="dk-stat">
            <span className="dk-stat-label"><MemoryStick />内存</span>
            <strong>{formatBytes(usage.memoryBytes)}{usage.memoryLimitBytes > 0 && <small>/ {formatBytes(usage.memoryLimitBytes)}</small>}</strong>
            {usage.memoryLimitBytes > 0 && <span className="dk-meter" role="meter" aria-label="内存占用" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(memoryShare * 100)}><i style={{ width: `${Math.max(2, memoryShare * 100)}%` }} /></span>}
          </div>
          {condition.health && (
            <div className="dk-stat">
              <span className="dk-stat-label"><HeartPulse />健康检查</span>
              <strong className={`dk-health ${condition.health}`}>{healthLabels[condition.health]}</strong>
            </div>
          )}
        </div>
      )}

      <section className="dk-section" aria-label="详细信息">
        <h3>详细信息</h3>
        <dl className="dk-facts">
          <Fact label="镜像"><code>{item.image}</code></Fact>
          <Fact label="分组">{app ? <>{app.title}<code>{item.project}</code></> : item.project ? <code>{item.project}</code> : standaloneTitle}</Fact>
          <Fact label="端口">
            {ports.length === 0 && unpublished.length === 0 && <span className="dk-faint">没有发布端口</span>}
            {ports.map((port) => (
              <span className="dk-fact-port" key={port.hostPort}>
                <code>{port.hostPort} → {port.containerPort}/tcp</code>
                {item.state === "running" && <a href={port.url} target="_blank" rel="noopener noreferrer">打开<ExternalLink /></a>}
              </span>
            ))}
            {item.ports.filter((port) => port.hostPort && port.protocol !== "tcp").map((port) => (
              <span className="dk-fact-port" key={`${port.hostPort}/${port.protocol}`}><code>{port.hostPort} → {port.containerPort}/{port.protocol}</code></span>
            ))}
            {unpublished.length > 0 && <span className="dk-faint">仅容器内：{unpublished.map((port) => `${port.containerPort}/${port.protocol}`).join("、")}</span>}
          </Fact>
          <Fact label="引擎状态"><code>{item.status}</code></Fact>
          <Fact label="创建时间">{formatDateTime(item.createdAt)}</Fact>
          <Fact label="容器 ID"><code className="dk-id">{item.id}</code></Fact>
        </dl>
      </section>
    </div>
  );
}

const healthLabels = { healthy: "健康", unhealthy: "不健康", starting: "检查中" } as const;

// What went wrong, in words, and where to look next.
function Diagnosis({ condition, onLogs }: { condition: Condition; onLogs: () => void }) {
  let title: string;
  let detail: string;
  if (condition.health === "unhealthy") {
    title = "健康检查未通过";
    detail = "容器仍在运行，但镜像自带的健康检查失败，服务可能无法正常响应。";
  } else if (condition.label === "重启中") {
    title = "容器在反复重启";
    detail = `它启动后很快退出（退出码 ${condition.exitCode}），引擎正按重启策略再次启动它。`;
  } else if (condition.exitCode !== undefined) {
    title = `容器异常退出（退出码 ${condition.exitCode}）`;
    detail = "日志里通常能看到退出前的错误。修复后可以重新启动。";
  } else {
    title = "容器处于异常状态";
    detail = "引擎没能停止或删除它，可能需要在主机上处理。";
  }
  return (
    <div className="dk-diagnosis" role="status">
      <CircleAlert />
      <div><strong>{title}</strong><p>{detail}</p></div>
      <button className="dk-btn" onClick={onLogs}><ScrollText />查看日志</button>
    </div>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="dk-fact">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}
