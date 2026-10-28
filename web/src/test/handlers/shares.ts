// Member shares: the lists, one share, its private conversations, save jobs,
// the notice feed and the members a share goes to.
import type {
  CreateMemberShare,
  MemberShare,
  MemberShareJob,
  MemberShareMessage,
  MemberShareNotification,
  MemberShareRecipient,
  operations,
  SaveMemberShare,
} from "../../api.generated";
import { apiError, http } from "../server";

type Member = operations["listShareMembers"]["responses"][200]["content"]["application/json"]["items"][number];

export const sharedAt = "2026-10-15T00:00:00Z";

// recipient has not read or saved anything of a share with one message.
export function recipient(userId: string, overrides: Partial<MemberShareRecipient> = {}): MemberShareRecipient {
  return { userId, readThrough: 0, senderReadThrough: 0, lastMessage: 1, saved: false, savedFiles: 0, ...overrides };
}

// memberShare is a published share s1 of one small file from alice to bob.
export function memberShare(overrides: Partial<MemberShare> = {}): MemberShare {
  return {
    id: "s1",
    sender: { id: "alice", username: "Alice" },
    status: "published",
    createdAt: sharedAt,
    expiresAt: "2099-01-01T00:00:00Z",
    entries: [{ id: "e1", path: "计划.txt", isDir: false, size: 2 }],
    recipients: [recipient("bob")],
    preparedBytes: 2,
    ...overrides,
  };
}

// message is a line of a private conversation, from alice unless sender says.
export function message(id: number, text: string, sender = { id: "alice", username: "Alice" }): MemberShareMessage {
  return { id, sender, text, createdAt: sharedAt };
}

// notice is the notice of a new message in bob's conversation of share s1.
export function notice(id: number, overrides: Partial<MemberShareNotification> = {}): MemberShareNotification {
  return { id, shareId: "s1", recipientId: "bob", kind: "message", createdAt: sharedAt, ...overrides };
}

// saveJob is bob's save of a share that is halfway through.
export function saveJob(overrides: Partial<MemberShareJob> = {}): MemberShareJob {
  return {
    id: "job1",
    shareId: "s1",
    userId: "bob",
    status: "running",
    createdAt: sharedAt,
    bytesDone: 1,
    bytesTotal: 2,
    completedIds: [],
    entryIds: [],
    ...overrides,
  };
}

// The lists are read on every request, so a test that changes one changes
// what the server says.
export interface SharesOptions {
  received?: MemberShare[];
  sent?: MemberShare[];
  members?: Member[];
  // The private conversation of a share with recipientId.
  messages?: (recipientId: string, shareId: string) => MemberShareMessage[];
  jobs?: MemberShareJob[];
  notices?: MemberShareNotification[];
}

// sharesHandlers answers what the share window and the provider read: the
// lists, each share (and a recipient's view of it), conversations, save jobs,
// notices after the cursor and the members to share with. A share neither
// list holds is not found. Writes are left to the test (readCursors,
// shareCreations, shareSaves), which sees them; the folders a save goes to
// are the file manager's (filesHandlers).
export function sharesHandlers(options: SharesOptions = {}) {
  const { received = [], sent = [], jobs = [], notices = [] } = options;
  const find = (shareId: string) => [...received, ...sent].find((share) => share.id === shareId);
  return [
    http.get("/api/v1/shares", ({ query, response }) =>
      response(200).json({ items: query.get("direction") === "sent" ? sent : received }),
    ),
    // Before /shares/{shareId}, which would take them for shares of those ids.
    http.get("/api/v1/shares/notifications", ({ query, response }) => {
      const after = Number(query.get("after") ?? 0);
      const limit = Number(query.get("limit") ?? 100);
      return response(200).json({ items: notices.filter((item) => item.id > after).slice(0, limit) });
    }),
    http.get("/api/v1/shares/members", ({ response }) => response(200).json({ items: options.members ?? [] })),
    http.get("/api/v1/shares/{shareId}", ({ params, response }) => {
      const share = find(params.shareId);
      return share ? response(200).json(share) : response("default").json(apiError("share_not_found"), { status: 404 });
    }),
    http.post("/api/v1/shares/{shareId}/view", ({ params, response }) => {
      const share = find(params.shareId);
      return share ? response(200).json(share) : response("default").json(apiError("share_not_found"), { status: 404 });
    }),
    http.get("/api/v1/shares/{shareId}/messages", ({ params, query, response }) => {
      const after = Number(query.get("after") ?? 0);
      const items = options.messages?.(query.get("recipientId") ?? "", params.shareId) ?? [];
      return response(200).json({ items: items.filter((item) => item.id > after) });
    }),
    http.get("/api/v1/shares/{shareId}/jobs", ({ params, response }) =>
      response(200).json({ items: jobs.filter((job) => job.shareId === params.shareId) }),
    ),
  ];
}

// readCursors records the read cursors the conversations advance.
export function readCursors() {
  const sent: Array<{ recipientId: string; through: number }> = [];
  const handler = http.post("/api/v1/shares/{shareId}/read", async ({ request, response }) => {
    sent.push(await request.json());
    return response(204).empty();
  });
  return { sent, handler };
}

// shareCreations records the shares the composer asks for and accepts each.
export function shareCreations() {
  const sent: CreateMemberShare[] = [];
  const handler = http.post("/api/v1/shares", async ({ request, response }) => {
    sent.push(await request.json());
    return response(202).json(memberShare({ status: "preparing" }));
  });
  return { sent, handler };
}

// shareSaves records the saves asked for and starts a job for each, which
// jobs, the list sharesHandlers reads, then holds.
export function shareSaves(jobs: MemberShareJob[]) {
  const sent: SaveMemberShare[] = [];
  const handler = http.post("/api/v1/shares/{shareId}/save", async ({ params, request, response }) => {
    const save = await request.json();
    sent.push(save);
    const job = saveJob({ id: `job${jobs.length + 1}`, shareId: params.shareId, entryIds: save.entryIds });
    jobs.push(job);
    return response(202).json(job);
  });
  return { sent, handler };
}
