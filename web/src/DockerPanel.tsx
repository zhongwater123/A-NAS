import { ArrowLeft, Box, CircleAlert, Layers, Play, RefreshCw, RotateCw, ScrollText, Square } from "lucide-react";
import { ReactNode, useCallback, useEffect, useRef, useState } from "react";

import {
  ContainerAction,
  ContainerAPIError,
  ContainerLogLine,
  ContainerSnapshot,
  ContainerState,
  ContainerSummary,
  applyContainerAction,
  readContainerLogs,
  readContainers,
} from "./containersApi";

export const containerPollInterval = 5_000;

const stateLabels: Record<ContainerState, string> = {
  running: "运行中",
  exited: "已停止",
  created: "未启动",
  paused: "已暂停",
  restarting: "重启中",
  removing: "删除中",
  dead: "异常",
};

const actionLabels: Record<ContainerAction, string> = { start: "启动", stop: "停止", restart: "重启" };

type View = { kind: "list"; tab: "containers" | "images" } | { kind: "logs"; container: ContainerSummary };

export function DockerPanel() {
  const { snapshot, error, loading, refresh } = useContainerSnapshot();
  const [view, setView] = useState<View>({ kind: "list", tab: "containers" });

  if (!snapshot && loading) {
    return <div className="center-state"><span className="loader" /><h2>正在连接 Docker…</h2></div>;
  }
  if (!snapshot && error === "containers_disabled") {
    return (
      <div className="center-state">
        <Box /><h2>Docker 未启用</h2>
        <p>安装 Docker 与 A-NAS 容器代理后，以 ANAS_CONTAINERS_MODE=agent 启动产品服务。</p>
      </div>
    );
  }
  if (!snapshot) {
    return (
      <div className="center-state">
        <CircleAlert /><h2>无法连接 Docker 引擎</h2><p>请确认 Docker 与容器代理正在运行。</p>
        <button className="primary" onClick={() => void refresh()}>重新连接</button>
      </div>
    );
  }
  if (view.kind === "logs") {
    return <ContainerLogs container={view.container} onBack={() => setView({ kind: "list", tab: "containers" })} />;
  }

  const running = snapshot.containers.filter((item) => item.state === "running").length;
  return (
    <div className="docker-page">
      <div className="page-heading">
        <div>
          <p className="section-label">DOCKER</p>
          <h2>容器</h2>
          <p>Docker {snapshot.engine.version} · API {snapshot.engine.apiVersion} · {snapshot.dataSource === "live" ? "实时主机" : "模拟数据"}</p>
        </div>
        <button className="icon-button refresh" aria-label="刷新容器列表" onClick={() => void refresh()}><RefreshCw /></button>
      </div>
      {error && <div className="status-banner error"><CircleAlert />连接中断，正在显示上次读取的容器状态</div>}

      <div className="docker-summary">
        <div className="docker-tabs" role="tablist" aria-label="Docker 视图">
          <button role="tab" aria-selected={view.tab === "containers"} onClick={() => setView({ kind: "list", tab: "containers" })}>
            <Box />容器<span>{snapshot.containers.length}</span>
          </button>
          <button role="tab" aria-selected={view.tab === "images"} onClick={() => setView({ kind: "list", tab: "images" })}>
            <Layers />镜像<span>{snapshot.images.length}</span>
          </button>
        </div>
        <span className="docker-count"><i className="state-dot running" />{running} 个运行中</span>
      </div>

      {view.tab === "containers" ? (
        snapshot.containers.length === 0 ? (
          <div className="empty-state"><Box /><h3>还没有容器</h3><p>通过应用中心安装应用后会出现在这里。</p></div>
        ) : (
          <div className="container-list">
            {snapshot.containers.map((item) => (
              <ContainerCard key={item.id} item={item} onChanged={refresh} onLogs={() => setView({ kind: "logs", container: item })} />
            ))}
          </div>
        )
      ) : (
        <ImageList snapshot={snapshot} />
      )}
    </div>
  );
}

