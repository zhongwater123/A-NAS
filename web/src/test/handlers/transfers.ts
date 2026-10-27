// Transfers: the account's uploads and downloads as the NAS lists them, and
// what the transfer center asks of them. Uploads themselves go through
// tus-js-client, which tests replace with a fake (its Node build cannot read a
// jsdom File), so the tus endpoints have no handlers here.
import type { Transfer } from "../../api.generated";
import { apiError, http, pathOf } from "../server";

// transfer is a download of one file in the shared space that finished just
// now, unless overrides say otherwise.
export function transfer(overrides: Partial<Transfer> = {}): Transfer {
  const now = new Date().toISOString();
  return {
    id: "download-1",
    direction: "download",
    kind: "file",
    name: "片段.mp4",
    location: { spaceId: "space:shared", spaceKind: "shared", folders: [] },
    totalBytes: 1024,
    transferredBytes: 1024,
    bytesPerSecond: 0,
    state: "completed",
    contentUrl: "/api/v1/transfers/download-1/content",
    createdAt: now,
    updatedAt: now,
    finishedAt: now,
    ...overrides,
  };
}

// transfersHandlers answers the account's transfers from list, read on every
// request so a test changes them as the NAS would. Pausing and canceling answer
// the listed transfer in its new state, and a transfer the list lacks is
// not_found, as on the NAS. sent keeps every change asked for, in order.
// (Preparing a download is the file manager's: downloads() in files.ts.)
export function transfersHandlers(list: () => Transfer[] = () => []) {
  const sent: Array<{ method: string; path: string }> = [];
  const record = (request: Request) => sent.push({ method: request.method, path: pathOf(request) });
  const listed = (id: string) => list().find((item) => item.id === id);
  const notFound = apiError("not_found", "传输不存在");
  const handlers = [
    http.get("/api/v1/transfers", ({ response }) => response(200).json({ items: list() })),
    http.get("/api/v1/transfers/{transferId}", ({ params, response }) => {
      const found = listed(params.transferId);
      return found ? response(200).json(found) : response(404).json(notFound);
    }),
    http.post("/api/v1/transfers/{transferId}/pause", ({ params, request, response }) => {
      record(request);
      const found = listed(params.transferId);
      return found ? response(200).json({ ...found, state: "paused" }) : response(404).json(notFound);
    }),
    http.post("/api/v1/transfers/{transferId}/cancel", ({ params, request, response }) => {
      record(request);
      const found = listed(params.transferId);
      return found ? response(200).json({ ...found, state: "canceled" }) : response(404).json(notFound);
    }),
    http.delete("/api/v1/transfers/{transferId}", ({ params, request, response }) => {
      record(request);
      return listed(params.transferId) ? response(204).empty() : response(404).json(notFound);
    }),
    http.delete("/api/v1/transfers", ({ request, response }) => {
      record(request);
      return response(204).empty();
    }),
  ];
  return { handlers, sent };
}
