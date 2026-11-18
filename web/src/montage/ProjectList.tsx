import { useQuery } from "@tanstack/react-query";
import { Clapperboard, Plus } from "lucide-react";

import { errorText } from "../api/errorMessages";
import { EmptyState, Notice, Spinner } from "../ui/workspace";
import workspaceStyles from "../ui/workspace.module.css";
import { listProjects } from "./api";
import styles from "./montage.module.css";

export function ProjectList({ onOpen, onNew }: { onOpen: (id: string) => void; onNew: () => void }) {
  const projects = useQuery({ queryKey: ["montage", "projects"], queryFn: listProjects });
  return (
    <section className={styles.mtPage} aria-label="剪辑项目">
      <header className={styles.mtHeader}>
        <h2>剪辑项目</h2>
        <button type="button" className={workspaceStyles.bkPrimary} onClick={onNew}>
          <Plus />
          新建项目
        </button>
      </header>
      {projects.error ? (
        <Notice tone="danger">{errorText(projects.error)}</Notice>
      ) : !projects.data ? (
        <Spinner />
      ) : projects.data.length === 0 ? (
        <EmptyState icon={<Clapperboard />} title="还没有剪辑项目">
          把一次旅行或活动拍的视频交给剪辑中心，选一首歌，就能剪成一条成片。
        </EmptyState>
      ) : (
        <ul className={styles.mtProjects}>
          {projects.data.map((project) => (
            <li key={project.id}>
              <button type="button" className={styles.mtProject} onClick={() => onOpen(project.id)}>
                <strong>{project.name}</strong>
                <small>{project.assetCount} 段视频</small>
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
