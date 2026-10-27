import {
  ArrowUpRight,
  BookText,
  Boxes,
  Check,
  ChevronLeft,
  CircleAlert,
  CodeXml,
  Download,
  Ellipsis,
  Globe,
  KeyRound,
  Loader,
  LockKeyhole,
  RotateCcw,
  ScanSearch,
  ShieldCheck,
  Trash2,
  TriangleAlert,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { CatalogApp } from "./api";
import { AppIcon, Folded, JobOutput, Logins, Progress, RichText } from "./parts";
import { appURL, categoryOf, installFailed, leftoverText, openApp, useElapsed } from "./model";

interface DetailProps {
  app: CatalogApp;
  backLabel: string;
  onBack: () => void;
  onInstall: () => void;
  onUninstall: () => void;
  onOpenDocker?: () => void;
}

export function AppDetail({ app, backLabel, onBack, onInstall, onUninstall, onOpenDocker }: DetailProps) {
  const category = categoryOf(app);
  const developer = app.developer || app.author;
  const facts = [
    { label: "版本", value: app.version },
    { label: "分类", value: category.label },
    { label: "开发者", value: developer },
    { label: "网页端口", value: app.webPort ? String(app.webPort) : "无网页界面" },
    { label: "清单更新", value: app.updated },
  ].filter((fact) => fact.value);
  const links = [
    { label: "官网", href: app.website, icon: Globe },
    { label: "源码", href: app.repo, icon: CodeXml },
    { label: "文档", href: app.docs, icon: BookText },
  ].filter((link) => link.href);

  return (
    <div className="ac-detail">
      <button className="ac-back" onClick={onBack}>
        <ChevronLeft />
        {backLabel}
      </button>

      <header className={`ac-app-head ${category.id || "other"}`}>
        <AppIcon app={app} size="xl" />
        <div className="ac-app-title">
          <h2>{app.title}</h2>
          <p>{app.tagline}</p>
          {developer && <small>{developer}</small>}
        </div>
        <HeaderActions app={app} onInstall={onInstall} onUninstall={onUninstall} onOpenDocker={onOpenDocker} />
      </header>

      <dl className="ac-facts">
        {facts.map((fact) => (
          <div key={fact.label}>
            <dt>{fact.label}</dt>
            <dd title={fact.value}>{fact.value}</dd>
          </div>
        ))}
      </dl>

      <StatusPanel app={app} onInstall={onInstall} onOpenDocker={onOpenDocker} />

      {app.notes && (
        <section className="ac-notes" aria-label="使用须知">
          <h3>
            <KeyRound />
            使用须知
          </h3>
          <Folded lines={9}>
            <RichText text={app.notes} />
          </Folded>
          {app.upstreamNotes ? (
            <details className="ac-upstream">
              <summary>
                <span>上游原始说明</span>
                <small>来自 CasaOS 应用商店，其中的设置步骤和路径针对 CasaOS，在 A-NAS 中不适用</small>
              </summary>
              <RichText text={app.upstreamNotes} />
            </details>
          ) : (
            <small>来自应用的上游清单；文件夹与端口以安装计划为准。</small>
          )}
        </section>
      )}

      {(app.description || app.tagline) && (
        <section className="ac-section">
          <h3>介绍</h3>
          <Folded>
            <RichText text={app.description || app.tagline} />
          </Folded>
        </section>
      )}

      <section className="ac-section">
        <h3>A-NAS 如何运行它</h3>
        <ul className="ac-trust">
          <li>
            <ScanSearch />
            <span>
              <strong>安装前可审查</strong>列出将拉取的镜像、开放的端口、使用的文件夹和完整 Compose 文件，确认后才执行。
            </span>
          </li>
          <li>
            <LockKeyhole />
            <span>
              <strong>独立账号，最小权限</strong>
              不获得特权、设备或宿主机网络，只能访问计划中的文件夹；能读写共享空间的服务必须以应用专属账号运行，共享空间默认只读。
            </span>
          </li>
          <li>
            <RotateCcw />
            <span>
              <strong>卸载保留数据</strong>卸载删除容器与网络，应用数据留在数据卷上，重新安装后可继续使用。
            </span>
          </li>
        </ul>
      </section>

      {links.length > 0 && (
        <section className="ac-section">
          <h3>链接</h3>
          <div className="ac-links">
            {links.map(({ label, href, icon: Icon }) => (
              <a key={label} href={href} target="_blank" rel="noopener noreferrer">
                <Icon />
                {label}
                <ArrowUpRight className="ac-link-arrow" />
              </a>
            ))}
          </div>
        </section>
      )}
      <p className="ac-source">
        清单来自 CasaOS AppStore（Apache-2.0），由 A-NAS 审查后内置；介绍和使用须知已按 A-NAS 校订。
      </p>
    </div>
  );
}

function HeaderActions({
  app,
  onInstall,
  onUninstall,
  onOpenDocker,
}: Pick<DetailProps, "app" | "onInstall" | "onUninstall" | "onOpenDocker">) {
  if (app.state === "installing" || app.state === "uninstalling") {
    return (
      <div className="ac-app-actions">
        <button className="ac-btn lg" disabled>
          <Loader className="ac-spin" />
          {app.state === "installing" ? "安装中…" : "卸载中…"}
        </button>
      </div>
    );
  }
  if (app.state === "available" || app.state === "failed") {
    return (
      <div className="ac-app-actions">
        <button className="ac-btn primary lg" onClick={onInstall}>
          <Download />
          安装
        </button>
        {app.state === "failed" && <MoreMenu onUninstall={onUninstall} onOpenDocker={onOpenDocker} />}
      </div>
    );
  }
  return (
    <div className="ac-app-actions">
      {appURL(app) ? (
        <button className="ac-btn primary lg" onClick={() => openApp(app)}>
          打开
          <ArrowUpRight />
        </button>
      ) : (
        <span className="ac-btn lg static">
          <Check />
          已安装
        </span>
      )}
      <MoreMenu onUninstall={onUninstall} onOpenDocker={onOpenDocker} />
    </div>
  );
}

function MoreMenu({ onUninstall, onOpenDocker }: { onUninstall: () => void; onOpenDocker?: () => void }) {
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (event: PointerEvent) => {
      if (!anchor.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [open]);
  // Focus goes back to the menu button first, so a dialog the item opens returns it there.
  const choose = (action?: () => void) => {
    setOpen(false);
    trigger.current?.focus();
    action?.();
  };
  return (
    <div
      className="ac-menu-anchor"
      ref={anchor}
      onKeyDown={(event) => {
        if (event.key === "Escape" && open) {
          event.stopPropagation();
          setOpen(false);
        }
      }}
    >
      <button
        ref={trigger}
        className={`ac-icon-btn ${open ? "open" : ""}`}
        aria-label="更多操作"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <Ellipsis />
      </button>
      {open && (
        <div className="ac-menu" role="menu">
          {onOpenDocker && (
            <button role="menuitem" onClick={() => choose(onOpenDocker)}>
              <Boxes />在 Docker 中查看
            </button>
          )}
          <button role="menuitem" className="danger" onClick={() => choose(onUninstall)}>
            <Trash2 />
            卸载
          </button>
        </div>
      )}
    </div>
  );
}

// What the app is doing now: running, being installed or removed, or why the last install failed.
function StatusPanel({
  app,
  onInstall,
  onOpenDocker,
}: {
  app: CatalogApp;
  onInstall: () => void;
  onOpenDocker?: () => void;
}) {
  const running = app.job?.state === "running";
  const elapsed = useElapsed(app.job?.startedAt, running);
  const url = appURL(app);

  if (app.state === "installing" || app.state === "uninstalling") {
    const installing = app.state === "installing";
    const latest = app.job?.output.at(-1);
    return (
      <section className="ac-status-card busy" aria-label={installing ? "安装进度" : "卸载进度"}>
        <div className="ac-status-head">
          <Loader className="ac-spin" />
          <div>
            <strong>
              {installing ? "正在安装" : "正在卸载"}
              {elapsed && <span className="ac-elapsed"> · 已用时 {elapsed}</span>}
            </strong>
            <p>
              {installing
                ? "拉取镜像和启动容器可能需要几分钟，可以关闭窗口，安装会在后台继续。"
                : "正在停止并删除容器与网络，应用数据会保留。"}
            </p>
          </div>
        </div>
        <Progress />
        {latest && <code className="ac-latest">{latest}</code>}
        {app.job && <JobOutput job={app.job} />}
      </section>
    );
  }

  if (app.state === "installed") {
    const healthy = app.running === app.total;
    return (
      <section className={`ac-status-card ${healthy ? "ok" : "warn"}`} aria-label="运行状态">
        <div className="ac-status-head">
          {healthy ? <ShieldCheck /> : <CircleAlert />}
          <div>
            <strong>
              已安装 · {app.running}/{app.total} 个容器运行中
            </strong>
            <p>
              {url ? (
                <>
                  在局域网中访问{" "}
                  <a href={url} target="_blank" rel="noopener noreferrer">
                    {url}
                  </a>
                </>
              ) : (
                "该应用没有网页界面。"
              )}
            </p>
          </div>
          {onOpenDocker && (
            <button className="ac-btn" onClick={onOpenDocker}>
              <Boxes />在 Docker 中查看
            </button>
          )}
        </div>
        {!healthy && (
          <ul className="ac-problems" aria-label="没有运行的容器">
            {(app.problems?.length ? app.problems : [{ service: "", state: "", exitCode: 0 }]).map((problem, index) => (
              <li key={`${problem.service}-${index}`}>
                <CircleAlert />
                {problemText(problem)}
              </li>
            ))}
            <li className="ac-problems-hint">原因通常在容器日志里，可在 Docker 中查看。</li>
          </ul>
        )}
        {app.runtimeOutdated && (
          <div className="ac-banner-note warning" role="note">
            <TriangleAlert />
            <span>
              它按旧的运行规则安装，容器仍以安装时的身份与权限运行。卸载（应用数据会保留）后重新安装，即按当前规则运行：以独立账号运行，共享空间默认只读。
            </span>
          </div>
        )}
        {app.credentials?.length ? (
          <div className="ac-status-logins" aria-label="登录信息">
            <strong>登录信息</strong>
            <Logins credentials={app.credentials} />
          </div>
        ) : null}
        {app.job && (
          <JobOutput job={app.job} summary={app.job.action === "install" ? "上次安装的输出" : "上次操作的输出"} />
        )}
      </section>
    );
  }

  if (installFailed(app) && app.job) {
    const leftovers = leftoverText(app);
    return (
      <section className="ac-status-card error" aria-label="安装失败">
        <div className="ac-status-head">
          <CircleAlert />
          <div>
            <strong>上次安装没有完成</strong>
            <p>{app.job.error || "Docker Compose 没有成功启动应用，详见下方输出。"}</p>
          </div>
          <button className="ac-btn" onClick={onInstall}>
            <RotateCcw />
            重新安装
          </button>
        </div>
        {leftovers && (
          <div className="ac-banner-note warning" role="note">
            <TriangleAlert />
            <span>{leftovers}</span>
          </div>
        )}
        <JobOutput job={app.job} open />
      </section>
    );
  }
  return null;
}

// problemText words why a container of an installed app is not running.
function problemText(problem: { service: string; state: string; exitCode: number }): string {
  const name = problem.service ? `容器 ${problem.service} ` : "有容器";
  switch (problem.state) {
    case "restarting":
      return `${name}不断重启（退出码 ${problem.exitCode}）`;
    case "exited":
    case "dead":
      return `${name}已退出（退出码 ${problem.exitCode}）`;
    case "created":
      return `${name}没有启动`;
    case "paused":
      return `${name}已暂停`;
    default:
      return `${name}没有运行`;
  }
}
