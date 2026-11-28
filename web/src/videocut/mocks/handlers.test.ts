import { readFileSync } from "node:fs";
import { beforeEach, describe, expect, it } from "vitest";
import { parse } from "yaml";

import { http, server } from "../../test/server";
import { SAMPLE_ID, VenusFake } from "./fake";
import { videocutHandlers } from "./handlers";

const METHODS = new Set(["get", "put", "post", "patch", "delete", "head", "options", "trace"]);

// notServed reads the bundled contract (tests run in web/) for VideoCut's
// operations that still declare 501: the ones VideoCut does not serve yet,
// which the stand-ins answer.
function notServed(): string[] {
  const bundle = parse(readFileSync("../api/openapi.bundle.yaml", "utf8")) as {
    paths: Record<string, Record<string, { responses?: Record<string, unknown> }>>;
  };
  return Object.entries(bundle.paths)
    .filter(([path]) => path.startsWith("/api/v1/videocut/"))
    .flatMap(([path, item]) =>
      Object.entries(item)
        .filter(([method, operation]) => METHODS.has(method) && operation.responses?.["501"] !== undefined)
        .map(([method]) => `${method.toUpperCase()} ${path}`),
    );
}

describe("Venus's stand-ins", () => {
  it("answer every operation VideoCut does not serve yet, and no other", () => {
    const served = videocutHandlers(new VenusFake(), http).map(({ info }) => {
      const path = String(info.path)
        .replace(window.location.origin, "")
        .replace(/:(\w+)/g, "{$1}");
      return `${String(info.method)} ${path}`;
    });
    const wanted = notServed();
    expect(wanted).toContain("POST /api/v1/videocut/projects/{projectId}/timeline/edits");
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
    const json = response.headers.get("Content-Type")?.includes("json");
    return { status: response.status, body: json ? await response.json() : undefined };
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

  it("repeats a video's request key, and refuses it for another request", async () => {
    const { body } = await send("POST", "/projects", { requestKey: "k4", files: [file("d.mp4")] });
    await send("POST", `/projects/${body.id}/generate`, { requestKey: "g4" });
    const fileId = body.files[0].id as string;
    const first = await send("POST", `/projects/${body.id}/assets`, { requestKey: "a1", assetId: "photo:1", fileId });
    expect(first.status).toBe(200);
    expect(
      (await send("POST", `/projects/${body.id}/assets`, { requestKey: "a1", assetId: "photo:1", fileId })).body,
    ).toEqual(first.body);
    const other = await send("POST", `/projects/${body.id}/assets`, { requestKey: "a1", assetId: "photo:2", fileId });
    expect(other).toMatchObject({ status: 409, body: { error: { code: "conflict" } } });
  });

  it("serves only the project's own videos, and moves listed projects on", async () => {
    const { body } = await send("POST", "/projects", { requestKey: "k5", files: [file("e.mp4")] });
    expect((await send("GET", `/projects/${SAMPLE_ID}/assets/sample-asset:1/frame?t=0`)).status).toBe(200);
    expect((await send("GET", `/projects/${body.id}/assets/sample-asset:1/frame?t=0`)).status).toBe(404);
    expect((await send("GET", `/projects/${body.id}/assets/sample-asset:1/proxy`)).status).toBe(404);
    await send("POST", `/projects/${body.id}/generate`, { requestKey: "g5" });
    const states = [];
    for (let i = 0; i < 3; i++) {
      const items = (await send("GET", "/projects")).body.items as { id: string; state: string }[];
      states.push(items.find((item) => item.id === body.id)?.state);
    }
    expect(states).toEqual(["generating", "generating", "ready"]);
  });
});
