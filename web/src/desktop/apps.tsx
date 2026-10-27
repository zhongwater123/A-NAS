import { ArrowDownUp, FolderClosed, Music2, Settings, Share2, ShoppingBag, Trash2 } from "lucide-react";
import { lazy, ReactNode, Suspense } from "react";

import type { Session, User } from "../api.generated";
import { AppCenterPanel } from "../appcenter/AppCenterPanel";
import { AssistantGlyph } from "../assistant/AssistantGlyph";
import { BackupCenter } from "../backup/BackupCenter";
import { BackupCenterGlyph } from "../backup/BackupCenterGlyph";
import { DockerGlyph } from "../docker/DockerGlyph";
import { DockerPanel } from "../docker/DockerPanel";
import { appProject } from "../docker/model";
import { FilePanel } from "../files/FilePanel";
import { MediaCenter } from "../media/MediaCenter";
import { type AppGlyph, MediaCenterGlyph } from "../media/MediaCenterGlyph";
import { MusicPanel } from "../music/MusicPanel";
import { displayName } from "../people/people";
import { PhotosGlyph } from "../photos/PhotosGlyph";
import { PhotosPanel } from "../photos/PhotosPanel";
import { SettingsPanel, SettingsSection, useStoragePlan } from "../settings/SettingsPanel";
import { SharesPanel } from "../shares/SharesPanel";
import type { TransferLocation } from "../transfers/api";
import { TrashPanel } from "../trash/TrashPanel";
import { DesktopApp } from "./DesktopGrid";
import type { HostStateView } from "./useHostState";

// The assistant window, with its chat libraries, loads when first opened.
const AssistantPanel = lazy(() =>
  import("../assistant/AssistantPanel").then((module) => ({ default: module.AssistantPanel })),
);
// The transfer center loads when first opened; the desktop only needs its
// status-bar pill and toasts.
const TransferCenter = lazy(() =>
  import("../transfers/TransferCenter").then((module) => ({ default: module.TransferCenter })),
);

// What another window can ask an app to show when it opens it.
export interface WindowIntentPayloads {
  // A folder the transfer center, the trash or the backup center asks the file manager to show.
  files: TransferLocation;
  // A photo another window names, such as one the assistant found.
  photos: { assetId: string };
  // The App Center's 在 Docker 中查看 opens Docker on that app's containers.
  docker: { project: string };
  // A toast of a scheduled task opens its run in VNAS, or the tasks.
  assistant: { sessionId?: string; title?: string };
}
// The intents waiting for their windows. Every request is a new object, so a
// window sees a request for the place it already shows; the window reports it
// handled and the desktop forgets it.
export type WindowIntents = { [ID in keyof WindowIntentPayloads]?: WindowIntentPayloads[ID] };
// OpenWindow opens or raises an app's window, with what to show for apps that take an intent.
export type OpenWindow = <ID extends WindowID>(
  id: ID,
  ...intent: ID extends keyof WindowIntentPayloads ? [intent?: WindowIntentPayloads[ID]] : []
) => void;

// WindowContext is what the desktop gives the apps' windows and icons.
export interface WindowContext {
  session: Session;
  isAdmin: boolean;
  host: HostStateView;
  // The assistant opens only when this device runs one for this account (ADR 0024).
  assistantReady: boolean;
  open: OpenWindow;
  intents: WindowIntents;
  intentHandled: (id: keyof WindowIntentPayloads) => void;
  // The section and any pending storage plan outlive the settings window, so
  // reopening it returns to where the user left off.
  settingsSection: SettingsSection;
  setSettingsSection: (section: SettingsSection) => void;
  storagePlan: ReturnType<typeof useStoragePlan>;
  changeUser: (user: User) => void;
  logout: () => void;
}

