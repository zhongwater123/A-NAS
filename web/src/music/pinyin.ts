import type { PinyinMatch } from "./library";

// loadPinyin loads pinyin-pro on first search and returns a matcher for full
// pinyin ("zhoujielun", "zhoujie") and initials ("zjl"). Each text's pinyin is
// worked out once, since titles, artists and albums repeat across searches.
export async function loadPinyin(): Promise<PinyinMatch> {
  const { pinyin } = await import("pinyin-pro");
  const cache = new Map<string, { full: string; initials: string }>();
  return (text, query) => {
    let entry = cache.get(text);
    if (!entry) {
      const syllables = (pinyin(text, { toneType: "none", type: "array", v: true }) as string[])
        .map((syllable) => syllable.toLocaleLowerCase("zh-CN").replace(/\s+/g, ""))
        .filter(Boolean);
      entry = { full: syllables.join(""), initials: syllables.map((syllable) => syllable[0]).join("") };
      cache.set(text, entry);
    }
    return entry.initials.includes(query) || entry.full.includes(query);
  };
}
