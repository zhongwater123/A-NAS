import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { RefObject } from "react";

import { errorText } from "../api/errorMessages";
import { relativeTime } from "../lib/time";
import { expiryOf } from "../trash/model";
import { Modal } from "../ui/modal";
import { useWindowShown } from "../ui/useWindowShown";
import { listTrash, purgePage, restorePage, type WorkspaceTrashedPage } from "./api";
import type { Kit } from "./kit";
import styles from "./workspace.module.css";

const trashKey = ["workspace", "trash"] as const;

// Trash lists the deleted pages, which everyone who uses the workspace sees
// as they saw the pages. Whoever uploaded a page, and administrators, bring
// it back with all its versions or remove it for good; the service removes
// the rest 30 days after they were deleted (docs/specs/shared-workspace.md).
// The days left read as in the desktop's 回收站, which lists them too.
export function Trash({
  container,
  userId,
  admin,
  kit,
  onClose,
}: {
  container: RefObject<HTMLElement | null>;
  userId: string;
  admin: boolean;
  kit: Kit;
  onClose: () => void;
}) {
  const queries = useQueryClient();
  // Others delete, restore and purge pages, and upkeep removes expired ones,
  // while the trash is open: it reads again as often as the list, and stops
  // with it while the window is out of sight.
  const shown = useWindowShown(container);
  const trash = useQuery({
    queryKey: trashKey,
    queryFn: ({ signal }) => listTrash(signal),
    refetchInterval: shown ? 10_000 : false,
    refetchIntervalInBackground: false,
  });
  const change = useMutation({
    mutationFn: ({ page, purge }: { page: WorkspaceTrashedPage; purge: boolean }) =>
      purge ? purgePage(page.id) : restorePage(page.id),
    onSuccess: (_, { page, purge }) =>
      kit.toast({ text: purge ? `已彻底删除“${page.title}”` : `已恢复“${page.title}”` }),
    onError: (error) => kit.toast({ text: errorText(error, "workspace"), tone: "error" }),
    // The trash and the page list read again together.
    onSettled: () => queries.invalidateQueries({ queryKey: ["workspace"] }),
  });
  const purge = async (page: WorkspaceTrashedPage) => {
    const ok = await kit.confirm({
      title: `彻底删除“${page.title}”？`,
      message: `全部 ${page.versions} 个版本一起删除，不能恢复。`,
      confirm: "彻底删除",
      danger: true,
    });
    if (ok) change.mutate({ page, purge: true });
  };
  return (
    <Modal title="回收站" container={container} wide onClose={onClose}>
      {trash.isError ? (
        <p className={styles.wsError} role="alert">
          {errorText(trash.error, "workspace")}
        </p>
      ) : !trash.data ? (
        <span className="loader" role="status" aria-label="正在读取回收站" />
      ) : trash.data.length ? (
        <ul className={styles.wsTrash} aria-label="删除的页面">
          {trash.data.map((page) => (
            <li key={page.id} className={styles.wsTrashRow}>
              <span className={styles.wsRowText}>
                <span className={styles.wsRowTitle}>{page.title}</span>
                <span className={styles.wsRowMeta}>
                  {page.deletedBy.name} 删除 · {relativeTime(page.deletedAt)} · {page.versions} 个版本 ·{" "}
                  {expiryOf(page).label}
                </span>
              </span>
              {(admin || page.createdBy.id === userId) && (
                <>
                  <button
                    type="button"
                    className={styles.wsButton}
                    disabled={change.isPending}
                    aria-label={`恢复“${page.title}”`}
                    onClick={() => change.mutate({ page, purge: false })}
                  >
                    恢复
                  </button>
                  <button
                    type="button"
                    className={`${styles.wsButton} ${styles.danger}`}
                    disabled={change.isPending}
                    aria-label={`彻底删除“${page.title}”`}
                    onClick={() => void purge(page)}
                  >
                    彻底删除
                  </button>
                </>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p className={styles.wsTrashEmpty}>回收站是空的</p>
      )}
    </Modal>
  );
}
