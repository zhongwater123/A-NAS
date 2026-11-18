import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  Check,
  ChevronLeft,
  Clock,
  Download,
  Film,
  Loader2,
  Music2,
  Pencil,
  Play,
  Plus,
  Scissors,
  Trash2,
  Video,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { errorText } from "../api/errorMessages";
import { getPhoto, originalURL, thumbnailURL } from "../photos/api";
import { VideoPlayer } from "../photos/VideoPlayer";
import { useDialogs } from "../ui/feedback";
import { Spinner } from "../ui/workspace";
import {
  cancelJob,
  deleteProject,
  dropAssets,
  generate,
  getJob,
  type MontageJob,
  type MontageProjectDetail,
  type MontageVersion,
  replan,
  retryAnalysis,
  retryJob,
  updateProject,
} from "./api";
import { Failed } from "./Failed";
import styles from "./montage.module.css";
import { projectQuery, songQuery } from "./queries";
import { active, needs, stateText, stepNames } from "./words";

// A job in progress is asked about every two seconds, while the window can be seen.
const POLL_MS = 2_000;
const when = new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" });
const length = (seconds: number) =>
  seconds >= 60 ? `${Math.floor(seconds / 60)} 分 ${Math.round(seconds % 60)} 秒` : `${Math.round(seconds)} 秒`;

// ProjectView is the prototype's upload page for one project: the project
// over its first video, its videos in the footage panel, the job and the
// film once there are any, and the bar that starts the film.
export function ProjectView({
  id,
  onBack,
  onPage,
  onAddVideos,
}: {
  id: string;
  onBack: () => void;
  onPage: (page: "song" | "ask") => void;
  onAddVideos: () => void;
}) {
  const client = useQueryClient();
  const project = useQuery(projectQuery(id));
  const song = useQuery(songQuery(id));
  const dialogs = useDialogs();
  const remove = useMutation({
    mutationFn: () => deleteProject(id),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["montage", "projects"] });
      onBack();
    },
  });
  const rename = useMutation({
    mutationFn: (name: string) => updateProject(id, { name }),
    onSuccess: (detail) => {
      client.setQueryData(["montage", "project", id], detail);
      void client.invalidateQueries({ queryKey: ["montage", "projects"] });
    },
  });

  if (project.error) {
    return (
      <main className={styles.mtMain}>
        <button type="button" className={`${styles.mtBtn} ${styles.small} ${styles.mtBack}`} onClick={onBack}>
          <ChevronLeft aria-hidden="true" />
          全部项目
        </button>
        <Failed error={project.error} onRetry={() => void project.refetch()} />
      </main>
    );
  }
  if (!project.data) {
    return (
      <div className={styles.mtCentered}>
        <Spinner />
      </div>
    );
  }
  const detail = project.data;
  const seconds = detail.assets.reduce((sum, asset) => sum + (asset.duration ?? 0), 0);
  const cover = detail.assets.find((asset) => asset.status === "available");
  return (
    <main className={styles.mtMain} aria-label={detail.name}>
      <button type="button" className={`${styles.mtBtn} ${styles.small} ${styles.mtBack}`} onClick={onBack}>
        <ChevronLeft aria-hidden="true" />
        全部项目
      </button>
      <section className={`${styles.mtHero} ${styles.compact}`}>
        {cover && <img src={thumbnailURL(cover.assetId)} alt="" />}
        <div className={styles.mtHeroCopy}>
          <div className={styles.mtEyebrow}>剪辑项目</div>
          <h1>{detail.name}</h1>
          <p>
            {detail.assetCount} 段视频 · 约 {length(seconds)} ·{" "}
            {detail.versions.length ? `已有 ${detail.versions.length} 版成片` : "还没有成片"}
          </p>
        </div>
        <div className={styles.mtHeroTools}>
          <button
            type="button"
            className={`${styles.mtBtn} ${styles.small}`}
            onClick={async () => {
              const name = await dialogs.prompt({
                title: "项目改名",
                label: "名称",
                value: detail.name,
                maxLength: 64,
              });
              if (name?.trim()) rename.mutate(name.trim());
            }}
          >
            <Pencil aria-hidden="true" />
            改名
          </button>
          <button
            type="button"
            className={`${styles.mtBtn} ${styles.small} ${styles.danger}`}
            onClick={async () => {
              const ok = await dialogs.confirm({
                title: "删除剪辑项目",
                message: "项目、版本记录和规划都会删除；相册里的原片和成片都保留。",
                confirm: "删除",
                danger: true,
              });
              if (ok) remove.mutate();
            }}
          >
            <Trash2 aria-hidden="true" />
            删除
          </button>
        </div>
      </section>
      {(rename.error || remove.error) && <p className={styles.mtError}>{errorText(rename.error ?? remove.error)}</p>}
      <Videos detail={detail} seconds={seconds} onAdd={onAddVideos} />
      <Films versions={detail.versions} />
      <Go detail={detail} songTitle={song.data?.title} songState={song.data?.state} onPage={onPage} />
      {dialogs.dialog}
    </main>
  );
}

