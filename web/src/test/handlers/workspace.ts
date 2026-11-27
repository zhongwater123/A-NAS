// 共享工作区: the shared pages with their versions, what a member picked,
// who may use it, and the account's agent tokens (ADR 0040). The handlers
// keep a small store, so that what a test saves is what it reads next.
import type {
  AgentToken,
  WorkspaceMemberAccount,
  WorkspacePage,
  WorkspacePresence,
  WorkspaceSelection,
  WorkspaceVersion,
  WorkspaceViewing,
} from "../../api.generated";
import { apiError, http } from "../server";

export const reader = { id: "user:alice", name: "alice" };
export const other = { id: "user:bob", name: "bob" };

export function version(number: number, overrides: Partial<WorkspaceVersion> = {}): WorkspaceVersion {
  return {
    version: number,
    size: 1200,
    savedBy: reader,
    via: { kind: "web" },
    note: "",
    changed: [],
    savedAt: new Date(Date.UTC(2026, 10, 25, 8, number)).toISOString(),
    ...overrides,
  };
}

export function workspacePage(id: string, title: string, versions: WorkspaceVersion[]): WorkspacePage {
  const newest = [...versions].sort((a, b) => b.version - a.version);
  return {
    id,
    title,
    createdBy: versions[0]?.savedBy ?? reader,
    createdAt: versions[0]?.savedAt ?? new Date().toISOString(),
    latest: newest[0],
    versions: newest,
    presence: nobody(),
  };
}

// nobody is a page's presence when no one has it open.
export const nobody = (): WorkspacePresence => ({ viewers: [], agents: [] });

const summaryOf = ({ id, title, createdBy, createdAt, latest, presence }: WorkspacePage) => ({
  id,
  title,
  createdBy,
  createdAt,
  latest,
  presence,
});

export interface WorkspaceStore {
  pages: WorkspacePage[];
  selection?: WorkspaceSelection;
  // viewing is what this tab said it shows; reports counts what it said;
  // seqs is the seq of each report and take-back, in the order they came.
  viewing?: WorkspaceViewing;
  reports: WorkspaceViewing[];
  seqs: number[];
  members: WorkspaceMemberAccount[];
  tokens: AgentToken[];
  uploads: { name: string | null; base?: number; body: string }[];
}

export function workspaceStore(pages: WorkspacePage[] = []): WorkspaceStore {
  return { pages, members: [], tokens: [], uploads: [], reports: [], seqs: [] };
}

// addVersion saves a version as the service would, for a test to bring in
// what another member or the reader's agent saved.
export function addVersion(store: WorkspaceStore, pageId: string, saved: Omit<Partial<WorkspaceVersion>, "version">) {
  const page = store.pages.find((item) => item.id === pageId);
  if (!page) throw new Error(`no page ${pageId}`);
  const next = version(page.latest.version + 1, saved);
  const updated = workspacePage(page.id, page.title, [next, ...page.versions]);
  store.pages = store.pages.map((item) =>
    item.id === pageId ? { ...updated, createdBy: page.createdBy, presence: page.presence } : item,
  );
  return next;
}

export function workspaceHandlers(store: WorkspaceStore) {
  const find = (pageId: string) => store.pages.find((page) => page.id === pageId);
  return [
    http.get("/api/v1/workspace/pages", ({ response }) => response(200).json({ items: store.pages.map(summaryOf) })),
    http.post("/api/v1/workspace/pages", async ({ request, query, response }) => {
      const body = await request.text();
      store.uploads.push({ name: query.get("name"), body });
      const title = /<title>([^<]*)<\/title>/.exec(body)?.[1] ?? query.get("name") ?? "页面";
      const page = workspacePage(`page_${store.pages.length + 1}`, title, [version(1)]);
      store.pages = [page, ...store.pages];
      return response(201).json(page);
    }),
    http.get("/api/v1/workspace/pages/{pageId}", ({ params, response }) => {
      const page = find(params.pageId);
      return page ? response(200).json(page) : response(404).json(apiError("not_found"));
    }),
    http.patch("/api/v1/workspace/pages/{pageId}", async ({ params, request, response }) => {
      const page = find(params.pageId);
      if (!page) return response(404).json(apiError("not_found"));
      const { title } = await request.json();
      page.title = title;
      return response(200).json(summaryOf(page));
    }),
    http.delete("/api/v1/workspace/pages/{pageId}", ({ params, response }) => {
      store.pages = store.pages.filter((page) => page.id !== params.pageId);
      return response(204).empty();
    }),
    http.post("/api/v1/workspace/pages/{pageId}/versions", async ({ params, query, request, response }) => {
      const page = find(params.pageId);
      if (!page) return response(404).json(apiError("not_found"));
      const base = Number(query.get("baseVersion"));
      if (base !== page.latest.version) {
        return response(409).json({ ...apiError("page_changed"), latest: page.latest });
      }
      store.uploads.push({ name: null, base, body: await request.text() });
      return response(201).json(addVersion(store, page.id, {}));
    }),
    http.put("/api/v1/workspace/viewing", async ({ request, response }) => {
      store.viewing = await request.json();
      store.reports.push(store.viewing);
      store.seqs.push(store.viewing.seq);
      return response(204).empty();
    }),
    http.delete("/api/v1/workspace/viewing/{tab}", ({ params, query, response }) => {
      if (store.viewing?.tab === params.tab) store.viewing = undefined;
      store.seqs.push(Number(query.get("seq")));
      return response(204).empty();
    }),
    http.put("/api/v1/workspace/selection", async ({ request, response }) => {
      store.selection = await request.json();
      return response(204).empty();
    }),
    http.delete("/api/v1/workspace/selection", ({ response }) => {
      store.selection = undefined;
      return response(204).empty();
    }),
    http.get("/api/v1/workspace/admin/members", ({ response }) => response(200).json({ items: store.members })),
    http.put("/api/v1/workspace/admin/members/{accountId}", ({ params, response }) => {
      store.members = store.members.map((item) => (item.id === params.accountId ? { ...item, member: true } : item));
      return response(204).empty();
    }),
    http.delete("/api/v1/workspace/admin/members/{accountId}", ({ params, response }) => {
      store.members = store.members.map((item) => (item.id === params.accountId ? { ...item, member: false } : item));
      return response(204).empty();
    }),
    ...agentTokenHandlers(store),
  ];
}

export function agentTokenHandlers(store: Pick<WorkspaceStore, "tokens">) {
  return [
    http.get("/api/v1/session/agent-tokens", ({ response }) => response(200).json({ items: store.tokens })),
    http.post("/api/v1/session/agent-tokens", async ({ request, response }) => {
      const { name, kind } = await request.json();
      const token: AgentToken = {
        id: `tok_${store.tokens.length + 1}`,
        name,
        kind,
        scope: "workspace",
        createdAt: new Date().toISOString(),
      };
      store.tokens = [...store.tokens, token];
      return response(201).json({ agentToken: token, token: `anas_ws_${"0".repeat(64)}` });
    }),
    http.delete("/api/v1/session/agent-tokens/{tokenId}", ({ params, response }) => {
      store.tokens = store.tokens.filter((token) => token.id !== params.tokenId);
      return response(204).empty();
    }),
  ];
}
