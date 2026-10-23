// Which writes a step VNAS takes on the desktop may cause without asking the
// member (user decision 2026-10-21, #160): creating, adding to lists, favorites,
// tags and play records go; anything that deletes, moves, renames, shares,
// copies, posts where the family sees it, or changes settings, accounts,
// storage, apps and containers waits for the member. A write no rule names
// waits too, so a new route is asked about until it is listed here.

interface Rule {
  method: string;
  path: RegExp;
  why?: string;
}

const ID = "[^/]+";
const route = (method: string, path: string, why?: string): Rule => ({
  method,
  path: new RegExp(`^/api/v1/${path}$`),
  why,
});

const DELETES = "这一步会删除内容";
const SHARES = "这一步会把内容分享给其他成员";
const COPIES = "这一步会复制内容，可能复制到其他成员看得到的地方";
const MOVES = "这一步会移动或改名";
const RESTORES = "这一步会恢复内容，可能替换现有的文件";
const FEEDBACK = "问题反馈全家都看得到";
const SYSTEM = "这一步会改动设置、账号或系统";
const ANY = "这一步会改动 NAS 上的内容";

const RULES: Rule[] = [
  // Harmless: new things, list membership, favorites, tags, play records and marks.
  route("POST", `spaces/${ID}/directories`),
  route("POST", `notifications/${ID}/acknowledge`),
  route("POST", `shares/${ID}/(view|read)`),
  route("POST", `shares/${ID}/save`),
  route("POST", "transfers/downloads"),
  route("POST", `transfers/${ID}/pause`),
  route("POST", "photos/image-searches"),
  route("POST", `photos/libraries/${ID}/albums`),
  route("POST", `photos/albums/${ID}/assets`),
  route("POST", `photos/assets/${ID}/tags`),
  route("DELETE", `photos/assets/${ID}/tags/${ID}`),
  route("PUT", `media/videos/${ID}/progress`),
  route("PUT", `media/titles/${ID}/watched`),
  route("(PUT|DELETE)", `media/favorites/${ID}`),
  route("POST", "media/collections"),
  route("POST", `media/collections/${ID}/items`),
  route("POST", `media/libraries/${ID}/scan`),
  route("POST", "music/library/scan"),
  route("(PUT|DELETE)", `music/favorites/${ID}`),
  route("POST", "music/plays"),
  route("POST", "music/playlists"),
  route("POST", `music/playlists/${ID}/tracks`),
  // Asked, with what the member is told.
  route("POST", "shares", SHARES),
  route("POST", `shares/${ID}/(messages|revoke)`, SHARES),
  route("POST", `files/${ID}/copies`, COPIES),
  route("POST", `photos/assets/${ID}/copies`, COPIES),
  route("PATCH", `files/${ID}`, MOVES),
  route("POST", `(trash/${ID}|photos/assets/${ID}|snapshots/${ID}(/entries/${ID})?)/restore`, RESTORES),
  route("[A-Z]+", "feedback(/.*)?", FEEDBACK),
  route(
    "[A-Z]+",
    "(session|users|viewing|storage|volumes|wallpaper|local-display|apps|containers|snapshots)(/.*)?",
    SYSTEM,
  ),
  route("[A-Z]+", `spaces/${ID}/(snapshots|snapshot-schedule)`, SYSTEM),
  route("DELETE", ".*", DELETES),
];

/**
 * Why the member is asked before the write goes, or undefined for a write that
 * goes without asking. VNAS's own window (the assistant API) is never asked
 * about: VNAS cannot operate it, and its writes are the member's own clicks.
 */
export function writeRisk(method: string, path: string): string | undefined {
  if (path.startsWith("/api/v1/assistant/")) return undefined;
  const rule = RULES.find((candidate) => new RegExp(`^${candidate.method}$`).test(method) && candidate.path.test(path));
  return rule ? rule.why : ANY;
}
