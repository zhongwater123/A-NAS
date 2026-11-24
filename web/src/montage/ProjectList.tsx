import { useQuery } from "@tanstack/react-query";
import { Check, ChevronRight, Clapperboard, Music2, Play, Plus, Scissors } from "lucide-react";

import { thumbnailURL } from "../photos/api";
import { Spinner } from "../ui/workspace";
import { Failed } from "./Failed";
import { HeroReel } from "./HeroReel";
import styles from "./montage.module.css";
import { Footage } from "./NewProject";
import { Films, Go } from "./ProjectView";
import { projectQuery, projectsQuery, songQuery } from "./queries";
import { useShown } from "./shown";

const day = new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric" });

// Home is the prototype's first page, where editing starts: the hero, over
// the device's own film when it keeps one; the projects as the footage
// step's folders, one selected with a click, unselected with another, and
// opened with its arrow; and, for the one selected, the bar that starts the
// film, the job and its 方案. Until the member clicks, the newest is selected.
// A member without projects sees the steps the first one goes through
// instead: the footage to make it from, then the song, the request and the
// bar, which wait for the project.
export function Home({
  userId,
  selected,
  onSelect,
  onOpen,
  onPage,
  onNew,
}: {
  userId: string;
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
        <HeroReel />
        <div className={styles.mtHeroCopy}>
          <h1>剪辑中心</h1>
        </div>
      </section>
      {projects.data?.length === 0 ? (
        <FirstRun userId={userId} onDone={onSelect} />
      ) : (
        <>
          <section className={`${styles.mtPanel} ${styles.mtStep} ${styles.media}`} aria-label="剪辑项目">
            <div className={styles.mtStepH}>
              <span className={styles.mtBadge} aria-hidden="true">
                <Clapperboard />
              </span>
              <div>
                <h2>项目</h2>
              </div>
              {chosen && (
                <small>
                  已选 <b>{chosen.name}</b> · {chosen.assetCount} 段
                </small>
              )}
            </div>
            {projects.error && <Failed inline error={projects.error} onRetry={() => void projects.refetch()} />}
            {!projects.data && !projects.error && <Spinner />}
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
                <p className={styles.mtFaint}>先选一个项目</p>
                <button type="button" className={`${styles.mtBtn} ${styles.primary} ${styles.big}`} disabled>
                  开始剪辑
                </button>
              </section>
            )
          )}
        </>
      )}
    </main>
  );
}

// FirstRun is the home page before the first project: the footage step
// makes it, and the song, the request and 开始剪辑 show what comes next,
// waiting for it.
function FirstRun({ userId, onDone }: { userId: string; onDone: (id: string) => void }) {
  return (
    <>
      <Footage userId={userId} step onDone={onDone} />
      <section className={`${styles.mtPanel} ${styles.mtGo}`} aria-label="开始剪辑">
        <div className={styles.mtOpts}>
          <button type="button" className={`${styles.mtOpt} ${styles.tune}`} disabled>
            <span className={styles.mtBadge} aria-hidden="true">
              <Music2 />
            </span>
            <span className={styles.mtOptTx}>
              <small>配乐</small>
              <b>还没选歌</b>
            </span>
          </button>
          <button type="button" className={`${styles.mtOpt} ${styles.cut}`} disabled>
            <span className={styles.mtBadge} aria-hidden="true">
              <Scissors />
            </span>
            <span className={styles.mtOptTx}>
              <small>剪辑要求</small>
              <b>按拍摄时间</b>
            </span>
          </button>
        </div>
        <button type="button" className={`${styles.mtBtn} ${styles.primary} ${styles.big}`} disabled>
          <Play aria-hidden="true" />
          开始剪辑
        </button>
        <p className={styles.mtFaint}>先新建项目</p>
      </section>
    </>
  );
}

// Studio is the selected project's editing, on the home page: its 方案 once
// made, the job, and the bar that starts the film.
function Studio({ id, onPage }: { id: string; onPage: (page: "song" | "ask") => void }) {
  const project = useQuery(projectQuery(id));
  const song = useQuery(songQuery(id, useShown()));
  if (project.error) return <Failed inline error={project.error} onRetry={() => void project.refetch()} />;
  if (!project.data) return null;
  return (
    <>
      <Films versions={project.data.versions} />
      <Go detail={project.data} song={song.data} onPage={onPage} />
    </>
  );
}
