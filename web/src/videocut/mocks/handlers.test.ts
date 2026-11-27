import { readFileSync } from "node:fs";
import { beforeEach, describe, expect, it } from "vitest";

import { http, server } from "../../test/server";
import { SAMPLE_ID, VenusFake } from "./fake";
import { videocutHandlers } from "./handlers";

// The operations the desktop's own handlers answer (test/handlers/shell.ts).
const DESKTOP = new Set(["GET /api/v1/videocut", "GET /api/v1/videocut/notices"]);

// contractOperations reads the bundled contract for VideoCut's operations; tests run in web/.
function contractOperations(): string[] {
  const bundle = readFileSync("../api/openapi.bundle.yaml", "utf8");
  const operations: string[] = [];
  let path: string | undefined;
  for (const line of bundle.split("\n")) {
    const opened = /^ {2}(\/api\/v1\/videocut[^:]*):$/.exec(line);
    if (opened) {
      path = opened[1];
      continue;
    }
    if (/^ {2}\S/.test(line) || /^\S/.test(line)) path = undefined;
    const method = /^ {4}(get|put|post|patch|delete):$/.exec(line);
    if (path && method) operations.push(`${method[1].toUpperCase()} ${path}`);
  }
  return operations;
}

describe("Venus's stand-ins", () => {
  it("answer every operation of VideoCut's contract", () => {
    const served = videocutHandlers(new VenusFake(), http).map(({ info }) => {
      const path = String(info.path)
        .replace(window.location.origin, "")
        .replace(/:(\w+)/g, "{$1}");
      return `${String(info.method)} ${path}`;
    });
    const wanted = contractOperations().filter((operation) => !DESKTOP.has(operation));
    expect(wanted.length).toBeGreaterThan(20);
    expect(served.sort()).toEqual(wanted.sort());
  });
});

describe("VenusFake behind the stand-ins", () => {
  const base = `${window.location.origin}/api/v1/videocut`;
  const send = async (method: string, path: string, body?: unknown) => {
    const response = await fetch(base + path, {
      method,
      headers: body === undefined ? {} : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await response.text();
    return { status: response.status, body: text ? JSON.parse(text) : undefined };
  };
  const file = (name: string, read = true) => ({
    name,
    size: 1000,
    modifiedAt: "2026-11-20T08:00:00Z",
    read,
    ...(read ? { duration: 8, width: 1920, height: 1080 } : {}),
  });

  beforeEach(() => {
    server.use(...videocutHandlers(new VenusFake(), http));
  });

  it("goes from the files the browser read to a film, and exports it", async () => {
    const made = await send("POST", "/projects", { requestKey: "k1", files: [file("a.mp4"), file("b.mp4", false)] });
    expect(made.status).toBe(201);
    expect(made.body.state).toBe("importing");
    const id = made.body.id as string;
    expect((await send("POST", "/projects", { requestKey: "k1", files: [file("a.mp4")] })).status).toBe(200);

    // The second file is read; the same file again counts once.
    const read = await send("POST", `/projects/${id}/files`, { files: [file("b.mp4"), file("a.mp4")] });
    expect(read.body).toMatchObject({ state: "pending", fileCount: 2 });
    const [first] = read.body.files as { id: string }[];
    expect(
      (await send("PATCH", `/projects/${id}/selection`, { files: [{ fileId: first.id, starred: true }] })).status,
    ).toBe(200);
    expect((await send("PATCH", `/projects/${id}/settings`, { aspect: "4:3" })).body.error.code).toBe(
      "validation_failed",
    );

    expect((await send("POST", `/projects/${id}/generate`, { requestKey: "g1" })).body.state).toBe("uploading");
    // Each read moves it on, as polling would see it.
    const states: string[] = [];
    for (let i = 0; i < 4; i++) states.push((await send("GET", `/projects/${id}`)).body.state);
    expect(states).toEqual(["generating", "generating", "ready", "ready"]);

    const timeline = (await send("GET", `/projects/${id}/timeline`)).body;
    expect(timeline.shots.length).toBe(15);
    const candidates = (await send("GET", `/projects/${id}/slots/3/candidates`)).body;
    expect(candidates.items.length).toBe(6);
    const replaced = await send("POST", `/projects/${id}/timeline/edits`, {
      baseRevision: 1,
      edit: { kind: "replace", slot: 3, beat: candidates.items[0].beat },
    });
    expect(replaced.body).toMatchObject({ revision: 2, canUndo: true, changedSlots: [3] });
    const stale = await send("POST", `/projects/${id}/timeline/edits`, {
      baseRevision: 1,
      edit: { kind: "remove", slot: 3 },
    });
    expect(stale).toMatchObject({ status: 409, body: { error: { code: "timeline_conflict" } } });
    const undone = await send("POST", `/projects/${id}/timeline/undo`, { baseRevision: 2 });
    expect(undone.body.shots[2].beat).toBe(timeline.shots[2].beat);
    expect(undone.body.canUndo).toBe(false);

    expect((await send("GET", `/projects/${id}/film`)).status).toBe(404);
    expect((await send("POST", `/projects/${id}/export`, { requestKey: "e1" })).body.state).toBe("exporting");
    const editing = await send("POST", `/projects/${id}/timeline/edits`, {
      baseRevision: 3,
      edit: { kind: "remove", slot: 1 },
    });
    expect(editing.body.error.code).toBe("project_state");
    expect((await send("GET", `/projects/${id}`)).body.state).toBe("exported");
    expect((await send("GET", `/projects/${id}/film`)).status).toBe(200);
  });

  it("keeps the sample read only and last in the list", async () => {
    await send("POST", "/projects", { requestKey: "k2", files: [file("c.mp4")] });
    const list = (await send("GET", "/projects")).body.items as { id: string }[];
    expect(list.at(-1)?.id).toBe(SAMPLE_ID);
    expect((await send("DELETE", `/projects/${SAMPLE_ID}`)).body.error.code).toBe("sample_read_only");
    expect((await send("GET", `/projects/${SAMPLE_ID}/timeline`)).status).toBe(200);
  });

  it("refuses a sixth star and generating with nothing selected", async () => {
    const files = Array.from({ length: 6 }, (_, i) => file(`${i}.mp4`));
    const { body } = await send("POST", "/projects", { requestKey: "k3", files });
    const ids = (body.files as { id: string }[]).map((f) => f.id);
    const starred = await send("PATCH", `/projects/${body.id}/selection`, {
      files: ids.map((fileId) => ({ fileId, starred: true })),
    });
    expect(starred).toMatchObject({ status: 422, body: { error: { code: "too_many_stars" } } });
    await send("PATCH", `/projects/${body.id}/selection`, {
      files: ids.map((fileId) => ({ fileId, selected: false })),
    });
    expect((await send("POST", `/projects/${body.id}/generate`, { requestKey: "g" })).body.error.code).toBe(
      "nothing_selected",
    );
  });
});
