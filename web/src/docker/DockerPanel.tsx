import { Box, Boxes, CircleAlert, CirclePause, Container, Layers, Play, RotateCw, Search, Square, X } from "lucide-react";
import { KeyboardEvent, ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { readApps } from "../appsApi";
import { ContainerAction, ContainerAPIError, ContainerSnapshot, ContainerSummary, applyContainerAction, readContainers } from "../containersApi";
import { ContainerDetail, DetailTab } from "./ContainerDetail";
import { ContainerPage } from "./ContainerList";
import { ImagePage } from "./ImageList";
import { AppRef, Filter, appFor, describeContainer, filterLabels, groupContainers, groupKey, matchesFilter, matchesImageQuery, matchesQuery, standaloneTitle } from "./model";
import { ContainerGlyph, Empty, Layer } from "./parts";
import "./docker.css";

export const containerPollInterval = 5_000;

const actionLabels: Record<ContainerAction, string> = { start: "启动", stop: "停止", restart: "重启" };

// The App Center asks to show one app's containers; at makes a repeated request distinct.
export interface DockerFocus {
  project: string;
  at: number;
}

type View = { kind: "containers"; filter: Filter } | { kind: "project"; key: string } | { kind: "images" };
type Selected = { id: string; tab: DetailTab };
type Confirm = { item: ContainerSummary; action: "stop" | "restart" };

export interface ContainerActions {
  pending: Readonly<Record<string, ContainerAction>>;
  failures: Readonly<Record<string, string>>;
  // Starting runs at once; stopping and restarting interrupt a service, so they ask first.
  request: (item: ContainerSummary, action: ContainerAction) => void;
}

export function DockerPanel({ focus, onFocusHandled, onOpenStore }: { focus?: DockerFocus; onFocusHandled?: () => void; onOpenStore?: () => void }) {
  const { snapshot, error, refreshing, refresh } = useContainerSnapshot();
  const apps = useInstalledApps();
  const [view, setView] = useState<View>({ kind: "containers", filter: "all" });
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<Selected>();
  const [confirm, setConfirm] = useState<Confirm>();
  const [pending, setPending] = useState<Record<string, ContainerAction>>({});
  const [failures, setFailures] = useState<Record<string, string>>({});
  const main = useRef<HTMLElement>(null);
  const listScroll = useRef(0);
  // The control that opened the confirmation; the inert page drops its focus, so it is restored by hand.
  const opener = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (!focus) return;
    setView({ kind: "project", key: focus.project });
    setSelected(undefined);
    setQuery("");
    onFocusHandled?.();
  }, [focus?.at]);
  useLayoutEffect(() => {
    if (main.current) main.current.scrollTop = selected ? 0 : listScroll.current;
  }, [selected?.id]);
  useEffect(() => {
    if (confirm || !opener.current) return;
    if (opener.current.isConnected) opener.current.focus();
    opener.current = null;
  }, [confirm]);

  const run = useCallback(async (item: ContainerSummary, action: ContainerAction) => {
    setPending((current) => ({ ...current, [item.id]: action }));
    setFailures((current) => without(current, item.id));
    try {
      await applyContainerAction(item.id, action);
      await refresh();
    } catch (failure) {
      setFailures((current) => ({ ...current, [item.id]: actionFailure(failure, action) }));
    } finally {
      setPending((current) => without(current, item.id));
    }
  }, [refresh]);
  const actions: ContainerActions = {
    pending,
    failures,
    request: (item, action) => {
      if (action === "start") return void run(item, action);
      opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      setConfirm({ item, action });
    },
  };

  const containers = snapshot?.containers ?? [];
  const groups = useMemo(() => groupContainers(containers, apps), [containers, apps]);

  if (!snapshot && !error) {
    return <div className="dk-shell single"><div className="dk-loading"><span className="loader" /><p>正在连接 Docker…</p></div></div>;
  }
  if (!snapshot && error === "containers_disabled") {
    return (
      <div className="dk-shell single">
        <Empty icon={<Container />} title="Docker 未启用">
          <p>安装 Docker 与 A-NAS 容器代理后即可在这里管理容器。</p>
        </Empty>
      </div>
    );
  }
  if (!snapshot) {
    return (
      <div className="dk-shell single">
        <Empty icon={<CircleAlert />} title="无法连接 Docker 引擎" tone="error">
          <p>请确认 Docker 与容器代理正在运行。</p>
          <button className="dk-btn primary" onClick={() => void refresh()}><RotateCw />重新连接</button>
        </Empty>
      </div>
    );
  }

  const count = (filter: Filter) => containers.filter((item) => matchesFilter(item, filter)).length;
  const attention = count("attention");
  const searching = query.trim() !== "";
  const navigate = (next: View) => {
    listScroll.current = 0;
    setSelected(undefined);
    setQuery("");
    setView(next);
  };
  const open = (item: ContainerSummary, tab: DetailTab = "overview") => {
    listScroll.current = main.current?.scrollTop ?? 0;
    setSelected({ id: item.id, tab });
  };
  const active = (next: View) =>
    !searching && !selected && view.kind === next.kind &&
    (next.kind !== "containers" || (view.kind === "containers" && view.filter === next.filter)) &&
    (next.kind !== "project" || (view.kind === "project" && view.key === next.key));

  const current = selected && containers.find((item) => item.id === selected.id);
  const projectGroup = view.kind === "project" ? groups.find((group) => group.key === view.key) : undefined;
  const pageTitle = searching ? "搜索结果" : view.kind === "images" ? "镜像" : view.kind === "project" ? projectGroup?.title ?? projectTitle(view.key, apps) : filterLabels[view.filter];

  let page: ReactNode;
  if (selected && !current) {
    page = (
      <Empty icon={<Box />} title="容器已不存在">
        <p>它可能已被删除或重新创建。</p>
        <button className="dk-btn" onClick={() => setSelected(undefined)}>返回{pageTitle}</button>
      </Empty>
    );
  } else if (current) {
    page = (
      <ContainerDetail
        key={current.id} item={current} app={appFor(groupKey(current), apps)} tab={selected!.tab} backLabel={pageTitle} actions={actions}
        onTab={(tab) => setSelected({ id: current.id, tab })} onBack={() => setSelected(undefined)}
      />
    );
  } else if (searching) {
    const matching = containers.filter((item) => matchesQuery(item, query, apps));
    const imageHit = snapshot.images.some((image) => matchesImageQuery(image, query));
    page = (
      <>
        <ContainerPage
          title="搜索结果" items={matching} groups={groupContainers(matching, apps)} apps={apps} actions={actions}
          refreshing={refreshing} onRefresh={refresh} onOpen={open} empty={!imageHit && <Empty icon={<Search />} title={`没有找到“${query.trim()}”`} />}
        />
        <ImagePage snapshot={snapshot} query={query} refreshing={refreshing} onRefresh={refresh} onOpen={open} embedded />
      </>
    );
  } else if (view.kind === "images") {
    page = <ImagePage snapshot={snapshot} query="" refreshing={refreshing} onRefresh={refresh} onOpen={open} />;
  } else if (view.kind === "project") {
    const items = projectGroup?.containers ?? [];
    page = (
      <ContainerPage
        title={pageTitle} project={view.key} app={appFor(view.key, apps)} items={items} apps={apps} actions={actions} totals
        refreshing={refreshing} onRefresh={refresh} onOpen={open}
        empty={<Empty icon={<Boxes />} title="这个分组里没有容器"><p>应用可能正在安装，或已被卸载。</p></Empty>}
      />
    );
  } else {
    const items = containers.filter((item) => matchesFilter(item, view.filter));
    page = (
      <ContainerPage
        title={pageTitle} items={items} groups={groupContainers(items, apps)} apps={apps} actions={actions} totals={view.filter === "all"}
        refreshing={refreshing} onRefresh={refresh} onOpen={open} empty={emptyFor(view.filter, containers.length, onOpenStore)}
      />
    );
  }

  const onSearchKey = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Escape" && query) {
      event.stopPropagation();
      setQuery("");
    }
  };

  return (
    <div className="dk-shell">
      <nav className="dk-nav" aria-label="Docker 导航" inert={Boolean(confirm)}>
        <label className="dk-search">
          <Search />
          <input value={query} onChange={(event) => { setQuery(event.target.value); setSelected(undefined); }} onKeyDown={onSearchKey} placeholder="搜索容器或镜像" aria-label="搜索容器或镜像" />
          {query && <button className="dk-search-clear" aria-label="清除搜索" onClick={() => setQuery("")}><X /></button>}
        </label>
        <NavItem active={active({ kind: "containers", filter: "all" })} onClick={() => navigate({ kind: "containers", filter: "all" })} tile="all" icon={<Boxes />} label={filterLabels.all} count={containers.length} />
        <NavItem active={active({ kind: "containers", filter: "running" })} onClick={() => navigate({ kind: "containers", filter: "running" })} tile="running" icon={<Play />} label={filterLabels.running} count={count("running")} />
        <NavItem active={active({ kind: "containers", filter: "stopped" })} onClick={() => navigate({ kind: "containers", filter: "stopped" })} tile="stopped" icon={<Square />} label={filterLabels.stopped} count={count("stopped")} />
        {(attention > 0 || (view.kind === "containers" && view.filter === "attention")) && (
          <NavItem active={active({ kind: "containers", filter: "attention" })} onClick={() => navigate({ kind: "containers", filter: "attention" })} tile="attention" icon={<CircleAlert />} label={filterLabels.attention} count={attention} />
        )}
        <NavItem active={active({ kind: "images" })} onClick={() => navigate({ kind: "images" })} tile="images" icon={<Layers />} label="镜像" count={snapshot.images.length} />
        {groups.length > 0 && <p className="dk-nav-label">分组</p>}
        {groups.map((group) => {
          const running = group.containers.filter((item) => item.state === "running").length;
          const trouble = group.containers.some((item) => describeContainer(item).attention);
          return (
            <NavItem
              key={group.key} active={active({ kind: "project", key: group.key })} onClick={() => navigate({ kind: "project", key: group.key })}
              icon={group.app ? <ContainerGlyph app={group.app} size="sm" /> : <span className={`dk-tile ${group.key ? "project" : "standalone"}`}>{group.key ? <Boxes /> : <Box />}</span>}
              label={group.title} count={group.containers.length} alert={trouble}
              hint={running < group.containers.length ? `${running}/${group.containers.length} 个运行中` : undefined}
            />
          );
        })}
        <footer className="dk-nav-foot">
          <span className={`dk-live ${error ? "offline" : ""}`} aria-hidden="true" />
          <span>Docker {snapshot.engine.version}<small>API {snapshot.engine.apiVersion}{snapshot.dataSource === "simulated" ? " · 模拟数据" : ""}</small></span>
        </footer>
      </nav>
      <main className="dk-main" ref={main} inert={Boolean(confirm)}>
        {error && <div className="dk-note error dk-offline" role="status"><CircleAlert />连接中断，正在显示上次读取的容器状态</div>}
        {page}
      </main>
      {confirm && <ConfirmDialog confirm={confirm} app={appFor(groupKey(confirm.item), apps)} onCancel={() => setConfirm(undefined)} onConfirm={() => { setConfirm(undefined); void run(confirm.item, confirm.action); }} />}
    </div>
  );
}