function Videos({ detail, seconds, onAdd }: { detail: MontageProjectDetail; seconds: number; onAdd: () => void }) {
  const trashed = detail.assets.filter((asset) => asset.status === "trashed").length;
  const missing = detail.assets.filter((asset) => asset.status === "missing").length;
  return (
    <section className={`${styles.mtPanel} ${styles.mtStep} ${styles.media}`} aria-label="素材">
      <div className={styles.mtStepH}>
        <span className={styles.mtBadge} aria-hidden="true">
          <Video />
        </span>
        <div>
          <div className={styles.mtStepN}>素材</div>
          <h2>这次要剪的视频</h2>
        </div>
        <small>
          <b>{detail.assetCount}</b> 段 · 约 {length(seconds)}
        </small>
      </div>
      <ul className={styles.mtThumbs}>
        {detail.assets.map((asset) => (
          <li key={asset.assetId} className={styles.mtThumb} data-status={asset.status}>
            <img src={thumbnailURL(asset.assetId)} alt="" loading="lazy" />
            {asset.status === "trashed" && <em>在回收站</em>}
            {asset.status === "missing" && <em>已不在相册</em>}
            {asset.duration !== undefined && <span>{length(asset.duration)}</span>}
          </li>
        ))}
      </ul>
      {trashed > 0 && <p className={`${styles.mtNote} ${styles.mtHint}`}>有 {trashed} 个视频在回收站，恢复后才能用</p>}
      {missing > 0 && <p className={`${styles.mtNote} ${styles.mtHint}`}>有 {missing} 个视频已经不在相册里了</p>}
      <button type="button" className={styles.mtAdd} onClick={onAdd}>
        <Plus aria-hidden="true" />
        添加视频
      </button>
    </section>
  );
}

const tones: Record<MontageJob["state"], string> = {
  queued: "running",
  running: "running",
  waiting: "waiting",
  needs_input: "stopped",
  failed: "stopped",
  cancelled: "stopped",
  done: "done",
};
const labels = { drop: "去掉这些继续", retryAnalysis: "重试分析", retry: "重试", replan: "重新规划" } as const;

