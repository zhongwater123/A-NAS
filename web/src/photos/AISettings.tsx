import { X } from "lucide-react";
import { Dialog } from "radix-ui";
import { useRef } from "react";

import feedbackStyles from "../ui/feedback.module.css";
import type { PhotoLibrary } from "./api";
import { type Caller, libraryName } from "./model";
import styles from "./photos.module.css";

// What local AI does with a library's photos, switched per library: people
// and pets (docs/architecture/photo-people.md) and the text in photos
// (docs/architecture/photo-ocr.md). The owner of a private library decides,
// an administrator for the shared one.
export type AISwitch = "people" | "texts";

const switches: Array<{ name: AISwitch; label: string; text: string }> = [
  { name: "people", label: "人物与宠物", text: "认出反复出现的人、猫和狗" },
  { name: "texts", label: "照片中的文字", text: "读出证件、票据和截图上的文字，可以按文字搜索" },
];

export function AISettings({
  caller,
  onSet,
  onClose,
}: {
  caller: Caller;
  onSet: (library: PhotoLibrary, name: AISwitch, on: boolean) => void;
  onClose: () => void;
}) {
  const libraries = caller.libraries.filter((library) => !library.viewing);
  const content = useRef<HTMLDivElement>(null);
  return (
    <Dialog.Root open modal={false} onOpenChange={(open) => !open && onClose()}>
      <div
        className={feedbackStyles.phDialogBackdrop}
        onPointerDown={(event) => {
          if (event.target === event.currentTarget) onClose();
        }}
      >
        <Dialog.Content
          className={`${feedbackStyles.phDialog} ${styles.phAiSettings}`}
          ref={content}
          aria-describedby={undefined}
          onInteractOutside={(event) => event.preventDefault()}
          // Escape in a dialog opened over this one, such as the question
          // before switching off, leaves this one open.
          onEscapeKeyDown={(event) => {
            const target = event.target as Node;
            if (target !== document.body && !content.current?.contains(target)) event.preventDefault();
          }}
        >
          <header>
            <Dialog.Title asChild>
              <h3>本地 AI 设置</h3>
            </Dialog.Title>
            <small>识别都在这台 NAS 上进行，每个图库分别设置</small>
            <Dialog.Close className={feedbackStyles.phDialogClose} aria-label="关闭">
              <X />
            </Dialog.Close>
          </header>
          {libraries.map((library) => {
            const name = libraryName(caller, library);
            return (
              <section className={styles.phSwitchList} key={library.id} aria-label={name}>
                <h4 className={styles.phSwitchHead}>
                  {name}
                  {!library.canSetPeople && <small>由管理员设置</small>}
                </h4>
                {switches.map((item) => {
                  const on = library[item.name] !== false;
                  return (
                    <div className={styles.phSwitchRow} key={item.name}>
                      <span>
                        <strong>{item.label}</strong>
                        <small>{item.text}</small>
                      </span>
                      <button
                        type="button"
                        role="switch"
                        aria-checked={on}
                        aria-label={`${name}的${item.label}`}
                        disabled={!library.canSetPeople}
                        className={`${styles.phSwitch}${on ? ` ${styles.on}` : ""}`}
                        onClick={() => onSet(library, item.name, !on)}
                      >
                        <span />
                      </button>
                    </div>
                  );
                })}
              </section>
            );
          })}
          <p className={styles.phAiSettingsNote}>
            关闭人物与宠物会删除识别出的人脸与宠物，起的名字和做过的更正保留；关闭照片中的文字会删除读出的文字，更正和修改记录保留。重新开启后随识别结果回来。
          </p>
        </Dialog.Content>
      </div>
    </Dialog.Root>
  );
}
