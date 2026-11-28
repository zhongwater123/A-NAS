import { useNav } from "../../app/nav";
import { useWorks } from "../../app/works";
import { messages } from "../../ui/messages";
import { Slot } from "../../ui/Slot";
import styles from "../layout.module.css";

// The start page (M1, M2): the import panel in the middle, the projects
// below. Each feature replaces its slot; the layout stays here.
export function StartPage() {
  return (
    <div className={styles.page}>
      <header className={styles.topBar}>
        <h1>{messages.title}</h1>
      </header>
      <div className={styles.start}>
        <div className={styles.row}>
          <Slot id="M2-1" />
          <Slot id="M2-2" />
        </div>
        <div className={styles.row}>
          <Slot id="M1-1">
            <WorkCards />
          </Slot>
          <Slot id="M1-2" />
          <Slot id="M1-3" />
        </div>
      </div>
    </div>
  );
}

// WorkCards stands in for M1-1's cards until the feature: each opens its project.
function WorkCards() {
  const works = useWorks();
  const nav = useNav();
  if (works.data?.length === 0) return <p>{messages.noWorks}</p>;
  return (
    <div className={styles.cards}>
      {works.data?.map((work) => (
        <button key={work.id} type="button" className={styles.part} onClick={() => nav.openWork(work.id)}>
          {work.name}
        </button>
      ))}
    </div>
  );
}
