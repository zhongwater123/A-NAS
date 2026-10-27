import { CircleAlert, Compass, PackageCheck, RotateCw, Search, ShieldCheck, ShoppingBag, X } from "lucide-react";
import { KeyboardEvent, ReactNode, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

import { APIError } from "../api/client";
import { AppDetail } from "./AppDetail";
import { AppList, CatalogApp, readApps } from "./api";
import styles from "./appcenter.module.css";
import { CategoryPage, CategoryTile, Discover, Installed, listedAsInstalled, SearchResults } from "./Browse";
import { InstallSheet, UninstallDialog } from "./InstallSheet";
import { categories, categoryOf, isBusy, search } from "./model";
import { preloadMarkdown } from "./parts";

const busyPollInterval = 1_500;
const idlePollInterval = 10_000;

type View = { kind: "discover" } | { kind: "installed" } | { kind: "category"; id: string };
type Modal = { kind: "install" | "uninstall"; appId: string };

// onOpenDocker shows an installed app's containers in the Docker window.
export function AppCenterPanel({ onOpenDocker }: { onOpenDocker?: (appID: string) => void }) {
  const { list, error, refresh } = useAppList();
  const [view, setView] = useState<View>({ kind: "discover" });
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<string>();
  const [modal, setModal] = useState<Modal>();
  const main = useRef<HTMLElement>(null);
  // The list's scroll position, restored when the owner comes back from an app.
  const listScroll = useRef(0);
  // The control that opened a sheet; making the page inert takes its focus, so it is restored by hand.
  const opener = useRef<HTMLElement | null>(null);

  const apps = list?.apps ?? [];
  const current = apps.find((app) => app.id === selected);
  const modalApp = apps.find((app) => app.id === modal?.appId);
  const busyApp = apps.find(isBusy);
  const installed = apps.filter(listedAsInstalled);

  useEffect(() => void preloadMarkdown(), []);
  const currentId = current?.id;
  useLayoutEffect(() => {
    if (main.current) main.current.scrollTop = currentId ? 0 : listScroll.current;
  }, [currentId]);
  useEffect(() => {
    if (modal || !opener.current) return;
    if (opener.current.isConnected) opener.current.focus();
    opener.current = null;
  }, [modal]);

  const show = (app: CatalogApp) => {
    listScroll.current = main.current?.scrollTop ?? 0;
    setSelected(app.id);
  };
  const openModal = (next: Modal) => {
    opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setModal(next);
  };
  const install = (app: CatalogApp) => {
    if (!current) show(app);
    openModal({ kind: "install", appId: app.id });
  };
  const navigate = (next: View) => {
    listScroll.current = 0;
    setSelected(undefined);
    setQuery("");
    setView(next);
  };
  const closeModal = useCallback(() => setModal(undefined), []);

  if (!list && error === "apps_disabled") {
    return (
      <FullState icon={<ShoppingBag />} title="应用中心未启用">
        <p>安装 Docker 与 A-NAS 容器代理后即可从这里安装应用。</p>
      </FullState>
    );
  }
  if (!list && error) {
    return (
      <FullState icon={<CircleAlert />} title="无法连接应用中心" tone="error">
        <p>请确认容器代理正在运行。</p>
        <button className={`${styles.acBtn} primary`} onClick={() => void refresh()}>
          <RotateCw />
          重新连接
        </button>
      </FullState>
    );
  }

  const searching = query.trim() !== "";
  const category = view.kind === "category" ? categories.find((item) => item.id === view.id) : undefined;
  const backLabel = searching ? "搜索结果" : view.kind === "installed" ? "已安装" : (category?.label ?? "发现");
  let page: ReactNode;
  if (!list) {
    page = (
      <div className={styles.acLoading}>
        <span className="loader" />
        <p>正在载入应用…</p>
      </div>
    );
  } else if (current) {
    page = (
      <AppDetail
        app={current}
        backLabel={backLabel}
        onBack={() => setSelected(undefined)}
        onInstall={() => install(current)}
        onUninstall={() => openModal({ kind: "uninstall", appId: current.id })}
        onOpenDocker={onOpenDocker && (() => onOpenDocker(current.id))}
      />
    );
  } else if (searching) {
    page = (
      <SearchResults
        query={query}
        apps={search(apps, query)}
        onShow={show}
        onInstall={install}
        onBrowse={() => navigate({ kind: "discover" })}
      />
    );
  } else if (view.kind === "installed") {
    page = (
      <Installed apps={installed} onShow={show} onInstall={install} onBrowse={() => navigate({ kind: "discover" })} />
    );
  } else if (category) {
    page = (
      <CategoryPage
        category={category}
        apps={apps.filter((app) => categoryOf(app) === category)}
        onShow={show}
        onInstall={install}
      />
    );
  } else {
    page = (
      <Discover apps={apps} onShow={show} onInstall={install} onCategory={(id) => navigate({ kind: "category", id })} />
    );
  }

  const onSearchKey = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Escape" && query) {
      event.stopPropagation();
      setQuery("");
    }
  };
  const active = (next: View) =>
    !searching &&
    !current &&
    view.kind === next.kind &&
    (next.kind !== "category" || (view.kind === "category" && view.id === next.id));

  return (
    <div className={styles.acShell}>
      <nav className={styles.acNav} aria-label="应用中心导航" inert={Boolean(modal)}>
        <label className={styles.acSearch}>
          <Search />
          <input
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setSelected(undefined);
            }}
            onKeyDown={onSearchKey}
            placeholder="搜索应用"
            aria-label="搜索应用"
          />
          {query && (
            <button className={styles.acSearchClear} aria-label="清除搜索" onClick={() => setQuery("")}>
              <X />
            </button>
          )}
        </label>
        <NavItem
          active={active({ kind: "discover" })}
          onClick={() => navigate({ kind: "discover" })}
          icon={
            <span className={`${styles.acTile} ${styles.discover}`}>
              <Compass />
            </span>
          }
          label="发现"
        />
        <NavItem
          active={active({ kind: "installed" })}
          onClick={() => navigate({ kind: "installed" })}
          icon={
            <span className={`${styles.acTile} ${styles.installed}`}>
              <PackageCheck />
            </span>
          }
          label="已安装"
          end={
            busyApp ? (
              <span className={`loader ${styles.small}`} aria-label="有任务进行中" />
            ) : (
              installed.length > 0 && <span className={styles.acCount}>{installed.length}</span>
            )
          }
        />
        <p className={styles.acNavLabel}>分类</p>
        {categories.map((item) => {
          const count = apps.filter((app) => app.category === item.id).length;
          if (list && count === 0) return null;
          return (
            <NavItem
              key={item.id}
              active={active({ kind: "category", id: item.id })}
              onClick={() => navigate({ kind: "category", id: item.id })}
              icon={<CategoryTile category={item} size="sm" />}
              label={item.label}
              end={list && <span className={`${styles.acCount} ${styles.muted}`}>{count}</span>}
            />
          );
        })}
        <footer className={styles.acNavFoot}>
          <ShieldCheck />
          <span>
            {apps.length > 0 ? `${apps.length} 个应用均经过安全审查` : "应用均经过安全审查"}
            <small>清单来自 CasaOS AppStore{list?.dataSource === "simulated" ? " · 模拟数据" : ""}</small>
          </span>
        </footer>
      </nav>
      <main className={styles.acMain} ref={main} inert={Boolean(modal)}>
        {error && list && (
          <div className={`${styles.acBannerNote} ${styles.error} ${styles.acOffline}`}>
            <CircleAlert />
            连接中断，正在显示上次读取的应用状态
          </div>
        )}
        {page}
      </main>
      {modal?.kind === "install" && modalApp && (
        <InstallSheet key={modalApp.id} app={modalApp} busyApp={busyApp} onChanged={refresh} onClose={closeModal} />
      )}
      {modal?.kind === "uninstall" && modalApp && (
        <UninstallDialog app={modalApp} onChanged={refresh} onClose={closeModal} />
      )}
    </div>
  );
}

