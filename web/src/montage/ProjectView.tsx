import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Pencil, Play, Plus, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { APIError } from "../api/client";
import { errorText } from "../api/errorMessages";
import { listTracks } from "../music/api";
import { getPhoto, originalURL } from "../photos/api";
import { VideoPlayer } from "../photos/VideoPlayer";
import { useDialogs } from "../ui/feedback";
import { Notice, Spinner } from "../ui/workspace";
import workspaceStyles from "../ui/workspace.module.css";
import {
  cancelJob,
  chooseSong,
  deleteProject,
  dropAssets,
  generate,
  getJob,
  getProject,
  getSong,
  type MontageJob,
  type MontageProjectDetail,
  type MontageVersion,
  replan,
  retryAnalysis,
  retryJob,
  songAudioURL,
  updateProject,
} from "./api";
import styles from "./montage.module.css";
import { active, limitsOf, needs, stateText, stepNames } from "./words";

// A job in progress is asked about every two seconds, while the window can be seen.
const POLL_MS = 2_000;
const MAX_ASK = 200;

export function ProjectView({ id, onBack, onAddVideos }: { id: string; onBack: () => void; onAddVideos: () => void }) {
  const client = useQueryClient();
  const project = useQuery({ queryKey: ["montage", "project", id], queryFn: () => getProject(id) });
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
    onSuccess: (detail) => client.setQueryData(["montage", "project", id], detail),
  });

  if (project.error) return <Notice tone="danger">{errorText(project.error)}</Notice>;
  if (!project.data) return <Spinner />;
  const detail = project.data;
  return (
    <section className={styles.mtPage} aria-label={detail.name}>
      <header className={styles.mtHeader}>
        <button type="button" className={styles.mtBack} onClick={onBack} aria-label="返回">
          <ArrowLeft />
        </button>
        <h2>{detail.name}</h2>
        <button
          type="button"
          className={styles.mtIcon}
          aria-label="改名"
          onClick={async () => {
            const name = await dialogs.prompt({ title: "项目改名", label: "名称", value: detail.name, maxLength: 64 });
            if (name?.trim()) rename.mutate(name.trim());
          }}
        >
          <Pencil />
        </button>
        <button type="button" className={styles.mtIcon} aria-label="追加视频" onClick={onAddVideos}>
          <Plus />
        </button>
        <button
          type="button"
          className={styles.mtIcon}
          aria-label="删除项目"
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
          <Trash2 />
        </button>
      </header>
      {(rename.error || remove.error) && <Notice tone="danger">{errorText(rename.error ?? remove.error)}</Notice>}
      <Videos detail={detail} />
      <Song projectId={id} />
      <Request detail={detail} />
      <JobCard projectId={id} latest={detail.job} />
      <Versions versions={detail.versions} />
      {dialogs.dialog}
    </section>
  );
}

function Videos({ detail }: { detail: MontageProjectDetail }) {
  const minutes = detail.assets.reduce((sum, asset) => sum + (asset.duration ?? 0), 0) / 60;
  const trashed = detail.assets.filter((asset) => asset.status === "trashed").length;
  const missing = detail.assets.filter((asset) => asset.status === "missing").length;
  return (
    <div className={styles.mtCard}>
      <h3>素材</h3>
      <p>
        {detail.assetCount} 段视频，共 {minutes.toFixed(1)} 分钟
      </p>
      {trashed > 0 && <Notice tone="warning">有 {trashed} 个视频在回收站，恢复后才能用</Notice>}
      {missing > 0 && <Notice tone="warning">有 {missing} 个视频已经不在相册里了</Notice>}
    </div>
  );
}