// Go is the prototype's bar under the footage: the song and the request,
// each opening its own page, and 开始剪辑. The job it starts shows above it
// as the prototype's processing panel, until the film is saved.
function Go({
  detail,
  songTitle,
  songState,
  onPage,
}: {
  detail: MontageProjectDetail;
  songTitle?: string;
  songState?: string;
  onPage: (page: "song" | "ask") => void;
}) {
  const client = useQueryClient();
  const latest = detail.job;
  // The job this page started, or else the project's latest.
  const [started, setStarted] = useState<string>();
  const jobId = started ?? latest?.id;
  const job = useQuery({
    queryKey: ["montage", "job", jobId],
    queryFn: ({ signal }) => getJob(jobId as string, signal),
    enabled: Boolean(jobId),
    initialData: latest && latest.id === jobId ? latest : undefined,
    refetchInterval: (query) => (active(query.state.data) ? POLL_MS : false),
    refetchIntervalInBackground: false,
  });
  const ended = useRef(false);
  useEffect(() => {
    // A job that saved a film shows its version on the project.
    if (job.data?.state === "done" && !ended.current) {
      ended.current = true;
      void client.invalidateQueries({ queryKey: ["montage", "project", detail.id] });
    }
  }, [job.data?.state, client, detail.id]);
  const start = useMutation({
    mutationFn: () => generate(detail.id),
    onSuccess: (created) => {
      ended.current = false;
      client.setQueryData(["montage", "job", created.id], created);
      setStarted(created.id);
    },
  });
  const act = useMutation({
    mutationFn: (action: "cancel" | "drop" | "retryAnalysis" | "retry" | "replan") => {
      const id = jobId as string;
      switch (action) {
        case "cancel":
          return cancelJob(id);
        case "drop":
          return dropAssets(id, job.data?.missingAssetIds ?? []);
        case "retryAnalysis":
          return retryAnalysis(id);
        case "retry":
          return retryJob(id);
        case "replan":
          return replan(id);
      }
    },
    onSuccess: (updated) => client.setQueryData(["montage", "job", updated.id], updated),
  });
  const current = job.data;
  const songReady = songState === "ready";
  const going = active(current) || current?.state === "needs_input";
  const shown = current && current.state !== "done" && current.state !== "cancelled";
  const tone = current ? tones[current.state] : undefined;
  const Icon = tone === "stopped" ? AlertTriangle : tone === "waiting" ? Clock : Loader2;
  const way = current?.state === "needs_input" ? needs[current.waitReason ?? ""] : undefined;
  const done = current?.steps.filter((step) => step.state === "done").length ?? 0;
  const order = detail.chronological ? "按拍摄时间" : "交给 VNAS 安排顺序";
  return (
    <>
      {shown && (
        <section className={`${styles.mtPanel} ${styles.mtWork}`} role="status" aria-label="生成进度">
          <div className={styles.mtWorkH} data-tone={tone}>
            <Icon aria-hidden="true" className={tone === "running" ? styles.mtSpin : undefined} />
            <h2>{stateText(current)}</h2>
          </div>
          <div className={styles.mtBar} aria-hidden="true">
            <span style={{ width: `${(done / current.steps.length) * 100}%` }} />
          </div>
          <ol aria-label="步骤">
            {current.steps.map((step) => (
              <li key={step.name} data-state={step.state}>
                <i aria-hidden="true" />
                {stepNames[step.name] ?? step.name}
              </li>
            ))}
          </ol>
          {current.state === "failed" && current.error && (
            <p className={`${styles.mtError} ${styles.mtHint}`}>{current.error}</p>
          )}
          {act.error && <p className={`${styles.mtError} ${styles.mtHint}`}>{errorText(act.error)}</p>}
          <div className={styles.mtLeave}>
            <span>{going ? "可以关掉窗口：VNAS 在后台规划，成片会存进你的相册。" : "这一次没有剪完。"}</span>
            {going && (
              <div>
                {way?.actions.map((action) => (
                  <button
                    key={action}
                    type="button"
                    className={`${styles.mtBtn} ${styles.small} ${styles.primary}`}
                    disabled={act.isPending}
                    onClick={() => act.mutate(action)}
                  >
                    {labels[action]}
                  </button>
                ))}
                <button
                  type="button"
                  className={`${styles.mtBtn} ${styles.small}`}
                  disabled={act.isPending}
                  onClick={() => act.mutate("cancel")}
                >
                  取消
                </button>
              </div>
            )}
          </div>
        </section>
      )}
      <section className={`${styles.mtPanel} ${styles.mtGo}`} aria-label="开始剪辑">
        <div className={styles.mtOpts}>
          <button type="button" className={`${styles.mtOpt} ${styles.tune}`} onClick={() => onPage("song")}>
            <span className={styles.mtBadge} aria-hidden="true">
              <Music2 />
            </span>
            <span className={styles.mtOptTx}>
              <small>配乐</small>
              <b>
                {songTitle ?? "还没选歌"}
                {songState === "analyzing" && " · 分析中"}
                {songState === "unusable" && " · 用不了"}
              </b>
            </span>
            <span className={styles.mtOptMore}>{songTitle ? "更改 ›" : "去选 ›"}</span>
          </button>
          <button type="button" className={`${styles.mtOpt} ${styles.cut}`} onClick={() => onPage("ask")}>
            <span className={styles.mtBadge} aria-hidden="true">
              <Scissors />
            </span>
            <span className={styles.mtOptTx}>
              <small>剪辑要求</small>
              <b>{detail.ask.trim() || order}</b>
            </span>
            <span className={styles.mtOptMore}>更改 ›</span>
          </button>
        </div>
        <button
          type="button"
          className={`${styles.mtBtn} ${styles.primary} ${styles.big}`}
          disabled={start.isPending || going || !songReady}
          onClick={() => start.mutate()}
        >
          <Play aria-hidden="true" />
          {detail.versions.length ? "再剪一版" : "开始剪辑"}
        </button>
        {start.error && <p className={styles.mtError}>{errorText(start.error)}</p>}
      </section>
    </>
  );
}

