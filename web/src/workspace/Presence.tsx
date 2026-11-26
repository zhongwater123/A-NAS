import { Bot } from "lucide-react";

import { relativeTime } from "../lib/time";
import { PersonAvatar } from "../people/PersonAvatar";
import { person, usePeople } from "../people/people";
import type { WorkspacePresence } from "./api";
import { agentText } from "./words";
import styles from "./workspace.module.css";

// Presence shows whose agents are at work on a page and who else has it
// open, as faces (docs/specs/shared-workspace.md, “谁在看、谁的 agent 在改”):
// in a page's tab beside its versions, and on its row in the window
// (compact). The member's own faces are left out; their agents are not,
// since another of their computers may be at work.
export function Presence({
  presence,
  userId,
  latest,
  compact = false,
}: {
  presence: WorkspacePresence;
  userId: string;
  latest: number;
  compact?: boolean;
}) {
  const people = usePeople();
  const others = presence.viewers.filter((viewer) => viewer.member.id !== userId);
  const [work, ...more] = presence.agents;
  if (!others.length && !work) return null;
  const names = others
    .map((viewer) => (viewer.version === latest ? viewer.member.name : `${viewer.member.name}（v${viewer.version}）`))
    .join("、");
  const faces = others.slice(0, compact ? 3 : 4);
  return (
    <span className={`${styles.wsPresence} ${compact ? styles.compact : ""}`}>
      {work && (
        <span className={styles.wsAgent} title={presence.agents.map(agentText).join("\n")}>
          <Bot className={styles.wsAgentIcon} aria-hidden="true" />
          <span className={styles.wsAgentText}>
            {agentText(work)}
            {!compact && <span className={styles.wsAgentWhen}> · {relativeTime(work.at)}</span>}
          </span>
          {more.length > 0 && <span className={styles.wsAgentMore}>+{more.length}</span>}
        </span>
      )}
      {others.length > 0 && (
        <span className={styles.wsFaces} role="img" aria-label={`正在看：${names}`} title={`正在看：${names}`}>
          {faces.map((viewer) => (
            <PersonAvatar
              key={viewer.member.id}
              who={person(people, { id: viewer.member.id, username: viewer.member.name })}
              className={styles.wsFace}
            />
          ))}
          {others.length > faces.length && (
            <span className={`${styles.wsFace} ${styles.wsFaceMore}`}>+{others.length - faces.length}</span>
          )}
        </span>
      )}
    </span>
  );
}
