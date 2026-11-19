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

import { APIError } from "../api/client";
import { errorText } from "../api/errorMessages";
import { getPhoto, originalURL, thumbnailURL } from "../photos/api";
import { clock, durationLabel } from "../photos/model";
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
  type MontageSong,
  type MontageVersion,
  replan,
  retryAnalysis,
  retryJob,
  updateProject,
} from "./api";
import { Failed } from "./Failed";
import styles from "./montage.module.css";
import { jobKey, projectKey, projectQuery, projectsKey, songQuery } from "./queries";
import { useShown } from "./shown";
import {
  actionLabels,
  actionsOf,
  active,
  failureText,
  type JobAction,
  planName,
  stageName,
  stageOf,
  stateText,
  stepLabel,
} from "./words";

// A job in progress is asked about every two seconds, while the window can be seen.
const POLL_MS = 2_000;
const when = new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" });
// The length of a project's videos together; none known yet reads as nothing.
const total = (seconds: number) => (seconds >= 1 ? ` · ${durationLabel(seconds)}` : "");

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
  const song = useQuery(songQuery(id, useShown()));
  const dialogs = useDialogs();
  const remove = useMutation({
    mutationFn: () => deleteProject(id),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: projectsKey });
      onBack();
    },
  });
  const rename = useMutation({
    mutationFn: (name: string) => updateProject(id, { name }),
    onSuccess: (detail) => {
      client.setQueryData(projectKey(id), detail);
      void client.invalidateQueries({ queryKey: projectsKey });
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
            {detail.assetCount} 段视频{total(seconds)} ·{" "}
            {detail.versions.length ? `${detail.versions.length} 版成片` : "还没有成片"}
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
      {(rename.error || remove.error) && (
        <p className={styles.mtError}>{errorText(rename.error ?? remove.error, "montage")}</p>
      )}
      <Videos detail={detail} seconds={seconds} onAdd={onAddVideos} />
      <Films versions={detail.versions} />
      <Go detail={detail} song={song.data} onPage={onPage} />
      {dialogs.dialog}
    </main>
  );
}

function Videos({ detail, seconds, onAdd }: { detail: MontageProjectDetail; seconds: number; onAdd: () => void }) {
  const trashed = detail.assets.filter((asset) => asset.status === "trashed").length;
  const missing = detail.assets.filter((asset) => asset.status === "missing").length;
  return (
    <section className={`${styles.mtPanel} ${styles.mtStep} ${styles.media}`} aria-label="视频">
      <div className={styles.mtStepH}>
        <span className={styles.mtBadge} aria-hidden="true">
          <Video />
        </span>
        <div>
          <h2>视频</h2>
        </div>
        <small>
          <b>{detail.assetCount}</b> 段{total(seconds)}
        </small>
      </div>
      <ul className={styles.mtThumbs}>
        {detail.assets.map((asset) => (
          <li key={asset.assetId} className={styles.mtThumb} data-status={asset.status}>
            <img src={thumbnailURL(asset.assetId)} alt="" loading="lazy" />
            {asset.status === "trashed" && <em>在回收站</em>}
            {asset.status === "missing" && <em>已不在相册</em>}
            {asset.duration !== undefined && <span>{clock(asset.duration)}</span>}
          </li>
        ))}
      </ul>
      {trashed > 0 && <p className={`${styles.mtNote} ${styles.mtHint}`}>{trashed} 个视频在回收站，恢复后才能用</p>}
      {missing > 0 && <p className={`${styles.mtNote} ${styles.mtHint}`}>{missing} 个视频已经不在相册里</p>}
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

// Why a film cannot start yet, for the song's part.
function songHint(song: MontageSong | null | undefined): string | undefined {
  if (song === undefined) return undefined;
  if (song === null) return "先选一首歌";
  if (song.state === "analyzing") return "配乐分析完成后可以开始";
  if (song.state === "unusable") return "这首歌不能用，请换一首";
  return undefined;
}

// Go is the prototype's bar under the footage: the song and the request,
// each opening its own page, and 开始剪辑. The project's latest job shows
// above it as the prototype's processing panel, until its films are saved;
// it comes with the project, so it is there again after a reload, after the
// window was closed, and after a visit to the song's or the request's page.
export function Go({
  detail,
  song,
  onPage,
}: {
  detail: MontageProjectDetail;
  // undefined while the song loads, null when the project has none.
  song: MontageSong | null | undefined;
  onPage: (page: "song" | "ask") => void;
}) {
  const client = useQueryClient();
  const shown = useShown();
  const jobId = detail.job?.id;
  const job = useQuery({
    queryKey: jobKey(jobId),
    queryFn: ({ signal }) => getJob(jobId as string, signal),
    enabled: Boolean(jobId),
    initialData: detail.job,
    refetchInterval: (query) => (shown && active(query.state.data) ? POLL_MS : false),
    refetchIntervalInBackground: false,
  });
  const current = job.data;
  // A film the job saved shows on the project at once: the first 方案 while
  // the second is still planned or rendered, and both when the job ends.
  const saved = current?.versions?.length ?? 0;
  const listed = detail.versions.filter((version) => version.jobId === jobId).length;
  useEffect(() => {
    if (saved > listed) void client.invalidateQueries({ queryKey: projectKey(detail.id) });
  }, [saved, listed, client, detail.id]);
  const start = useMutation({
    mutationFn: () => generate(detail.id),
    onSuccess: (created) => {
      client.setQueryData(jobKey(created.id), created);
      // The project carries its latest job: pages that read it again, as
      // this bar does when it is mounted anew, find the job just started.
      client.setQueryData<MontageProjectDetail>(projectKey(detail.id), (old) => old && { ...old, job: created });
    },
  });
  const act = useMutation({
    mutationFn: (action: JobAction | "cancel") => {
      const id = jobId as string;
      switch (action) {
        case "cancel":
          return cancelJob(id);
        case "drop":
          return dropAssets(id, current?.missingAssetIds ?? []);
        case "retryAnalysis":
          return retryAnalysis(id);
        case "retry":
          return retryJob(id);
        case "replan":
          return replan(id);
      }
    },
    onSuccess: (updated) => client.setQueryData(jobKey(updated.id), updated),
    onError: (error) => {
      // The job moved on meanwhile, as when its film already went to the
      // photo library and it can no longer be cancelled: show where it is.
      if (error instanceof APIError && error.code === "job_state") void job.refetch();
    },
  });
  const going = active(current) || current?.state === "needs_input";
  const panel = current && current.state !== "done" && current.state !== "cancelled";
  const tone = current ? tones[current.state] : undefined;
  const Icon = tone === "stopped" ? AlertTriangle : tone === "waiting" ? Clock : Loader2;
  const actions = current ? actionsOf(current) : [];
  const done = current?.steps.filter((step) => step.state === "done").length ?? 0;
  const order = detail.chronological ? "按拍摄时间" : "由 VNAS 安排顺序";
  const hint = songHint(song);
  // The videos a stopped job names, those the photo library still shows.
  const named =
    current?.state === "needs_input" && current.waitReason !== "asset_missing" ? current.missingAssetIds : [];
  return (
    <>
      {panel && (
        <section className={`${styles.mtPanel} ${styles.mtWork}`} role="status" aria-label="生成进度">
          <div className={styles.mtWorkH} data-tone={tone}>
            <Icon aria-hidden="true" className={tone === "running" ? styles.mtSpin : undefined} />
            <h2>{stateText(current)}</h2>
          </div>
          <div className={styles.mtBar} aria-hidden="true">
            <span style={{ width: `${(done / current.steps.length) * 100}%` }} />
          </div>
          <div className={styles.mtStages}>
            {[0, 1, 2].map((stage) => {
              const own = current.steps.filter((step) => stageOf(step.name) === stage);
              return (
                own.length > 0 && (
                  <section key={stage}>
                    <h3>{stageName(stage)}</h3>
                    <ol aria-label={stageName(stage)}>
                      {own.map((step) => (
                        <li key={step.name} data-state={step.state}>
                          <i aria-hidden="true" />
                          {stepLabel(step.name)}
                        </li>
                      ))}
                    </ol>
                  </section>
                )
              );
            })}
          </div>
          {named && named.length > 0 && (
            <ul className={`${styles.mtThumbs} ${styles.mtHint}`} aria-label="涉及的视频">
              {named.map((assetId) => (
                <li key={assetId} className={styles.mtThumb}>
                  <img src={thumbnailURL(assetId)} alt="" loading="lazy" />
                </li>
              ))}
            </ul>
          )}
          {current.state === "failed" && failureText(current) && (
            <p className={`${styles.mtError} ${styles.mtHint}`}>{failureText(current)}</p>
          )}
          {act.error && <p className={`${styles.mtError} ${styles.mtHint}`}>{errorText(act.error, "montage")}</p>}
          {going && (
            <div className={styles.mtLeave}>
              <span>{active(current) && "可以关掉窗口，任务在设备上继续"}</span>
              <div>
                {actions.map((action) => (
                  <button
                    key={action}
                    type="button"
                    className={`${styles.mtBtn} ${styles.small} ${styles.primary}`}
                    disabled={act.isPending}
                    onClick={() => act.mutate(action)}
                  >
                    {actionLabels[action]}
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
            </div>
          )}
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
                {song?.title ?? "还没选歌"}
                {song?.state === "analyzing" && (song.waiting ? " · 等配乐分析服务" : " · 分析中")}
                {song?.state === "unusable" && " · 不能用"}
              </b>
            </span>
            <span className={styles.mtOptMore}>{song ? "更改 ›" : "去选 ›"}</span>
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
          disabled={start.isPending || going || song?.state !== "ready"}
          onClick={() => start.mutate()}
        >
          <Play aria-hidden="true" />
          {detail.versions.length ? "再剪一版" : "开始剪辑"}
        </button>
        {!going && hint && <p className={styles.mtFaint}>{hint}</p>}
        {start.error && <p className={styles.mtError}>{errorText(start.error, "montage")}</p>}
      </section>
    </>
  );
}

// Films is the prototype's 挑一个 page: the latest job's 方案 side by side,
// each played where the photo library plays its videos, and the films of
// earlier jobs below, each of which plays here too.
export function Films({ versions }: { versions: MontageVersion[] }) {
  const [playing, setPlaying] = useState<number>();
  if (!versions.length) return null;
  const latest = versions[0].jobId;
  const pair = versions.filter((version) => version.jobId === latest).sort((a, b) => a.plan - b.plan);
  const earlier = versions.filter((version) => version.jobId !== latest);
  const old = earlier.find((version) => version.number === playing && !version.gone);
  return (
    <section className={`${styles.mtPanel} ${styles.mtFinish}`} aria-label="成片">
      <div className={styles.mtOk}>
        <span className={styles.mtOkBadge} aria-hidden="true">
          <Check />
        </span>
        <div>
          <h2>成片</h2>
          <p>{pair.length > 1 ? `${pair.length} 个方案，都已存进你的个人图库` : "已存进你的个人图库"}</p>
        </div>
      </div>
      <div className={styles.mtFilms}>
        {pair.map((version) => (
          <FilmCard key={version.number} version={version} />
        ))}
      </div>
      {earlier.length > 0 && (
        <>
          <div className={styles.mtActs}>
            <span className={styles.mtFaint}>更早的版本</span>
            <ul className={styles.mtVersions} aria-label="更早的版本">
              {earlier.map((version) => (
                <li key={version.number}>
                  <b>第 {version.number} 版</b>
                  {version.gone ? (
                    <span>成片已不在相册</span>
                  ) : (
                    <>
                      <button
                        type="button"
                        aria-pressed={playing === version.number}
                        aria-label={`播放第 ${version.number} 版`}
                        onClick={() => setPlaying(playing === version.number ? undefined : version.number)}
                      >
                        <Play aria-hidden="true" />
                        播放
                      </button>
                      <a href={originalURL(version.filmAssetId, true)} aria-label={`下载第 ${version.number} 版`}>
                        <Download aria-hidden="true" />
                        下载
                      </a>
                    </>
                  )}
                </li>
              ))}
            </ul>
          </div>
          {old && (
            <div className={`${styles.mtFilm} ${styles.mtHint}`} role="group" aria-label={`第 ${old.number} 版`}>
              <FilmPlayer key={old.filmAssetId} assetId={old.filmAssetId} />
            </div>
          )}
        </>
      )}
    </section>
  );
}

// FilmCard is one 方案's film, as the prototype's version card.
function FilmCard({ version }: { version: MontageVersion }) {
  const name = planName(version.plan);
  return (
    <article className={styles.mtFilmCard} aria-label={name}>
      <div className={styles.mtFilm}>
        {version.gone ? (
          <div className={styles.mtCentered}>
            <Film aria-hidden="true" />
            成片已不在相册
          </div>
        ) : (
          <FilmPlayer key={version.filmAssetId} assetId={version.filmAssetId} />
        )}
      </div>
      <div className={styles.mtFilmBody}>
        <div className={styles.mtFilmTitle}>
          <h3>{name}</h3>
          <span className={styles.mtTag}>第 {version.number} 版</span>
        </div>
        <p className={styles.mtFaint}>{when.format(new Date(version.createdAt))}</p>
        {!version.gone && (
          <a className={`${styles.mtBtn} ${styles.primary}`} href={originalURL(version.filmAssetId, true)}>
            <Download aria-hidden="true" />
            下载{name}
          </a>
        )}
      </div>
    </article>
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
        {errorText(asset.error, "photos")}
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
        autoPlay={false}
        onDownload={() => window.location.assign(originalURL(asset.data.id, true))}
      />
    </div>
  );
}
