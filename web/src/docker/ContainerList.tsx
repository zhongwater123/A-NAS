import {
  Activity,
  Box,
  Boxes,
  Cpu,
  ExternalLink,
  MemoryStick,
  Play,
  RefreshCw,
  RotateCw,
  ScrollText,
  Square,
} from "lucide-react";
import { ReactNode } from "react";

import { formatBytes } from "../bytes";
import { ContainerSummary } from "../containersApi";
import { DetailTab } from "./ContainerDetail";
import type { ContainerActions } from "./DockerPanel";
import {
  AppRef,
  Group,
  appFor,
  appLink,
  describeContainer,
  formatPercent,
  groupKey,
  totals as sumTotals,
  webPorts,
} from "./model";
import { ContainerGlyph, IconAction, PortLinks, StatePill } from "./parts";

interface PageProps {
  title: string;
  items: ContainerSummary[];
  // Grouped by project; a project's own page lists its containers without headings.
  groups?: Group[];
  project?: string;
  app?: AppRef;
  apps: ReadonlyMap<string, AppRef>;
  actions: ContainerActions;
  totals?: boolean;
  refreshing: boolean;
  empty: ReactNode;
  onRefresh: () => Promise<void>;
  onOpen: (item: ContainerSummary, tab?: DetailTab) => void;
}

export function ContainerPage({
  title,
  items,
  groups,
  project,
  app,
  apps,
  actions,
  totals,
  refreshing,
  empty,
  onRefresh,
  onOpen,
}: PageProps) {
  const sum = sumTotals(items);
  const link = app && appLink(app);
  return (
    <div className="dk-page">
      <header className="dk-page-head">
        {project !== undefined &&
          (app ? (
            <ContainerGlyph app={app} size="lg" />
          ) : (
            <span className={`dk-tile lg ${project ? "project" : "standalone"}`}>{project ? <Boxes /> : <Box />}</span>
          ))}
        <div className="dk-page-title">
          <h2>{title}</h2>
          {project && project !== title && <code title="Compose 项目">{project}</code>}
        </div>
        {link && (
          <a className="dk-btn" href={link} target="_blank" rel="noopener noreferrer">
            <ExternalLink />
            打开 {app!.title}
          </a>
        )}
        <button
          className="dk-icon-btn ghost"
          aria-label="刷新"
          title="刷新"
          disabled={refreshing}
          onClick={() => void onRefresh()}
        >
          <RefreshCw className={refreshing ? "dk-spin" : ""} />
        </button>
      </header>

      {totals && items.length > 0 && (
        <div className="dk-stats">
          <Stat icon={<Activity />} label="运行中" value={`${sum.running}`} unit={`/ ${sum.total}`} />
          <Stat icon={<Cpu />} label="CPU" value={formatPercent(sum.cpuPercent)} />
          <Stat icon={<MemoryStick />} label="内存" value={formatBytes(sum.memoryBytes)} />
        </div>
      )}

      {items.length === 0 ? (
        empty
      ) : groups ? (
        groups.map((group) => (
          <GroupSection
            key={group.key}
            group={group}
            apps={apps}
            actions={actions}
            showRunning={Boolean(totals)}
            onOpen={onOpen}
          />
        ))
      ) : (
        <div className="dk-list">
          {items.map((item) => (
            <ContainerRow
              key={item.id}
              item={item}
              app={appFor(groupKey(item), apps)}
              actions={actions}
              onOpen={onOpen}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function Stat({ icon, label, value, unit }: { icon: ReactNode; label: string; value: string; unit?: string }) {
  return (
    <div className="dk-stat">
      <span className="dk-stat-label">
        {icon}
        {label}
      </span>
      <strong>
        {value}
        {unit && <small>{unit}</small>}
      </strong>
    </div>
  );
}

// The full list counts what runs in each group; a filtered one only how many matched.
function GroupSection({
  group,
  apps,
  actions,
  showRunning,
  onOpen,
}: {
  group: Group;
  apps: ReadonlyMap<string, AppRef>;
  actions: ContainerActions;
  showRunning: boolean;
  onOpen: PageProps["onOpen"];
}) {
  const running = group.containers.filter((item) => item.state === "running").length;
  const total = group.containers.length;
  return (
    <section className="dk-group" aria-label={group.title}>
      <header className="dk-group-head">
        {group.app ? (
          <ContainerGlyph app={group.app} size="sm" />
        ) : (
          <span className={`dk-tile ${group.key ? "project" : "standalone"}`}>{group.key ? <Boxes /> : <Box />}</span>
        )}
        <h3>{group.title}</h3>
        <span className="dk-group-count">
          {!showRunning
            ? `${total} 个容器`
            : running === total
              ? `${running} 个运行中`
              : `${running}/${total} 个运行中`}
        </span>
      </header>
      <div className="dk-list">
        {group.containers.map((item) => (
          <ContainerRow
            key={item.id}
            item={item}
            app={appFor(groupKey(item), apps)}
            actions={actions}
            onOpen={onOpen}
          />
        ))}
      </div>
    </section>
  );
}

function ContainerRow({
  item,
  app,
  actions,
  onOpen,
}: {
  item: ContainerSummary;
  app?: AppRef;
  actions: ContainerActions;
  onOpen: PageProps["onOpen"];
}) {
  const condition = describeContainer(item);
  const pending = actions.pending[item.id];
  const failure = actions.failures[item.id];
  const running = item.state === "running";
  return (
    <article className={`dk-row ${condition.tone}`} aria-label={`容器 ${item.name}`}>
      <ContainerGlyph condition={condition} />
      <div className="dk-row-text">
        <div className="dk-row-title">
          <button className="dk-row-link" onClick={() => onOpen(item)}>
            {item.name}
          </button>
          <StatePill condition={condition} />
          {running && <PortLinks ports={webPorts(item, app)} item={item} />}
        </div>
        <p className="dk-row-meta">
          <span>{condition.detail}</span>
          <span className="dk-image" title={item.image}>
            {item.image}
          </span>
        </p>
        {failure && (
          <p className="dk-row-error" role="alert">
            {failure}
          </p>
        )}
      </div>
      {item.usage ? (
        <div className="dk-usage" aria-label="资源占用">
          <span>
            CPU<b>{formatPercent(item.usage.cpuPercent)}</b>
          </span>
          <span>
            内存<b>{formatBytes(item.usage.memoryBytes)}</b>
          </span>
        </div>
      ) : (
        <div className="dk-usage" />
      )}
      <div className="dk-row-actions">
        {running || item.state === "paused" || item.state === "restarting" ? (
          <>
            <IconAction
              label={`重启 ${item.name}`}
              icon={<RotateCw />}
              busy={pending === "restart"}
              disabled={Boolean(pending)}
              onClick={() => actions.request(item, "restart")}
            />
            <IconAction
              label={`停止 ${item.name}`}
              icon={<Square />}
              tone="danger"
              busy={pending === "stop"}
              disabled={Boolean(pending)}
              onClick={() => actions.request(item, "stop")}
            />
          </>
        ) : (
          <IconAction
            label={`启动 ${item.name}`}
            icon={<Play />}
            busy={pending === "start"}
            disabled={Boolean(pending) || item.state === "removing"}
            onClick={() => actions.request(item, "start")}
          />
        )}
        <IconAction label={`查看 ${item.name} 的日志`} icon={<ScrollText />} onClick={() => onOpen(item, "logs")} />
      </div>
    </article>
  );
}
