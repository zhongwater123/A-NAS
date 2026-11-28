// ConfirmDialog asks before something that cannot be taken back (PRD A.1):
// centred, over a 60% black backdrop, 440 wide, the dangerous button in
// magenta. Radix's AlertDialog keeps focus in it and takes Escape.
import { AlertDialog } from "radix-ui";
import type { ReactNode } from "react";

import { messages } from "./messages";
import styles from "./venus.module.css";
import { useWindowRoot } from "./windowRoot";

export function ConfirmDialog({
  title,
  message,
  confirm,
  onConfirm,
  onCancel,
}: {
  title: string;
  message: ReactNode;
  confirm: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  // Inside the window, never over the whole desktop (WindowRoot).
  const container = useWindowRoot();
  if (!container) throw new Error("ConfirmDialog opens inside VideoCutPanel");
  return (
    <AlertDialog.Root
      open
      onOpenChange={(open) => {
        if (!open) onCancel();
      }}
    >
      <AlertDialog.Portal container={container.current}>
        <AlertDialog.Overlay className={styles.dialogBackdrop} />
        <AlertDialog.Content className={styles.dialog}>
          <AlertDialog.Title>{title}</AlertDialog.Title>
          <AlertDialog.Description>{message}</AlertDialog.Description>
          <div className={styles.dialogActions}>
            <AlertDialog.Cancel className={styles.action}>{messages.cancel}</AlertDialog.Cancel>
            <AlertDialog.Action className={`${styles.action} ${styles.danger}`} onClick={onConfirm}>
              {confirm}
            </AlertDialog.Action>
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
}
