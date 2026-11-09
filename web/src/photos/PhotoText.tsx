import { Check, ChevronRight, Copy, Pencil, RotateCcw, ScanText, X } from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";

import { copyText } from "../lib/clipboard";
import { formatDate } from "../lib/time";
import {
  correctPhotoText,
  getPhotoText,
  listPhotoTextChanges,
  type PhotoAsset,
  type PhotoText,
  type PhotoTextChange,
  restorePhotoText,
} from "./api";
import { type Caller, libraryName, libraryOf, messageOf } from "./model";
import styles from "./photos.module.css";

// The text local AI read from a photo (docs/architecture/photo-ocr.md): the
// document's fields, which members check and correct for every library that
// holds the photo, the whole text, and every change made, with who made it.

type Field = PhotoText["fields"][number];
type Reason = NonNullable<Field["reasons"]>[number];

const kindNames: Record<NonNullable<PhotoText["kind"]>, string> = {
  id_card: "身份证人像面",
  id_card_back: "身份证国徽面",
  bank_card: "银行卡",
  bank_slip: "银行回单",
  invoice: "发票",
  receipt: "小票",
  plain: "文字",
};

const reasonNames: Record<Reason, string> = {
  not_in_text: "全文里找不到",
  format: "格式不对",
  mismatch: "与其他字段对不上",
  low_confidence: "识别没把握",
  unfinished: "没有读完",
};

// when is a change's time, with its year only when another year's.
const when = (at: string) =>
  formatDate(at, {
    ...(new Date(at).getFullYear() === new Date().getFullYear() ? {} : { year: "numeric" }),
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  });

// undoOf says how changes[index], of a list newest first, is taken back: a
// field that held a correction before it gets that correction again, one
// that showed what was read loses its correction. Undefined when the field
// already stands so.
function undoOf(changes: PhotoTextChange[], index: number) {
  const entry = changes[index];
  const before = changes.slice(index + 1).find((older) => older.field === entry.field);
  const restore = !before || before.restored;
  const latest = changes.find((newer) => newer.field === entry.field) ?? entry;
  const already = restore ? latest.restored : !latest.restored && latest.new === entry.old;
  return already ? undefined : { restore };
}

interface Props {
  asset: PhotoAsset;
  caller: Caller;
  // Whether the caller may correct the fields: anyone who sees the photo,
  // except through a viewing grant and in the trash.
  editable: boolean;
  onError: (message: string) => void;
}

