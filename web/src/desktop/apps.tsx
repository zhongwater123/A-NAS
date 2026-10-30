import { ArrowDownUp, FolderClosed, Music2, Settings, Share2, ShoppingBag, Trash2 } from "lucide-react";
import { createContext, ReactNode, use } from "react";

import type { Session, User } from "../api.generated";
import { AssistantGlyph } from "../assistant/AssistantGlyph";
import { BackupCenterGlyph } from "../backup/BackupCenterGlyph";
import { DockerGlyph } from "../docker/DockerGlyph";
import { appProject } from "../docker/model";
import { FamilyGlyph } from "../family/FamilyGlyph";
import { type AppGlyph, MediaCenterGlyph } from "../media/MediaCenterGlyph";
import { displayName } from "../people/people";
import { PhotosGlyph } from "../photos/PhotosGlyph";
import type { SettingsSection } from "../settings/SettingsPanel";
import type { StoragePlanFlow } from "../settings/storagePlan";
import type { TransferLocation } from "../transfers/api";
import { lazyCode } from "../ui/lazyCode";
import { WebSearchGlyph } from "../websearch/WebSearchGlyph";
import { DesktopApp } from "./DesktopGrid";
import type { HostStateView } from "./useHostState";

// Every window loads its code and styles when it is first opened; the desktop
// needs only the apps' icons and shows a spinner in the window until then.
const AppCenterPanel = lazyCode(() => import("../appcenter/AppCenterPanel").then((module) => module.AppCenterPanel));
const AssistantPanel = lazyCode(() => import("../assistant/AssistantPanel").then((module) => module.AssistantPanel));
const BackupCenter = lazyCode(() => import("../backup/BackupCenter").then((module) => module.BackupCenter));
const FamilyPanel = lazyCode(() => import("../family/FamilyPanel").then((module) => module.FamilyPanel));
const DockerPanel = lazyCode(() => import("../docker/DockerPanel").then((module) => module.DockerPanel));
const FilePanel = lazyCode(() => import("../files/FilePanel").then((module) => module.FilePanel));
const MediaCenter = lazyCode(() => import("../media/MediaCenter").then((module) => module.MediaCenter));
const MusicPanel = lazyCode(() => import("../music/MusicPanel").then((module) => module.MusicPanel));
const PhotosPanel = lazyCode(() => import("../photos/PhotosPanel").then((module) => module.PhotosPanel));
const SettingsPanel = lazyCode(() => import("../settings/SettingsPanel").then((module) => module.SettingsPanel));
const SharesPanel = lazyCode(() => import("../shares/SharesPanel").then((module) => module.SharesPanel));
const TransferCenter = lazyCode(() => import("../transfers/TransferCenter").then((module) => module.TransferCenter));
const TrashPanel = lazyCode(() => import("../trash/TrashPanel").then((module) => module.TrashPanel));
const WebSearchPanel = lazyCode(() => import("../websearch/WebSearchPanel").then((module) => module.WebSearchPanel));

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
// window sees a request for the place it already shows. The window hands back
// the intent it handled, and the desktop forgets it only if no newer request
// has taken its place.
export type WindowIntents = { [ID in keyof WindowIntentPayloads]?: WindowIntentPayloads[ID] };
// OpenWindow opens or raises an app's window, with what to show for apps that take an intent.
export type OpenWindow = <ID extends WindowID>(
  id: ID,
  ...intent: ID extends keyof WindowIntentPayloads ? [intent?: WindowIntentPayloads[ID]] : []
) => void;

// WindowContext is what the desktop gives the apps' windows and icons. A new
// one re-renders every open window, so it holds only what changes rarely.
export interface WindowContext {
  session: Session;
  isAdmin: boolean;
  // The assistant opens only when this device runs one for this account (ADR 0024).
  assistantReady: boolean;
  // Web search shows only while an administrator leaves it on (ADR 0030).
  webSearchEnabled: boolean;
  open: OpenWindow;
  intents: WindowIntents;
  intentHandled: <ID extends keyof WindowIntentPayloads>(id: ID, handled: WindowIntentPayloads[ID]) => void;
  changeUser: (user: User) => void;
  logout: () => void;
}

// SettingsState is what the desktop keeps for the settings window alone: the
// host's state, new with every poll, and the section and any pending storage
// plan, which outlive the window so reopening it returns to where the user
// left off. A context of its own re-renders only that window.
export interface SettingsState {
  host: HostStateView;
  section: SettingsSection;
  setSection: (section: SettingsSection) => void;
  storagePlan: StoragePlanFlow;
}
export const SettingsStateContext = createContext<SettingsState | undefined>(undefined);

function SettingsWindow({ session, changeUser, logout }: Pick<WindowContext, "session" | "changeUser" | "logout">) {
  const state = use(SettingsStateContext);
  if (!state) throw new Error("SettingsWindow outside the desktop");
  return (
    <SettingsPanel
      session={session}
      host={state.host}
      section={state.section}
      onSectionChange={state.setSection}
      storagePlan={state.storagePlan}
      onUserChange={changeUser}
      onLogout={logout}
    />
  );
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
    // An app an administrator turned off has no icon at all.
    visible?: (context: WindowContext) => boolean;
  };
  // The window's content.
  render: (context: WindowContext) => ReactNode;
}

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
        onIntentHandled={(handled) => intentHandled("files", handled)}
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
        `账号 密码 smb 用户 成员 权限 存储 磁盘 数据卷 系统 日志 审计 登录记录 活动 log${isAdmin ? " 终端 shell 命令行 bash 网页搜索开关" : ""}`,
    },
    render: ({ session, changeUser, logout }) => (
      <SettingsWindow session={session} changeUser={changeUser} logout={logout} />
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
      <TransferCenter onOpenFiles={() => open("files")} onOpenLocation={(location) => open("files", location)} />
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
        onIntentHandled={(handled) => intentHandled("docker", handled)}
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
        onIntentHandled={(handled) => intentHandled("photos", handled)}
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
  websearch: {
    title: "网页搜索",
    icon: WebSearchGlyph,
    tone: "websearch",
    bounds: { x: 240, y: 50, width: 1040, height: 700 },
    launcher: {
      id: "websearch",
      keywords: "搜索 搜索引擎 网页 百度 必应 搜狗 图片 视频 资讯 新闻 种子 search web",
      visible: ({ webSearchEnabled }) => webSearchEnabled,
    },
    render: () => <WebSearchPanel />,
  },
  family: {
    title: "家庭中心",
    icon: FamilyGlyph,
    tone: "family",
    bounds: { x: 200, y: 44, width: 1120, height: 730 },
    launcher: { id: "family", keywords: "家庭 宝宝 成长 疫苗 身高 体重 大事记 亲友 family baby" },
    render: ({ session, open }) => (
      <FamilyPanel userId={session.user.id} onShowPhoto={(assetId) => open("photos", { assetId })} />
    ),
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
      <AssistantPanel
        username={session.user.username}
        onShowPhoto={(assetId) => open("photos", { assetId })}
        intent={intents.assistant}
        onIntentHandled={(handled) => intentHandled("assistant", handled)}
      />
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
    if (!launcher || (launcher.adminOnly && !context.isAdmin) || (launcher.visible && !launcher.visible(context)))
      return [];
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
