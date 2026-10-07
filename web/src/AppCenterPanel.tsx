import { ArrowLeft, CircleAlert, Download, FolderOpen, Network, Search, ShieldCheck, ShoppingBag, Trash2, UserRound } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { AppJob, AppList, AppsAPIError, CatalogApp, InstallPlan, iconURL, installApp, readApps, readPlan, uninstallApp } from "./appsApi";

const busyPollInterval = 1_500;
const idlePollInterval = 10_000;

const categoryLabels: Record<string, string> = {
  Media: "影音",
  Networking: "网络",
  Utilities: "工具",
  Productivity: "效率",
  Backup: "备份",
  Cloud: "云盘",
  Developer: "开发",
  "Home Automation": "智能家居",
  Analytics: "监控",
  Downloader: "下载",
  Finance: "财务",
};

const mountLabels: Record<InstallPlan["mounts"][number]["kind"], string> = {
  appdata: "应用数据",
  shared: "共享数据",
  system: "系统只读",
};

export function AppCenterPanel() {
  const { list, error, refresh } = useAppList();
  const [selected, setSelected] = useState<string>();
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState("");

  const apps = list?.apps ?? [];
  const categories = useMemo(() => [...new Set(apps.map((app) => app.category).filter(Boolean))].sort(), [apps]);
  const visible = apps.filter((app) => {
    const text = `${app.title} ${app.tagline} ${app.description}`.toLowerCase();
    return (!category || app.category === category) && (!query || text.includes(query.trim().toLowerCase()));
  });

  if (!list && !error) return <div className="center-state"><span className="loader" /><h2>正在载入应用中心…</h2></div>;
  if (!list && error === "apps_disabled") {
    return <div className="center-state"><ShoppingBag /><h2>应用中心未启用</h2><p>安装 Docker 与 A-NAS 容器代理后即可从这里安装应用。</p></div>;
  }
  if (!list) {
    return (
      <div className="center-state">
        <CircleAlert /><h2>无法连接应用中心</h2><p>请确认容器代理正在运行。</p>
        <button className="primary" onClick={() => void refresh()}>重新连接</button>
      </div>
    );
  }

  const current = apps.find((app) => app.id === selected);
  if (current) return <AppDetail app={current} onBack={() => setSelected(undefined)} onChanged={refresh} />;

  return (
    <div className="store-page">
      <div className="page-heading">
        <div>
          <p className="section-label">APP CENTER</p>
          <h2>应用中心</h2>
          <p>{apps.length} 个经过安全审查的应用 · 清单来自 CasaOS AppStore · {list.dataSource === "live" ? "实时主机" : "模拟数据"}</p>
        </div>
      </div>
      {error && <div className="status-banner error"><CircleAlert />连接中断，正在显示上次读取的应用状态</div>}
      <div className="store-toolbar">
        <label className="store-search">
          <Search />
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索应用" aria-label="搜索应用" />
        </label>
        <div className="store-categories" role="group" aria-label="应用分类">
          <button aria-pressed={!category} onClick={() => setCategory("")}>全部</button>
          {categories.map((item) => (
            <button key={item} aria-pressed={category === item} onClick={() => setCategory(item)}>{categoryLabels[item] ?? item}</button>
          ))}
        </div>
      </div>
      {visible.length === 0 ? (
        <div className="empty-state"><Search /><h3>没有匹配的应用</h3></div>
      ) : (
        <div className="store-grid">
          {visible.map((app) => (
            <button key={app.id} className="store-card" aria-label={`${app.title}，${stateText(app)}`} onClick={() => setSelected(app.id)}>
              <img src={iconURL(app.id)} alt="" />
              <span className="store-card-text">
                <strong>{app.title}</strong>
                <small>{app.tagline}</small>
              </span>
              <span className={`store-badge ${app.state}`}>{stateText(app)}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function AppDetail({ app, onBack, onChanged }: { app: CatalogApp; onBack: () => void; onChanged: () => Promise<void> }) {
  const [plan, setPlan] = useState<InstallPlan>();
  const [planning, setPlanning] = useState(false);
  const [confirmUninstall, setConfirmUninstall] = useState(false);
  const [failure, setFailure] = useState<string>();
  const busy = app.state === "installing" || app.state === "uninstalling";

  const preparePlan = async () => {
    setPlanning(true);
    setFailure(undefined);
    try {
      setPlan(await readPlan(app.id));
    } catch (error) {
      setFailure(describeError(error));
    } finally {
      setPlanning(false);
    }
  };
  const confirmInstall = async () => {
    if (!plan) return;
    setFailure(undefined);
    try {
      await installApp(app.id, plan.digest);
      setPlan(undefined);
      await onChanged();
    } catch (error) {
      setFailure(describeError(error));
      if (error instanceof AppsAPIError && error.code === "plan_changed") setPlan(undefined);
    }
  };
  const confirmRemoval = async () => {
    setConfirmUninstall(false);
    setFailure(undefined);
    try {
      await uninstallApp(app.id);
      await onChanged();
    } catch (error) {
      setFailure(describeError(error));
    }
  };

  return (
    <div className="store-detail">
      <button className="icon-button store-back" aria-label="返回应用列表" onClick={onBack}><ArrowLeft /></button>
      <header className="store-detail-header">
        <img src={iconURL(app.id)} alt="" />
        <div>
          <h2>{app.title}</h2>
          <p>{app.tagline}</p>
          <small>{[app.version && `版本 ${app.version}`, app.author, categoryLabels[app.category] ?? app.category].filter(Boolean).join(" · ")}</small>
        </div>
        <div className="store-detail-actions">
          {app.state === "available" && (
            <button className="primary" disabled={planning} onClick={() => void preparePlan()}><Download size={15} />{planning ? "正在生成计划…" : "安装"}</button>
          )}
          {app.state === "installed" && !confirmUninstall && (
            <button className="danger-outline" onClick={() => setConfirmUninstall(true)}><Trash2 size={15} />卸载</button>
          )}
          {busy && <span className="store-progress"><span className="loader small" />{app.state === "installing" ? "安装中" : "卸载中"}</span>}
        </div>
      </header>

      {failure && <div className="status-banner error" role="alert"><CircleAlert />{failure}</div>}

      {confirmUninstall && (
        <div className="store-confirm" role="group" aria-label={`确认卸载${app.title}`}>
          <p>卸载会停止并删除 {app.title} 的容器和网络；应用数据会保留，重新安装后可继续使用。</p>
          <div>
            <button onClick={() => setConfirmUninstall(false)}>取消</button>
            <button className="danger" onClick={() => void confirmRemoval()}>确认卸载</button>
          </div>
        </div>
      )}

      {plan && <PlanReview plan={plan} onCancel={() => setPlan(undefined)} onConfirm={() => void confirmInstall()} />}

      {app.state === "installed" && (
        <div className="store-installed">
          <ShieldCheck />
          <div>
            <strong>已安装 · {app.running}/{app.total} 个容器运行中</strong>
            <p>{app.webPort ? `在局域网中通过设备地址的 ${app.webPort} 端口访问（${app.scheme}://设备地址:${app.webPort}${app.path}）。` : "该应用没有网页界面。"}可在 Docker 中查看容器与日志。</p>
          </div>
        </div>
      )}

      {app.job && <JobLog job={app.job} />}

      <section className="store-description">
        <h3>介绍</h3>
        <p>{app.description || app.tagline}</p>
        {app.website && <p className="store-website">官网：{app.website}</p>}
      </section>
    </div>
  );
}

function PlanReview({ plan, onCancel, onConfirm }: { plan: InstallPlan; onCancel: () => void; onConfirm: () => void }) {
  return (
    <section className="install-plan" aria-label="安装计划">
      <h3>安装计划</h3>
      <p>确认后 A-NAS 将执行下列操作。容器不会获得特权、设备或宿主机网络，只能访问下面列出的文件夹。</p>
      <dl>
        <dt><UserRound />运行身份</dt>
        <dd>
          <span className="plan-row"><code>{plan.identity.username}</code>UID {plan.identity.uid}</span>
          {plan.mounts.some((mount) => mount.kind === "shared")
            ? <small className="plan-warning">该应用将获得共享空间的读写权限（与成员相同），卸载时撤销；不能访问任何人的个人空间。</small>
            : <small>只访问自己的应用数据，不能访问共享空间或个人空间。</small>}
        </dd>
        <dt><Download />拉取镜像</dt>
        <dd>{plan.images.map((image) => <code key={image}>{image}</code>)}</dd>
        <dt><Network />开放端口</dt>
        <dd>
          {plan.ports.length === 0 && <span>不开放端口</span>}
          {plan.ports.map((port) => (
            <span key={`${port.hostPort}/${port.protocol}`} className="plan-row"><code>{port.hostPort}/{port.protocol}</code>→ 容器 {port.containerPort}{port.purpose && <small>{port.purpose}</small>}</span>
          ))}
        </dd>
        <dt><FolderOpen />使用文件夹</dt>
        <dd>
          {plan.mounts.length === 0 && <span>不使用宿主机文件夹</span>}
          {plan.mounts.map((mount) => (
            <span key={`${mount.hostPath}:${mount.containerPath}`} className="plan-row">
              <em className={`mount-kind ${mount.kind}`}>{mountLabels[mount.kind]}{mount.readOnly ? " · 只读" : ""}</em>
              <code>{mount.hostPath}</code>{mount.purpose && <small>{mount.purpose}</small>}
            </span>
          ))}
        </dd>
        <dt><ShieldCheck />创建容器</dt>
        <dd>{plan.containers.map((name) => <code key={name}>{name}</code>)}</dd>
      </dl>
      <details>
        <summary>查看将运行的 Compose 文件</summary>
        <pre>{plan.compose}</pre>
      </details>
      <div className="install-plan-actions">
        <small title={plan.digest}>计划摘要 {plan.digest.slice(0, 12)}</small>
        <button onClick={onCancel}>取消</button>
        <button className="primary" onClick={onConfirm}>确认安装</button>
      </div>
    </section>
  );
}

function JobLog({ job }: { job: AppJob }) {
  const title = { install: "安装", uninstall: "卸载" }[job.action];
  const state = { running: "进行中", succeeded: "已完成", failed: "失败" }[job.state];
  return (
    <section className={`job-log ${job.state}`} aria-label={`${title}任务`}>
      <h3>{title}{state}</h3>
      {job.error && <p className="job-error">{job.error}</p>}
      {job.output.length > 0 && <pre>{job.output.slice(-12).join("\n")}</pre>}
    </section>
  );
}

function useAppList() {
  const [list, setList] = useState<AppList>();
  const [error, setError] = useState<string>();
  const active = useRef<AbortController | undefined>(undefined);

  const refresh = useCallback(async () => {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    try {
      setList(await readApps(controller.signal));
      setError(undefined);
    } catch (failure) {
      if (failure instanceof DOMException && failure.name === "AbortError") return;
      setError(failure instanceof AppsAPIError ? failure.code : "request_failed");
    }
  }, []);

  // Poll quickly while an install or uninstall runs so progress stays live.
  const busy = list?.apps.some((app) => app.state === "installing" || app.state === "uninstalling") ?? false;
  useEffect(() => {
    void refresh();
    const interval = window.setInterval(() => {
      if (!document.hidden) void refresh();
    }, busy ? busyPollInterval : idlePollInterval);
    const resume = () => {
      if (!document.hidden) void refresh();
    };
    document.addEventListener("visibilitychange", resume);
    return () => {
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", resume);
    };
  }, [refresh, busy]);
  useEffect(() => () => active.current?.abort(), []);

  return { list, error, refresh };
}

function stateText(app: CatalogApp): string {
  switch (app.state) {
    case "installed":
      return app.running === app.total ? "已安装" : `已安装 · ${app.running}/${app.total} 运行`;
    case "installing":
      return "安装中";
    case "uninstalling":
      return "卸载中";
    default:
      return app.job?.state === "failed" ? "安装失败" : "可安装";
  }
}

function describeError(error: unknown): string {
  if (!(error instanceof AppsAPIError)) return "操作失败，请稍后重试";
  // Conflict messages end with the port or container that is taken.
  const taken = error.detail.split(": ").pop();
  const messages: Record<string, string> = {
    plan_changed: "应用清单已变化，请重新确认安装计划",
    app_busy: "另一个应用正在安装或卸载，请稍候",
    already_installed: "该应用已经安装",
    not_installed: "该应用尚未安装",
    port_in_use: `端口已被占用${taken ? `（${taken}）` : ""}`,
    name_in_use: `容器名已被占用${taken ? `（${taken}）` : ""}`,
    policy_violation: "该应用不符合安装安全策略",
    volume_unavailable: "数据卷当前不可用，请检查存储状态后重试",
    apps_unavailable: "无法连接容器代理",
    network_error: "无法连接 A-NAS",
  };
  return messages[error.code] ?? "容器引擎未能完成操作";
}