function Song({ projectId }: { projectId: string }) {
  const client = useQueryClient();
  const song = useQuery({
    queryKey: ["montage", "song", projectId],
    queryFn: async () => {
      try {
        return await getSong(projectId);
      } catch (error) {
        if (error instanceof APIError && error.code === "no_song") return null;
        throw error;
      }
    },
    // A song being analysed is asked about again until it is done.
    refetchInterval: (query) => (query.state.data?.state === "analyzing" ? POLL_MS * 2 : false),
    refetchIntervalInBackground: false,
  });
  const [choosing, setChoosing] = useState(false);
  const tracks = useQuery({ queryKey: ["music", "tracks"], queryFn: listTracks, enabled: choosing });
  const choose = useMutation({
    mutationFn: (trackId: string) => chooseSong(projectId, trackId),
    onSuccess: (chosen) => {
      setChoosing(false);
      client.setQueryData(["montage", "song", projectId], chosen);
    },
  });
  const [start, setStart] = useState("");
  const setStartAt = useMutation({
    mutationFn: (at: number) => updateProject(projectId, { songStart: at }),
    onSuccess: () => client.invalidateQueries({ queryKey: ["montage", "song", projectId] }),
  });
  const current = song.data;
  const excerpt = current?.selection?.excerpt;
  return (
    <div className={styles.mtCard}>
      <h3>配乐</h3>
      {song.error && <Notice tone="danger">{errorText(song.error)}</Notice>}
      {current === null && !choosing && <p>还没有选歌：从音乐库里选一首。</p>}
      {current && (
        <>
          <p>
            <strong>{current.title}</strong>
            {current.state === "analyzing" && "　正在分析节拍与段落…"}
            {current.state === "unusable" && "　这首歌用不了，换一首吧"}
          </p>
          {excerpt && (
            <>
              <p>
                选段 {excerpt[0].toFixed(1)}–{excerpt[1].toFixed(1)} 秒
                {current.selection?.chorus === false && "：没找到副歌，请试听确认"}
              </p>
              <audio
                controls
                preload="none"
                src={songAudioURL(projectId, current.transcode ? excerpt[0] : undefined)}
              />
              <form
                className={styles.mtInline}
                onSubmit={(event) => {
                  event.preventDefault();
                  const at = Number(start);
                  if (Number.isFinite(at) && at >= 0) setStartAt.mutate(at);
                }}
              >
                <label>
                  起点（秒）
                  <input
                    type="number"
                    min={0}
                    step={0.1}
                    value={start}
                    onChange={(event) => setStart(event.target.value)}
                  />
                </label>
                <button
                  type="submit"
                  className={workspaceStyles.bkSecondary}
                  disabled={setStartAt.isPending || start === ""}
                >
                  改起点
                </button>
              </form>
              {setStartAt.error && <Notice tone="danger">{errorText(setStartAt.error)}</Notice>}
            </>
          )}
        </>
      )}
      {choosing ? (
        tracks.error ? (
          <Notice tone="danger">{errorText(tracks.error)}</Notice>
        ) : !tracks.data ? (
          <Spinner small />
        ) : (
          <ul className={styles.mtTracks} aria-label="音乐库">
            {tracks.data.items.map((track) => (
              <li key={track.id}>
                <button type="button" disabled={choose.isPending} onClick={() => choose.mutate(track.id)}>
                  {track.title}
                  {track.artist ? ` · ${track.artist}` : ""}
                </button>
              </li>
            ))}
          </ul>
        )
      ) : (
        <button type="button" className={workspaceStyles.bkSecondary} onClick={() => setChoosing(true)}>
          {current ? "换一首" : "选歌"}
        </button>
      )}
      {choose.error && <Notice tone="danger">{errorText(choose.error)}</Notice>}
    </div>
  );
}

function Request({ detail }: { detail: MontageProjectDetail }) {
  const client = useQueryClient();
  const [ask, setAsk] = useState(detail.ask);
  const save = useMutation({
    mutationFn: (change: { ask?: string; chronological?: boolean }) => updateProject(detail.id, change),
    onSuccess: (updated) => client.setQueryData(["montage", "project", detail.id], updated),
  });
  const limits = limitsOf(ask);
  return (
    <div className={styles.mtCard}>
      <h3>一句话要求</h3>
      <textarea
        aria-label="一句话要求"
        maxLength={MAX_ASK}
        value={ask}
        placeholder="例如：多放海边和日落，结尾安静一点"
        onChange={(event) => setAsk(event.target.value)}
        onBlur={() => ask !== detail.ask && save.mutate({ ask })}
      />
      {limits.length > 0 && (
        <p className={styles.mtHint} role="note">
          首版的限制：{limits.join("；")}。可以照样开始。
        </p>
      )}
      <label className={styles.mtCheck}>
        <input
          type="checkbox"
          checked={detail.chronological}
          onChange={(event) => save.mutate({ chronological: event.target.checked })}
        />
        按拍摄时间排列
      </label>
      {save.error && <Notice tone="danger">{errorText(save.error)}</Notice>}
    </div>
  );
}

