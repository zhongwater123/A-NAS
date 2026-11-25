// Modal is a dialog inside a window. It stays in the window, as the desktop's
// other in-window dialogs do, so the rest of the desktop keeps working;
// Escape and the close button end it unless it is busy. It follows the
// window's palette through the --ph-* variables its root sets.
import { X } from "lucide-react";
import { Dialog } from "radix-ui";
import type { ReactNode, RefObject } from "react";

import styles from "./modal.module.css";

export function Modal({
  title,
  container,
  busy,
  wide,
  large,
  onClose,
  children,
  footer,
}: {
  title: string;
  // container is the window, where the dialog opens.
  container: RefObject<HTMLElement | null>;
  busy?: boolean;
  wide?: boolean;
  // large holds a grid of photos, which scrolls on its own.
  large?: boolean;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <Dialog.Root
      open
      modal={false}
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <Dialog.Portal container={container.current}>
        <div
          className={styles.fcBackdrop}
          aria-hidden="true"
          onPointerDown={() => {
            if (!busy) onClose();
          }}
        />
        <Dialog.Content
          className={`${styles.fcModal} ${wide ? styles.wide : ""} ${large ? styles.large : ""}`}
          aria-describedby={undefined}
          onInteractOutside={(event) => event.preventDefault()}
        >
          <header className={styles.fcModalHead}>
            <Dialog.Title>{title}</Dialog.Title>
            <Dialog.Close className={styles.fcModalClose} aria-label="关闭" disabled={busy}>
              <X size={17} />
            </Dialog.Close>
          </header>
          <div className={styles.fcModalBody}>{children}</div>
          {footer && <footer className={styles.fcModalFoot}>{footer}</footer>}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
