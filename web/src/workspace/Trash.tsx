import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { RefObject } from "react";
import { useState } from "react";

import { errorText } from "../api/errorMessages";
import { calendarDays, relativeTime } from "../lib/time";
import { Modal } from "../ui/modal";
import { listTrash, purgePage, restorePage, type WorkspaceTrashedPage } from "./api";
import type { Kit } from "./kit";
import styles from "./workspace.module.css";

const trashKey = ["workspace", "trash"] as const;

// purgeText says when a page in the trash goes for good.
const purgeText = (purgeAt: string) => {
  const days = calendarDays(purgeAt);
  return days > 0 ? `${days} 天后彻底删除` : "今天彻底删除";
};

// Trash lists the deleted pages, which everyone who uses the workspace sees
// as they saw the pages. Whoever uploaded a page, and administrators, bring
// it back with all its versions or remove it for good; the service removes
// the rest 30 days after they were deleted (docs/specs/shared-workspace.md).
export function Trash({
  container,
  userId,
  admin,
  kit,
  onRestored,
  onClose,
}: {
  container: RefObject<HTMLElement | null>;
  userId: string;
  admin: boolean;
  kit: Kit;
  onRestored: () => void;
  onClose: () => void;
}) {
  const queries = useQueryClient();
  // Others delete, restore and purge pages, and upkeep removes expired
  // ones, while the trash is open: it reads again as often as the list.
  const trash = useQuery({
    queryKey: trashKey,
    queryFn: ({ signal }) => listTrash(signal),
    refetchInterval: 10_000,
    refetchIntervalInBackground: false,
  });
  const [busy, setBusy] = useState<string>();
  const act = async (page: WorkspaceTrashedPage, run: () => Promise<unknown>, done: string) => {
    setBusy(page.id);
    try {
      await run();
      kit.toast({ text: done });
    } catch (error) {
      kit.toast({ text: errorText(error, "workspace"), tone: "error" });
    } finally {
      setBusy(undefined);
      void queries.invalidateQueries({ queryKey: trashKey });
    }
  };
  const restore = (page: WorkspaceTrashedPage) =>
    act(
      page,
      async () => {
        await restorePage(page.id);
        onRestored();
      },
      `已恢复“${page.title}”`,
    );
  const purge = async (page: WorkspaceTrashedPage) => {
    const ok = await kit.confirm({
      title: `彻底删除“${page.title}”？`,
      message: `全部 ${page.versions} 个版本一起删除，不能恢复。`,
      confirm: "彻底删除",
      danger: true,
    });
    if (ok) await act(page, () => purgePage(page.id), `已彻底删除“${page.title}”`);
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
                  {purgeText(page.purgeAt)}
                </span>
              </span>
              {(admin || page.createdBy.id === userId) && (
                <>
                  <button
                    type="button"
                    className={styles.wsButton}
                    disabled={busy !== undefined}
                    aria-label={`恢复“${page.title}”`}
                    onClick={() => void restore(page)}
                  >
                    恢复
                  </button>
                  <button
                    type="button"
                    className={`${styles.wsButton} ${styles.danger}`}
                    disabled={busy !== undefined}
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
