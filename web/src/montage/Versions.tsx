import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, Download, Film, Scissors, Wand2 } from "lucide-react";
import { type FormEvent, type RefObject, useEffect, useRef, useState } from "react";

import { errorText } from "../api/errorMessages";
import { getPhoto, originalURL } from "../photos/api";
import { VideoPlayer } from "../photos/VideoPlayer";
import { Spinner } from "../ui/workspace";
import {
  confirmVersion,
  frameURL,
  getVersionOutline,
  type MontageDraftShot,
  type MontageProjectDetail,
  type MontageVersion,
  type MontageVersionOutline,
  recutVersion,
} from "./api";
import styles from "./montage.module.css";
import { jobKey, outlineKey, projectKey, projectsKey } from "./queries";
import { limitsOf, versionTitle } from "./words";

const when = new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" });
// A sentence to recut by is as long as a member's ask.
const CHANGE_MOST = 200;

// Films is the prototype's 挑一版 page (docs/specs/montage-center.md, 选版页):
// the latest job's films side by side, each with what it is and what can be
// done with it, and the films of earlier jobs below, each opened the same way.
// One film plays at a time.
export function Films({ detail, onEdit }: { detail: MontageProjectDetail; onEdit: (versionId: string) => void }) {
  const [opened, setOpened] = useState<string>();
  const box = useOnePlaying();
  const versions = detail.versions;
  if (!versions.length) return null;
  const latest = versions[0].jobId;
  const pair = versions.filter((version) => version.jobId === latest).sort((a, b) => a.plan - b.plan);
  const earlier = versions.filter((version) => version.jobId !== latest);
  const old = earlier.find((version) => version.id === opened && !version.gone);
  const card = (version: MontageVersion) => (
    <FilmCard
      key={version.id}
      detail={detail}
      version={version}
      chosen={detail.chosenVersionId === version.id}
      onEdit={onEdit}
    />
  );
  return (
    <section className={`${styles.mtPanel} ${styles.mtFinish}`} aria-label="成片" ref={box}>
      <div className={styles.mtOk}>
        <span className={styles.mtOkBadge} aria-hidden="true">
          <Check />
        </span>
        <div>
          <h2>成片</h2>
          <p>{pair.length > 1 ? `${pair.length} 个方案，都已存进你的个人图库` : "已存进你的个人图库"}</p>
        </div>
      </div>
      <div className={styles.mtFilms}>{pair.map(card)}</div>
      {earlier.length > 0 && (
        <>
          <div className={styles.mtActs}>
            <span className={styles.mtFaint}>{detail.chosenVersionId ? "其他版本" : "更早的版本"}</span>
            <ul className={styles.mtVersions} aria-label="更早的版本">
              {earlier.map((version) => (
                <li key={version.id}>
                  <b>第 {version.number} 版</b>
                  <span className={styles.mtFaint}>{versionTitle(version)}</span>
                  {detail.chosenVersionId === version.id && <span className={styles.mtTag}>已确认</span>}
                  {version.gone ? (
                    <span>成片已不在相册</span>
                  ) : (
                    <>
                      <button
                        type="button"
                        aria-pressed={opened === version.id}
                        aria-label={`播放第 ${version.number} 版`}
                        onClick={() => setOpened(opened === version.id ? undefined : version.id)}
                      >
                        <ChevronDown aria-hidden="true" />
                        {opened === version.id ? "收起" : "播放"}
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
            <div className={`${styles.mtFilms} ${styles.mtHint}`} role="group" aria-label={`第 ${old.number} 版`}>
              {card(old)}
            </div>
          )}
        </>
      )}
    </section>
  );
}

// useOnePlaying pauses the other films of a box when one starts to play.
function useOnePlaying(): RefObject<HTMLElement | null> {
  const box = useRef<HTMLElement>(null);
  useEffect(() => {
    const element = box.current;
    if (!element) return;
    const played = (event: Event) => {
      for (const video of element.querySelectorAll("video")) if (video !== event.target) video.pause();
    };
    // play does not bubble: the box hears it on its way down.
    element.addEventListener("play", played, true);
    return () => element.removeEventListener("play", played, true);
  });
  return box;
}

// FilmCard is one version as the prototype's version card: the film, a strip
// of its shots, the plan it was cut by, its numbers, and 确认这一版, 修改镜头
// and a sentence to recut it by.
function FilmCard({
  detail,
  version,
  chosen,
  onEdit,
}: {
  detail: MontageProjectDetail;
  version: MontageVersion;
  chosen: boolean;
  onEdit: (versionId: string) => void;
}) {
  const client = useQueryClient();
  const title = versionTitle(version);
  // The film's own shots and plan: not its draft's, which an edit not saved
  // yet has changed. A film does not change, so they are read once.
  const outline = useQuery({
    queryKey: outlineKey(version.id),
    queryFn: ({ signal }) => getVersionOutline(version.id, signal),
    enabled: version.editable,
    staleTime: Number.POSITIVE_INFINITY,
  });
  const confirm = useMutation({
    mutationFn: () => confirmVersion(version.id),
    onSuccess: (updated) => {
      client.setQueryData(projectKey(detail.id), updated);
      void client.invalidateQueries({ queryKey: projectsKey });
    },
  });
  const [change, setChange] = useState("");
  const recut = useMutation({
    mutationFn: (sentence: string) => recutVersion(version.id, sentence),
    onSuccess: (job) => {
      setChange("");
      // The job shows where the project's jobs show, as generating's does.
      client.setQueryData(jobKey(job.id), job);
      client.setQueryData<MontageProjectDetail>(projectKey(detail.id), (old) => old && { ...old, job });
    },
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (change.trim()) recut.mutate(change.trim());
  };
  const limits = limitsOf(change);
  return (
    <article className={styles.mtFilmCard} aria-label={title} data-chosen={chosen || undefined}>
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
      {outline.data && <Strip projectId={detail.id} shots={outline.data.shots} />}
      <div className={styles.mtFilmBody}>
        <div className={styles.mtFilmTitle}>
          <h3>{title}</h3>
          <span className={styles.mtTag}>第 {version.number} 版</span>
          {chosen && <span className={`${styles.mtTag} ${styles.ok}`}>已确认</span>}
        </div>
        <p className={styles.mtFaint}>{when.format(new Date(version.createdAt))}</p>
        {outline.data && <Plan outline={outline.data} />}
        {!version.editable && <p className={styles.mtFaint}>这一版只能播放，不能修改或重剪：它的镜头记录已经不在了</p>}
        <form className={styles.mtSayIt} onSubmit={submit}>
          <input
            value={change}
            maxLength={CHANGE_MOST}
            disabled={!version.editable}
            placeholder="说一句要改什么，比如：结尾留长一点"
            aria-label={`对第 ${version.number} 版的修改要求`}
            onChange={(event) => setChange(event.target.value)}
          />
          <button
            type="submit"
            className={`${styles.mtBtn} ${styles.small}`}
            disabled={!change.trim() || recut.isPending || version.gone || !version.editable}
          >
            <Wand2 aria-hidden="true" />
            重剪
          </button>
        </form>
        {version.editable && (version.kind === "edit" || change.trim()) && (
          <p className={styles.mtFaint}>
            {version.kind === "edit"
              ? "重剪从这一版所来自的规划出发，手动换过的镜头不会带进去"
              : "重剪从这一版的规划出发，“修改镜头”里的改动不会带进去"}
          </p>
        )}
        {limits.length > 0 && <p className={styles.mtFaint}>{limits.join("；")}</p>}
        {(recut.error || confirm.error) && (
          <p className={styles.mtError}>{errorText(recut.error ?? confirm.error, "montage")}</p>
        )}
        <div className={styles.mtFilmActs}>
          {!version.gone && (
            <a
              className={`${styles.mtBtn} ${styles.small}`}
              href={originalURL(version.filmAssetId, true)}
              aria-label={`下载${title}`}
            >
              <Download aria-hidden="true" />
              下载
            </a>
          )}
          <button
            type="button"
            className={`${styles.mtBtn} ${styles.small}`}
            disabled={!version.editable}
            onClick={() => onEdit(version.id)}
          >
            <Scissors aria-hidden="true" />
            修改镜头
          </button>
          <button
            type="button"
            className={`${styles.mtBtn} ${styles.small} ${styles.primary}`}
            disabled={chosen || confirm.isPending || version.gone}
            onClick={() => confirm.mutate()}
          >
            <Check aria-hidden="true" />
            {chosen ? "已确认" : "确认这一版"}
          </button>
        </div>
      </div>
    </article>
  );
}

// Strip is a version's shots as thumbnails, each as wide as its slots.
export function Strip({ projectId, shots }: { projectId: string; shots: MontageDraftShot[] }) {
  return (
    <div className={styles.mtStrip} aria-hidden="true">
      {shots.map((shot) => (
        <img
          key={shot.number}
          src={frameURL(projectId, shot.assetId, shot.from)}
          alt=""
          loading="lazy"
          style={{ flexGrow: shot.end - shot.start }}
        />
      ))}
    </div>
  );
}

// Plan is the plan a version was cut by: the design, the parts in a row, and
// the whole plan unfolded; then its numbers.
function Plan({ outline }: { outline: MontageVersionOutline }) {
  const [open, setOpen] = useState(false);
  return (
    <div className={styles.mtPlan}>
      {outline.plan.design && <p className={styles.mtDesign}>{outline.plan.design}</p>}
      <p className={styles.mtChain}>
        {outline.parts.map((part) => (
          <span key={part.number}>{part.name || `第 ${part.number} 段`}</span>
        ))}
      </p>
      {outline.unmet.length > 0 && (
        <p className={`${styles.mtNote} ${styles.mtHint}`}>素材里没找到：{outline.unmet.join("；")}</p>
      )}
      <button type="button" className={styles.mtLink} aria-expanded={open} onClick={() => setOpen(!open)}>
        {open ? "收起" : "看完整规划"}
      </button>
      {open && (
        <dl className={styles.mtPlanAll}>
          <dt>开头</dt>
          <dd>{outline.plan.opening}</dd>
          <dt>结尾</dt>
          <dd>{outline.plan.ending}</dd>
          {outline.parts.map((part) => (
            <div key={part.number}>
              <dt>{part.name || `第 ${part.number} 段`}</dt>
              <dd>{part.summary}</dd>
            </div>
          ))}
        </dl>
      )}
      <div className={styles.mtStats}>
        <div>
          <b>{outline.counts.shots}</b>
          <span>个镜头</span>
        </div>
        <div>
          <b>{outline.counts.videos}</b>
          <span>段原片</span>
        </div>
        <div data-warn={outline.counts.portrait > 0 || undefined}>
          <b>{outline.counts.portrait}</b>
          <span>个竖拍</span>
        </div>
      </div>
    </div>
  );
}

// FilmPlayer plays a version where the photo library plays its videos.
export function FilmPlayer({
  assetId,
  start,
  end,
  autoPlay = false,
}: {
  assetId: string;
  start?: number;
  end?: number;
  autoPlay?: boolean;
}) {
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
        autoPlay={autoPlay}
        start={start}
        end={end}
        onDownload={() => window.location.assign(originalURL(asset.data.id, true))}
      />
    </div>
  );
}
