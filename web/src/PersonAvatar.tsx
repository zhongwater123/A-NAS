import { useState } from "react";

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
      className={`${className} person-avatar ${avatarTone(who.username)} ${version ? "pictured" : ""}`}
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

// initial is the first character as a reader sees it, so an emoji joined
// from several code points stays whole.
export function initial(name: string): string {
  const first = graphemes ? graphemes.segment(name)[Symbol.iterator]().next().value?.segment : [...name][0];
  return first?.toUpperCase() ?? "?";
}