function ContainerCard({ item, onChanged, onLogs }: { item: ContainerSummary; onChanged: () => Promise<void>; onLogs: () => void }) {
  const [confirming, setConfirming] = useState<ContainerAction>();
  const [pending, setPending] = useState<ContainerAction>();
  const [failure, setFailure] = useState<string>();
  const running = item.state === "running";

  const run = async (action: ContainerAction) => {
    setConfirming(undefined);
    setPending(action);
    setFailure(undefined);
    try {
      await applyContainerAction(item.id, action);
      await onChanged();
    } catch (error) {
      setFailure(actionFailure(error, action));
    } finally {
      setPending(undefined);
    }
  };
  // Stopping or restarting interrupts the service; starting does not need confirmation.
  const request = (action: ContainerAction) => (action === "start" ? void run(action) : setConfirming(action));

  return (
    <article className={`container-card state-${item.state}`} aria-label={`容器 ${item.name}`}>
      <div className="container-visual"><Box /></div>
      <div className="container-main">
        <div className="container-title">
          <h4>{item.name}</h4>
          <span className={`container-state ${item.state}`}><i className={`state-dot ${item.state}`} />{stateLabels[item.state]}</span>
          {item.project && <span className="container-project">{item.project}</span>}
        </div>
        <p title={item.image}>{item.image}</p>
        <div className="container-meta">
          <small>{item.status}</small>
          {item.ports.filter((port) => port.hostPort).map((port) => (
            <span className="port-chip" key={`${port.hostPort}-${port.containerPort}-${port.protocol}`}>{port.hostPort}→{port.containerPort}/{port.protocol}</span>
          ))}
        </div>
        {failure && <p className="container-error" role="alert">{failure}</p>}
      </div>
      <div className="container-side">
        {item.usage && (
          <div className="container-usage">
            <span>CPU <strong>{item.usage.cpuPercent.toFixed(1)}%</strong></span>
            <span>内存 <strong>{formatBytes(item.usage.memoryBytes)}</strong></span>
          </div>
        )}
        {confirming ? (
          <div className="container-confirm" role="group" aria-label={`确认${actionLabels[confirming]}${item.name}`}>
            <span>{actionLabels[confirming]}后服务会中断</span>
            <button onClick={() => setConfirming(undefined)}>取消</button>
            <button className="danger" onClick={() => void run(confirming)}>确认{actionLabels[confirming]}</button>
          </div>
        ) : (
          <div className="container-actions">
            {running ? (
              <>
                <ActionButton label={`重启${item.name}`} icon={<RotateCw />} busy={pending === "restart"} disabled={Boolean(pending)} onClick={() => request("restart")} />
                <ActionButton label={`停止${item.name}`} icon={<Square />} busy={pending === "stop"} disabled={Boolean(pending)} onClick={() => request("stop")} />
              </>
            ) : (
              <ActionButton label={`启动${item.name}`} icon={<Play />} busy={pending === "start"} disabled={Boolean(pending) || item.state === "removing"} onClick={() => request("start")} />
            )}
            <ActionButton label={`查看${item.name}日志`} icon={<ScrollText />} onClick={onLogs} />
          </div>
        )}
      </div>
    </article>
  );
}

function ActionButton({ label, icon, busy, disabled, onClick }: { label: string; icon: ReactNode; busy?: boolean; disabled?: boolean; onClick: () => void }) {
  return (
    <button className="icon-button container-action" aria-label={label} title={label} disabled={disabled} onClick={onClick}>
      {busy ? <span className="loader small" /> : icon}
    </button>
  );
}

