import { Clapperboard } from "lucide-react";
import { useEffect, useState } from "react";

import { errorText } from "../api/errorMessages";
import { EmptyState, Notice, Spinner } from "../ui/workspace";
import workspaceStyles from "../ui/workspace.module.css";
import { listProjects, type MontageProject } from "./api";
import styles from "./montage.module.css";

// MontagePanel is the 剪辑中心 window: the member's projects. Creating them,
// choosing a song and making a film come with the later steps of the plan
// (docs/architecture/montage-center.md, 交付).
export function MontagePanel() {
  const [projects, setProjects] = useState<MontageProject[]>();
  const [failure, setFailure] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    listProjects(controller.signal)
      .then(setProjects)
      .catch((error: unknown) => {
        if (!controller.signal.aborted) setFailure(errorText(error));
      });
    return () => controller.abort();
  }, []);
  return (
    <div className={`${workspaceStyles.bkApp} ${styles.mtApp}`}>
      {failure ? (
        <Notice tone="danger">{failure}</Notice>
      ) : projects === undefined ? (
        <Spinner />
      ) : projects.length === 0 ? (
        <EmptyState icon={<Clapperboard />} title="还没有剪辑项目" />
      ) : (
        <ul className={styles.mtProjects}>
          {projects.map((project) => (
            <li key={project.id}>{project.name}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