// Films is the prototype's 完成 page: the film saved, played where the photo
// library plays its videos, and every version of it.
function Films({ versions }: { versions: MontageVersion[] }) {
  const [chosen, setChosen] = useState<number>();
  const playable = versions.filter((version) => !version.gone);
  const shown = versions.find((version) => version.number === chosen) ?? playable[0];
  if (!versions.length) return null;
  return (
    <section className={`${styles.mtPanel} ${styles.mtFinish}`} aria-label="成片">
      <div className={styles.mtOk}>
        <span className={styles.mtOkBadge} aria-hidden="true">
          <Check />
        </span>
        <div>
          <h2>成片已存进相册</h2>
          <p>
            {shown
              ? `第 ${shown.number} 版 · ${when.format(new Date(shown.createdAt))} · 在你的个人图库里`
              : "成片都已从相册删除"}
          </p>
        </div>
      </div>
      {shown && (
        <div className={styles.mtFilm}>
          <FilmPlayer key={shown.filmAssetId} assetId={shown.filmAssetId} />
        </div>
      )}
      <div className={styles.mtActs}>
        {shown && (
          <a className={`${styles.mtBtn} ${styles.primary}`} href={originalURL(shown.filmAssetId, true)}>
            <Download aria-hidden="true" />
            下载第 {shown.number} 版
          </a>
        )}
        <div className={styles.mtVersions} role="group" aria-label="版本">
          {versions.map((version) => (
            <button
              key={version.number}
              type="button"
              aria-pressed={version.number === shown?.number}
              disabled={version.gone}
              title={version.gone ? "成片已不在相册" : undefined}
              onClick={() => setChosen(version.number)}
            >
              第 {version.number} 版
            </button>
          ))}
        </div>
      </div>
    </section>
  );
}

// FilmPlayer plays a version where the photo library plays its videos.
function FilmPlayer({ assetId }: { assetId: string }) {
  const stage = useRef<HTMLDivElement>(null);
  const asset = useQuery({ queryKey: ["photos", "asset", assetId], queryFn: () => getPhoto(assetId) });
  if (asset.error) {
    return (
      <div className={styles.mtCentered}>
        <Film aria-hidden="true" />
        {errorText(asset.error)}
      </div>
    );
  }
  if (!asset.data) {
    return (
      <div className={styles.mtCentered}>
        <Spinner small />
      </div>
    );
  }
  return (
    <div ref={stage} style={{ position: "absolute", inset: 0 }}>
      <VideoPlayer
        asset={asset.data}
        stage={stage}
        onDownload={() => window.location.assign(originalURL(asset.data.id, true))}
      />
    </div>
  );
}