export function PhotoTextPanel({ asset, caller, editable, onError }: Props) {
  const [text, setText] = useState<PhotoText>();
  const [editing, setEditing] = useState<string>();
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState<string>();
  // The changes, read when they are shown and again after a change.
  const [changes, setChanges] = useState<PhotoTextChange[]>();
  const [changesShown, setChangesShown] = useState(false);
  // A field's form gives the focus back to the field's pencil when it closes,
  // so that the viewer's keys keep working.
  const pencils = useRef(new Map<string, HTMLButtonElement>());
  const edited = useRef<string | undefined>(undefined);

  // The photo's details say whether it has text to show. The viewer keys this
  // panel by the photo, so another photo starts afresh.
  const read = asset.text === "read";
  useEffect(() => {
    if (!read) return;
    let live = true;
    getPhotoText(asset.id).then(
      (value) => live && setText(value),
      // A photo whose text cannot be read now shows none.
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [asset.id, read]);

  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(undefined), 1500);
    return () => window.clearTimeout(timer);
  }, [copied]);

  // A pencil takes no focus while a change is being saved.
  useEffect(() => {
    if (editing !== undefined || busy || edited.current === undefined) return;
    pencils.current.get(edited.current)?.focus();
    edited.current = undefined;
  }, [editing, busy]);

  const status = text?.status ?? asset.text;
  if (!status || status === "none" || status === "off") return null;

  const stopEditing = () => {
    edited.current = editing;
    setEditing(undefined);
  };
  const loadChanges = () => listPhotoTextChanges(asset.id).then(setChanges, (caught) => onError(messageOf(caught)));
  const change = async (action: () => Promise<PhotoText>) => {
    setBusy(true);
    try {
      setText(await action());
      stopEditing();
      // A closed list is read again when it opens.
      if (changesShown) await loadChanges();
      else setChanges(undefined);
    } catch (caught) {
      onError(messageOf(caught));
    } finally {
      setBusy(false);
    }
  };
  const save = (event: FormEvent, name: string) => {
    event.preventDefault();
    void change(() => correctPhotoText(asset.id, name, draft));
  };
  const libraryOfChange = (library: PhotoTextChange["library"]) => {
    const known = libraryOf(caller, library.id);
    if (known) return libraryName(caller, known);
    if (library.kind === "private" && library.ownerName) return `${library.ownerName}的图库`;
    return library.kind ? "图库" : "已删除的图库";
  };

  if (status !== "read") {
    return (
      <section className={styles.phText} aria-label="照片中的文字">
        <h4>
          <ScanText />
          照片中的文字
        </h4>
        <p className={styles.phTextNote}>
          {status === "queued" ? "等待本地 AI 读取" : "本地 AI 没能读出这张照片的文字"}
        </p>
      </section>
    );
  }
  // Shown once read.
  if (!text) return null;

  const plain = !text.fields.length;
  return (
    <section className={styles.phText} aria-label="照片中的文字">
      <h4>
        <ScanText />
        照片中的文字
        {text.kind && !plain && <span className={styles.phTextKind}>{kindNames[text.kind]}</span>}
        <span className={styles.phTextMachine}>机器识别</span>
      </h4>

      {!plain && (
        <ul className={styles.phTextFields}>
          {text.fields.map((field) => (
            <li key={field.name} className={field.passed ? undefined : styles.unchecked}>
              <span className={styles.phTextName}>{field.name}</span>
              {editing === field.name ? (
                <form className={styles.phTextEdit} onSubmit={(event) => save(event, field.name)}>
                  <input
                    aria-label={`更正${field.name}`}
                    value={draft}
                    maxLength={200}
                    autoFocus
                    disabled={busy}
                    onChange={(event) => setDraft(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === "Escape") {
                        event.stopPropagation();
                        stopEditing();
                      }
                    }}
                  />
                  <button type="submit" aria-label="保存" title="保存" disabled={busy}>
                    {busy ? <span className={styles.phSpinner} /> : <Check />}
                  </button>
                  <button type="button" aria-label="取消" title="取消" onClick={stopEditing}>
                    <X />
                  </button>
                </form>
              ) : (
                <>
                  {field.value ? (
                    <span className={styles.phTextValue}>{field.value}</span>
                  ) : (
                    <span className={`${styles.phTextValue} ${styles.phTextEmpty}`}>没有读出</span>
                  )}
                  <span className={styles.phTextTools}>
                    {field.value && (
                      <button
                        type="button"
                        aria-label={`复制${field.name}`}
                        title="复制"
                        onClick={() => copyText(field.value) && setCopied(field.name)}
                      >
                        {copied === field.name ? <Check /> : <Copy />}
                      </button>
                    )}
                    {editable && (
                      <button
                        ref={(node) => {
                          if (node) pencils.current.set(field.name, node);
                          else pencils.current.delete(field.name);
                        }}
                        type="button"
                        aria-label={`更正${field.name}`}
                        title="更正"
                        disabled={busy}
                        onClick={() => {
                          setDraft(field.value);
                          setEditing(field.name);
                        }}
                      >
                        <Pencil />
                      </button>
                    )}
                  </span>
                  {!field.passed ? (
                    <span className={styles.phTextCheck}>
                      待核对：{(field.reasons ?? []).map((reason) => reasonNames[reason]).join("、") || "请对照照片"}
                      {editable && field.value && (
                        <button
                          type="button"
                          disabled={busy}
                          title="与照片上的一致"
                          onClick={() => void change(() => correctPhotoText(asset.id, field.name, field.value))}
                        >
                          确认
                        </button>
                      )}
                    </span>
                  ) : (
                    field.correction && (
                      <span className={styles.phTextBy}>
                        {field.correction.username} 更正于 {when(field.correction.at)}
                        {editable && (
                          <button
                            type="button"
                            disabled={busy}
                            title={field.read ? `识别结果：${field.read}` : "没有读出"}
                            onClick={() => void change(() => restorePhotoText(asset.id, field.name))}
                          >
                            <RotateCcw />
                            恢复识别结果
                          </button>
                        )}
                      </span>
                    )
                  )}
                </>
              )}
            </li>
          ))}
        </ul>
      )}

      <details className={styles.phTextMore} open={plain}>
        <summary>
          <ChevronRight />
          全文
        </summary>
        <p className={styles.phTextWhole}>{text.text}</p>
        {!text.complete && <small className={styles.phTextNote}>没有读完，后面的内容缺失</small>}
      </details>

      {!plain && (
        <details
          className={styles.phTextMore}
          onToggle={(event) => {
            const open = event.currentTarget.open;
            setChangesShown(open);
            if (open && !changes) void loadChanges();
          }}
        >
          <summary>
            <ChevronRight />
            修改记录
          </summary>
          {changes === undefined ? (
            <span className={styles.phSpinner} />
          ) : changes.length ? (
            <ol className={styles.phTextChanges}>
              {changes.map((entry, index) => {
                const undo = editable ? undoOf(changes, index) : undefined;
                return (
                  <li key={`${entry.at}:${index}`}>
                    <span>
                      {entry.username} · {when(entry.at)} · {libraryOfChange(entry.library)}
                    </span>
                    <span className={styles.phTextChange}>
                      {entry.field}：
                      {entry.restored ? (
                        <>恢复为识别结果 {entry.new || "（空）"}</>
                      ) : entry.old === entry.new ? (
                        <>确认 {entry.new || "（空）"}</>
                      ) : (
                        <>
                          <del>{entry.old || "（空）"}</del> → {entry.new || "（空）"}
                        </>
                      )}
                      {undo && (
                        <button
                          type="button"
                          disabled={busy}
                          aria-label={
                            undo.restore ? `把${entry.field}改回识别结果` : `把${entry.field}改回 ${entry.old || "空"}`
                          }
                          onClick={() =>
                            void change(() =>
                              undo.restore
                                ? restorePhotoText(asset.id, entry.field)
                                : correctPhotoText(asset.id, entry.field, entry.old),
                            )
                          }
                        >
                          改回
                        </button>
                      )}
                    </span>
                  </li>
                );
              })}
            </ol>
          ) : (
            <p className={styles.phTextNote}>还没有人改过</p>
          )}
        </details>
      )}
    </section>
  );
}
