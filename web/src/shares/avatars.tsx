import { PersonAvatar } from "../people/PersonAvatar";
import { displayName, type People, person } from "../people/people";
import styles from "./avatars.module.css";

// Avatar shows a person by id, completed from the shared people directory.
// className sizes it for a place of its own, such as a toast.
export function Avatar({
  people,
  id,
  username,
  size = "md",
  className = "",
}: {
  people: People;
  id: string;
  username?: string;
  size?: "xs" | "sm" | "md" | "lg";
  className?: string;
}) {
  return (
    <PersonAvatar
      who={person(people, { id, username: username ?? id })}
      className={`${styles.shAvatar} ${styles[`sh-avatar-${size}`] ?? ""} ${className}`}
    />
  );
}
export function nameOf(people: People, id: string, username?: string): string {
  return displayName(person(people, { id, username: username ?? people.byId(id)?.username ?? id }));
}

// PeopleStack overlaps the first few faces of a share's recipients.
export function PeopleStack({ people, ids, max = 3 }: { people: People; ids: string[]; max?: number }) {
  const shown = ids.slice(0, ids.length > max ? max - 1 : max);
  return (
    <span className={`${styles.shStack} ${styles[`count-${Math.min(ids.length, max)}`] ?? ""}`} aria-hidden="true">
      {shown.map((id) => (
        <Avatar key={id} people={people} id={id} size="sm" />
      ))}
      {ids.length > max && (
        <span className={`${styles.shAvatar} ${styles.shAvatarSm} ${styles.shStackMore}`}>
          +{ids.length - shown.length}
        </span>
      )}
    </span>
  );
}

export function names(people: People, ids: string[], max = 3): string {
  const list = ids.slice(0, max).map((id) => nameOf(people, id));
  return ids.length > max ? `${list.join("、")} 等 ${ids.length} 人` : list.join("、");
}
