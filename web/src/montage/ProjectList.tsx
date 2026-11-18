import { useQuery } from "@tanstack/react-query";
import { ChevronRight, Clapperboard, Plus } from "lucide-react";

import { Spinner } from "../ui/workspace";
import { listProjects } from "./api";
import { Failed } from "./Failed";
import styles from "./montage.module.css";

export const projectsQuery = {
  queryKey: ["montage", "projects"],
  queryFn: ({ signal }: { signal: AbortSignal }) => listProjects(signal),
};
const day = new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric" });

// Home is the prototype's first page: the hero, then the member's projects
// in the footage step's panel, newest first, and a new one below them.
export function Home({ onOpen, onNew }: { onOpen: (id: string) => void; onNew: () => void }) {
  const projects = useQuery(projectsQuery);
  return (
    <main className={styles.mtMain}>
      <section className={styles.mtHero}>
        <div className={styles.mtHeroCopy}>
          <div className={styles.mtEyebrow}>A-NAS · 剪辑中心</div>
          <h1>
            把一次旅行，
            <br />
            剪成一支<em>跟着音乐走</em>的短片
          </h1>
          <p>
            交给剪辑中心一批视频，选一首歌、说一句要求。VNAS
            看懂每段画面，按歌曲的段落和小节规划剪法，成片存回你的相册。
          </p>
          <ol className={styles.mtHow}>
            <li>1 · 交视频</li>
            <li>2 · 选歌</li>
            <li>3 · 写要求</li>
            <li>4 · 看成片</li>
          </ol>
        </div>
      </section>
      <section className={`${styles.mtPanel} ${styles.mtStep} ${styles.media}`} aria-label="剪辑项目">
        <div className={styles.mtStepH}>
          <span className={styles.mtBadge} aria-hidden="true">
            <Clapperboard />
          </span>
          <div>
            <div className={styles.mtStepN}>项目</div>
            <h2>我的剪辑</h2>
          </div>
          {projects.data && (
            <small>
              共 <b>{projects.data.length}</b> 个项目
            </small>
          )}
        </div>
        {projects.error && <Failed inline error={projects.error} onRetry={() => void projects.refetch()} />}
        {!projects.data && !projects.error && <Spinner />}
        {projects.data?.length === 0 && <p className={styles.mtFaint}>还没有项目：新建一个，交一批视频开始。</p>}
        <ul className={styles.mtRows}>
          {projects.data?.map((project) => (
            <li key={project.id}>
              <button type="button" className={styles.mtRow} onClick={() => onOpen(project.id)}>
                <span className={styles.mtRowIcon} aria-hidden="true">
                  <Clapperboard />
                </span>
                <span className={styles.mtRowName}>
                  <b>{project.name}</b>
                  <small>{day.format(new Date(project.createdAt))} 新建</small>
                </span>
                <span className={styles.mtRowEnd}>{project.assetCount} 段视频</span>
                <ChevronRight className={styles.mtChev} aria-hidden="true" />
              </button>
            </li>
          ))}
        </ul>
        <button type="button" className={styles.mtAdd} onClick={onNew}>
          <Plus aria-hidden="true" />
          新建项目
        </button>
      </section>
    </main>
  );
}
