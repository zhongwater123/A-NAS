import { useState } from "react";

import styles from "../desktop/desktop.module.css";
import { avatarURL } from "./api";
import { displayName, type Person } from "./people";

// PersonAvatar shows an account's avatar, or the first character of the name
// it goes by on a disc whose colour follows the account name. className sets
// the size; the picture is decorative because a name is always shown nearby.
export function PersonAvatar({ who, className }: { who: Person; className: string }) {
  const [failed, setFailed] = useState<string>();
  const version = who.id && who.avatarVersion && failed !== who.avatarVersion ? who.avatarVersion : undefined;
  return (
    <span
      className={`${className} ${styles.personAvatar} ${styles[avatarTone(who.username)] ?? ""} ${version ? styles.pictured : ""}`}
      aria-hidden="true"
    >
      {version ? (
        <img
          src={avatarURL(who.id!, version)}
          alt=""
          draggable={false}
          decoding="async"
          onError={() => setFailed(version)}
        />
      ) : (
        initial(displayName(who))
      )}
    </span>
  );
}

// avatarTone gives a person the same colour wherever their initial appears.
export function avatarTone(username: string): string {
  return `avatar-c${[...username].reduce((sum, character) => sum + character.charCodeAt(0), 0) % 6}`;
}

const graphemes =
  typeof Intl.Segmenter === "function" ? new Intl.Segmenter("zh-CN", { granularity: "grapheme" }) : undefined;

const han = /\p{Script=Han}/u;

// initial is the character a text avatar shows: a Chinese name's last
// character, since the first is usually the family name (陈小禾 → 禾), and
// otherwise the first letter (mom → M). Characters are taken as a reader
// sees them, so an emoji joined from several code points stays whole.
export function initial(name: string): string {
  const trimmed = name.trim();
  const characters = graphemes ? Array.from(graphemes.segment(trimmed), (part) => part.segment) : [...trimmed];
  const last = characters.at(-1);
  if (last && han.test(last)) return last;
  return characters[0]?.toUpperCase() ?? "?";
}
