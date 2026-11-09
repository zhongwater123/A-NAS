import { describe, expect, it } from "vitest";

import { writeRisk } from "./webosWrites";

// What the member is told, as webosWrites.ts words it; undefined: the write goes without asking.
const GOES = undefined;
const DELETES = "这一步会删除内容";
const SHARES = "这一步会把内容分享给其他成员";
const COPIES = "这一步会复制内容，可能复制到其他成员看得到的地方";
const MOVES = "这一步会移动或改名";
const RESTORES = "这一步会恢复内容，可能替换现有的文件";
const FEEDBACK = "问题反馈全家都看得到";
const SYSTEM = "这一步会改动设置、账号或系统";
const ANY = "这一步会改动 NAS 上的内容";

type Write = [method: string, path: string, why: string | undefined];

// One row per rule of the table in webosWrites.ts, in its order (#160): the
// writes the rule answers, then writes that look like them but must get
// another answer. A rule widened to save the member a question, such as
// every DELETE under photos/assets/ or every POST under a space, lets one of
// the look-alikes go.
const RULES: Array<[rule: string, answered: Write[], lookAlikes: Write[]]> = [
  [
    "POST spaces/{id}/directories",
    [["POST", "/api/v1/spaces/s1/directories", GOES]],
    [
      ["POST", "/api/v1/spaces/s1/uploads", ANY],
      ["PUT", "/api/v1/spaces/s1/directories", ANY],
    ],
  ],
  [
    "POST notifications/{id}/acknowledge",
    [["POST", "/api/v1/notifications/n1/acknowledge", GOES]],
    [["DELETE", "/api/v1/notifications/n1/acknowledge", DELETES]],
  ],
  [
    "POST shares/{id}/(view|read)",
    [
      ["POST", "/api/v1/shares/x/view", GOES],
      ["POST", "/api/v1/shares/x/read", GOES],
    ],
    [["POST", "/api/v1/shares/x/revoke", SHARES]],
  ],
  ["POST shares/{id}/save", [["POST", "/api/v1/shares/x/save", GOES]], [["POST", "/api/v1/shares/x/messages", SHARES]]],
  [
    "POST transfers/downloads",
    [["POST", "/api/v1/transfers/downloads", GOES]],
    [["DELETE", "/api/v1/transfers/downloads", DELETES]],
  ],
  [
    "POST transfers/{id}/pause",
    [["POST", "/api/v1/transfers/t1/pause", GOES]],
    [["POST", "/api/v1/transfers/t1/cancel", ANY]],
  ],
  [
    "POST photos/image-searches",
    [["POST", "/api/v1/photos/image-searches", GOES]],
    [["POST", "/api/v1/photos/libraries/l1/uploads", ANY]],
  ],
  [
    "POST photos/libraries/{id}/albums",
    [["POST", "/api/v1/photos/libraries/l1/albums", GOES]],
    [["POST", "/api/v1/photos/libraries/l1/directories", ANY]],
  ],
  [
    "POST photos/albums/{id}/assets",
    [["POST", "/api/v1/photos/albums/a1/assets", GOES]],
    [["DELETE", "/api/v1/photos/albums/a1/assets/p1", DELETES]],
  ],
  [
    "POST photos/assets/{id}/tags",
    [["POST", "/api/v1/photos/assets/p1/tags", GOES]],
    [["POST", "/api/v1/photos/assets/p1/copies", COPIES]],
  ],
  [
    "DELETE photos/assets/{id}/tags/{id}",
    [["DELETE", "/api/v1/photos/assets/p1/tags/beach", GOES]],
    [
      ["DELETE", "/api/v1/photos/assets/p1", DELETES],
      ["DELETE", "/api/v1/photos/assets/p1/ai-labels/l1", DELETES],
    ],
  ],
  [
    "PUT media/videos/{id}/progress",
    [["PUT", "/api/v1/media/videos/v1/progress", GOES]],
    [["PATCH", "/api/v1/media/videos/v1", ANY]],
  ],
  [
    "PUT media/titles/{id}/watched",
    [["PUT", "/api/v1/media/titles/t1/watched", GOES]],
    [["DELETE", "/api/v1/media/titles/t1/watched", DELETES]],
  ],
  [
    "(PUT|DELETE) media/favorites/{id}",
    [
      ["PUT", "/api/v1/media/favorites/t1", GOES],
      ["DELETE", "/api/v1/media/favorites/t1", GOES],
    ],
    [["DELETE", "/api/v1/media/history/v1", DELETES]],
  ],
  [
    "POST media/collections",
    [["POST", "/api/v1/media/collections", GOES]],
    [["DELETE", "/api/v1/media/collections/c1", DELETES]],
  ],
  [
    "POST media/collections/{id}/items",
    [["POST", "/api/v1/media/collections/c1/items", GOES]],
    [["DELETE", "/api/v1/media/collections/c1/items/t1", DELETES]],
  ],
  [
    "POST media/libraries/{id}/scan",
    [["POST", "/api/v1/media/libraries/l1/scan", GOES]],
    [["POST", "/api/v1/media/libraries", ANY]],
  ],
  ["POST music/library/scan", [["POST", "/api/v1/music/library/scan", GOES]], [["PUT", "/api/v1/music/sources", ANY]]],
  [
    "(PUT|DELETE) music/favorites/{id}",
    [
      ["PUT", "/api/v1/music/favorites/k1", GOES],
      ["DELETE", "/api/v1/music/favorites/k1", GOES],
    ],
    [["DELETE", "/api/v1/music/recent", DELETES]],
  ],
  ["POST music/plays", [["POST", "/api/v1/music/plays", GOES]], [["DELETE", "/api/v1/music/plays", DELETES]]],
  ["POST music/playlists", [["POST", "/api/v1/music/playlists", GOES]], [["PATCH", "/api/v1/music/playlists/p1", ANY]]],
  [
    "POST music/playlists/{id}/tracks",
    [["POST", "/api/v1/music/playlists/p1/tracks", GOES]],
    [["DELETE", "/api/v1/music/playlists/p1", DELETES]],
  ],
  ["POST shares", [["POST", "/api/v1/shares", SHARES]], [["DELETE", "/api/v1/shares", DELETES]]],
  [
    "POST shares/{id}/(messages|revoke)",
    [
      ["POST", "/api/v1/shares/x/messages", SHARES],
      ["POST", "/api/v1/shares/x/revoke", SHARES],
    ],
    [["POST", "/api/v1/shares/x/save", GOES]],
  ],
  [
    "POST files/{id}/copies",
    [["POST", "/api/v1/files/f1/copies", COPIES]],
    [["DELETE", "/api/v1/files/f1/copies", DELETES]],
  ],
  [
    "POST photos/assets/{id}/copies",
    [["POST", "/api/v1/photos/assets/p1/copies", COPIES]],
    [["POST", "/api/v1/photos/assets/p1/tags", GOES]],
  ],
  ["PATCH files/{id}", [["PATCH", "/api/v1/files/f1", MOVES]], [["DELETE", "/api/v1/files/f1", DELETES]]],
  [
    "POST (trash/{id}|photos/assets/{id}|snapshots/{id}(/entries/{id})?)/restore",
    [
      ["POST", "/api/v1/trash/t1/restore", RESTORES],
      ["POST", "/api/v1/photos/assets/p1/restore", RESTORES],
      ["POST", "/api/v1/snapshots/s1/restore", RESTORES],
      ["POST", "/api/v1/snapshots/s1/entries/e1/restore", RESTORES],
    ],
    [
      ["POST", "/api/v1/family/babies/b1/restore", ANY],
      ["POST", "/api/v1/snapshots/s1/keep", SYSTEM],
    ],
  ],
  [
    "any feedback(/.*)?",
    [
      ["POST", "/api/v1/feedback", FEEDBACK],
      ["PATCH", "/api/v1/feedback/f1", FEEDBACK],
      ["DELETE", "/api/v1/feedback/f1/comments/c1", FEEDBACK],
    ],
    [["POST", "/api/v1/feedbacks", ANY]],
  ],
  [
    "any (session|users|viewing|storage|volumes|wallpaper|local-display|apps|containers|snapshots)(/.*)?",
    [
      ["DELETE", "/api/v1/session", SYSTEM],
      ["POST", "/api/v1/session/password", SYSTEM],
      ["POST", "/api/v1/users", SYSTEM],
      ["DELETE", "/api/v1/users/u1", SYSTEM],
      ["DELETE", "/api/v1/viewing/g1", SYSTEM],
      ["POST", "/api/v1/storage/plans/p1/execute", SYSTEM],
      ["POST", "/api/v1/volumes/v1/conversion", SYSTEM],
      ["PATCH", "/api/v1/wallpaper", SYSTEM],
      ["PUT", "/api/v1/local-display", SYSTEM],
      ["POST", "/api/v1/apps/a1/install", SYSTEM],
      ["POST", "/api/v1/containers/c1/actions", SYSTEM],
      ["DELETE", "/api/v1/snapshots/s1", SYSTEM],
    ],
    [
      ["POST", "/api/v1/app-shortcuts", ANY],
      ["POST", "/api/v1/sessions", ANY],
    ],
  ],
  [
    "any spaces/{id}/(snapshots|snapshot-schedule)",
    [
      ["POST", "/api/v1/spaces/s1/snapshots", SYSTEM],
      ["PUT", "/api/v1/spaces/s1/snapshot-schedule", SYSTEM],
    ],
    [["POST", "/api/v1/spaces/s1/directories", GOES]],
  ],
  [
    "DELETE .*",
    [
      ["DELETE", "/api/v1/files/f1", DELETES],
      ["DELETE", "/api/v1/family/records/r1", DELETES],
    ],
    [["PATCH", "/api/v1/family/records/r1", ANY]],
  ],
];

