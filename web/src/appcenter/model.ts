import {
  Activity,
  BookOpen,
  Download,
  FolderSync,
  Images,
  type LucideIcon,
  Music,
  NotebookPen,
  Router,
  Shapes,
  Workflow,
  Wrench,
} from "lucide-react";
import { useEffect, useState } from "react";

import { AppsAPIError, CatalogApp, InstallPlan, ServiceRuntime } from "../appsApi";

export interface Category {
  id: string;
  label: string;
  blurb: string;
  icon: LucideIcon;
}

// Same order as appstore.Categories; the server assigns every catalog app one of these.
export const categories: Category[] = [
  { id: "media", label: "影音", blurb: "音乐、有声书与播客", icon: Music },
  { id: "reading", label: "阅读", blurb: "电子书、漫画与新闻订阅", icon: BookOpen },
  { id: "photos", label: "照片", blurb: "照片与视频图库", icon: Images },
  { id: "downloads", label: "下载", blurb: "BT 与网盘文件下载", icon: Download },
  { id: "files", label: "文件与同步", blurb: "网盘、同步、文件分享与备份", icon: FolderSync },
  { id: "notes", label: "笔记与知识", blurb: "笔记、知识库与书签", icon: NotebookPen },
  { id: "workflow", label: "协作与自动化", blurb: "协作文档、白板、聊天与工作流", icon: Workflow },
  { id: "tools", label: "实用工具", blurb: "PDF、格式转换与记账", icon: Wrench },
  { id: "network", label: "网络工具", blurb: "动态域名与网速测试", icon: Router },
  { id: "system", label: "监控与安全", blurb: "监控面板与密码管理", icon: Activity },
];

// An older container agent reports no A-NAS category.
export const otherCategory: Category = { id: "", label: "其他", blurb: "尚未分类的应用", icon: Shapes };

export function categoryOf(app: CatalogApp): Category {
  return categories.find((category) => category.id === app.category) ?? otherCategory;
}

export const mountLabels: Record<InstallPlan["mounts"][number]["kind"], string> = {
  appdata: "应用数据",
  shared: "共享空间",
  system: "系统只读",
};

export const runAsLabels: Record<ServiceRuntime["runAs"], string> = {
  app: "应用账号",
  "app-after-init": "初始化后降权",
  image: "镜像自带身份",
};

// One line on how the app runs, from its services; an older container agent sends none.
export function runtimeSummary(plan: InstallPlan): string {
  const services = plan.services ?? [];
  const account = plan.identity.username;
  if (services.length === 0) return `账号 ${account}`;
  if (services.every((service) => service.runAs === "image")) return "以镜像自带的身份运行，可能是 root";
  if (services.some((service) => service.runAs === "image"))
    return `部分服务以镜像自带的身份运行，其余以 ${account} 运行`;
  if (services.some((service) => service.runAs === "app-after-init"))
    return `以 root 初始化后，以独立账号 ${account} 运行`;
  return `以独立账号 ${account} 运行`;
}

// What one service is allowed, in the words of the install plan.
export function serviceText(service: ServiceRuntime, plan: InstallPlan): string {
  const shared =
    service.shared === "write"
      ? "，经 a-nas-users 读写共享空间"
      : service.shared === "read"
        ? "，经 a-nas-users 只读访问共享空间"
        : "";
  switch (service.runAs) {
    case "app":
      return `UID ${plan.identity.uid}，没有特权能力${shared}`;
    case "app-after-init":
      return `入口程序以 root 准备后，以 UID ${plan.identity.uid} 运行${shared}`;
    default:
      return "可能是 root，只能访问自己的应用数据";
  }
}

export const isBusy = (app: CatalogApp) => app.state === "installing" || app.state === "uninstalling";
// A failed install was rolled back (available) or left containers behind (failed).
export const installFailed = (app: CatalogApp) =>
  app.state === "failed" || (app.state === "available" && app.job?.action === "install" && app.job.state === "failed");

// The containers a failed install could not remove, which were never checked against its plan.
export function leftoverText(app: CatalogApp): string | undefined {
  if (app.state !== "failed") return undefined;
  return `回滚没有完成：${app.total} 个容器留在 Docker 中（${app.running} 个运行中），它们没有按安装计划核对。重新安装会先删除它们，也可以在“更多操作”中卸载。`;
}

export function stateText(app: CatalogApp): string {
  switch (app.state) {
    case "installed":
      return app.running === app.total ? "已安装" : `已安装 · ${app.running}/${app.total} 运行`;
    case "installing":
      return "安装中";
    case "uninstalling":
      return "卸载中";
    default:
      return installFailed(app) ? "安装失败" : "可安装";
  }
}

// The app's web interface on the address this desktop was opened from.
export function appURL(
  app: Pick<CatalogApp, "webPort" | "scheme" | "path">,
  location: Location = window.location,
): string | undefined {
  if (!app.webPort) return undefined;
  const url = new URL(location.href);
  url.protocol = `${app.scheme || "http"}:`;
  url.port = String(app.webPort);
  url.pathname = app.path || "/";
  url.search = "";
  url.hash = "";
  return url.toString();
}

export function openApp(app: Pick<CatalogApp, "webPort" | "scheme" | "path">) {
  const url = appURL(app);
  if (url) window.open(url, "_blank", "noopener,noreferrer");
}

// Apps matching every word, best first: a word in the name counts more than
// one in the category or tagline, which count more than one in the description.
export function search(apps: CatalogApp[], query: string): CatalogApp[] {
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const scored = apps.map((app) => {
    const fields: Array<[string, number]> = [
      [`${app.title} ${app.id}`, 8],
      [categoryOf(app).label, 4],
      [app.tagline, 3],
      [app.description, 1],
    ];
    let score = 0;
    for (const word of words) {
      const best = Math.max(
        0,
        ...fields.filter(([text]) => text.toLowerCase().includes(word)).map(([, weight]) => weight),
      );
      if (best === 0) return { app, score: 0 };
      score += best;
    }
    return { app, score };
  });
  return scored
    .filter((item) => item.score > 0)
    .sort((a, b) => b.score - a.score)
    .map((item) => item.app);
}

export function describeError(error: unknown): string {
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
    address_pool_missing: "Docker 尚未配置 A-NAS 网络地址池，暂不能安装会新建网络的应用",
    docker_runtime_outdated: "Docker 或 Docker Compose 版本过旧，无法把应用限制在它的文件夹内，请先升级",
    volume_unavailable: "数据卷当前不可用，请检查存储状态后重试",
    apps_unavailable: "无法连接容器代理",
    network_error: "无法连接 A-NAS",
  };
  return messages[error.code] ?? "容器引擎未能完成操作";
}

// Elapsed time of a running job as m:ss, ticking once a second.
export function useElapsed(startedAt: string | undefined, running: boolean): string | undefined {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!running) return;
    const tick = () => setNow(Date.now());
    const first = window.setTimeout(tick, 0);
    const timer = window.setInterval(tick, 1_000);
    return () => {
      window.clearTimeout(first);
      window.clearInterval(timer);
    };
  }, [running]);
  const started = startedAt ? Date.parse(startedAt) : NaN;
  if (!running || Number.isNaN(started)) return undefined;
  const seconds = Math.max(0, Math.floor((now - started) / 1_000));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}