function NavItem({ active, onClick, icon, tile, label, count, alert, hint }: {
  active: boolean; onClick: () => void; icon: ReactNode; tile?: string; label: string; count: number; alert?: boolean; hint?: string;
}) {
  return (
    <button className={`dk-nav-item ${active ? "active" : ""}`} aria-current={active ? "page" : undefined} title={hint} onClick={onClick}>
      {tile ? <span className={`dk-tile ${tile}`}>{icon}</span> : icon}
      <span>{label}</span>
      {alert && <i className="dk-nav-alert" role="img" aria-label="有容器需要关注" />}
      <span className="dk-count">{count}</span>
    </button>
  );
}

function ConfirmDialog({ confirm, app, onCancel, onConfirm }: { confirm: Confirm; app?: AppRef; onCancel: () => void; onConfirm: () => void }) {
  const { item, action } = confirm;
  const stop = action === "stop";
  // Stopping one container of an app can take out part of the app or all of it.
  const effect = stop
    ? app ? `${app.title} 的部分或全部功能会不可用，之后可以随时重新启动。` : "它提供的服务会中断，之后可以随时重新启动。"
    : `${app ? app.title : "它提供的服务"}会短暂中断，通常几秒后恢复。`;
  return (
    <Layer label={`确认${actionLabels[action]} ${item.name}`} onDismiss={onCancel}>
      <div className="dk-confirm">
        <span className={`dk-confirm-art ${stop ? "danger" : ""}`}>{stop ? <CirclePause /> : <RotateCw />}</span>
        <h3>{actionLabels[action]} {item.name}？</h3>
        <p>{effect}</p>
      </div>
      <footer className="dk-sheet-foot">
        <button className="dk-btn" onClick={onCancel}>取消</button>
        <button className={`dk-btn ${stop ? "danger" : "primary"}`} onClick={onConfirm}>确认{actionLabels[action]}</button>
      </footer>
    </Layer>
  );
}

