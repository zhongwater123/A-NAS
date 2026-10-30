// 家庭中心 tells a member once, on the desktop, what needs them: a baby's
// vaccine doses coming due and, for guardians, someone asking to join. The
// server remembers what each member was told, so a hint shows once across
// devices. Accounts outside the family are told nothing.
import { ChevronRight, HeartHandshake, X } from "lucide-react";
import { useEffect, useState } from "react";

import { nameOf, usePeople } from "../people/people";
import { type FamilyHint, listHints, markHintsSeen } from "./api";
import styles from "./notices.module.css";

const HIDE_MS = 15_000;

export function FamilyNotices({ onOpen }: { onOpen: () => void }) {
  const [hints, setHints] = useState<FamilyHint[]>([]);
  const [held, setHeld] = useState(false);
  const people = usePeople();

  useEffect(() => {
    let live = true;
    listHints().then(
      (items) => {
        if (live) setHints(items);
      },
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, []);

  const dismiss = () => {
    const keys = hints.map((hint) => hint.key);
    setHints([]);
    if (keys.length) void markHintsSeen(keys).catch(() => undefined);
  };

  useEffect(() => {
    if (!hints.length || held) return;
    const timer = window.setTimeout(dismiss, HIDE_MS);
    return () => window.clearTimeout(timer);
  });

  if (!hints.length) return null;
  const lines = summarize(hints, (username) => nameOf(people, username));
  return (
    // Pointing at the hint or focusing it keeps it up.
    <div
      className={styles.fnHolder}
      data-vnas-overlay=""
      onPointerEnter={() => setHeld(true)}
      onPointerLeave={() => setHeld(false)}
      onFocus={() => setHeld(true)}
      onBlur={() => setHeld(false)}
    >
      <section className={styles.fnToast} role="status" aria-label="家庭中心提醒">
        <span className={styles.fnMark} aria-hidden="true">
          <HeartHandshake />
        </span>
        <div className={styles.fnCopy}>
          <small>家庭中心</small>
          {lines.map((line) => (
            <strong key={line}>{line}</strong>
          ))}
          <button
            type="button"
            className={styles.fnOpen}
            onClick={() => {
              dismiss();
              onOpen();
            }}
          >
            去看看
            <ChevronRight />
          </button>
        </div>
        <button type="button" className={styles.fnClose} aria-label="关闭" onClick={dismiss}>
          <X />
        </button>
      </section>
    </div>
  );
}

// summarize turns the hints into a line per baby and one for join requests,
// naming requesters as the desktop does.
export function summarize(hints: FamilyHint[], name: (username: string) => string = (username) => username): string[] {
  const lines: string[] = [];
  const byBaby = new Map<string, FamilyHint[]>();
  for (const hint of hints) {
    if (hint.kind !== "vaccine" || !hint.babyId) continue;
    byBaby.set(hint.babyId, [...(byBaby.get(hint.babyId) ?? []), hint]);
  }
  for (const doses of byBaby.values()) {
    const first = doses[0];
    lines.push(
      doses.length === 1
        ? `${first.babyName}的${first.vaccine}第 ${first.dose} 剂快到接种时间了`
        : `${first.babyName}有 ${doses.length} 剂疫苗可以预约接种`,
    );
  }
  const requests = hints.filter((hint) => hint.kind === "join_request");
  if (requests.length === 1) lines.push(`${name(requests[0].username ?? "")}想以“${requests[0].role}”的身份加入家庭`);
  else if (requests.length > 1) lines.push(`${requests.length} 位家人申请加入家庭`);
  return lines;
}
