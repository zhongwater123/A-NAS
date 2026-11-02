import { SmilePlus } from "lucide-react";
import { AnimatePresence, motion } from "motion/react";
import { Popover } from "radix-ui";
import { useState } from "react";

import { type FamilyReaction, type FamilyReactionName, type FamilyRecord, familyError, react } from "./api";
import { useFamily } from "./context";
import styles from "./family.module.css";
import { useMemberName } from "./parts";

const order: FamilyReactionName[] = ["heart", "like", "laugh", "adore", "clap"];

export const reactionEmoji: Record<FamilyReactionName, string> = {
  heart: "❤️",
  like: "👍",
  laugh: "😂",
  adore: "🥰",
  clap: "👏",
};

// toggled is a record's reactions with the reader's one added or taken
// back, kept in the window's order.
function toggled(reactions: FamilyReaction[], name: FamilyReactionName, me: string, on: boolean) {
  const who = new Map(reactions.map((reaction) => [reaction.name, reaction.accountIds]));
  const others = (who.get(name) ?? []).filter((id) => id !== me);
  who.set(name, on ? [...others, me] : others);
  return order
    .filter((item) => (who.get(item)?.length ?? 0) > 0)
    .map((item) => ({ name: item, accountIds: who.get(item)! }));
}

// useReact toggles the reader's reaction on a record at once and settles on
// what the server answers; a failure puts the record back.
export function useReact(record: FamilyRecord, onChanged: (record: FamilyRecord) => void) {
  const family = useFamily();
  return async (name: FamilyReactionName) => {
    const mine = record.reactions.some(
      (reaction) => reaction.name === name && reaction.accountIds.includes(family.userId),
    );
    onChanged({ ...record, reactions: toggled(record.reactions, name, family.userId, !mine) });
    try {
      onChanged({ ...record, reactions: await react(record.id, name, !mine) });
    } catch (caught) {
      onChanged(record);
      family.toast({ text: familyError(caught), tone: "error" });
    }
  };
}

// Reactions shows the reactions left on a record, each with its count;
// the reader's own are marked, and a tap takes one back or joins in.
export function Reactions({ record, onReact }: { record: FamilyRecord; onReact: (name: FamilyReactionName) => void }) {
  const family = useFamily();
  const nameOf = useMemberName();
  if (record.reactions.length === 0) return null;
  return (
    <div className={styles.fcReactions}>
      <AnimatePresence initial={false} mode="popLayout">
        {record.reactions.map((reaction) => {
          const mine = reaction.accountIds.includes(family.userId);
          const names = reaction.accountIds.map((id) => nameOf(id, "").role || nameOf(id, "").name).join("、");
          return (
            <motion.button
              key={reaction.name}
              layout
              type="button"
              className={`${styles.fcReaction} ${mine ? styles.mine : ""}`}
              aria-pressed={mine}
              aria-label={`${reactionEmoji[reaction.name]} ${reaction.accountIds.length}：${names}`}
              title={names}
              onClick={() => onReact(reaction.name)}
              initial={{ scale: 0.4, opacity: 0 }}
              animate={{ scale: 1, opacity: 1 }}
              exit={{ scale: 0.4, opacity: 0 }}
              whileTap={{ scale: 0.9 }}
              transition={{ type: "spring", stiffness: 520, damping: 28 }}
            >
              <motion.span
                key={reaction.accountIds.length}
                initial={{ scale: 1.6 }}
                animate={{ scale: 1 }}
                transition={{ type: "spring", stiffness: 420, damping: 14 }}
              >
                {reactionEmoji[reaction.name]}
              </motion.span>
              <span className={styles.fcReactionCount}>{reaction.accountIds.length}</span>
            </motion.button>
          );
        })}
      </AnimatePresence>
    </div>
  );
}

// ReactionPicker offers the five reactions from a small popover in a
// record's by-line.
export function ReactionPicker({ onReact }: { onReact: (name: FamilyReactionName) => void }) {
  const [open, setOpen] = useState(false);
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger className={`${styles.fcIconButton} ${styles.fcByIcon}`} aria-label="表情回应">
        <SmilePlus size={16} />
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content className={styles.fcPicker} side="top" align="end" sideOffset={6}>
          {order.map((name, index) => (
            <motion.button
              key={name}
              type="button"
              aria-label={`回应 ${reactionEmoji[name]}`}
              initial={{ y: 8, opacity: 0 }}
              animate={{ y: 0, opacity: 1 }}
              transition={{ delay: index * 0.03, type: "spring", stiffness: 500, damping: 30 }}
              whileHover={{ scale: 1.3, y: -3 }}
              whileTap={{ scale: 0.85 }}
              onClick={() => {
                setOpen(false);
                onReact(name);
              }}
            >
              {reactionEmoji[name]}
            </motion.button>
          ))}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