function emptyFor(filter: Filter, total: number, onOpenStore?: () => void): ReactNode {
  if (total === 0) {
    return (
      <Empty icon={<Container />} title="还没有容器">
        <p>从应用中心安装的应用会出现在这里。</p>
        {onOpenStore && <button className="dk-btn primary" onClick={onOpenStore}>打开应用中心</button>}
      </Empty>
    );
  }
  if (filter === "attention") return <Empty icon={<Play />} title="所有容器都运行正常" tone="ok" />;
  return <Empty icon={filter === "running" ? <Play /> : <Square />} title={filter === "running" ? "没有运行中的容器" : "没有已停止的容器"} />;
}

function without<T>(record: Record<string, T>, key: string): Record<string, T> {
  const { [key]: _removed, ...rest } = record;
  return rest;
}

function projectTitle(key: string, apps: ReadonlyMap<string, AppRef>): string {
  return appFor(key, apps)?.title ?? (key || standaloneTitle);
}

function actionFailure(error: unknown, action: ContainerAction): string {
  const code = error instanceof ContainerAPIError ? error.code : "";
  if (code === "container_not_found") return "容器已不存在，列表即将刷新";
  if (code === "containers_unavailable" || code === "network_error") return `无法连接 Docker，${actionLabels[action]}未执行`;
  return `${actionLabels[action]}失败，请查看日志`;
}

