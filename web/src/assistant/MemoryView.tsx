// 记忆: what the assistant carries into every new conversation — its notes, what
// it noted about the member, and the skills it learned — from Hermes's own
// learning graph. The member can read each in full and delete it, or clear
// everything; clearing is only complete with the conversations deleted too.
import {
  BookOpen,
  Brain,
  ChevronDown,
  ChevronUp,
  CircleAlert,
  Info,
  LoaderCircle,
  Sparkles,
  Trash2,
  UserRound,
  X,
} from "lucide-react";
import { type ReactNode, useCallback, useEffect, useState } from "react";

import { formatDate } from "../lib/time";
import {
  type AssistantMemory,
  clearMemory,
  failureText,
  forgetMemoryItem,
  getMemory,
  type LearnedSkill,
  type MemoryItem,
  memoryItem,
} from "./api";
import styles from "./assistant.module.css";

export function MemoryView({ onConversationsDeleted }: { onConversationsDeleted(): void }) {
  const [memory, setMemory] = useState<AssistantMemory>();
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const load = useCallback(
    () =>
      getMemory()
        .then((data) => {
          setMemory(data);
          setError("");
        })
        .catch((reason) => setError(failureText(reason))),
    [],
  );
  useEffect(() => {
    void load();
  }, [load]);

  const forget = async (id: string) => {
    try {
      await forgetMemoryItem(id);
      setMemory(
        (current) =>
          current && {
            memories: current.memories.filter((item) => item.id !== id),
            skills: current.skills.filter((skill) => skill.id !== id),
          },
      );
    } catch (reason) {
      setNotice(failureText(reason));
      void load();
    }
  };

  const clear = async (conversations: boolean) => {
    try {
      const cleared = await clearMemory(conversations);
      const parts = [`${cleared.memories} 条记忆`, `${cleared.skills} 个技能`];
      if (conversations) parts.push(`${cleared.conversations} 个对话`);
      setNotice(`已清除 ${parts.join("、")}${cleared.busy ? `；${cleared.busy} 个对话正在回答，没有删除` : ""}`);
      if (conversations) onConversationsDeleted();
    } catch (reason) {
      setNotice(failureText(reason));
    }
    void load();
  };

  if (!memory) {
    return (
      <div className={styles.asMemory}>
        {error ? (
          <div className={styles.asError} role="alert">
            <CircleAlert />
            <span>{error}</span>
          </div>
        ) : (
          <div className={styles.asLoading}>
            <LoaderCircle />
            正在读取…
          </div>
        )}
      </div>
    );
  }
  const notes = memory.memories.filter((item) => item.store === "memory");
  const aboutYou = memory.memories.filter((item) => item.store === "profile");
  const empty = !memory.memories.length && !memory.skills.length;
  return (
    <div className={styles.asMemory}>
      <section className={styles.asMemoryIntro}>
        <span className={styles.asMemoryMark}>
          <Brain />
        </span>
        <div>
          <h3>VNAS 记住的内容</h3>
          <p>每个新对话开始时，VNAS 都会带上这里的记忆和它学会的技能。删除后，新对话不再带入。</p>
        </div>
      </section>
      <p className={styles.asMemoryNote}>
        <Info />
        <span>
          继续以前的对话时，那个对话开始时的记忆仍在其中，VNAS
          也能从以前的对话里查到相关内容。只有连同全部对话一起清除，才算彻底。
        </span>
      </p>
      {notice && (
        <div className={styles.asMemoryNotice} role="status">
          <span>{notice}</span>
          <button type="button" aria-label="关闭" onClick={() => setNotice("")}>
            <X />
          </button>
        </div>
      )}

      {empty ? (
        <div className={styles.asMemoryEmpty}>
          <Brain />
          <strong>VNAS 还没有记住任何内容</strong>
          <span>它记下的笔记、对你的了解和学会的技能会出现在这里。</span>
        </div>
      ) : (
        <>
          <MemoryGroup title="关于你" hint="VNAS 对你的了解" icon={<UserRound />} items={aboutYou} onForget={forget} />
          <MemoryGroup
            title="VNAS 的笔记"
            hint="它为以后的对话记下的事"
            icon={<BookOpen />}
            items={notes}
            onForget={forget}
          />
          {memory.skills.length > 0 && (
            <section className={styles.asMemoryGroup}>
              <header>
                <Sparkles />
                <h4>学会的技能</h4>
                <span className={styles.asMemoryCount}>{memory.skills.length}</span>
                <small>删除的技能移进归档，新对话不再使用</small>
              </header>
              <ul>
                {memory.skills.map((skill) => (
                  <SkillRow key={skill.id} skill={skill} onForget={() => forget(skill.id)} />
                ))}
              </ul>
            </section>
          )}
        </>
      )}
      {!empty && <ClearAll onClear={clear} />}
    </div>
  );
}

function MemoryGroup({
  title,
  hint,
  icon,
  items,
  onForget,
}: {
  title: string;
  hint: string;
  icon: ReactNode;
  items: MemoryItem[];
  onForget(id: string): Promise<void>;
}) {
  if (!items.length) return null;
  return (
    <section className={styles.asMemoryGroup}>
      <header>
        {icon}
        <h4>{title}</h4>
        <span className={styles.asMemoryCount}>{items.length}</span>
        <small>{hint}</small>
      </header>
      <ul>
        {items.map((item) => (
          <MemoryRow key={item.id} item={item} onForget={() => onForget(item.id)} />
        ))}
      </ul>
    </section>
  );
}

