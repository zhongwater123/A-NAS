import { QueryCache, QueryClient, QueryClientProvider, useQuery, useQueryClient } from "@tanstack/react-query";
import { FileCode2, PlugZap, Upload, Users } from "lucide-react";
import { type DragEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { APIError } from "../api/client";
import { errorText } from "../api/errorMessages";
import { carriesFiles } from "../lib/pickedFiles";
import { relativeTime } from "../lib/time";
import { useDialogs, useToasts } from "../ui/feedback";
import { useWindowShown } from "../ui/useWindowShown";
import { createPage, listPages, type WorkspacePageSummary } from "./api";
import { ConnectAgent } from "./ConnectAgent";
import type { Kit } from "./kit";
import { Members } from "./Members";
import { PageView, pagesKey } from "./PageView";
import { acceptHTML, Failed, HTMLPicker } from "./parts";
import { savedText } from "./words";
import styles from "./workspace.module.css";

// WorkspacePanel is the 共享工作区 window (docs/specs/shared-workspace.md):
// the shared pages on the left, the open one on the right in its sandboxed
// frame. A member uploads a page by choosing or dropping an HTML file and
// connects their own agent here; administrators choose who may use it.
export function WorkspacePanel({ userId, admin }: { userId: string; admin: boolean }) {
  // An account an administrator takes out keeps nothing it had read: the
  // first answer that says so empties the window.
  const [forbidden, setForbidden] = useState<APIError>();
  const [client] = useState(() => {
    const onError = (error: unknown) => {
      if (error instanceof APIError && error.code === "workspace_forbidden") setForbidden(error);
    };
    return new QueryClient({
      queryCache: new QueryCache({ onError }),
      // A request the service refused (4xx) is not tried again; one it could
      // not answer is, once.
      defaultOptions: {
        queries: {
          retry: (failures, error) => failures < 1 && !(error instanceof APIError && error.status < 500),
          staleTime: 2_000,
          refetchOnWindowFocus: false,
        },
      },
    });
  });
  useEffect(() => {
    if (forbidden) client.clear();
  }, [forbidden, client]);
  return (
    <QueryClientProvider client={client}>
      {forbidden ? (
        <div className={`${styles.wsApp} ${styles.closed}`}>
          <Failed error={forbidden} onRetry={() => setForbidden(undefined)} />
        </div>
      ) : (
        <Workspace userId={userId} admin={admin} />
      )}
    </QueryClientProvider>
  );
}

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
  // The page open on the right: the newest until one is chosen.
  const [chosen, setChosen] = useState<string>();
  const pages = list.data ?? [];
  const open = pages.find((page) => page.id === chosen)?.id ?? (chosen === undefined ? pages[0]?.id : undefined);
  const [dialogOpen, setDialogOpen] = useState<"connect" | "members">();
  const [uploading, setUploading] = useState(false);
  const [dropping, setDropping] = useState(false);
  const picker = useRef<HTMLInputElement>(null);

  const upload = async (file: File) => {
    setUploading(true);
    try {
      const page = await createPage(file);
      await queries.invalidateQueries({ queryKey: pagesKey });
      setChosen(page.id);
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
  // A page deleted here or by someone else gives way to the newest.
  const gone = useCallback(() => {
    setChosen(undefined);
    void queries.invalidateQueries({ queryKey: pagesKey });
  }, [queries]);

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
      <aside className={styles.wsSide}>
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
        <nav className={styles.wsList} aria-label="页面">
          {pages.map((page) => (
            <PageRow key={page.id} page={page} open={page.id === open} onOpen={() => setChosen(page.id)} />
          ))}
        </nav>
        <footer className={styles.wsSideFoot}>
          <button type="button" className={styles.wsSideAction} onClick={() => setDialogOpen("connect")}>
            <PlugZap size={16} aria-hidden="true" />
            连接 agent
          </button>
          {admin && (
            <button type="button" className={styles.wsSideAction} onClick={() => setDialogOpen("members")}>
              <Users size={16} aria-hidden="true" />
              成员
            </button>
          )}
        </footer>
      </aside>
      <main className={styles.wsMain}>
        {list.isError && !list.data ? (
          <Failed error={list.error} onRetry={() => void list.refetch()} />
        ) : !list.data ? (
          <div className={styles.wsCentered}>
            <span className="loader" role="status" aria-label="正在读取页面" />
          </div>
        ) : open ? (
          <PageView key={open} pageId={open} userId={userId} admin={admin} shown={shown} kit={kit} onGone={gone} />
        ) : (
          <div className={styles.wsCentered}>
            <FileCode2 className={styles.wsCenteredIcon} aria-hidden="true" />
            <p>{pages.length ? "选择左边的页面" : "还没有页面"}</p>
            {!pages.length && (
              <button
                type="button"
                className={`${styles.wsButton} ${styles.primary}`}
                onClick={() => picker.current?.click()}
              >
                上传 HTML 文件
              </button>
            )}
          </div>
        )}
      </main>
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

function PageRow({ page, open, onOpen }: { page: WorkspacePageSummary; open: boolean; onOpen: () => void }) {
  return (
    <button
      type="button"
      className={styles.wsRow}
      aria-current={open ? "page" : undefined}
      title={page.title}
      onClick={onOpen}
    >
      <span className={styles.wsRowTitle}>{page.title}</span>
      <span className={styles.wsRowMeta}>
        {savedText(page.latest)} · v{page.latest.version} · {relativeTime(page.latest.savedAt)}
      </span>
    </button>
  );
}
