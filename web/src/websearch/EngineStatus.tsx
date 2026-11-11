import { ChevronDown } from "lucide-react";
import { Popover } from "radix-ui";

import { calendarMoment } from "../lib/time";
import type { WebSearchPausedEngine } from "./api";
import { pauseReasons } from "./pauses";
import styles from "./websearch.module.css";

// EngineStatus counts the engines a search has no answer from and opens to
// their names by reason: those that did not answer in time, those their site
// blocked, and those asked too often just before.
export function EngineStatus({
  unresponsive,
  paused,
  busy,
}: {
  unresponsive: string[];
  paused: WebSearchPausedEngine[];
  busy: string[];
}) {
  const count = unresponsive.length + paused.length + busy.length;
  if (count === 0) return null;
  return (
    <Popover.Root>
      <Popover.Trigger className={styles.wsSilentButton}>
        {count} 个搜索来源没有回答
        <ChevronDown aria-hidden />
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          className={styles.wsSilentPanel}
          align="end"
          sideOffset={6}
          collisionPadding={12}
          aria-label="没有回答的引擎"
        >
          <Group title="未响应" names={unresponsive} />
          <Group
            title="被网站拦截，暂停中"
            names={paused.map(
              (site) => `${site.engine}（${pauseReasons[site.reason]}，${calendarMoment(site.until)} 恢复）`,
            )}
          />
          <Group title="搜索太频繁，本次跳过" names={busy} />
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

function Group({ title, names }: { title: string; names: string[] }) {
  if (names.length === 0) return null;
  return (
    <section>
      <h3>
        {title} <span>{names.length}</span>
      </h3>
      <p>{names.join("、")}</p>
    </section>
  );
}