function JobCard({ projectId, latest }: { projectId: string; latest?: MontageJob }) {
  const client = useQueryClient();
  // The job this page started, or else the project's latest.
  const [started, setStarted] = useState<string>();
  const jobId = started ?? latest?.id;
  const job = useQuery({
    queryKey: ["montage", "job", jobId],
    queryFn: () => getJob(jobId as string),
    enabled: Boolean(jobId),
    initialData: latest && latest.id === jobId ? latest : undefined,
    // Asked again every two seconds while it goes on; not while the window is hidden.
    refetchInterval: (query) => (active(query.state.data) ? POLL_MS : false),
    refetchIntervalInBackground: false,
  });
  const ended = useRef(false);
  useEffect(() => {
    // A job that saved a film shows its version on the project.
    if (job.data?.state === "done" && !ended.current) {
      ended.current = true;
      void client.invalidateQueries({ queryKey: ["montage", "project", projectId] });
    }
  }, [job.data?.state, client, projectId]);
  const start = useMutation({
    mutationFn: () => generate(projectId),
    onSuccess: (started) => {
      ended.current = false;
      client.setQueryData(["montage", "job", started.id], started);
      setStarted(started.id);
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
  const way = current?.state === "needs_input" ? needs[current.waitReason ?? ""] : undefined;
  const labels = { drop: "去掉这些继续", retryAnalysis: "重试分析", retry: "重试", replan: "重新规划" } as const;
  const done = current?.steps.filter((step) => step.state === "done").length ?? 0;
  return (
    <div className={styles.mtCard}>
      <h3>生成</h3>
      {current && (
        <div className={styles.mtJob} role="status" aria-label="生成进度">
          <p>
            <strong>{stateText(current)}</strong>
            {active(current) && `（${done} / ${current.steps.length} 步）`}
          </p>
          <ol className={styles.mtSteps}>
            {current.steps.map((step) => (
              <li key={step.name} data-state={step.state}>
                {stepNames[step.name] ?? step.name}
              </li>
            ))}
          </ol>
          {current.state === "failed" && current.error && <Notice tone="danger">{current.error}</Notice>}
        </div>
      )}
      {(start.error || act.error) && <Notice tone="danger">{errorText(start.error ?? act.error)}</Notice>}
      <div className={styles.mtActions}>
        {active(current) || current?.state === "needs_input" ? (
          <>
            {way?.actions.map((action) => (
              <button
                key={action}
                type="button"
                className={workspaceStyles.bkPrimary}
                disabled={act.isPending}
                onClick={() => act.mutate(action)}
              >
                {labels[action]}
              </button>
            ))}
            <button
              type="button"
              className={workspaceStyles.bkSecondary}
              disabled={act.isPending}
              onClick={() => act.mutate("cancel")}
            >
              取消
            </button>
          </>
        ) : (
          <button
            type="button"
            className={workspaceStyles.bkPrimary}
            disabled={start.isPending}
            onClick={() => start.mutate()}
          >
            <Play />
            {current?.state === "done" ? "再生成一版" : "开始"}
          </button>
        )}
      </div>
    </div>
  );
}

function Versions({ versions }: { versions: MontageVersion[] }) {
  const [playing, setPlaying] = useState<number>();
  if (!versions.length) return null;
  return (
    <div className={styles.mtCard}>
      <h3>成片</h3>
      <ul className={styles.mtVersions}>
        {versions.map((version) => (
          <li key={version.number}>
            <span>
              第 {version.number} 版 ·{" "}
              {new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" }).format(
                new Date(version.createdAt),
              )}
            </span>
            {version.gone ? (
              <small>成片已不在相册</small>
            ) : (
              <>
                <button type="button" className={styles.mtLink} onClick={() => setPlaying(version.number)}>
                  播放
                </button>
                <a className={styles.mtLink} href={originalURL(version.filmAssetId, true)}>
                  下载
                </a>
              </>
            )}
          </li>
        ))}
      </ul>
      {playing !== undefined && <Film version={versions.find((version) => version.number === playing)} />}
    </div>
  );
}

// Film plays a version where the photo library plays its videos.
function Film({ version }: { version?: MontageVersion }) {
  const stage = useRef<HTMLDivElement>(null);
  const asset = useQuery({
    queryKey: ["photos", "asset", version?.filmAssetId],
    queryFn: () => getPhoto(version?.filmAssetId as string),
    enabled: Boolean(version && !version.gone),
  });
  if (!version) return null;
  if (asset.error) return <Notice tone="danger">{errorText(asset.error)}</Notice>;
  if (!asset.data) return <Spinner small />;
  return (
    <div ref={stage} className={styles.mtFilm}>
      <VideoPlayer
        asset={asset.data}
        stage={stage}
        onDownload={() => window.location.assign(originalURL(asset.data.id, true))}
      />
    </div>
  );
}
