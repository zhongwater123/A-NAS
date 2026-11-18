import { useQuery } from "@tanstack/react-query";
import { Check, ChevronRight, Clapperboard, Plus } from "lucide-react";

import { thumbnailURL } from "../photos/api";
import { Spinner } from "../ui/workspace";
import { listProjects, type MontageProject } from "./api";
import { Failed } from "./Failed";
import { HeroReel } from "./HeroReel";
import styles from "./montage.module.css";
import { Films, Go } from "./ProjectView";
import { projectQuery, songQuery } from "./queries";

export const projectsQuery = {
  queryKey: ["montage", "projects"],
  queryFn: ({ signal }: { signal: AbortSignal }) => listProjects(signal),
};
const day = new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric" });

// The hero's videos: three, from the newest projects first.
function reelOf(projects: MontageProject[]): string[] {
  const reel: string[] = [];
  for (let round = 0; reel.length < 3 && round < 6; round++) {
    for (const project of projects) {
      const id = project.previewAssetIds?.[round];
      if (id && reel.length < 3) reel.push(id);
    }
  }
  return reel;
}

// Home is the prototype's first page, where editing starts: the hero over
// the member's own videos; the projects as the footage step's folders,
// one selected with a click, unselected with another, and opened with its
// arrow; and, for the one selected, the bar that starts the film, the job
// and its 方案. Until the member clicks, the newest is selected.
export function Home({
  selected,
  onSelect,
  onOpen,
  onPage,
  onNew,
}: {
  // undefined: the newest; null: none.
  selected?: string | null;
  onSelect: (id: string | null) => void;
  onOpen: (id: string) => void;
  onPage: (id: string, page: "song" | "ask") => void;
  onNew: () => void;
}) {
  const projects = useQuery(projectsQuery);
  const list = projects.data ?? [];
  const chosen = selected === null ? undefined : (list.find((project) => project.id === selected) ?? list[0]);
  return (
    <main className={styles.mtMain}>
      <section className={styles.mtHero}>
        <HeroReel assetIds={reelOf(list)} />
        <div className={styles.mtHeroCopy}>
          <div className={styles.mtEyebrow}>A-NAS · 剪辑中心</div>
          <h1>
            把一次旅行，
            <br />
            剪成一支<em>跟着音乐走</em>的短片
          </h1>
          <p>选一个项目，点开始剪辑。VNAS 看懂每段画面，按歌曲的段落和小节规划两个方案，你挑一个，成片存回你的相册。</p>
        </div>
      </section>
      <section className={`${styles.mtPanel} ${styles.mtStep} ${styles.media}`} aria-label="剪辑项目">
        <div className={styles.mtStepH}>
          <span className={styles.mtBadge} aria-hidden="true">
            <Clapperboard />
          </span>
          <div>
            <div className={styles.mtStepN}>素材</div>
            <h2>选这次要剪的项目</h2>
          </div>
          {chosen && (
            <small>
              已选 <b>{chosen.name}</b> · {chosen.assetCount} 段
            </small>
          )}
        </div>
        {projects.error && <Failed inline error={projects.error} onRetry={() => void projects.refetch()} />}
        {!projects.data && !projects.error && <Spinner />}
        {projects.data?.length === 0 && <p className={styles.mtFaint}>还没有项目：新建一个，交一批视频开始。</p>}
        <ul className={styles.mtRows}>
          {list.map((project) => {
            const on = project.id === chosen?.id;
            return (
              <li key={project.id} className={`${styles.mtRow} ${on ? styles.on : ""}`}>
                <button
                  type="button"
                  className={styles.mtPick}
                  aria-pressed={on}
                  aria-label={`选中 ${project.name}`}
                  onClick={() => onSelect(on ? null : project.id)}
                >
                  <span className={styles.mtTick} aria-hidden="true">
                    <Check />
                  </span>
                  <span className={styles.mtRowIcon} aria-hidden="true">
                    <Clapperboard />
                  </span>
                  <span className={styles.mtRowName}>
                    <b>{project.name}</b>
                    <small>
                      {project.assetCount} 段视频 · {day.format(new Date(project.createdAt))} 新建
                    </small>
                  </span>
                </button>
                <ul className={styles.mtStrip} aria-hidden="true">
                  {(project.previewAssetIds ?? []).map((id) => (
                    <li key={id}>
                      <img src={thumbnailURL(id)} alt="" loading="lazy" />
                    </li>
                  ))}
                </ul>
                <span className={styles.mtRowEnd}>{on ? "已选" : "未选"}</span>
                <button
                  type="button"
                  className={styles.mtOpen}
                  aria-label={`打开 ${project.name}`}
                  onClick={() => onOpen(project.id)}
                >
                  <ChevronRight aria-hidden="true" />
                </button>
              </li>
            );
          })}
        </ul>
        <button type="button" className={styles.mtAdd} onClick={onNew}>
          <Plus aria-hidden="true" />
          新建项目
        </button>
      </section>
      {chosen ? (
        <Studio key={chosen.id} id={chosen.id} onPage={(page) => onPage(chosen.id, page)} />
      ) : (
        list.length > 0 && (
          <section className={`${styles.mtPanel} ${styles.mtGo}`} aria-label="开始剪辑">
            <p className={styles.mtFaint}>先在上面选一个项目，再开始剪辑。</p>
            <button type="button" className={`${styles.mtBtn} ${styles.primary} ${styles.big}`} disabled>
              开始剪辑
            </button>
          </section>
        )
      )}
    </main>
  );
}

// Studio is the selected project's editing, on the home page: its 方案 once
// made, the job, and the bar that starts the film.
function Studio({ id, onPage }: { id: string; onPage: (page: "song" | "ask") => void }) {
  const project = useQuery(projectQuery(id));
  const song = useQuery(songQuery(id));
  if (project.error) return <Failed inline error={project.error} onRetry={() => void project.refetch()} />;
  if (!project.data) return null;
  return (
    <>
      <Films versions={project.data.versions} />
      <Go detail={project.data} songTitle={song.data?.title} songState={song.data?.state} onPage={onPage} />
    </>
  );
}
