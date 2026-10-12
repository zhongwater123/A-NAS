import { ChevronRight, Compass, LockKeyhole, PackageCheck, PackageOpen, RotateCcw, Search, ShieldCheck } from "lucide-react";

import { CatalogApp } from "../appsApi";
import { AppAction, AppIcon } from "./parts";
import { Category, categories, categoryOf, installFailed, otherCategory, stateText, useElapsed } from "./model";

interface BrowseProps {
  onShow: (app: CatalogApp) => void;
  onInstall: (app: CatalogApp) => void;
}

// Discover shows a few apps per category and links to the rest.
const shelfSize = 4;
// Icons fanned out on the banner, one per kind of app.
const bannerApps = ["immich", "navidrome", "vaultwarden", "syncthing", "memos"];

export function Discover({ apps, onShow, onInstall, onCategory }: BrowseProps & { apps: CatalogApp[]; onCategory: (id: string) => void }) {
  const shelves = [...categories, otherCategory]
    .map((category) => ({ category, apps: apps.filter((app) => categoryOf(app) === category) }))
    .filter((shelf) => shelf.apps.length > 0);
  const art =[...apps].sort((a, b) => rank(a) - rank(b)).slice(0, 5);
  return (
    <div className="ac-page">
      <section className="ac-banner" aria-label="应用中心简介">
        <div className="ac-banner-text">
          <p className="ac-eyebrow">发现</p>
          <h2>给 NAS 装上好用的应用</h2>
          <p>{apps.length} 个开源应用均经过 A-NAS 安全审查。安装前列出它会用到的文件夹和端口，确认后才执行；每个应用以独立账号运行，卸载时保留数据。</p>
          <ul className="ac-banner-points">
            <li><ShieldCheck />安装前可审查</li>
            <li><LockKeyhole />独立账号运行</li>
            <li><RotateCcw />卸载保留数据</li>
          </ul>
        </div>
        <div className="ac-banner-art" aria-hidden="true">
          {art.map((app, index) => <span key={app.id} className={`ac-banner-icon i${index}`}><AppIcon app={app} size="lg" /></span>)}
        </div>
      </section>
      {shelves.map(({ category, apps: shelf }) => (
        <section key={category.id} className="ac-shelf" aria-label={category.label}>
          <header className="ac-shelf-head">
            <CategoryTile category={category} />
            <div>
              <h3>{category.label}</h3>
              <p>{category.blurb}</p>
            </div>
            {shelf.length > shelfSize && category.id && (
              <button className="ac-link-button" onClick={() => onCategory(category.id)}>全部 {shelf.length} 个<ChevronRight /></button>
            )}
          </header>
          <AppGrid apps={shelf.slice(0, shelfSize)} onShow={onShow} onInstall={onInstall} />
        </section>
      ))}
    </div>
  );
}

function rank(app: CatalogApp): number {
  const index = bannerApps.indexOf(app.id);
  return index < 0 ? bannerApps.length : index;
}

export function CategoryPage({ category, apps, onShow, onInstall }: BrowseProps & { category: Category; apps: CatalogApp[] }) {
  return (
    <div className="ac-page">
      <header className="ac-page-head">
        <CategoryTile category={category} size="lg" />
        <div>
          <h2>{category.label}</h2>
          <p>{category.blurb} · {apps.length} 个应用</p>
        </div>
      </header>
      <AppGrid apps={apps} onShow={onShow} onInstall={onInstall} />
    </div>
  );
}

export function SearchResults({ query, apps, onShow, onInstall, onBrowse }: BrowseProps & { query: string; apps: CatalogApp[]; onBrowse: () => void }) {
  return (
    <div className="ac-page">
      <header className="ac-page-head">
        <div>
          <h2>“{query.trim()}”的搜索结果</h2>
          <p>{apps.length > 0 ? `找到 ${apps.length} 个应用` : "按名称、简介或分类搜索"}</p>
        </div>
      </header>
      {apps.length > 0 ? (
        <AppGrid apps={apps} showCategory onShow={onShow} onInstall={onInstall} />
      ) : (
        <div className="ac-empty">
          <span className="ac-empty-art"><Search /></span>
          <h3>没有匹配的应用</h3>
          <p>换个关键词试试，或者按分类浏览全部应用。</p>
          <button className="ac-btn" onClick={onBrowse}><Compass />浏览全部应用</button>
        </div>
      )}
    </div>
  );
}

