import { CalendarClock, Camera, FileSearch, HardDriveUpload, LayoutDashboard, PanelLeft } from "lucide-react";
import {
  createContext,
  FormEvent,
  ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import type { FileEntry, Space } from "../api.generated";
import { listSpaces } from "../files/api";
import type { TransferLocation } from "../transfers/api";
import {
  createSnapshot,
  getSchedule,
  isVolumeOffline,
  listSnapshots,
  messageOf,
  type Snapshot,
  type SnapshotSchedule,
} from "./api";
import { BackupCenterGlyph } from "./BackupCenterGlyph";
import { ExternalPage } from "./ExternalPage";
import { FindPage } from "./FindPage";
import { defaultSnapshotName, spaceAudience, spaceLabel } from "./format";
import { OverviewPage } from "./OverviewPage";
import { Dialog, Spinner, type Toast, useToasts } from "../ui/workspace";
import { SpaceIcon } from "./parts";
import { type Crumb, RestoreDialog, type RestoreRequest } from "./RestoreDialog";
import { SchedulesPage } from "./SchedulesPage";
import { SnapshotsPage } from "./SnapshotsPage";
import "./backup.css";

export type Page = "overview" | "snapshots" | "find" | "schedules" | "external";

export interface SpaceData {
  snapshots?: Snapshot[];
  schedule?: SnapshotSchedule;
  // Why the snapshots could not be listed; offline is the data volume.
  error?: string;
  offline?: boolean;
}

interface Backup {
  userId: string;
  isAdmin: boolean;
  spaces: Space[];
  data: Record<string, SpaceData>;
  spaceId: string;
  setSpaceId: (id: string) => void;
  // focus is a snapshot the snapshots page should open; it calls
  // consumeFocus once it has, so later refreshes keep the person's choice.
  focus?: string;
  consumeFocus: () => void;
  // restored counts finished restores, so views of what is in a space reload.
  restored: number;
  navigate: (page: Page, options?: { spaceId?: string; focus?: string }) => void;
  refresh: (spaceId?: string) => Promise<void>;
  setSchedule: (schedule: SnapshotSchedule) => void;
  canManage: (space: Space) => boolean;
  createSnapshot: (space: Space) => void;
  restore: (request: RestoreRequest) => void;
  toast: (text: string, tone?: Toast["tone"], action?: Toast["action"]) => void;
  // showInFiles opens the file manager at a folder of a space.
  showInFiles?: (space: Space, folders: Crumb[]) => void;
  openTrash?: () => void;
}

const BackupContext = createContext<Backup | null>(null);

export function useBackup(): Backup {
  const value = useContext(BackupContext);
  if (!value) throw new Error("useBackup outside the backup center");
  return value;
}

const refreshEvery = 60_000;
const narrowWidth = 720;

interface RestoreJob {
  key: number;
  request: RestoreRequest;
  background: boolean;
}

// Finding a lost file is why most people open the backup center, so it
// comes right after the overview.
const navigation: { page: Page; label: string; icon: ReactNode }[] = [
  { page: "overview", label: "概览", icon: <LayoutDashboard /> },
  { page: "find", label: "找回文件", icon: <FileSearch /> },
  { page: "snapshots", label: "快照", icon: <Camera /> },
  { page: "schedules", label: "定时快照", icon: <CalendarClock /> },
];

// BackupCenter is the 备份中心 window: snapshots of each space, their
// schedules, finding and restoring files, and where backups to other disks
// will live. The server decides every permission.
export function BackupCenter({
  userId,
  isAdmin,
  onShowInFiles,
  onOpenTrash,
}: {
  userId: string;
  isAdmin: boolean;
  onShowInFiles?: (location: TransferLocation) => void;
  onOpenTrash?: () => void;
}) {
  const [page, setPage] = useState<Page>("overview");
  const [spaces, setSpaces] = useState<Space[]>();
  const [failure, setFailure] = useState("");
  const [spaceId, setSpaceId] = useState("");
  const [focus, setFocus] = useState<string>();
  const [data, setData] = useState<Record<string, SpaceData>>({});
  const [creating, setCreating] = useState<Space>();
  const [restores, setRestores] = useState<RestoreJob[]>([]);
  const [restored, setRestored] = useState(0);
  const [width, setWidth] = useState(1000);
  const [drawer, setDrawer] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const restoreKey = useRef(0);
  const { toast, element: toasts } = useToasts();
  const narrow = width < narrowWidth;

  useEffect(() => {
    const element = root.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  // Viewing another member's space is for browsing files, not for managing
  // its snapshots, so the backup center shows only the user's own spaces.
  useEffect(() => {
    let live = true;
    listSpaces()
      .then((items) => {
        if (!live) return;
        const own = items
          .filter((space) => !space.viewing)
          .sort((a, b) => (a.kind === b.kind ? 0 : a.kind === "private" ? -1 : 1));
        setSpaces(own);
        setSpaceId((current) => current || own[0]?.id || "");
      })
      .catch((caught) => {
        if (live) setFailure(messageOf(caught));
      });
    return () => {
      live = false;
    };
  }, []);

  const loadSpace = useCallback(async (id: string) => {
    const [snapshots, schedule] = await Promise.allSettled([listSnapshots(id), getSchedule(id)]);
    setData((current) => ({
      ...current,
      [id]: {
        snapshots: snapshots.status === "fulfilled" ? snapshots.value : current[id]?.snapshots,
        schedule: schedule.status === "fulfilled" ? schedule.value : current[id]?.schedule,
        error: snapshots.status === "rejected" ? messageOf(snapshots.reason) : undefined,
        offline: snapshots.status === "rejected" && isVolumeOffline(snapshots.reason),
      },
    }));
  }, []);

  const refresh = useCallback(
    async (id?: string) => {
      const targets = id ? [id] : (spaces ?? []).map((space) => space.id);
      await Promise.all(targets.map(loadSpace));
    },
    [loadSpace, spaces],
  );

  // Scheduled snapshots appear without the window being reopened.
  useEffect(() => {
    if (!spaces) return;
    void refresh();
    const timer = window.setInterval(() => void refresh(), refreshEvery);
    return () => window.clearInterval(timer);
  }, [refresh, spaces]);

  const navigate = useCallback((next: Page, options?: { spaceId?: string; focus?: string }) => {
    if (options?.spaceId) setSpaceId(options.spaceId);
    setFocus(options?.focus);
    setPage(next);
    setDrawer(false);
  }, []);

  const consumeFocus = useCallback(() => setFocus(undefined), []);
  const canManage = useCallback(
    (space: Space) =>
      data[space.id]?.schedule?.canManage ?? (space.kind === "shared" ? isAdmin : space.ownerUserId === userId),
    [data, isAdmin, userId],
  );
  const setSchedule = useCallback((schedule: SnapshotSchedule) => {
    setData((current) => ({ ...current, [schedule.spaceId]: { ...current[schedule.spaceId], schedule } }));
  }, []);
  const restore = useCallback((request: RestoreRequest) => {
    setRestores((current) => [...current, { key: ++restoreKey.current, request, background: false }]);
  }, []);
  const finishRestore = (key: number) => setRestores((current) => current.filter((job) => job.key !== key));
  const showInFiles = useMemo(
    () =>
      onShowInFiles &&
      ((space: Space, folders: Crumb[]) => onShowInFiles({ spaceId: space.id, spaceKind: space.kind, folders })),
    [onShowInFiles],
  );

  const context = useMemo<Backup>(
    () => ({
      userId,
      isAdmin,
      spaces: spaces ?? [],
      data,
      spaceId,
      setSpaceId,
      focus,
      consumeFocus,
      restored,
      navigate,
      refresh,
      setSchedule,
      canManage,
      createSnapshot: setCreating,
      restore,
      toast,
      showInFiles,
      openTrash: onOpenTrash,
    }),
    [
      userId,
      isAdmin,
      spaces,
      data,
      spaceId,
      focus,
      consumeFocus,
      restored,
      navigate,
      refresh,
      setSchedule,
      canManage,
      restore,
      toast,
      showInFiles,
      onOpenTrash,
    ],
  );

  const background = restores.filter((job) => job.background);
  const sidebarOpen = !narrow || drawer;

  return (
    <BackupContext.Provider value={context}>
      <div ref={root} className={`bk-app${narrow ? " narrow" : ""}`}>
        {narrow && drawer && <div className="bk-scrim" onClick={() => setDrawer(false)} />}
        {sidebarOpen && (
          <nav className="bk-sidebar" aria-label="备份中心导航">
            <div className="bk-brand">
              <span className="bk-brand-mark">
                <BackupCenterGlyph aria-hidden="true" />
              </span>
              <div>
                <strong>备份中心</strong>
                <small>快照 · 恢复 · 备份</small>
              </div>
            </div>
            <div className="bk-nav">
              {navigation.map((item) => (
                <button
                  key={item.page}
                  type="button"
                  className={page === item.page ? "active" : ""}
                  aria-current={page === item.page ? "page" : undefined}
                  onClick={() => navigate(item.page)}
                >
                  {item.icon}
                  <span>{item.label}</span>
                </button>
              ))}
            </div>
            <p className="bk-nav-heading">备份到其他位置</p>
            <div className="bk-nav">
              <button
                type="button"
                className={page === "external" ? "active" : ""}
                aria-current={page === "external" ? "page" : undefined}
                onClick={() => navigate("external")}
              >
                <HardDriveUpload />
                <span>外部备份</span>
                <small className="bk-chip">规划中</small>
              </button>
            </div>
            <div className="bk-sidebar-fill" />
            {background.length > 0 && (
              <div className="bk-activity" role="status">
                <Spinner small />
                <div>
                  <strong>正在恢复 {background.length} 项</strong>
                  <small>{background.map((job) => job.request.entry.name).join("、")}</small>
                </div>
              </div>
            )}
            <SidebarStatus spaces={spaces ?? []} data={data} />
          </nav>
        )}
        <main className="bk-main">
          {narrow && (
            <div className="bk-topbar">
              <button type="button" className="bk-icon-button" aria-label="显示导航" onClick={() => setDrawer(true)}>
                <PanelLeft />
              </button>
              <span className="bk-brand-mark small">
                <BackupCenterGlyph aria-hidden="true" />
              </span>
              <strong>备份中心</strong>
            </div>
          )}
          {!spaces ? (
            <div className="bk-loading">
              {failure ? (
                <p>{failure}</p>
              ) : (
                <>
                  <Spinner />
                  正在读取空间…
                </>
              )}
            </div>
          ) : (
            <div className="bk-page-scroll">
              {page === "overview" && <OverviewPage />}
              {page === "snapshots" && <SnapshotsPage />}
              {page === "find" && <FindPage />}
              {page === "schedules" && <SchedulesPage />}
              {page === "external" && <ExternalPage />}
            </div>
          )}
        </main>
        {creating && (
          <CreateSnapshotDialog
            space={creating}
            onClose={() => setCreating(undefined)}
            onCreated={(snapshot) => {
              setCreating(undefined);
              void loadSpace(snapshot.spaceId);
              toast(`已创建快照“${snapshot.name}”`, "success", {
                label: "查看",
                run: () => navigate("snapshots", { spaceId: snapshot.spaceId, focus: snapshot.id }),
              });
            }}
          />
        )}
        {restores.map((job) => (
          <RestoreDialog
            key={job.key}
            request={job.request}
            onClose={() => finishRestore(job.key)}
            onBackground={() =>
              setRestores((current) =>
                current.map((item) => (item.key === job.key ? { ...item, background: true } : item)),
              )
            }
            onRestored={(entry: FileEntry, location: string, folders: Crumb[]) => {
              finishRestore(job.key);
              setRestored((count) => count + 1);
              toast(
                `已恢复“${entry.name}”到 ${location}`,
                "success",
                showInFiles
                  ? { label: "在文件管理中显示", run: () => showInFiles(job.request.space, folders) }
                  : undefined,
              );
            }}
            onFailed={(message) => {
              finishRestore(job.key);
              toast(message, "error");
            }}
            onOpenTrash={onOpenTrash}
          />
        ))}
        {toasts}
      </div>
    </BackupContext.Provider>
  );
}

// SidebarStatus sums up protection at the bottom of the navigation.
function SidebarStatus({ spaces, data }: { spaces: Space[]; data: Record<string, SpaceData> }) {
  if (!spaces.length) return null;
  const offline = spaces.some((space) => data[space.id]?.offline);
  const failed = spaces.some((space) => data[space.id]?.schedule?.lastRun?.status === "failed");
  const scheduled = spaces.filter((space) => data[space.id]?.schedule?.enabled).length;
  const tone = offline || failed ? "warning" : scheduled === spaces.length ? "ok" : "idle";
  const text = offline
    ? "数据卷离线"
    : failed
      ? "最近一次定时快照失败"
      : scheduled
        ? `${scheduled}/${spaces.length} 个空间已开启定时快照`
        : "尚未开启定时快照";
  return (
    <div className={`bk-sidebar-status ${tone}`}>
      <i />
      {text}
    </div>
  );
}

function CreateSnapshotDialog({
  space,
  onClose,
  onCreated,
}: {
  space: Space;
  onClose: () => void;
  onCreated: (snapshot: Snapshot) => void;
}) {
  const [name, setName] = useState(() => defaultSnapshotName());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setError("");
    try {
      onCreated(await createSnapshot(space.id, name.trim()));
    } catch (caught) {
      setError(messageOf(caught).replace("目标位置已有同名项目", "这个空间已有同名快照"));
      setBusy(false);
    }
  };
  return (
    <Dialog
      title="创建快照"
      icon={<Camera />}
      busy={busy}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="bk-secondary" disabled={busy} onClick={onClose}>
            取消
          </button>
          <button type="submit" form="bk-create-form" className="bk-primary" disabled={busy || !name.trim()}>
            {busy ? (
              <>
                <Spinner small />
                正在创建…
              </>
            ) : (
              "创建快照"
            )}
          </button>
        </>
      }
    >
      <form id="bk-create-form" className="bk-create" onSubmit={(event) => void submit(event)}>
        <div className="bk-create-space">
          <SpaceIcon space={space} />
          <div>
            <strong>{spaceLabel(space)}</strong>
            <small>{spaceAudience(space)}</small>
          </div>
        </div>
        <label className="bk-field">
          <span className="bk-field-label">快照名称</span>
          <input
            data-autofocus
            value={name}
            disabled={busy}
            maxLength={80}
            onChange={(event) => setName(event.target.value)}
            aria-label="快照名称"
          />
        </label>
        {error && (
          <div className="bk-inline-error" role="alert">
            <span>{error}</span>
          </div>
        )}
        <ul className="bk-facts">
          <li>冻结这个空间此刻的全部文件，之后只读，可随时浏览和恢复。</li>
          <li>适合在整理、批量改名或删除文件之前拍一份，出了差错可以整份找回。</li>
          <li>创建时几乎不占空间；之后修改或删除的文件，旧内容会保留到快照被删除。</li>
          <li>手动快照不会被定时快照的保留规则删除。</li>
        </ul>
      </form>
    </Dialog>
  );
}
