import type { Project } from "../../api/client";
import { useGenerate } from "../../app/features";
import { useNav } from "../../app/nav";
import { ActionButton } from "../../ui/ActionButton";
import { messages } from "../../ui/messages";
import { Slot } from "../../ui/Slot";
import styles from "../layout.module.css";

// The prepare page (M3, M4): the files by day on the left, the settings on
// the right; the import's progress and failures under the top bar.
export function PreparePage({ work }: { work: Project }) {
  const nav = useNav();
  const generate = useGenerate(work.id);
  return (
    <div className={styles.page}>
      <header className={styles.topBar}>
        <button type="button" className={styles.back} onClick={nav.toStart}>
          {messages.back}
        </button>
        <h1>{work.name}</h1>
      </header>
      <div className={styles.prepare}>
        <div className={styles.row}>
          <Slot id="M3-1" />
          <Slot id="M3-2" />
        </div>
        <div className={styles.prepareBody}>
          <div className={styles.column}>
            <Slot id="M3-3" />
            <Slot id="M3-4" />
          </div>
          <div className={styles.column}>
            <Slot id="M4-1" />
            <Slot id="M4-2" />
            <Slot id="M4-3" />
            <Slot id="M4-4" />
            <Slot id="M4-5">
              <ActionButton onClick={() => void generate.start()} disabled={generate.starting}>
                {messages.startGenerating}
              </ActionButton>
            </Slot>
          </div>
        </div>
      </div>
    </div>
  );
}
