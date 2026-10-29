import { ImagePlus, Mic, Square, TriangleAlert, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import { errorText } from "../api/errorMessages";
import { listPhotoLibraries, uploadPhoto } from "../photos/api";
import {
  addRecord,
  addRecording,
  CustomVaccineCode,
  type FamilyBaby,
  type FamilyKind,
  type FamilyRecord,
  type FamilyRecordInput,
  familyError,
  getGrowth,
  requestKey,
  updateRecord,
} from "./api";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { decimal, kindLabels, today } from "./format";
import { Field, Modal, Thumb } from "./parts";

// Draft is what the composer opens with: a kind to add, a record to edit, a
// scheduled dose to record or an automatic milestone to annotate.
export interface Draft {
  kind: FamilyKind;
  record?: FamilyRecord;
  vaccine?: { code: string; dose: number };
  milestone?: { autoKey: string; title: string; date: string };
}

// A file chosen to upload, and how far it got.
interface Pending {
  key: string;
  file: File;
  preview?: string;
  progress: number;
  assetId?: string;
  failed?: string;
}

const photoTypes = "image/*,video/*,.heic,.heif,.mov";
const audioTypes = "audio/*,.m4a,.amr,.3gp,.aac,.mp3,.wav,.ogg,.flac";

// Composer adds or edits one record. Photos and videos are uploaded to the
// shared library first, as the person adding them, and the record names
// them; files that fail stay listed to try again.
export function Composer({
  baby,
  draft,
  onClose,
  onSaved,
}: {
  baby: FamilyBaby;
  draft: Draft;
  onClose: () => void;
  onSaved: () => void;
}) {
  const family = useFamily();
  const editing = draft.record;
  const kind = draft.kind;
  const key = useMemo(() => requestKey(), []);
  const [occurredOn, setOccurredOn] = useState(editing?.occurredOn ?? draft.milestone?.date ?? today());
  const [text, setText] = useState(editing?.text ?? "");
  const [kept, setKept] = useState(editing?.photos ?? []);
  const [files, setFiles] = useState<Pending[]>([]);
  const [also, setAlso] = useState<string[]>([]);
  const [height, setHeight] = useState(numberText(editing?.measurement?.heightCm, 1));
  const [weight, setWeight] = useState(numberText(editing?.measurement?.weightKg, 2));
  const [head, setHead] = useState(numberText(editing?.measurement?.headCm, 1));
  const [vaccineCode, setVaccineCode] = useState(editing?.vaccine?.code ?? draft.vaccine?.code ?? "");
  const [vaccineName, setVaccineName] = useState(editing?.vaccine?.name ?? "");
  const [dose, setDose] = useState(String(editing?.vaccine?.dose ?? draft.vaccine?.dose ?? 1));
  const [clinic, setClinic] = useState(editing?.vaccine?.clinic ?? "");
  const [batch, setBatch] = useState(editing?.vaccine?.batch ?? "");
  const [title, setTitle] = useState(editing?.milestone?.title ?? "");
  const autoKey = editing?.milestone?.autoKey || draft.milestone?.autoKey || "";
  const [audio, setAudio] = useState<{ blob: Blob; name: string }>();
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<number>();
  const [problem, setProblem] = useState("");
  const [warning, setWarning] = useState("");
  const [previous, setPrevious] = useState<{ height?: number; weight?: number }>();

  // A measurement is compared with the one before, to catch a typo.
  useEffect(() => {
    if (kind !== "measurement") return;
    let live = true;
    getGrowth(baby.id).then(
      (growth) => {
        if (!live) return;
        const earlier = growth.measurements.filter((item) => item.recordId !== editing?.id && item.date <= occurredOn);
        const last = <T,>(pick: (item: (typeof earlier)[number]) => T | undefined) =>
          earlier
            .map(pick)
            .filter((value): value is T => value !== undefined)
            .at(-1);
        setPrevious({ height: last((item) => item.heightCm), weight: last((item) => item.weightKg) });
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [kind, baby.id, editing?.id, occurredOn]);

  useEffect(() => () => files.forEach((item) => item.preview && URL.revokeObjectURL(item.preview)), [files]);

  const others = family.overview.babies.filter((item) => item.id !== baby.id);
  const heading = editing
    ? `编辑${kindLabels[kind]}`
    : draft.milestone
      ? draft.milestone.title
      : `记录${kindLabels[kind]}`;

  const addFiles = (list: FileList | File[]) => {
    const next = [...list].map((file) => ({
      key: `${file.name}-${file.size}-${file.lastModified}-${Math.random()}`,
      file,
      preview: file.type.startsWith("image/") && !/hei[cf]$/i.test(file.name) ? URL.createObjectURL(file) : undefined,
      progress: 0,
    }));
    setFiles((current) => [...current, ...next]);
  };

  // upload sends the chosen files that are not in the shared library yet.
  const upload = async (): Promise<string[] | undefined> => {
    const waiting = files.filter((item) => !item.assetId);
    if (waiting.length === 0) return files.map((item) => item.assetId!);
    const libraries = await listPhotoLibraries();
    const shared = libraries.find((library) => library.kind === "shared");
    if (!shared) throw new Problem("找不到共享图库，暂时不能上传照片。");
    const done = new Map<string, string>();
    const failed = new Map<string, string>();
    for (const item of waiting) {
      try {
        const result = await uploadPhoto(shared.id, item.file, (fraction) =>
          setFiles((current) =>
            current.map((entry) => (entry.key === item.key ? { ...entry, progress: fraction } : entry)),
          ),
        );
        if ("id" in result) done.set(item.key, result.id);
        else failed.set(item.key, "这是实况照片的视频部分，请和照片一起选择");
      } catch (caught) {
        failed.set(item.key, errorText(caught, "photos"));
      }
      setFiles((current) =>
        current.map((entry) =>
          done.has(entry.key)
            ? { ...entry, assetId: done.get(entry.key), progress: 1, failed: undefined }
            : failed.has(entry.key)
              ? { ...entry, failed: failed.get(entry.key) }
              : entry,
        ),
      );
    }
    if (failed.size > 0) return undefined;
    return files.map((item) => item.assetId ?? done.get(item.key)!);
  };

  const measurement = () => {
    const parse = (value: string) => (value.trim() === "" ? undefined : Number(value));
    return { heightCm: parse(height), weightKg: parse(weight), headCm: parse(head) };
  };

  // Ask before saving a measurement that differs sharply from the last one.
  const check = (): boolean => {
    if (kind !== "measurement" || warning || !previous) return true;
    const { heightCm, weightKg } = measurement();
    const notes: string[] = [];
    if (weightKg !== undefined && previous.weight && Math.abs(weightKg - previous.weight) / previous.weight > 0.3)
      notes.push(`体重比上次（${decimal(previous.weight, 2)} kg）变化超过 30%`);
    if (heightCm !== undefined && previous.height && previous.height - heightCm > 2)
      notes.push(`身长比上次（${decimal(previous.height, 1)} cm）少了 2 cm 以上`);
    if (notes.length === 0) return true;
    setWarning(`${notes.join("，")}，请核对后再保存。`);
    return false;
  };

  const save = async () => {
    if (busy || !check()) return;
    setBusy(true);
    setProblem("");
    try {
      if (kind === "recording") {
        if (editing) await updateRecord(editing.id, { text: text.trim(), occurredOn, version: editing.version });
        else {
          if (!audio) throw new Problem("请先录音或选择录音文件。");
          await addRecording(
            baby.id,
            audio.blob,
            audio.name,
            { title: text.trim(), occurredOn, requestKey: key },
            setProgress,
          );
        }
        onSaved();
        return;
      }
      const uploaded = await upload();
      if (!uploaded) {
        setProblem("有文件没有上传成功，可以再试一次，或移除它们后保存。");
        setBusy(false);
        return;
      }
      const photos = [...kept.map((photo) => photo.assetId), ...uploaded];
      const input: FamilyRecordInput = { requestKey: key, text: text.trim() };
      if (kind !== "photos" && !autoKey) input.occurredOn = occurredOn;
      switch (kind) {
        case "photos":
        case "journal":
          input.photos = photos;
          break;
        case "measurement":
          input.measurement = measurement();
          break;
        case "vaccine":
          input.vaccine = {
            code: vaccineCode,
            dose: Number(dose),
            clinic: clinic.trim(),
            batch: batch.trim(),
            ...(vaccineCode === CustomVaccineCode ? { name: vaccineName.trim() } : {}),
          };
          input.certificates = photos;
          break;
        case "milestone":
          input.milestone = autoKey ? { title: "", autoKey } : { title: title.trim() };
          input.photos = photos;
          break;
      }
      if (editing) {
        delete input.requestKey;
        await updateRecord(editing.id, { ...input, version: editing.version });
      } else {
        await addRecord(baby.id, { ...input, kind, ...(kind === "photos" && also.length ? { alsoBabies: also } : {}) });
      }
      onSaved();
    } catch (caught) {
      setProblem(caught instanceof Problem ? caught.message : familyError(caught));
      setBusy(false);
    }
  };

  const photoCount = kept.length + files.length;
  const ready = (() => {
    switch (kind) {
      case "photos":
        return photoCount > 0;
      case "recording":
        return Boolean(editing || audio);
      case "journal":
        return Boolean(text.trim() || photoCount);
      case "measurement":
        return Boolean(height.trim() || weight.trim() || head.trim());
      case "vaccine":
        return Boolean(vaccineCode && Number(dose) >= 1 && (vaccineCode !== CustomVaccineCode || vaccineName.trim()));
      case "milestone":
        return Boolean(autoKey || title.trim());
    }
  })();

  const photoPicker = (
    <PhotoPicker
      label={kind === "vaccine" ? "接种凭证（可选，不放进云相册）" : kind === "photos" ? "照片和视频" : "照片（可选）"}
      imagesOnly={kind === "vaccine"}
      kept={kept}
      onRemoveKept={(assetId) => setKept((current) => current.filter((photo) => photo.assetId !== assetId))}
      files={files}
      onAdd={addFiles}
      onRemove={(key) => setFiles((current) => current.filter((item) => item.key !== key))}
      disabled={busy}
    />
  );

  return (
    <Modal
      title={heading}
      busy={busy}
      wide={kind === "photos" || kind === "journal"}
      onClose={onClose}
      footer={
        <>
          <span className={styles.fcGrow} />
          <button type="button" className={styles.fcSecondary} onClick={onClose} disabled={busy}>
            取消
          </button>
          <button type="button" className={styles.fcPrimary} onClick={() => void save()} disabled={!ready || busy}>
            {busy ? "正在保存…" : warning ? "仍然保存" : "保存"}
          </button>
        </>
      }
    >
      {kind === "milestone" && !autoKey && (
        <>
          <Field label="发生了什么">
            <input
              className={styles.fcInput}
              value={title}
              maxLength={30}
              onChange={(event) => setTitle(event.target.value)}
              autoFocus
            />
          </Field>
          <div className={styles.fcChips} aria-label="常见的里程碑">
            {family.catalog.milestones.map((suggestion) => (
              <button
                key={suggestion}
                type="button"
                className={`${styles.fcChip} ${title === suggestion ? styles.active : ""}`}
                onClick={() => setTitle(suggestion)}
              >
                {suggestion}
              </button>
            ))}
          </div>
        </>
      )}
      {kind === "vaccine" && (
        <VaccineFields
          code={vaccineCode}
          onCode={setVaccineCode}
          name={vaccineName}
          onName={setVaccineName}
          dose={dose}
          onDose={setDose}
          clinic={clinic}
          onClinic={setClinic}
          batch={batch}
          onBatch={setBatch}
        />
      )}
      {kind === "measurement" && (
        <div className={styles.fcFormRow3}>
          <Field label="身长 / 身高（cm）">
            <input
              className={styles.fcInput}
              inputMode="decimal"
              type="number"
              step="0.1"
              min="30"
              max="200"
              value={height}
              onChange={(event) => {
                setHeight(event.target.value);
                setWarning("");
              }}
            />
          </Field>
          <Field label="体重（kg）">
            <input
              className={styles.fcInput}
              inputMode="decimal"
              type="number"
              step="0.01"
              min="0.5"
              max="100"
              value={weight}
              onChange={(event) => {
                setWeight(event.target.value);
                setWarning("");
              }}
            />
          </Field>
          <Field label="头围（cm）">
            <input
              className={styles.fcInput}
              inputMode="decimal"
              type="number"
              step="0.1"
              min="20"
              max="70"
              value={head}
              onChange={(event) => {
                setHead(event.target.value);
                setWarning("");
              }}
            />
          </Field>
        </div>
      )}
      {kind === "recording" && !editing && <AudioPicker audio={audio} onAudio={setAudio} disabled={busy} />}
      {(kind !== "photos" || editing) && !autoKey && (
        <Field label={kind === "measurement" ? "测量日期" : kind === "vaccine" ? "接种日期" : "日期"}>
          <input
            className={styles.fcInput}
            type="date"
            value={occurredOn}
            min={kind === "measurement" || kind === "vaccine" || kind === "milestone" ? baby.birthDate : undefined}
            max={today()}
            onChange={(event) => {
              setOccurredOn(event.target.value);
              setWarning("");
            }}
          />
        </Field>
      )}
      {/* A photo record leads with its photos; the others with their text. */}
      {kind === "photos" && photoPicker}
      {kind !== "measurement" && (
        <Field
          label={
            kind === "recording"
              ? "标题"
              : kind === "journal"
                ? "写点什么"
                : kind === "photos"
                  ? "说一句（可选）"
                  : "说明（可选）"
          }
        >
          {kind === "journal" || kind === "milestone" ? (
            <textarea
              className={`${styles.fcTextarea} ${kind === "journal" ? styles.writing : ""}`}
              rows={kind === "journal" ? 6 : 3}
              maxLength={5000}
              value={text}
              onChange={(event) => setText(event.target.value)}
              autoFocus={kind === "journal"}
            />
          ) : (
            <input
              className={styles.fcInput}
              value={text}
              maxLength={kind === "recording" ? 60 : 500}
              onChange={(event) => setText(event.target.value)}
            />
          )}
        </Field>
      )}
      {kind !== "photos" && (kind === "journal" || kind === "milestone" || kind === "vaccine") && photoPicker}
      {kind === "photos" && !editing && others.length > 0 && (
        <div className={styles.fcAlso}>
          {others.map((other) => (
            <label key={other.id}>
              <input
                type="checkbox"
                checked={also.includes(other.id)}
                onChange={(event) =>
                  setAlso((current) =>
                    event.target.checked ? [...current, other.id] : current.filter((id) => id !== other.id),
                  )
                }
              />
              也放进{other.nickname || other.name}的频道
            </label>
          ))}
        </div>
      )}
      {(kind === "photos" || kind === "journal" || kind === "milestone") && !editing && (
        <p className={styles.fcFootnote}>照片视频会以你的身份存进共享图库，在相册里也能看到。</p>
      )}
      {progress !== undefined && busy && (
        <div
          className={styles.fcProgress}
          role="progressbar"
          aria-valuenow={Math.round(progress * 100)}
          aria-valuemin={0}
          aria-valuemax={100}
        >
          <span style={{ width: `${progress * 100}%` }} />
        </div>
      )}
      {warning && (
        <p className={styles.fcWarning}>
          <TriangleAlert size={14} /> {warning}
        </p>
      )}
      {problem && <p className={styles.fcProblem}>{problem}</p>}
    </Modal>
  );
}

// Problem is a reason the composer explains itself.
class Problem extends Error {}

function numberText(value: number | undefined, digits: number): string {
  return value === undefined ? "" : value.toFixed(digits);
}

function VaccineFields(props: {
  code: string;
  onCode: (code: string) => void;
  name: string;
  onName: (name: string) => void;
  dose: string;
  onDose: (dose: string) => void;
  clinic: string;
  onClinic: (clinic: string) => void;
  batch: string;
  onBatch: (batch: string) => void;
}) {
  const { catalog } = useFamily();
  const vaccines = catalog.vaccines.vaccines;
  return (
    <>
      <div className={styles.fcFormRow}>
        <Field label="疫苗">
          <select className={styles.fcInput} value={props.code} onChange={(event) => props.onCode(event.target.value)}>
            <option value="" disabled>
              选择疫苗
            </option>
            <optgroup label="免疫规划疫苗（免费）">
              {vaccines
                .filter((vaccine) => vaccine.programme)
                .map((vaccine) => (
                  <option key={vaccine.code} value={vaccine.code}>
                    {vaccine.name}
                  </option>
                ))}
            </optgroup>
            <optgroup label="非免疫规划疫苗（自费）">
              {vaccines
                .filter((vaccine) => !vaccine.programme)
                .map((vaccine) => (
                  <option key={vaccine.code} value={vaccine.code}>
                    {vaccine.name}
                  </option>
                ))}
            </optgroup>
            <option value={CustomVaccineCode}>其他疫苗…</option>
          </select>
        </Field>
        <Field label="第几剂">
          <input
            className={styles.fcInput}
            type="number"
            min="1"
            max="10"
            value={props.dose}
            onChange={(event) => props.onDose(event.target.value)}
          />
        </Field>
      </div>
      {props.code === CustomVaccineCode && (
        <Field label="疫苗名称">
          <input
            className={styles.fcInput}
            value={props.name}
            maxLength={30}
            onChange={(event) => props.onName(event.target.value)}
          />
        </Field>
      )}
      <div className={styles.fcFormRow}>
        <Field label="接种单位（可选）">
          <input
            className={styles.fcInput}
            value={props.clinic}
            maxLength={50}
            onChange={(event) => props.onClinic(event.target.value)}
          />
        </Field>
        <Field label="批号（可选）">
          <input
            className={styles.fcInput}
            value={props.batch}
            maxLength={50}
            onChange={(event) => props.onBatch(event.target.value)}
          />
        </Field>
      </div>
    </>
  );
}

function PhotoPicker({
  label,
  imagesOnly,
  kept,
  onRemoveKept,
  files,
  onAdd,
  onRemove,
  disabled,
}: {
  label: string;
  imagesOnly: boolean;
  kept: FamilyRecord["photos"];
  onRemoveKept: (assetId: string) => void;
  files: Pending[];
  onAdd: (files: FileList | File[]) => void;
  onRemove: (key: string) => void;
  disabled: boolean;
}) {
  const input = useRef<HTMLInputElement>(null);
  const [over, setOver] = useState(false);
  return (
    <div className={styles.fcField}>
      <span className={styles.fcFieldLabel}>{label}</span>
      <div
        className={`${styles.fcDrop} ${over ? styles.over : ""}`}
        onDragOver={(event) => {
          event.preventDefault();
          setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={(event) => {
          event.preventDefault();
          setOver(false);
          if (!disabled) onAdd(event.dataTransfer.files);
        }}
      >
        {kept.map((photo) => (
          <div key={photo.assetId} className={styles.fcPick}>
            <Thumb assetId={photo.assetId} />
            {!disabled && (
              <button
                type="button"
                className={styles.fcPickRemove}
                aria-label="移除这张"
                onClick={() => onRemoveKept(photo.assetId)}
              >
                <X size={12} />
              </button>
            )}
          </div>
        ))}
        {files.map((item) => (
          <div
            key={item.key}
            className={`${styles.fcPick} ${item.failed ? styles.failed : ""}`}
            title={item.failed ?? item.file.name}
          >
            {item.preview ? (
              <img src={item.preview} alt="" />
            ) : (
              <span className={styles.fcPickName}>{item.file.name}</span>
            )}
            {item.progress > 0 && item.progress < 1 && (
              <span className={styles.fcPickProgress} style={{ width: `${item.progress * 100}%` }} />
            )}
            {item.failed && <span className={styles.fcPickFailed}>上传失败</span>}
            {!disabled && !item.assetId && (
              <button
                type="button"
                className={styles.fcPickRemove}
                aria-label={`移除 ${item.file.name}`}
                onClick={() => onRemove(item.key)}
              >
                <X size={12} />
              </button>
            )}
          </div>
        ))}
        <button type="button" className={styles.fcPickAdd} onClick={() => input.current?.click()} disabled={disabled}>
          <ImagePlus size={20} />
          <span>{kept.length + files.length ? "继续添加" : "选择或拖入"}</span>
        </button>
        <input
          ref={input}
          type="file"
          multiple
          hidden
          accept={imagesOnly ? "image/*,.heic,.heif" : photoTypes}
          onChange={(event) => {
            if (event.target.files) onAdd(event.target.files);
            event.target.value = "";
          }}
        />
      </div>
    </div>
  );
}

// AudioPicker records in the page where the browser allows the microphone
// (HTTPS, or the device's own screen), or takes a recording made elsewhere.
function AudioPicker({
  audio,
  onAudio,
  disabled,
}: {
  audio?: { blob: Blob; name: string };
  onAudio: (audio?: { blob: Blob; name: string }) => void;
  disabled: boolean;
}) {
  const input = useRef<HTMLInputElement>(null);
  const recorder = useRef<MediaRecorder>(null);
  const [recording, setRecording] = useState(false);
  const [seconds, setSeconds] = useState(0);
  const [problem, setProblem] = useState("");
  const canRecord =
    typeof window !== "undefined" &&
    window.isSecureContext &&
    Boolean(navigator.mediaDevices?.getUserMedia) &&
    typeof MediaRecorder !== "undefined";
  const url = useMemo(() => (audio ? URL.createObjectURL(audio.blob) : undefined), [audio]);
  useEffect(
    () => () => {
      if (url) URL.revokeObjectURL(url);
    },
    [url],
  );
  useEffect(() => {
    if (!recording) return;
    const timer = window.setInterval(() => setSeconds((value) => value + 1), 1000);
    return () => window.clearInterval(timer);
  }, [recording]);
  useEffect(() => () => recorder.current?.stream.getTracks().forEach((track) => track.stop()), []);

  const start = async () => {
    setProblem("");
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      const chunks: Blob[] = [];
      const media = new MediaRecorder(stream);
      media.ondataavailable = (event) => chunks.push(event.data);
      media.onstop = () => {
        stream.getTracks().forEach((track) => track.stop());
        const type = (media.mimeType || "audio/webm").split(";")[0];
        const blob = new Blob(chunks, { type });
        onAudio({ blob, name: `录音.${type.includes("mp4") ? "m4a" : type.includes("ogg") ? "ogg" : "webm"}` });
        setRecording(false);
      };
      recorder.current = media;
      media.start();
      setSeconds(0);
      setRecording(true);
    } catch {
      setProblem("没能打开麦克风，请检查浏览器的麦克风权限。");
    }
  };

  return (
    <div className={styles.fcField}>
      <span className={styles.fcFieldLabel}>录音</span>
      <div className={styles.fcAudioPick}>
        {audio && url ? (
          <>
            <audio controls src={url} className={styles.fcAudio} />
            <button type="button" className={styles.fcLink} onClick={() => onAudio(undefined)} disabled={disabled}>
              重新选择
            </button>
          </>
        ) : recording ? (
          <button type="button" className={styles.fcRecordStop} onClick={() => recorder.current?.stop()}>
            <Square size={13} fill="currentColor" /> 停止录音 {Math.floor(seconds / 60)}:
            {String(seconds % 60).padStart(2, "0")}
          </button>
        ) : (
          <>
            {canRecord && (
              <button type="button" className={styles.fcRecordStart} onClick={() => void start()} disabled={disabled}>
                <Mic size={15} /> 开始录音
              </button>
            )}
            <button
              type="button"
              className={styles.fcSecondary}
              onClick={() => input.current?.click()}
              disabled={disabled}
            >
              选择录音文件
            </button>
          </>
        )}
        <input
          ref={input}
          type="file"
          accept={audioTypes}
          hidden
          onChange={(event) => {
            const file = event.target.files?.[0];
            if (file) onAudio({ blob: file, name: file.name });
            event.target.value = "";
          }}
        />
      </div>
      {!canRecord && !audio && (
        <span className={styles.fcFieldHint}>
          通过局域网用 HTTP 打开时，浏览器不允许网页使用麦克风。可以先用手机或电脑的录音应用录好，再选择文件。
        </span>
      )}
      {problem && <span className={styles.fcProblem}>{problem}</span>}
    </div>
  );
}