describe("writeRisk", () => {
  describe.each(RULES)("%s", (_rule, answered, lookAlikes) => {
    it.each(answered)("answers %s %s", (method, path, why) => {
      expect(writeRisk(method, path)).toBe(why);
    });
    it.each(lookAlikes)("leaves %s %s to another answer", (method, path, why) => {
      expect(writeRisk(method, path)).toBe(why);
    });
  });

  it("asks about a write no rule names", () => {
    expect(writeRisk("POST", "/api/v1/something-new")).toBe(ANY);
    expect(writeRisk("POST", "/api/v1/family/babies")).toBe(ANY);
  });

  it("matches whole paths and whole path segments", () => {
    expect(writeRisk("POST", "/api/v1/spaces/s1/directories/d1")).toBe(ANY);
    expect(writeRisk("POST", "/api/v1/spaces/s1/d1/directories")).toBe(ANY);
    expect(writeRisk("POST", "/other/api/v1/spaces/s1/directories")).toBe(ANY);
    expect(writeRisk("PUT", "/api/v1/media/favorites/t1/extra")).toBe(ANY);
  });

  it("matches the whole method", () => {
    expect(writeRisk("POSTS", "/api/v1/spaces/s1/directories")).toBe(ANY);
    expect(writeRisk("XPOST", "/api/v1/spaces/s1/directories")).toBe(ANY);
    expect(writeRisk("DELETED", "/api/v1/music/favorites/k1")).toBe(ANY);
  });

  it("never asks about VNAS's own window, whose writes are the member's own clicks", () => {
    expect(writeRisk("DELETE", "/api/v1/assistant/sessions/s1")).toBe(GOES);
    expect(writeRisk("POST", "/api/v1/assistant/memory/clear")).toBe(GOES);
    expect(writeRisk("DELETE", "/api/v1/assistant")).toBe(DELETES);
  });
});