export function Installed({ apps, onShow, onInstall, onBrowse }: BrowseProps & { apps: CatalogApp[]; onBrowse: () => void }) {
  return (
    <div className="ac-page">
      <header className="ac-page-head">
        <span className="ac-tile lg installed"><PackageCheck /></span>
        <div>
          <h2>已安装</h2>
          <p>{apps.length > 0 ? `${apps.length} 个应用 · 容器与日志可在 Docker 中查看` : "从这里打开和管理已安装的应用"}</p>
        </div>
      </header>
      {apps.length > 0 ? (
        <div className="ac-list" role="list">
          {apps.map((app) => <InstalledRow key={app.id} app={app} onShow={onShow} onInstall={onInstall} />)}
        </div>
      ) : (
        <div className="ac-empty">
          <span className="ac-empty-art"><PackageOpen /></span>
          <h3>还没有安装应用</h3>
          <p>在“发现”中挑选应用，查看安装计划后一键安装。</p>
          <button className="ac-btn primary" onClick={onBrowse}><Compass />去发现应用</button>
        </div>
      )}
    </div>
  );
}

// Installed apps, apps being installed or removed, and failed installs the owner should see.
export const listedAsInstalled = (app: CatalogApp) => app.state !== "available" || installFailed(app);

function InstalledRow({ app, onShow, onInstall }: BrowseProps & { app: CatalogApp }) {
  const elapsed = useElapsed(app.job?.startedAt, app.job?.state === "running");
  const [tone, line] = statusLine(app, elapsed);
  return (
    <article className="ac-row" role="listitem">
      <AppIcon app={app} size="sm" />
      <div className="ac-row-text">
        <button className="ac-card-link" aria-label={`${app.title}，${stateText(app)}`} onClick={() => onShow(app)}>{app.title}</button>
        <small className={`ac-status ${tone}`}><i />{line}</small>
      </div>
      <AppAction app={app} onInstall={onInstall} />
      <ChevronRight className="ac-row-chevron" aria-hidden="true" />
    </article>
  );
}

function statusLine(app: CatalogApp, elapsed?: string): ["ok" | "warn" | "busy" | "error", string] {
  switch (app.state) {
    case "installed": {
      if (app.running < app.total) return ["warn", `${app.running}/${app.total} 个容器运行中`];
      return ["ok", ["运行中", `${app.total} 个容器`, app.webPort && `端口 ${app.webPort}`].filter(Boolean).join(" · ")];
    }
    case "installing":
      return ["busy", `正在安装${elapsed ? ` · 已用时 ${elapsed}` : "…"}`];
    case "uninstalling":
      return ["busy", "正在卸载…"];
    default:
      return ["error", "安装失败，打开查看原因"];
  }
}

function AppGrid({ apps, showCategory, onShow, onInstall }: BrowseProps & { apps: CatalogApp[]; showCategory?: boolean }) {
  return (
    <div className="ac-grid" role="list">
      {apps.map((app) => <AppCard key={app.id} app={app} showCategory={showCategory} onShow={onShow} onInstall={onInstall} />)}
    </div>
  );
}

function AppCard({ app, showCategory, onShow, onInstall }: BrowseProps & { app: CatalogApp; showCategory?: boolean }) {
  const failed = installFailed(app);
  const partial = app.state === "installed" && app.running < app.total;
  const meta = failed ? "上次安装失败" : partial ? `${app.running}/${app.total} 个容器运行中` : showCategory ? categoryOf(app).label : undefined;
  return (
    <article className="ac-card" role="listitem">
      <AppIcon app={app} />
      <div className="ac-card-text">
        <button className="ac-card-link" aria-label={`${app.title}，${stateText(app)}`} onClick={() => onShow(app)}>{app.title}</button>
        <p>{app.tagline || app.description}</p>
        {meta && <small className={failed ? "danger" : partial ? "warn" : undefined}>{meta}</small>}
      </div>
      <AppAction app={app} onInstall={onInstall} />
    </article>
  );
}

export function CategoryTile({ category, size }: { category: Category; size?: "sm" | "lg" }) {
  const Icon = category.icon;
  return <span className={`ac-tile ${size ?? ""} ${category.id || "other"}`} aria-hidden="true"><Icon /></span>;
}