function useContainerSnapshot() {
  const [snapshot, setSnapshot] = useState<ContainerSnapshot>();
  const [error, setError] = useState<string>();
  const [refreshing, setRefreshing] = useState(false);
  const active = useRef<AbortController | undefined>(undefined);

  const refresh = useCallback(async () => {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setRefreshing(true);
    try {
      setSnapshot(await readContainers(controller.signal));
      setError(undefined);
    } catch (failure) {
      if (failure instanceof DOMException && failure.name === "AbortError") return;
      setError(failure instanceof ContainerAPIError ? failure.code : "request_failed");
    } finally {
      if (active.current === controller) setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
    const interval = window.setInterval(() => {
      if (!document.hidden) void refresh();
    }, containerPollInterval);
    const resume = () => {
      if (!document.hidden) void refresh();
    };
    document.addEventListener("visibilitychange", resume);
    return () => {
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", resume);
      active.current?.abort();
    };
  }, [refresh]);

  return { snapshot, error, refreshing, refresh };
}

// Catalog names and web ports for App Center projects. Docker still works
// without them: a missing App Center only means projects show their raw names.
function useInstalledApps(): ReadonlyMap<string, AppRef> {
  const [apps, setApps] = useState<ReadonlyMap<string, AppRef>>(new Map());
  useEffect(() => {
    const controller = new AbortController();
    readApps(controller.signal)
      .then((list) => setApps(new Map(list.apps.map((app) => [app.id, { id: app.id, title: app.title, webPort: app.webPort, scheme: app.scheme, path: app.path }]))))
      .catch(() => undefined);
    return () => controller.abort();
  }, []);
  return apps;
}
