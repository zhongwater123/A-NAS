import {
  ArrowLeft, ChevronRight, CircleAlert, FileAudio, Folder, HardDrive, Laptop, Loader2, ShieldCheck, Upload, Users, X,
} from "lucide-react";
import { type DragEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";

import { type FileEntry, listEntries, listSpaces, type Space } from "../api";
import { formatBytes } from "../bytes";
import { messageOf, Spinner, useVoice } from "./context";
import { TemplateGrid } from "./SummaryView";
import { audioExtensions, importQueue, isAudioName, uploadFolder } from "./uploads";
import { importRecording } from "./voiceApi";

interface Props {
  initialFiles: File[];
  onClose: () => void;
  onImported: (count: number, opened?: string) => void;
  onOpenFiles: () => void;
}

type Source = "computer" | "nas";
interface Picked { entry: FileEntry; path: string }

// ImportDialog adds recordings: files from this computer, which are uploaded
// to 个人空间/录音 first, or audio already on the NAS, which stays where it
// is. The scene and languages chosen here run after recognition.
export function ImportDialog({ initialFiles, onClose, onImported, onOpenFiles }: Props) {
  const voice = useVoice();
  const [source, setSource] = useState<Source>("computer");
  const [files, setFiles] = useState<File[]>(initialFiles);
  const [picked, setPicked] = useState<Picked[]>([]);
  const [template, setTemplate] = useState("general");
  const [language, setLanguage] = useState("");
  const [translateTo, setTranslateTo] = useState("");
  const [hotwords, setHotwords] = useState<string[]>([]);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const status = voice.status;
  useEffect(() => {
    const escape = (event: globalThis.KeyboardEvent) => { if (event.key === "Escape" && !busy) onClose(); };
    window.addEventListener("keydown", escape);
    return () => window.removeEventListener("keydown", escape);
  }, [busy, onClose]);

  const count = source === "computer" ? files.length : picked.length;
  const addWord = () => {
    const words = draft.split(/[,，、\s]+/).map((word) => word.trim()).filter(Boolean);
    if (words.length) setHotwords((current) => [...new Set([...current, ...words])].slice(0, 100));
    setDraft("");
  };
  const options = { language, template: template === "none" ? "" : template, translateTo, hotwords };

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      if (source === "computer") {
        await importQueue.upload(files, options, voice.userID);
        onImported(files.length);
        return;
      }
      let first: string | undefined;
      const problems: string[] = [];
      for (const item of picked) {
        try {
          const recording = await importRecording({ ...options, fileId: item.entry.id });
          first ??= recording.id;
        } catch (caught) {
          problems.push(`${item.entry.name}：${messageOf(caught, "导入失败")}`);
        }
      }
      if (problems.length && !first) { setError(problems.join("\n")); return; }
      if (problems.length) voice.toast(problems.join("；"), "error");
      onImported(picked.length - problems.length, first);
    } catch (caught) {
      setError(messageOf(caught, "导入失败"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="vo-dialog-backdrop" onPointerDown={(event) => { if (event.target === event.currentTarget && !busy) onClose(); }}>
      <div className="vo-dialog import" role="dialog" aria-modal="true" aria-label="导入录音">
        <header className="vo-dialog-head">
          <div><h3>导入录音</h3><small>转写、识别发言人后，按你选的场景整理</small></div>
          <button type="button" className="vo-icon-button" aria-label="关闭" onClick={onClose} disabled={busy}><X /></button>
        </header>
        <div className="vo-dialog-scroll">
          <div className="vo-sources" role="tablist" aria-label="录音来自">
            <button type="button" role="tab" aria-selected={source === "computer"} className={source === "computer" ? "active" : ""} onClick={() => setSource("computer")}>
              <Laptop /><span><strong>这台电脑</strong><small>上传到个人空间的“{uploadFolder}”文件夹</small></span>
            </button>
            <button type="button" role="tab" aria-selected={source === "nas"} className={source === "nas" ? "active" : ""} onClick={() => setSource("nas")}>
              <HardDrive /><span><strong>NAS 上的文件</strong><small>个人空间或共享空间里已有的录音，留在原处</small></span>
            </button>
          </div>
          {source === "computer" ? <ComputerPicker files={files} onChange={setFiles} /> : <NasPicker picked={picked} onChange={setPicked} onOpenFiles={onOpenFiles} />}

          <section className="vo-options">
            <h4>整理成</h4>
            <TemplateGrid templates={status?.templates ?? []} selected={template} onPick={setTemplate} compact />
            <label className="vo-check">
              <input type="checkbox" checked={template === "none"} onChange={(event) => setTemplate(event.target.checked ? "none" : "general")} />
              只要转写，暂不总结
            </label>
            <div className="vo-option-row">
              <label className="vo-field">
                <span className="vo-field-label">录音里说的是</span>
                <select value={language} onChange={(event) => setLanguage(event.target.value)}>
                  {(status?.spokenLanguages ?? [{ code: "", name: "自动" }]).map((item) => <option key={item.code} value={item.code}>{item.name}</option>)}
                </select>
              </label>
              <label className="vo-field">
                <span className="vo-field-label">同时翻译成</span>
                <select value={translateTo} onChange={(event) => setTranslateTo(event.target.value)}>
                  <option value="">不翻译</option>
                  {(status?.languages ?? []).map((item) => <option key={item.code} value={item.code}>{item.name}</option>)}
                </select>
              </label>
            </div>
            <div className="vo-field">
              <span className="vo-field-label">容易听错的词 <small>人名、地名、专业术语，帮助识别（可选）</small></span>
              <div className="vo-words" onClick={(event) => (event.currentTarget.querySelector("input") as HTMLInputElement | null)?.focus()}>
                {hotwords.map((word) => (
                  <span key={word}>{word}<button type="button" aria-label={`删除 ${word}`} onClick={() => setHotwords(hotwords.filter((other) => other !== word))}><X /></button></span>
                ))}
                <input value={draft} placeholder={hotwords.length ? "" : "输入后按回车，例如 王老师、A-NAS"} aria-label="添加热词"
                  onChange={(event) => setDraft(event.target.value)} onBlur={addWord}
                  onKeyDown={(event: KeyboardEvent<HTMLInputElement>) => {
                    if (event.key === "Enter" || event.key === "," || event.key === "，") { event.preventDefault(); addWord(); }
                    if (event.key === "Backspace" && !draft && hotwords.length) setHotwords(hotwords.slice(0, -1));
                  }} />
              </div>
            </div>
          </section>
          {status && !status.recognizer.available && (
            <p className="vo-note warn"><CircleAlert />管理员还没有配置豆包语音，录音会先排队，配置后自动开始。</p>
          )}
          <p className="vo-note"><ShieldCheck />录音会经火山引擎对象存储中转给豆包语音转写，识别完即从中转处删除；转写文字交给 VNAS 使用的文本模型翻译与总结；声纹只在这台 NAS 上计算和保存。</p>
          {error && <p className="vo-error" role="alert">{error}</p>}
        </div>
        <footer className="vo-dialog-actions">
          <button type="button" className="vo-button" onClick={onClose} disabled={busy}>取消</button>
          <button type="button" className="vo-button primary" disabled={busy || !count} onClick={() => void submit()}>
            {busy ? <Loader2 className="vo-spin" /> : <Upload />}{count > 1 ? `开始处理 ${count} 段录音` : "开始处理"}
          </button>
        </footer>
      </div>
    </div>
  );
}

function ComputerPicker({ files, onChange }: { files: File[]; onChange: (files: File[]) => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [over, setOver] = useState(false);
  const add = (list: ArrayLike<File>) => {
    const known = new Set(files.map((file) => `${file.name}/${file.size}`));
    const added = Array.from(list).filter((file) => isAudioName(file.name) && !known.has(`${file.name}/${file.size}`));
    if (added.length) onChange([...files, ...added]);
  };
  const drop = (event: DragEvent) => {
    event.preventDefault();
    event.stopPropagation();
    setOver(false);
    add(event.dataTransfer.files);
  };
  return (
    <div className="vo-picker">
      <div className={`vo-dropzone${over ? " over" : ""}${files.length ? " filled" : ""}`}
        onDragOver={(event) => { event.preventDefault(); event.stopPropagation(); setOver(true); }}
        onDragLeave={() => setOver(false)} onDrop={drop}>
        <input ref={input} type="file" multiple accept={audioExtensions.join(",")} hidden onChange={(event) => { if (event.target.files) add(event.target.files); event.target.value = ""; }} />
        <FileAudio />
        <strong>把录音拖到这里，或 <button type="button" className="vo-link" onClick={() => input.current?.click()}>选择文件</button></strong>
        <small>MP3、M4A、WAV、AMR、FLAC、OGG 等，也可以是带声音的视频；单段最长 5 小时</small>
      </div>
      {files.length > 0 && (
        <ul className="vo-file-list">
          {files.map((file) => (
            <li key={`${file.name}/${file.size}`}>
              <FileAudio /><span>{file.name}</span><small>{formatBytes(file.size)}</small>
              <button type="button" className="vo-icon-button small" aria-label={`移除 ${file.name}`} onClick={() => onChange(files.filter((other) => other !== file))}><X /></button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

const spaceName = (space: Space) => space.kind === "shared" ? "共享空间" : "个人空间";

// NasPicker browses the member's spaces for audio files.
function NasPicker({ picked, onChange, onOpenFiles }: { picked: Picked[]; onChange: (picked: Picked[]) => void; onOpenFiles: () => void }) {
  const [spaces, setSpaces] = useState<Space[]>();
  const [trail, setTrail] = useState<{ space: Space; folders: FileEntry[] }>();
  const [entries, setEntries] = useState<FileEntry[]>();
  const [error, setError] = useState("");
  useEffect(() => { void listSpaces().then((list) => setSpaces(list.filter((space) => !space.viewing))).catch((caught) => setError(messageOf(caught, "无法读取空间"))); }, []);
  useEffect(() => {
    if (!trail) return;
    setEntries(undefined);
    void listEntries(trail.space.id, trail.folders.at(-1)?.id ?? "")
      .then((list) => setEntries(list.filter((entry) => entry.kind === "directory" || isAudioName(entry.name)).sort((a, b) => (a.kind === b.kind ? a.name.localeCompare(b.name, "zh-CN") : a.kind === "directory" ? -1 : 1))))
      .catch((caught) => setError(messageOf(caught, "无法读取文件夹")));
  }, [trail]);
  const toggle = (entry: FileEntry) => {
    if (picked.some((item) => item.entry.id === entry.id)) onChange(picked.filter((item) => item.entry.id !== entry.id));
    else onChange([...picked, { entry, path: [trail ? spaceName(trail.space) : "", ...(trail?.folders.map((folder) => folder.name) ?? [])].join(" / ") }]);
  };
  if (error) return <div className="vo-browser"><p className="vo-error">{error}</p></div>;
  if (!spaces) return <div className="vo-browser center"><Spinner /></div>;
  return (
    <div className="vo-browser">
      <div className="vo-crumbs">
        {trail ? (
          <>
            <button type="button" className="vo-icon-button small" aria-label="返回" onClick={() => setTrail(trail.folders.length ? { ...trail, folders: trail.folders.slice(0, -1) } : undefined)}><ArrowLeft /></button>
            <button type="button" onClick={() => setTrail({ space: trail.space, folders: [] })}>{spaceName(trail.space)}</button>
            {trail.folders.map((folder, index) => (
              <span key={folder.id}><ChevronRight /><button type="button" onClick={() => setTrail({ ...trail, folders: trail.folders.slice(0, index + 1) })}>{folder.name}</button></span>
            ))}
          </>
        ) : <span>选择位置</span>}
        <span className="vo-crumbs-count">{picked.length ? `已选 ${picked.length} 个` : ""}</span>
      </div>
      <ul className="vo-entries">
        {!trail && spaces.map((space) => (
          <li key={space.id}><button type="button" onClick={() => setTrail({ space, folders: [] })}>{space.kind === "shared" ? <Users /> : <HardDrive />}<span>{spaceName(space)}</span><ChevronRight /></button></li>
        ))}
        {trail && entries === undefined && <li className="center"><Spinner /></li>}
        {trail && entries?.length === 0 && <li className="vo-entries-empty">这里没有录音。<button type="button" className="vo-link" onClick={onOpenFiles}>在文件管理中查看</button></li>}
        {trail && entries?.map((entry) => entry.kind === "directory" ? (
          <li key={entry.id}><button type="button" onClick={() => setTrail({ ...trail, folders: [...trail.folders, entry] })}><Folder /><span>{entry.name}</span><ChevronRight /></button></li>
        ) : (
          <li key={entry.id}>
            <label className={picked.some((item) => item.entry.id === entry.id) ? "checked" : ""}>
              <input type="checkbox" checked={picked.some((item) => item.entry.id === entry.id)} onChange={() => toggle(entry)} />
              <FileAudio /><span>{entry.name}</span><small>{formatBytes(entry.sizeBytes)}</small>
            </label>
          </li>
        ))}
      </ul>
    </div>
  );
}
