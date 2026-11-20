import {
  closestCenter,
  DndContext,
  type DragEndEvent,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import {
  horizontalListSortingStrategy,
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, Download, Lock, LockOpen, Play, RotateCcw, Save, Search, Shuffle } from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";

import { APIError } from "../api/client";
import { errorText } from "../api/errorMessages";
import { clock } from "../photos/model";
import { useDialogs, useToasts } from "../ui/feedback";
import { Spinner } from "../ui/workspace";
import {
  draftCandidates,
  draftShotsURL,
  editDraft,
  frameURL,
  getVersionDraft,
  type MontageCandidate,
  type MontageDraft,
  type MontageDraftOp,
  type MontageDraftShot,
  type MontageJob,
  type MontageProjectDetail,
  saveDraft,
} from "./api";
import { Failed } from "./Failed";
import styles from "./montage.module.css";
import { draftKey, jobKey, projectKey } from "./queries";
import { RoughCut } from "./RoughCut";
import { FilmPlayer } from "./Versions";

// EditPage is the prototype's 精修 page (docs/specs/montage-center.md, 修改页):
// the music's parts as a bar, the shots grouped by part, the shot opened
// under its part with the beats it could have instead, and on the right a
// search over the project's videos and a viewer that tries a beat. Every
// step goes to the draft kept on the NAS (#596), which checks it, so the
// page holds nothing of its own but what it shows.

const SIZES = ["远景", "全景", "中景", "近景", "特写"];
// How wide a second of a shot is.
const PX_PER_SECOND = 30;
const secs = (value: number) => `${value.toFixed(1)} 秒`;

// The strength of a part of the music, as the bar colours it: a build leads
// into a chorus.
export type Strength = "quiet" | "mid" | "build" | "peak";
export const strengthOf = (part: { level: string; label: string }): Strength =>
  part.label === "build" ? "build" : part.level === "高潮" ? "peak" : part.level === "安静" ? "quiet" : "mid";
export const strengthNames: Record<Strength, string> = { quiet: "安静", mid: "中等", build: "铺垫", peak: "高潮" };

export default function EditPage({
  projectId,
  versionId,
  onBack,
}: {
  projectId: string;
  versionId: string;
  // Back to the project, where a film being saved shows its progress.
  onBack: () => void;
}) {
  const client = useQueryClient();
  const draft = useQuery({
    queryKey: draftKey(versionId),
    queryFn: ({ signal }) => getVersionDraft(versionId, signal),
  });
  const { toast, element: toasts } = useToasts();
  const dialogs = useDialogs();
  const [selected, setSelected] = useState(1);
  const [open, setOpen] = useState(false);
  const [preview, setPreview] = useState<MontageCandidate>();
  const [rough, setRough] = useState<number>();
  // The draft as it is now, for a step made from an undo kept in a toast.
  const current = () => client.getQueryData<MontageDraft>(draftKey(versionId)) as MontageDraft;

  const failure = (error: unknown) => {
    if (error instanceof APIError && error.code === "draft_conflict") void draft.refetch();
    const text =
      error instanceof APIError && error.code === "edit_refused" ? error.message : errorText(error, "montage");
    toast({ text, tone: "error" });
  };
  const step = useMutation({
    mutationFn: (op: MontageDraftOp) => {
      const now = current();
      return editDraft(now.id, now.revision, op);
    },
    onSuccess: (next) => {
      client.setQueryData(draftKey(versionId), next);
      setPreview(undefined);
      if (next.message)
        toast({
          text: next.message,
          action: next.canUndo ? { label: "撤销", run: () => step.mutate({ type: "undo" }) } : undefined,
        });
    },
    onError: failure,
  });
  const save = useMutation({
    mutationFn: () => saveDraft(current().id),
    onSuccess: (job: MontageJob) => {
      // The project shows the job as it shows generating's.
      client.setQueryData(jobKey(job.id), job);
      client.setQueryData<MontageProjectDetail>(projectKey(projectId), (old) => old && { ...old, job });
      void client.invalidateQueries({ queryKey: projectKey(projectId) });
      onBack();
    },
    onError: failure,
  });

  if (draft.error) {
    return (
      <main className={styles.mtMain}>
        <button type="button" className={`${styles.mtBtn} ${styles.small} ${styles.mtBack}`} onClick={onBack}>
          <ChevronLeft aria-hidden="true" />
          换一版
        </button>
        <Failed error={draft.error} onRetry={() => void draft.refetch()} />
      </main>
    );
  }
  if (!draft.data) {
    return (
      <div className={styles.mtCentered}>
        <Spinner />
      </div>
    );
  }
  const d = draft.data;
  const shot = d.shots.find((each) => each.number === selected) ?? d.shots[0];
  const unlocked = d.shots.filter((each) => !each.locked).length;
  const changed = d.shots.filter((each) => each.changed).length;
  const extended = d.shots.filter((each) => each.extended).length;
  const busy = step.isPending || save.isPending;

  const select = (number: number) => {
    setOpen(number === selected ? !open : true);
    setSelected(number);
    setPreview(undefined);
  };
  const shuffle = async () => {
    if (unlocked === d.shots.length) {
      const all = await dialogs.confirm({
        title: "重洗镜头",
        message: `还没有锁定镜头，重洗会换掉全部 ${d.shots.length} 个镜头。`,
        confirm: "全部重洗",
        cancel: "先去锁定",
      });
      if (!all) return;
    }
    step.mutate({ type: "shuffle" });
  };
  const replace = (candidate: MontageCandidate) =>
    step.mutate({ type: "replace", shot: shot.number, beat: candidate.beat });

  return (
    <main className={`${styles.mtMain} ${styles.mtEdit}`} aria-label={`第 ${d.versionNumber} 版 · 精修镜头`}>
      <div className={styles.mtEditBar}>
        <button type="button" className={`${styles.mtBtn} ${styles.small} ${styles.ghost}`} onClick={onBack}>
          <ChevronLeft aria-hidden="true" />
          换一版
        </button>
        <h1>
          第 {d.versionNumber} 版<span className={styles.mtFaint}> · 精修镜头</span>
        </h1>
        <span className={styles.mtGrow} />
        <span className={`${styles.mtTag} ${changed || extended ? styles.changed : ""}`}>
          {d.shots.length} 个镜头
          {changed ? ` · 已换 ${changed}` : ""}
          {extended ? ` · 延长 ${extended}` : ""}
        </span>
        <button
          type="button"
          className={`${styles.mtBtn} ${styles.small}`}
          disabled={busy || !unlocked}
          onClick={() => void shuffle()}
          title="锁住喜欢的镜头，其余的按规划的画面重新挑一遍"
        >
          <Shuffle aria-hidden="true" />
          {unlocked < d.shots.length ? `重洗没锁的 ${unlocked} 镜` : "重洗没锁的"}
        </button>
        <button
          type="button"
          className={`${styles.mtBtn} ${styles.small}`}
          disabled={busy}
          onClick={() => step.mutate({ type: "reset" })}
        >
          <RotateCcw aria-hidden="true" />
          全部还原
        </button>
        <button type="button" className={`${styles.mtBtn} ${styles.small}`} onClick={() => setRough(1)}>
          <Play aria-hidden="true" />
          粗预览
        </button>
        <a className={`${styles.mtBtn} ${styles.small}`} href={draftShotsURL(d.id)} download>
          <Download aria-hidden="true" />
          导出镜头清单
        </a>
        <button
          type="button"
          className={`${styles.mtBtn} ${styles.small} ${styles.primary}`}
          disabled={busy}
          onClick={() => save.mutate()}
        >
          <Save aria-hidden="true" />
          存为新版本
        </button>
      </div>
      <div className={styles.mtEditBody}>
        <div>
          <MusicBar draft={d} />
          {d.parts.map((part) => {
            const own = d.shots.filter((each) => each.part === part.number);
            if (!own.length) return null;
            return (
              <section key={part.number} className={styles.mtPart} aria-label={`段落 ${part.number}`}>
                <div className={styles.mtPartH}>
                  <span className={styles.mtFaint}>段落 {part.number}</span>
                  <h2>{part.name || `第 ${part.number} 段`}</h2>
                  <span className={styles.mtFaint}>{part.summary}</span>
                  {own.length > 1 && <span className={styles.mtHintText}>↔ 拖动换顺序</span>}
                </div>
                <Tiles
                  projectId={projectId}
                  draft={d}
                  shots={own}
                  selected={shot.number}
                  open={open}
                  disabled={busy}
                  onSelect={select}
                  onLock={(each) => step.mutate({ type: each.locked ? "unlock" : "lock", shot: each.number })}
                  onMove={(from, to) => step.mutate({ type: "move", shot: from, to })}
                />
                {open && shot.part === part.number && (
                  <Drawer
                    projectId={projectId}
                    draft={d}
                    shot={shot}
                    preview={preview}
                    disabled={busy}
                    onPreview={setPreview}
                    onReplace={replace}
                    onStep={(op) => step.mutate(op)}
                    onClose={() => {
                      setOpen(false);
                      setPreview(undefined);
                    }}
                    onPlay={() => setRough(shot.number)}
                  />
                )}
              </section>
            );
          })}
        </div>
        <Side
          key={shot.number}
          projectId={projectId}
          draft={d}
          shot={shot}
          preview={preview}
          disabled={busy}
          onPreview={setPreview}
          onReplace={replace}
        />
      </div>
      {rough !== undefined && <RoughCut draft={d} from={rough} onClose={() => setRough(undefined)} />}
      {toasts}
      {dialogs.dialog}
    </main>
  );
}

// MusicBar is the music's parts in a row, coloured by strength, each with its start.
function MusicBar({ draft }: { draft: MontageDraft }) {
  return (
    <div className={`${styles.mtPanel} ${styles.mtMusic}`}>
      <div className={styles.mtMusicH}>
        <span>
          {Math.round(draft.seconds)} 秒 · {draft.shots.length} 个镜头
        </span>
        <span className={styles.mtLegend}>
          {Object.entries(strengthNames).map(([key, name]) => (
            <span key={key}>
              <i data-strength={key} />
              {name}
            </span>
          ))}
        </span>
      </div>
      <div className={styles.mtBands}>
        {draft.parts.map((part) => (
          <div key={part.number} data-strength={strengthOf(part)} style={{ flexGrow: part.end - part.start }}>
            {part.name || strengthNames[strengthOf(part)]}
            <span>{clock(part.start)}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

// Tiles are a part's shots, as wide as they are long; an unlocked shot is
// dragged to another place of its part (dnd-kit).
function Tiles({
  projectId,
  draft,
  shots,
  selected,
  open,
  disabled,
  onSelect,
  onLock,
  onMove,
}: {
  projectId: string;
  draft: MontageDraft;
  shots: MontageDraftShot[];
  selected: number;
  open: boolean;
  disabled: boolean;
  onSelect: (number: number) => void;
  onLock: (shot: MontageDraftShot) => void;
  onMove: (from: number, to: number) => void;
}) {
  const sensors = useSensors(
    // A press that does not move is a click: the shot opens.
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const ended = ({ active, over }: DragEndEvent) => {
    if (over && active.id !== over.id) onMove(Number(active.id), Number(over.id));
  };
  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={ended}>
      <SortableContext items={shots.map((shot) => shot.number)} strategy={horizontalListSortingStrategy}>
        <div className={styles.mtTiles}>
          {shots.map((shot) => (
            <Tile
              key={`${shot.number}:${shot.beat}`}
              projectId={projectId}
              shot={shot}
              strength={strengthOf(draft.parts[shot.part - 1] ?? { level: "", label: "" })}
              selected={shot.number === selected}
              open={open && shot.number === selected}
              disabled={disabled}
              onSelect={() => onSelect(shot.number)}
              onLock={() => onLock(shot)}
            />
          ))}
        </div>
      </SortableContext>
    </DndContext>
  );
}

function Tile({
  projectId,
  shot,
  strength,
  selected,
  open,
  disabled,
  onSelect,
  onLock,
}: {
  projectId: string;
  shot: MontageDraftShot;
  strength: Strength;
  selected: boolean;
  open: boolean;
  disabled: boolean;
  onSelect: () => void;
  onLock: () => void;
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: shot.number,
    disabled: shot.locked || disabled,
  });
  const length = shot.end - shot.start;
  return (
    <div
      ref={setNodeRef}
      className={styles.mtTile}
      data-selected={selected || undefined}
      data-open={open || undefined}
      data-locked={shot.locked || undefined}
      data-dragging={isDragging || undefined}
      style={{
        width: Math.max(84, Math.round(length * PX_PER_SECOND)),
        transform: CSS.Transform.toString(transform),
        transition,
      }}
    >
      <button
        type="button"
        className={styles.mtTilePick}
        aria-label={`第 ${shot.number} 镜，${clock(shot.start)}，${secs(length)}`}
        onClick={onSelect}
        {...attributes}
        {...listeners}
      >
        <img src={frameURL(projectId, shot.assetId, shot.from)} alt="" draggable={false} />
        <span className={styles.mtTileNum}>{shot.number}</span>
        <span className={styles.mtTileBadges}>
          {shot.changed && <span className={`${styles.mtTag} ${styles.changed}`}>已换</span>}
          {shot.extended && <span className={`${styles.mtTag} ${styles.changed}`}>延长</span>}
          {shot.portrait && <span className={`${styles.mtTag} ${styles.warn}`}>竖</span>}
        </span>
        <i className={styles.mtTileLevel} data-strength={strength} />
        <span className={styles.mtTileMeta}>
          <span>
            {clock(shot.start)} · {secs(length)}
          </span>
          <span className={styles.mtTag}>{shot.shotSize}</span>
        </span>
      </button>
      <button
        type="button"
        className={styles.mtTileLock}
        aria-label={shot.locked ? `解锁第 ${shot.number} 镜` : `锁定第 ${shot.number} 镜`}
        aria-pressed={shot.locked}
        title={shot.locked ? "解锁" : "锁定这一镜，重洗时不动"}
        disabled={disabled}
        onClick={onLock}
      >
        {shot.locked ? <Lock aria-hidden="true" /> : <LockOpen aria-hidden="true" />}
      </button>
    </div>
  );
}

// Drawer is the shot opened under its part: where it is, what the plan asked
// of it, what can be done to it, and the beats it could have instead.
function Drawer({
  projectId,
  draft,
  shot,
  preview,
  disabled,
  onPreview,
  onReplace,
  onStep,
  onClose,
  onPlay,
}: {
  projectId: string;
  draft: MontageDraft;
  shot: MontageDraftShot;
  preview?: MontageCandidate;
  disabled: boolean;
  onPreview: (candidate: MontageCandidate) => void;
  onReplace: (candidate: MontageCandidate) => void;
  onStep: (op: MontageDraftOp) => void;
  onClose: () => void;
  onPlay: () => void;
}) {
  const near = useQuery({
    queryKey: ["montage", "candidates", draft.id, draft.revision, shot.number],
    queryFn: ({ signal }) => draftCandidates(draft.id, { shot: shot.number }, signal),
  });
  const part = draft.parts[shot.part - 1];
  const strength = part ? strengthOf(part) : "mid";
  const choices = [...(near.data?.sameCard ?? []), ...(near.data?.planned ?? [])];
  const free = choices.filter((candidate) => candidate.fits && !candidate.usedBy);
  return (
    <div className={styles.mtDrawer} role="group" aria-label={`第 ${shot.number} 镜`}>
      <div className={styles.mtDrawerH}>
        <b>第 {shot.number} 镜</b>
        <span className={styles.mtTag} data-strength={strength}>
          ● {strengthNames[strength]}
        </span>
        <span className={styles.mtTag}>
          {clock(shot.start)}–{clock(shot.end)} · {secs(shot.end - shot.start)}
        </span>
        <span className={styles.mtDrawerQuery}>规划要：{shot.query}</span>
        <span className={styles.mtGrow} />
        <span className={styles.mtHintText}>单击试看 · 双击替换</span>
        <button type="button" className={`${styles.mtBtn} ${styles.small} ${styles.ghost}`} onClick={onPlay}>
          <Play aria-hidden="true" />
          从这里预览
        </button>
        <button
          type="button"
          className={`${styles.mtBtn} ${styles.small} ${styles.ghost}`}
          disabled={disabled}
          onClick={() => onStep({ type: shot.locked ? "unlock" : "lock", shot: shot.number })}
        >
          {shot.locked ? "已锁定" : "锁定这一镜"}
        </button>
        {shot.changed && (
          <button
            type="button"
            className={`${styles.mtBtn} ${styles.small} ${styles.ghost}`}
            disabled={disabled}
            onClick={() => onStep({ type: "revert", shot: shot.number })}
          >
            还原这一镜
          </button>
        )}
        <button
          type="button"
          className={`${styles.mtBtn} ${styles.small} ${styles.ghost} ${styles.danger}`}
          disabled={disabled || draft.shots.length <= 1}
          onClick={() => onStep({ type: "delete", shot: shot.number })}
        >
          删除这一镜
        </button>
        <button type="button" className={`${styles.mtBtn} ${styles.small} ${styles.ghost}`} onClick={onClose}>
          收起
        </button>
      </div>
      {near.data && (
        <div className={styles.mtBeats}>
          {choices.map((candidate) => (
            <Beat
              key={candidate.beat}
              projectId={projectId}
              candidate={candidate}
              previewing={preview?.beat === candidate.beat}
              disabled={disabled}
              onPreview={onPreview}
              onReplace={onReplace}
            />
          ))}
        </div>
      )}
      {near.data && !free.length && <p className={styles.mtNote}>这张卡片没有可换的节拍了，可以在右边搜别的素材。</p>}
    </div>
  );
}

// Beat is a beat the shot could have: a click tries it, a double click puts
// it in. One another shot has, or too short for this shot, cannot be chosen.
function Beat({
  projectId,
  candidate,
  previewing,
  disabled,
  onPreview,
  onReplace,
}: {
  projectId: string;
  candidate: MontageCandidate;
  previewing: boolean;
  disabled: boolean;
  onPreview: (candidate: MontageCandidate) => void;
  onReplace: (candidate: MontageCandidate) => void;
}) {
  const off = Boolean(candidate.usedBy) || !candidate.fits;
  return (
    <button
      type="button"
      className={styles.mtBeat}
      data-previewing={previewing || undefined}
      data-off={off || undefined}
      aria-label={`${candidate.scene} · ${candidate.what}`}
      onClick={() => onPreview(candidate)}
      onDoubleClick={() => {
        if (!off && !disabled) onReplace(candidate);
      }}
    >
      <span className={styles.mtBeatPic}>
        <img src={frameURL(projectId, candidate.assetId, candidate.from)} alt="" loading="lazy" />
        {candidate.usedBy ? <em>已用在第 {candidate.usedBy} 镜</em> : !candidate.fits ? <em>不够长</em> : null}
      </span>
      <span className={styles.mtBeatText}>
        <b>{candidate.scene}</b> · {candidate.what}
      </span>
      <span className={styles.mtBeatTags}>
        <span className={styles.mtTag}>{candidate.shotSize}</span>
        {candidate.portrait && <span className={`${styles.mtTag} ${styles.warn}`}>竖</span>}
      </span>
    </button>
  );
}

// Side is the right column: a search over the project's videos with its
// filters, and a viewer that tries the beat clicked, or plays the shot.
function Side({
  projectId,
  draft,
  shot,
  preview,
  disabled,
  onPreview,
  onReplace,
}: {
  projectId: string;
  draft: MontageDraft;
  shot: MontageDraftShot;
  preview?: MontageCandidate;
  disabled: boolean;
  onPreview: (candidate?: MontageCandidate) => void;
  onReplace: (candidate: MontageCandidate) => void;
}) {
  // The box starts with what the plan asked of the shot; nothing is
  // searched until the member asks (Enter or 搜索).
  const [text, setText] = useState(shot.query);
  const [q, setQ] = useState("");
  const [size, setSize] = useState("");
  const [scene, setScene] = useState("");
  const [landscape, setLandscape] = useState(false);
  const results = useQuery({
    queryKey: ["montage", "candidates", draft.id, draft.revision, shot.number, q, size, scene, landscape],
    queryFn: ({ signal }) =>
      draftCandidates(
        draft.id,
        { shot: shot.number, ...(q ? { q } : {}), ...(size ? { size } : {}), ...(scene ? { scene } : {}), landscape },
        signal,
      ),
  });
  const viewer = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (preview) viewer.current?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
  }, [preview]);
  const search = (event: FormEvent) => {
    event.preventDefault();
    setQ(text.trim());
  };
  return (
    <aside className={`${styles.mtPanel} ${styles.mtSide}`} aria-label="换镜头">
      <form className={styles.mtFind} onSubmit={search} role="search">
        <h3>换第 {shot.number} 镜</h3>
        <label className={styles.mtSearch}>
          <Search aria-hidden="true" />
          <input
            type="search"
            value={text}
            maxLength={200}
            placeholder="写想要的画面，比如：直升机停在雪地上"
            aria-label="搜素材"
            onChange={(event) => setText(event.target.value)}
          />
          <button type="submit" className={`${styles.mtBtn} ${styles.small}`}>
            搜索
          </button>
        </label>
        <div className={styles.mtChips} role="group" aria-label="景别">
          {["", ...SIZES].map((each) => (
            <button
              key={each || "all"}
              type="button"
              aria-pressed={size === each}
              className={styles.mtChip}
              onClick={() => setSize(each)}
            >
              {each || "全部景别"}
            </button>
          ))}
        </div>
        <div className={styles.mtChips}>
          <select aria-label="场景" value={scene} onChange={(event) => setScene(event.target.value)}>
            <option value="">全部场景</option>
            {(results.data?.scenes ?? []).map((each) => (
              <option key={each.scene} value={each.scene}>
                {each.scene} {each.cards}
              </option>
            ))}
          </select>
          <label>
            <input type="checkbox" checked={landscape} onChange={(event) => setLandscape(event.target.checked)} />
            只看横拍
          </label>
        </div>
      </form>
      <div className={styles.mtResults} aria-label="搜索结果" aria-busy={results.isFetching}>
        {results.error ? (
          <p className={styles.mtNote}>{errorText(results.error, "montage")}</p>
        ) : !results.data ? (
          <Spinner small />
        ) : results.data.results.length === 0 ? (
          <p className={styles.mtFaint}>没有符合筛选的素材</p>
        ) : (
          results.data.results.map((candidate) => (
            <Beat
              key={candidate.beat}
              projectId={projectId}
              candidate={candidate}
              previewing={preview?.beat === candidate.beat}
              disabled={disabled}
              onPreview={onPreview}
              onReplace={onReplace}
            />
          ))
        )}
      </div>
      <div className={styles.mtViewer} ref={viewer}>
        <div className={styles.mtViewerH}>
          {preview ? (
            <>
              <b>候选</b>
              <span>
                {preview.scene} · {preview.what}
              </span>
            </>
          ) : (
            <>
              <b>第 {shot.number} 镜</b>
              <span>{shot.what}</span>
            </>
          )}
        </div>
        <div className={styles.mtFilm}>
          {preview ? (
            <FilmPlayer
              key={`${preview.beat}@${preview.from}`}
              assetId={preview.assetId}
              start={preview.from}
              end={preview.to}
              autoPlay
            />
          ) : (
            <FilmPlayer key={`${shot.beat}@${shot.from}`} assetId={shot.assetId} start={shot.from} end={shot.to} />
          )}
        </div>
        {preview && (
          <div className={styles.mtFilmActs}>
            <button type="button" className={`${styles.mtBtn} ${styles.small}`} onClick={() => onPreview(undefined)}>
              取消
            </button>
            <button
              type="button"
              className={`${styles.mtBtn} ${styles.small} ${styles.primary}`}
              disabled={disabled || Boolean(preview.usedBy) || !preview.fits}
              onClick={() => onReplace(preview)}
            >
              替换第 {shot.number} 镜
            </button>
          </div>
        )}
      </div>
    </aside>
  );
}
