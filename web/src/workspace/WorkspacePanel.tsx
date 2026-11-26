import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, FileCode2, PlugZap, Upload, Users } from "lucide-react";
import { type DragEvent, useMemo, useRef, useState } from "react";

import { APIError } from "../api/client";
import { errorText } from "../api/errorMessages";
import { carriesFiles } from "../lib/pickedFiles";
import { relativeTime } from "../lib/time";
import { useDialogs, useToasts } from "../ui/feedback";
import { RowMenu } from "../ui/RowMenu";
import { useWindowShown } from "../ui/useWindowShown";
import {
  createPage,
  deletePage,
  fileURL,
  getPage,
  listPages,
  renamePage,
  saveVersion,
  tabName,
  tabURL,
  type WorkspacePageSummary,
} from "./api";
import { ConnectAgent } from "./ConnectAgent";
import type { Kit } from "./kit";
import { Members } from "./Members";
import { Presence } from "./Presence";
import { acceptHTML, Failed, HTMLPicker } from "./parts";
import { WorkspaceQueries } from "./queries";
import { savedText } from "./words";
import styles from "./workspace.module.css";

export const pagesKey = ["workspace", "pages"] as const;

// WorkspacePanel is the 共享工作区 window (docs/specs/shared-workspace.md):
// the shared pages, each opening in a browser tab of its own (PageTab). A
// member uploads a page by choosing or dropping an HTML file, manages pages
// from their rows and connects their own agent here; administrators choose
// who may use it.
export function WorkspacePanel({ userId, admin }: { userId: string; admin: boolean }) {
  return (
    <WorkspaceQueries className={styles.wsApp}>
      <Workspace userId={userId} admin={admin} />
    </WorkspaceQueries>
  );
}

// openPage opens a page's tab, or returns to it when it is open. Not
// noopener: a browser then makes a new tab for every click, whatever its
// name. The tab itself lets go of the desktop as it loads (tab.tsx).
const openPage = (pageId: string) => window.open(tabURL(pageId), tabName(pageId));

function Workspace({ userId, admin }: { userId: string; admin: boolean }) {
  const root = useRef<HTMLDivElement>(null);
  const shown = useWindowShown(root);
  const { toast, element: toasts } = useToasts();
  const { confirm, prompt, dialog } = useDialogs();
  const kit = useMemo<Kit>(() => ({ toast, confirm, prompt }), [toast, confirm, prompt]);
  const queries = useQueryClient();
  const list = useQuery({
    queryKey: pagesKey,
    queryFn: ({ signal }) => listPages(signal),
    refetchInterval: shown ? 10_000 : false,
    refetchIntervalInBackground: false,
  });
  const pages = list.data ?? [];
  const [dialogOpen, setDialogOpen] = useState<"connect" | "members">();
  const [uploading, setUploading] = useState(false);
  const [dropping, setDropping] = useState(false);
  const picker = useRef<HTMLInputElement>(null);
  const refresh = () => void queries.invalidateQueries({ queryKey: pagesKey });

  // A tab opens only from the member's click, so a new page offers one.
  const upload = async (file: File) => {
    setUploading(true);
    try {
      const page = await createPage(file);
      refresh();
      toast({ text: `已上传“${page.title}”`, action: { label: "打开", run: () => openPage(page.id) } });
    } catch (error) {
      toast({ text: errorText(error, "workspace"), tone: "error" });
    } finally {
      setUploading(false);
    }
  };
  const drop = (event: DragEvent) => {
    event.preventDefault();
    setDropping(false);
    const file = event.dataTransfer.files[0];
    if (file && acceptHTML(file, toast)) void upload(file);
  };

  return (
    <div
      className={styles.wsApp}
      ref={root}
      onDragOver={(event) => {
        if (!carriesFiles(event.dataTransfer)) return;
        event.preventDefault();
        setDropping(true);
      }}
      onDragLeave={(event) => {
        if (event.currentTarget === event.target) setDropping(false);
      }}
      onDrop={drop}
    >
      <header className={styles.wsHead}>
        <h2 className={styles.wsHeadTitle}>
          页面{list.data && <span className={styles.wsFaint}>{pages.length}</span>}
        </h2>
        <button type="button" className={styles.wsButton} onClick={() => setDialogOpen("connect")}>
          <PlugZap size={15} aria-hidden="true" />
          连接 agent
        </button>
        {admin && (
          <button type="button" className={styles.wsButton} onClick={() => setDialogOpen("members")}>
            <Users size={15} aria-hidden="true" />
            成员
          </button>
        )}
        <button
          type="button"
          className={`${styles.wsButton} ${styles.primary}`}
          disabled={uploading}
          onClick={() => picker.current?.click()}
        >
          <Upload size={15} aria-hidden="true" />
          {uploading ? "正在上传…" : "上传页面"}
        </button>
        <HTMLPicker ref={picker} toast={toast} onFile={(file) => void upload(file)} />
      </header>
      {list.isError && !list.data ? (
        <Failed error={list.error} onRetry={() => void list.refetch()} />
      ) : !list.data ? (
        <div className={styles.wsCentered}>
          <span className="loader" role="status" aria-label="正在读取页面" />
        </div>
      ) : pages.length ? (
        <ul className={styles.wsList} aria-label="页面">
          {pages.map((page) => (
            <PageRow
              key={page.id}
              page={page}
              userId={userId}
              canDelete={admin || page.createdBy.id === userId}
              kit={kit}
              onChanged={refresh}
            />
          ))}
        </ul>
      ) : (
        <div className={styles.wsCentered}>
          <FileCode2 className={styles.wsCenteredIcon} aria-hidden="true" />
          <p>还没有页面</p>
          <button
            type="button"
            className={`${styles.wsButton} ${styles.primary}`}
            onClick={() => picker.current?.click()}
          >
            上传 HTML 文件
          </button>
        </div>
      )}
      {dropping && (
        <div className={styles.wsDrop} aria-hidden="true">
          <Upload size={22} />
          松开以上传为新页面
        </div>
      )}
      {dialogOpen === "connect" && (
        <ConnectAgent account={userId} container={root} onClose={() => setDialogOpen(undefined)} />
      )}
      {dialogOpen === "members" && (
        <Members container={root} userId={userId} kit={kit} onClose={() => setDialogOpen(undefined)} />
      )}
      {dialog}
      {toasts}
    </div>
  );
}