function ImageList({ snapshot }: { snapshot: ContainerSnapshot }) {
  if (snapshot.images.length === 0) {
    return <div className="empty-state"><Layers /><h3>还没有镜像</h3></div>;
  }
  return (
    <div className="image-list" role="table" aria-label="镜像">
      {snapshot.images.map((image) => (
        <div className="image-row" role="row" key={image.id}>
          <span role="cell" className="image-tag">{image.tags[0] ?? "未标记镜像"}{image.tags.length > 1 && <small> +{image.tags.length - 1}</small>}</span>
          <span role="cell">{formatBytes(image.sizeBytes)}</span>
          <span role="cell">{formatDate(image.createdAt)}</span>
          <span role="cell" className="image-id" title={image.id}>{shortImageID(image.id)}</span>
        </div>
      ))}
    </div>
  );
}

function ContainerLogs({ container, onBack }: { container: ContainerSummary; onBack: () => void }) {
  const [lines, setLines] = useState<ContainerLogLine[]>();
  const [failure, setFailure] = useState<string>();
  const [loading, setLoading] = useState(false);
  const bottom = useRef<HTMLDivElement>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    try {
      setLines(await readContainerLogs(container.id, 200, signal));
      setFailure(undefined);
    } catch (error) {
      if (!(error instanceof DOMException && error.name === "AbortError")) setFailure("无法读取日志");
    } finally {
      setLoading(false);
    }
  }, [container.id]);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);
  // Recent Chromium returns a Promise from scrollIntoView; an effect must not return it.
  useEffect(() => {
    bottom.current?.scrollIntoView?.({ block: "end" });
  }, [lines]);

  return (
    <div className="docker-logs">
      <div className="logs-heading">
        <button className="icon-button" aria-label="返回容器列表" onClick={onBack}><ArrowLeft /></button>
        <div><h3>{container.name}</h3><p>最近 200 行日志</p></div>
        <button className="icon-button refresh" aria-label="刷新日志" disabled={loading} onClick={() => void load()}><RefreshCw className={loading ? "spinning" : ""} /></button>
      </div>
      {failure && <div className="status-banner error"><CircleAlert />{failure}</div>}
      <div className="logs-body" role="log" aria-label={`${container.name} 日志`}>
        {lines?.length === 0 && <p className="logs-empty">暂无日志输出</p>}
        {lines?.map((line, index) => (
          <div className={`log-line ${line.stream}`} key={index}>
            <time>{line.time ? formatClock(line.time) : ""}</time>
            <span>{line.text}</span>
          </div>
        ))}
        <div ref={bottom} />
      </div>
    </div>
  );
}

function useContainerSnapshot() {
  const [snapshot, setSnapshot] = useState<ContainerSnapshot>();
  const [error, setError] = useState<string>();
  const [loading, setLoading] = useState(true);
  const active = useRef<AbortController | undefined>(undefined);

  const refresh = useCallback(async () => {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    try {
      setSnapshot(await readContainers(controller.signal));
      setError(undefined);
    } catch (failure) {
      if (failure instanceof DOMException && failure.name === "AbortError") return;
      setError(failure instanceof ContainerAPIError ? failure.code : "request_failed");
    } finally {
      if (active.current === controller) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
    const interval = window.setInterval(() => {
      if (!document.hidden) void refresh();
    }, containerPollInterval);
    return () => {
      window.clearInterval(interval);
      active.current?.abort();
    };
  }, [refresh]);

  return { snapshot, error, loading, refresh };
}

function actionFailure(error: unknown, action: ContainerAction): string {
  const code = error instanceof ContainerAPIError ? error.code : "";
  if (code === "container_not_found") return "容器已不存在，列表即将刷新";
  if (code === "containers_unavailable" || code === "network_error") return `无法连接 Docker，${actionLabels[action]}未执行`;
  return `${actionLabels[action]}失败，请查看日志`;
}

function formatBytes(bytes: number): string {
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GiB`;
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(0)} MiB`;
  return `${Math.max(1, Math.round(bytes / 1024))} KiB`;
}

function formatDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" }).format(date);
}

function formatClock(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(date);
}

function shortImageID(id: string): string {
  return id.replace(/^sha256:/, "").slice(0, 12);
}