function MemoryRow({ item, onForget }: { item: MemoryItem; onForget(): Promise<void> }) {
  const [open, setOpen] = useState(false);
  const long = item.text.length > 160 || item.text.split("\n").length > 3;
  return (
    <li className={styles.asMemoryItem}>
      <p className={open || !long ? "" : styles.clamped}>{item.text}</p>
      <footer>
        {item.updatedAt && <small>{dateText(item.updatedAt)}</small>}
        {long && (
          <button type="button" className={styles.asLink} onClick={() => setOpen((value) => !value)}>
            {open ? (
              <>
                收起
                <ChevronUp />
              </>
            ) : (
              <>
                展开
                <ChevronDown />
              </>
            )}
          </button>
        )}
        <Forget label="删除这条记忆？" onConfirm={onForget} />
      </footer>
    </li>
  );
}

function SkillRow({ skill, onForget }: { skill: LearnedSkill; onForget(): Promise<void> }) {
  const [content, setContent] = useState<string>();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState("");
  const toggle = () => {
    setOpen((value) => !value);
    if (content === undefined)
      memoryItem(skill.id)
        .then((detail) => setContent(detail.content))
        .catch((reason) => setError(failureText(reason)));
  };
  const origin =
    skill.createdBy === "learn"
      ? "按你的要求学会"
      : skill.createdBy === "agent"
        ? "VNAS 自己总结"
        : `用过 ${skill.useCount} 次`;
  return (
    <li className={`${styles.asMemoryItem} skill`}>
      <div className={styles.asSkillLine}>
        <code>{skill.id}</code>
        <span className={styles.asChip}>{origin}</span>
        {skill.category && skill.category !== "general" && (
          <span className={`${styles.asChip} ${styles.quiet}`}>{skill.category}</span>
        )}
      </div>
      {open &&
        (error ? (
          <p className={styles.asMemoryError}>
            <CircleAlert />
            {error}
          </p>
        ) : content === undefined ? (
          <p className={styles.asMemoryLoading}>
            <LoaderCircle />
            正在读取…
          </p>
        ) : (
          <pre>{content}</pre>
        ))}
      <footer>
        {skill.updatedAt && <small>{dateText(skill.updatedAt)}</small>}
        <button type="button" className={styles.asLink} onClick={toggle}>
          {open ? (
            <>
              收起
              <ChevronUp />
            </>
          ) : (
            <>
              查看全文
              <ChevronDown />
            </>
          )}
        </button>
        <Forget label="删除这个技能？" onConfirm={onForget} />
      </footer>
    </li>
  );
}

// A delete that asks once, in place.
function Forget({ label, onConfirm }: { label: string; onConfirm(): Promise<void> }) {
  const [asking, setAsking] = useState(false);
  const [busy, setBusy] = useState(false);
  if (!asking)
    return (
      <button type="button" className={styles.asForget} aria-label="删除" onClick={() => setAsking(true)}>
        <Trash2 />
      </button>
    );
  return (
    <span className={styles.asForgetAsk}>
      <span>{label}</span>
      <button
        type="button"
        className={styles.danger}
        disabled={busy}
        onClick={() => {
          setBusy(true);
          void onConfirm().finally(() => {
            setBusy(false);
            setAsking(false);
          });
        }}
      >
        {busy ? <LoaderCircle /> : "删除"}
      </button>
      <button type="button" disabled={busy} onClick={() => setAsking(false)}>
        取消
      </button>
    </span>
  );
}

function ClearAll({ onClear }: { onClear(conversations: boolean): Promise<void> }) {
  const [asking, setAsking] = useState(false);
  const [conversations, setConversations] = useState(true);
  const [busy, setBusy] = useState(false);
  return (
    <section className={`${styles.asMemoryClear}${asking ? ` ${styles.asking}` : ""}`}>
      {!asking ? (
        <>
          <div>
            <strong>全部清除</strong>
            <span>删除上面所有记忆与技能，可以同时删除全部对话。</span>
          </div>
          <button type="button" onClick={() => setAsking(true)}>
            全部清除…
          </button>
        </>
      ) : (
        <>
          <div>
            <strong>清除 VNAS 记住的全部内容？</strong>
            <label>
              <input
                type="checkbox"
                checked={conversations}
                onChange={(event) => setConversations(event.target.checked)}
              />
              同时删除全部对话（这样才彻底，删除后无法恢复）
            </label>
          </div>
          <span className={styles.asMemoryClearActions}>
            <button type="button" disabled={busy} onClick={() => setAsking(false)}>
              取消
            </button>
            <button
              type="button"
              className={styles.danger}
              disabled={busy}
              onClick={() => {
                setBusy(true);
                void onClear(conversations).finally(() => {
                  setBusy(false);
                  setAsking(false);
                });
              }}
            >
              {busy ? <LoaderCircle /> : "清除"}
            </button>
          </span>
        </>
      )}
    </section>
  );
}

function dateText(seconds: number) {
  return formatDate(seconds * 1000, { year: "numeric", month: "long", day: "numeric" });
}