// PageRow is one page: its title opens it in its own tab, its menu manages it.
function PageRow({
  page,
  userId,
  canDelete,
  kit,
  onChanged,
}: {
  page: WorkspacePageSummary;
  userId: string;
  canDelete: boolean;
  kit: Kit;
  onChanged: () => void;
}) {
  const upload = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const failed = (error: unknown) => kit.toast({ text: errorText(error, "workspace"), tone: "error" });

  // A new version goes on the one the list showed; one saved since is the
  // member's to keep or to save over.
  const uploadVersion = async (file: File, base: number) => {
    setBusy(true);
    try {
      const saved = await saveVersion(page.id, base, file);
      onChanged();
      kit.toast({ text: `“${page.title}”已保存为 v${saved.version}` });
    } catch (error) {
      if (error instanceof APIError && error.code === "page_changed") {
        const latest = await getPage(page.id).then(
          (current) => current.latest,
          () => undefined,
        );
        if (latest) {
          setBusy(false);
          const again = await kit.confirm({
            title: "页面已有新版本",
            message: `${latest.savedBy.name} 刚保存了 v${latest.version}。仍然上传，你的文件会成为 v${latest.version + 1}，v${latest.version} 的修改不在里面。`,
            confirm: `上传为 v${latest.version + 1}`,
          });
          if (again) await uploadVersion(file, latest.version);
          return;
        }
      }
      failed(error);
    } finally {
      setBusy(false);
    }
  };
  const rename = async () => {
    const title = await kit.prompt({
      title: "改名",
      label: "标题",
      value: page.title,
      confirm: "保存",
      maxLength: 200,
    });
    if (title === undefined || title.trim() === page.title) return;
    try {
      await renamePage(page.id, title.trim());
      onChanged();
    } catch (error) {
      failed(error);
    }
  };
  const exportLatest = () => {
    const link = document.createElement("a");
    link.href = fileURL(page.id, page.latest.version);
    link.download = "";
    link.click();
  };
  const remove = async () => {
    const ok = await kit.confirm({
      title: `删除“${page.title}”？`,
      message: `全部 ${page.latest.version} 个版本一起删除，所有人都看不到，不能恢复。`,
      confirm: "删除",
      danger: true,
    });
    if (!ok) return;
    try {
      await deletePage(page.id);
      kit.toast({ text: `已删除“${page.title}”` });
      onChanged();
    } catch (error) {
      failed(error);
    }
  };

  return (
    <li className={styles.wsRow}>
      <a
        className={styles.wsRowOpen}
        href={tabURL(page.id)}
        target={tabName(page.id)}
        title={`在新标签页打开“${page.title}”`}
      >
        <FileCode2 className={styles.wsRowIcon} aria-hidden="true" />
        <span className={styles.wsRowText}>
          <span className={styles.wsRowTitle}>{page.title}</span>
          <span className={styles.wsRowMeta}>
            {savedText(page.latest)} · v{page.latest.version} · {relativeTime(page.latest.savedAt)}
          </span>
        </span>
        <Presence presence={page.presence} userId={userId} latest={page.latest.version} compact />
        <ArrowUpRight className={styles.wsRowArrow} aria-hidden="true" />
      </a>
      <RowMenu
        label={`更多操作：${page.title}`}
        triggerClassName={styles.wsIconButton}
        items={[
          { label: "上传新版本…", run: () => upload.current?.click() },
          { label: "改名…", run: () => void rename() },
          { label: `导出 v${page.latest.version}`, run: exportLatest },
          ...(canDelete ? [{ label: "删除…", run: () => void remove() }] : []),
        ]}
      />
      <HTMLPicker
        ref={upload}
        disabled={busy}
        toast={kit.toast}
        onFile={(file) => void uploadVersion(file, page.latest.version)}
      />
    </li>
  );
}
