// The MSW stand-ins for Venus's operations (#694): every operation of
// api/paths/videocut.yaml but the switch and the desktop's notices, which the
// desktop's own handlers answer. They are typed by the contract
// (api.generated.ts) and answer from a VenusFake, so the Web goes through the
// whole flow without VideoCut. Remove a stand-in once VideoCut serves its
// operation.
import { HttpResponse } from "msw";
import type { OpenApiHttpHandlers } from "openapi-msw";

import type { paths } from "../../api.generated";
import { catalog, FakeError, type VenusFake } from "./fake";

// A JPEG of one grey pixel, for every frame.
const FRAME = Uint8Array.from(
  atob(
    "/9j/4AAQSkZJRgABAQEASABIAAD/2wBDAP//////////////////////////////////////////////////////////////////////////////////////wgALCAABAAEBAREA/8QAFBABAAAAAAAAAAAAAAAAAAAAAP/aAAgBAQABPxA=",
  ),
  (c) => c.charCodeAt(0),
);

// binary is a media answer; the contract types no body for it, so it is a plain Response.
const binary = (
  contentType: string,
  body: BodyInit | null = new Uint8Array(0),
  headers: Record<string, string> = {},
): Response => new HttpResponse(body, { status: 200, headers: { "Content-Type": contentType, ...headers } });

// answer runs what the fake does and answers its error as VideoCut would.
async function answer(run: () => Response | Promise<Response>): Promise<Response> {
  try {
    return await run();
  } catch (error) {
    if (error instanceof FakeError) {
      return HttpResponse.json({ error: { code: error.code, message: error.code } }, { status: error.status });
    }
    throw error;
  }
}

export function videocutHandlers(fake: VenusFake, http: OpenApiHttpHandlers<paths>) {
  return [
    http.get("/api/v1/videocut/catalog", ({ response }) => response(200).json(catalog)),
    http.get("/api/v1/videocut/songs/{songId}/audio", () => binary("audio/mpeg")),
    http.get("/api/v1/videocut/projects", ({ response }) => response(200).json(fake.list())),
    http.post("/api/v1/videocut/projects", ({ request, response }) =>
      answer(async () => {
        const body = await request.json();
        const { project, created } = fake.create(body.requestKey, body.files);
        return created ? response(201).json(project) : response(200).json(project);
      }),
    ),
    http.get("/api/v1/videocut/projects/{projectId}", ({ params, response }) =>
      answer(() => response(200).json(fake.project(params.projectId))),
    ),
    http.delete("/api/v1/videocut/projects/{projectId}", ({ params, response }) =>
      answer(() => {
        fake.remove(params.projectId);
        return response(204).empty();
      }),
    ),
    http.post("/api/v1/videocut/projects/{projectId}/files", ({ params, request, response }) =>
      answer(async () => response(200).json(fake.addFiles(params.projectId, (await request.json()).files))),
    ),
    http.delete("/api/v1/videocut/projects/{projectId}/files/{fileId}", ({ params, response }) =>
      answer(() => response(200).json(fake.removeFile(params.projectId, params.fileId))),
    ),
    http.patch("/api/v1/videocut/projects/{projectId}/selection", ({ params, request, response }) =>
      answer(async () => response(200).json(fake.select(params.projectId, (await request.json()).files))),
    ),
    http.patch("/api/v1/videocut/projects/{projectId}/settings", ({ params, request, response }) =>
      answer(async () => response(200).json(fake.settings(params.projectId, await request.json()))),
    ),
    http.put("/api/v1/videocut/projects/{projectId}/song", ({ params, request, response }) =>
      answer(async () => {
        const { trackId } = await request.json();
        return response(200).json(
          fake.chooseSong(params.projectId, { source: "library", id: trackId, title: "音乐库里的歌" }),
        );
      }),
    ),
    http.get("/api/v1/videocut/projects/{projectId}/song", ({ params, response }) =>
      answer(() => response(200).json(fake.songStructure(params.projectId))),
    ),
    http.post("/api/v1/videocut/projects/{projectId}/song/import", ({ params, query, response }) =>
      answer(() => {
        const name = query.get("name") ?? "导入的歌";
        return response(200).json(
          fake.chooseSong(params.projectId, { source: "imported", id: `song:${name}`, title: name }),
        );
      }),
    ),
    http.get("/api/v1/videocut/projects/{projectId}/song/audio", () => binary("audio/mpeg")),
    http.post("/api/v1/videocut/projects/{projectId}/heartbeat", ({ params, response }) =>
      answer(() => {
        fake.heartbeat(params.projectId);
        return response(204).empty();
      }),
    ),
    http.post("/api/v1/videocut/projects/{projectId}/generate", ({ params, request, response }) =>
      answer(async () => response(200).json(fake.generate(params.projectId, (await request.json()).requestKey))),
    ),
    http.post("/api/v1/videocut/projects/{projectId}/resume", ({ params, response }) =>
      answer(() => response(200).json(fake.resume(params.projectId))),
    ),
    http.post("/api/v1/videocut/projects/{projectId}/retry", ({ params, response }) =>
      answer(() => response(200).json(fake.retry(params.projectId))),
    ),
    http.get("/api/v1/videocut/projects/{projectId}/assets", ({ params, response }) =>
      answer(() => response(200).json({ items: fake.assetsOf(params.projectId) })),
    ),
    http.post("/api/v1/videocut/projects/{projectId}/assets", ({ params, request, response }) =>
      answer(async () => {
        const body = await request.json();
        return response(200).json(fake.addVideo(params.projectId, body.assetId, body.fileId));
      }),
    ),
    http.get("/api/v1/videocut/projects/{projectId}/assets/{assetId}/proxy", () => binary("video/mp4")),
    http.get("/api/v1/videocut/projects/{projectId}/assets/{assetId}/frame", () => binary("image/jpeg", FRAME)),
    http.get("/api/v1/videocut/projects/{projectId}/timeline", ({ params, response }) =>
      answer(() => response(200).json(fake.timeline(params.projectId))),
    ),
    http.post("/api/v1/videocut/projects/{projectId}/timeline/edits", ({ params, request, response }) =>
      answer(async () => {
        const body = await request.json();
        return response(200).json(fake.edit(params.projectId, body.baseRevision, body.edit));
      }),
    ),
    http.post("/api/v1/videocut/projects/{projectId}/timeline/undo", ({ params, request, response }) =>
      answer(async () => response(200).json(fake.undo(params.projectId, (await request.json()).baseRevision))),
    ),
    http.get("/api/v1/videocut/projects/{projectId}/slots/{slot}/candidates", ({ params, response }) =>
      answer(() => response(200).json(fake.candidates(params.projectId, Number(params.slot)))),
    ),
    http.get("/api/v1/videocut/assistant", ({ response }) => response(200).json({ search: true })),
    http.post("/api/v1/videocut/projects/{projectId}/assistant", ({ params, request, response }) =>
      answer(async () => {
        const body = await request.json();
        return response(200).json(fake.ask(params.projectId, body.text, body.slot));
      }),
    ),
    http.post("/api/v1/videocut/projects/{projectId}/export", ({ params, request, response }) =>
      answer(async () => response(200).json(fake.export(params.projectId, (await request.json()).requestKey))),
    ),
    http.get("/api/v1/videocut/projects/{projectId}/film", ({ params }) =>
      answer(() => {
        if (!fake.hasFilm(params.projectId)) throw new FakeError(404, "not_found");
        return binary("video/mp4", new Uint8Array(0), { "Content-Disposition": 'attachment; filename="film.mp4"' });
      }),
    ),
  ];
}