function NavItem({
  active,
  onClick,
  icon,
  label,
  end,
}: {
  active: boolean;
  onClick: () => void;
  icon: ReactNode;
  label: string;
  end?: ReactNode;
}) {
  return (
    <button
      className={`${styles.acNavItem} ${active ? styles.active : ""}`}
      aria-current={active ? "page" : undefined}
      onClick={onClick}
    >
      {icon}
      <span>{label}</span>
      {end}
    </button>
  );
}

function FullState({
  icon,
  title,
  tone,
  children,
}: {
  icon: ReactNode;
  title: string;
  tone?: "error";
  children: ReactNode;
}) {
  return (
    <div className={`${styles.acShell} ${styles.single}`}>
      <div className={`${styles.acEmpty} ${styles.full}`}>
        <span className={`${styles.acEmptyArt} ${styles[tone ?? ""] ?? ""}`}>{icon}</span>
        <h3>{title}</h3>
        {children}
      </div>
    </div>
  );
}

function useAppList() {
  const [list, setList] = useState<AppList>();
  const [error, setError] = useState<string>();
  const active = useRef<AbortController | undefined>(undefined);

  const refresh = useCallback(() => {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    return readApps(controller.signal)
      .then((read) => {
        setList(read);
        setError(undefined);
      })
      .catch((failure: unknown) => {
        if (failure instanceof DOMException && failure.name === "AbortError") return;
        setError(failure instanceof APIError ? failure.code : "request_failed");
      });
  }, []);

  // Poll quickly while an install or uninstall runs so progress stays live.
  const busy = list?.apps.some(isBusy) ?? false;
  useEffect(() => {
    void refresh();
    const interval = window.setInterval(
      () => {
        if (!document.hidden) void refresh();
      },
      busy ? busyPollInterval : idlePollInterval,
    );
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