interface AppDefinition {
  title: string;
  icon: AppGlyph;
  // The gradient behind the app's desktop and dock icons.
  tone: string;
  // Apps on a white tile (相册, VNAS) keep it in their window's title bar too.
  lightTitle?: boolean;
  // Where the window first opens and how large.
  bounds: { x: number; y: number; width: number; height: number };
  // The app's desktop icon and launcher entry. Apps without one open from
  // elsewhere, such as 分享 from the system rail.
  launcher?: {
    // Saved icon arrangements name the icon by this id, so it stays when the app is renamed.
    id: string;
    // Keywords let the launcher find an app by what it holds, not only by its name.
    keywords: string | ((context: WindowContext) => string);
    // Docker and the App Center run containers with root-equivalent engine access.
    adminOnly?: boolean;
    // An app that is not ready shows a disabled icon marked 规划中.
    ready?: (context: WindowContext) => boolean;
  };
  // The window's content.
  render: (context: WindowContext) => ReactNode;
}

const loading = (
  <div className="center-state">
    <span className="loader" />
  </div>
);

// The desktop's built-in apps. Their order is the initial order of the desktop
// icons and of the windows on the page.
const definitions = {
  shares: {
    title: "分享",
    icon: Share2,
    tone: "share",
    bounds: { x: 214, y: 56, width: 1160, height: 700 },
    render: () => <SharesPanel />,
  },
  files: {
    title: "文件管理",
    icon: FolderClosed,
    tone: "files",
    bounds: { x: 230, y: 70, width: 900, height: 620 },
    launcher: { id: "files", keywords: "文件 共享 上传 shared" },
    render: ({ session, intents, intentHandled, open }) => (
      <FilePanel
        userID={session.user.id}
        intent={intents.files}
        onIntentHandled={() => intentHandled("files")}
        onOpenTransfers={() => open("transfers")}
        onOpenPhotos={() => open("photos")}
        onOpenMedia={() => open("media")}
        onOpenMusic={() => open("music")}
      />
    ),
  },
  trash: {
    title: "回收站",
    icon: Trash2,
    tone: "trash",
    bounds: { x: 260, y: 76, width: 920, height: 620 },
    launcher: { id: "trash", keywords: "删除 恢复" },
    render: ({ open }) => <TrashPanel onShowInFiles={(location) => open("files", location)} />,
  },
  settings: {
    title: "设置",
    icon: Settings,
    tone: "settings",
    bounds: { x: 300, y: 76, width: 920, height: 620 },
    launcher: {
      id: "settings",
      // The administrator terminal lives in Settings → 终端, so searching for it in the launcher finds 设置.
      keywords: ({ isAdmin }) =>
        `账号 密码 smb 用户 成员 权限 存储 磁盘 数据卷 系统 日志 审计 登录记录 活动 log${isAdmin ? " 终端 shell 命令行 bash" : ""}`,
    },
    render: (context) => (
      <SettingsPanel
        session={context.session}
        host={context.host}
        section={context.settingsSection}
        onSectionChange={context.setSettingsSection}
        storagePlan={context.storagePlan}
        onUserChange={context.changeUser}
        onLogout={context.logout}
      />
    ),
  },
  store: {
    title: "应用中心",
    icon: ShoppingBag,
    tone: "store",
    bounds: { x: 300, y: 60, width: 940, height: 660 },
    launcher: { id: "store", keywords: "应用 安装 商店 app", adminOnly: true },
    render: ({ open }) => <AppCenterPanel onOpenDocker={(appID) => open("docker", { project: appProject(appID) })} />,
  },
  media: {
    title: "影视中心",
    icon: MediaCenterGlyph,
    tone: "video",
    bounds: { x: 210, y: 46, width: 1120, height: 720 },
    launcher: { id: "video", keywords: "影视 电影 电视剧 视频 播放 movie video" },
    render: ({ session, isAdmin }) => <MediaCenter userId={session.user.id} isAdmin={isAdmin} />,
  },
  transfers: {
    title: "传输中心",
    icon: ArrowDownUp,
    tone: "download",
    bounds: { x: 270, y: 84, width: 900, height: 600 },
    // The id stays "download" so the icon keeps its place on desktops arranged before the rename.
    launcher: { id: "download", keywords: "传输 上传 下载 进度 续传 transfer upload download" },
    render: ({ open }) => (
      <Suspense fallback={loading}>
        <TransferCenter onOpenFiles={() => open("files")} onOpenLocation={(location) => open("files", location)} />
      </Suspense>
    ),
  },
  backup: {
    title: "备份中心",
    icon: BackupCenterGlyph,
    tone: "backup",
    bounds: { x: 220, y: 52, width: 1080, height: 700 },
    // 备份中心 replaced the separate 文件快照 window and the planned 备份 entry; it keeps the backup ID so stored icon orders still place it.
    launcher: { id: "backup", keywords: "备份 快照 文件快照 恢复 找回 历史版本 定时 snapshot backup" },
    render: ({ session, isAdmin, open }) => (
      <BackupCenter
        userId={session.user.id}
        isAdmin={isAdmin}
        onShowInFiles={(location) => open("files", location)}
        onOpenTrash={() => open("trash")}
      />
    ),
  },
  docker: {
    title: "Docker",
    icon: DockerGlyph,
    tone: "docker",
    bounds: { x: 300, y: 70, width: 960, height: 660 },
    launcher: { id: "docker", keywords: "容器 镜像 container", adminOnly: true },
    render: ({ intents, intentHandled, open }) => (
      <DockerPanel
        intent={intents.docker}
        onIntentHandled={() => intentHandled("docker")}
        onOpenStore={() => open("store")}
      />
    ),
  },
  photos: {
    title: "相册",
    icon: PhotosGlyph,
    tone: "photos",
    lightTitle: true,
    bounds: { x: 250, y: 64, width: 960, height: 660 },
    launcher: { id: "photos", keywords: "照片 图片 图库 photo" },
    render: ({ session, isAdmin, intents, intentHandled }) => (
      <PhotosPanel
        userId={session.user.id}
        userName={displayName(session.user)}
        isAdmin={isAdmin}
        intent={intents.photos}
        onIntentHandled={() => intentHandled("photos")}
      />
    ),
  },
  music: {
    title: "音乐",
    icon: Music2,
    tone: "music",
    bounds: { x: 240, y: 56, width: 1080, height: 700 },
    launcher: { id: "music", keywords: "歌曲 专辑 歌手 歌单 播放 music" },
    render: ({ session, open }) => <MusicPanel userId={session.user.id} onOpenFiles={() => open("files")} />,
  },
  assistant: {
    title: "VNAS",
    icon: AssistantGlyph,
    tone: "ai",
    lightTitle: true,
    bounds: { x: 260, y: 54, width: 1040, height: 700 },
    launcher: {
      id: "ai",
      keywords: "vnas 助手 智能 问答 聊天 assistant ai",
      ready: ({ assistantReady }) => assistantReady,
    },
    render: ({ session, open, intents, intentHandled }) => (
      <Suspense fallback={null}>
        <AssistantPanel
          username={session.user.username}
          onShowPhoto={(assetId) => open("photos", { assetId })}
          intent={intents.assistant}
          onIntentHandled={() => intentHandled("assistant")}
        />
      </Suspense>
    ),
  },
} satisfies Record<string, AppDefinition>;

// Each built-in app's window, named by its key above.
export type WindowID = keyof typeof definitions;
export const apps: Record<WindowID, AppDefinition> = definitions;

export const windowIDs = Object.keys(apps) as WindowID[];

// desktopApps lists the built-in apps for the desktop icons and the launcher.
export function desktopApps(context: WindowContext, isOpen: (id: WindowID) => boolean): DesktopApp[] {
  return windowIDs.flatMap((id): DesktopApp[] => {
    const { title, icon: Icon, tone, launcher } = apps[id];
    if (!launcher || (launcher.adminOnly && !context.isAdmin)) return [];
    if (launcher.ready && !launcher.ready(context))
      return [{ id: launcher.id, label: title, ariaLabel: `${title}，规划中`, tone, icon: <Icon />, disabled: true }];
    return [
      {
        id: launcher.id,
        label: title,
        // A Latin name keeps a space after 打开, as in 打开 Docker.
        ariaLabel: `打开${/^[A-Za-z]/.test(title) ? " " : ""}${title}`,
        keywords: typeof launcher.keywords === "function" ? launcher.keywords(context) : launcher.keywords,
        tone,
        icon: <Icon />,
        active: isOpen(id),
        onClick: () => context.open(id),
      },
    ];
  });
}
