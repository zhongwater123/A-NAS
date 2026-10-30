import { ImagePlus } from "lucide-react";
import { AnimatePresence, motion } from "motion/react";
import { useRef, useState } from "react";

import { addRecord, type FamilyBaby, type FamilyRecord, familyError, requestKey } from "./api";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { today } from "./format";
import { MemberFace } from "./parts";

// QuickJournal writes a journal entry for today at the top of the timeline
// without a dialog: one line that opens up as it is written. Ctrl+Enter
// saves; adding photos takes the text into the full composer.
export function QuickJournal({
  baby,
  onAdded,
  onMore,
}: {
  baby: FamilyBaby;
  onAdded: (record: FamilyRecord) => void;
  onMore: (text: string) => void;
}) {
  const family = useFamily();
  const me = family.overview.members.find((member) => member.accountId === family.userId);
  const [text, setText] = useState("");
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const key = useRef(requestKey());
  const field = useRef<HTMLTextAreaElement>(null);

  const close = () => {
    setOpen(false);
    field.current?.blur();
  };
  const save = async () => {
    const value = text.trim();
    if (!value || busy) return;
    setBusy(true);
    try {
      const record = await addRecord(baby.id, {
        kind: "journal",
        text: value,
        occurredOn: today(),
        requestKey: key.current,
      });
      key.current = requestKey();
      setText("");
      close();
      onAdded(record);
    } catch (caught) {
      family.toast({ text: familyError(caught), tone: "error" });
    }
    setBusy(false);
  };

  return (
    <motion.div className={`${styles.fcQuickJournal} ${open ? styles.open : ""}`} layout transition={{ duration: 0.2 }}>
      {me && <MemberFace accountId={me.accountId} username={me.username} className={styles.fcFace} />}
      <div className={styles.fcQuickJournalBody}>
        <textarea
          ref={field}
          className={styles.fcQuickJournalField}
          rows={open ? 3 : 1}
          maxLength={5000}
          placeholder={`写${baby.nickname || baby.name}的日志`}
          aria-label="写日志"
          value={text}
          disabled={busy}
          onFocus={() => setOpen(true)}
          onChange={(event) => setText(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
              event.preventDefault();
              void save();
            } else if (event.key === "Escape" && !text.trim()) {
              close();
            }
          }}
        />
        <AnimatePresence>
          {open && (
            <motion.div
              className={styles.fcQuickJournalActions}
              initial={{ opacity: 0, y: -6 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, y: -6 }}
              transition={{ duration: 0.18 }}
            >
              <button type="button" className={styles.fcLink} onClick={() => onMore(text)} disabled={busy}>
                <ImagePlus size={14} /> 加照片
              </button>
              <span className={styles.fcGrow} />
              <small>Ctrl + Enter 保存</small>
              <button
                type="button"
                className={styles.fcSecondary}
                onClick={() => {
                  setText("");
                  close();
                }}
                disabled={busy}
              >
                取消
              </button>
              <button
                type="button"
                className={styles.fcPrimary}
                onClick={() => void save()}
                disabled={busy || !text.trim()}
              >
                {busy ? "正在保存…" : "保存"}
              </button>
            </motion.div>
          )}
        </AnimatePresence>
      </div>
    </motion.div>
  );
}
