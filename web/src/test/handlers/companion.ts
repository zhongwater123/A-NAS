import type { CompanionState } from "../../api.generated";
import { apiError, http } from "../server";

export function companionHandlers(initial: Partial<CompanionState> = {}) {
  let state: CompanionState = {
    conversationId: "",
    revision: 0,
    character: "glow",
    quiet: false,
    playCount: 0,
    active: null,
    pending: null,
    ...initial,
  };
  return [
    http.get("/api/v1/companion", ({ response }) => response(200).json(state)),
    http.post("/api/v1/companion/claim", async ({ request, response }) => {
      const body = await request.json();
      if (body.revision !== state.revision) return response(409).json(apiError("companion_conflict"));
      state = {
        ...state,
        revision: state.revision + 1,
        active: { clientId: body.clientId, kind: body.kind, epoch: (state.active?.epoch ?? 0) + 1, remainingMs: 15000 },
        pending: null,
      };
      return response(200).json(state);
    }),
    http.post("/api/v1/companion/heartbeat", ({ response }) => response(200).json(state)),
    http.post("/api/v1/companion/release", ({ response }) => {
      state = { ...state, revision: state.revision + 1, active: null, pending: null };
      return response(200).json(state);
    }),
    http.post("/api/v1/companion/actions", async ({ request, response }) => {
      const body = await request.json();
      state = {
        ...state,
        revision: state.revision + 1,
        conversationId: body.type === "conversation" ? (body.conversationId ?? "") : state.conversationId,
        character: body.type === "character" ? body.character! : state.character,
        quiet: body.type === "quiet" ? true : body.type === "wake" ? false : state.quiet,
        playCount: state.playCount + (body.type === "ball" ? 1 : 0),
      };
      return response(200).json(state);
    }),
  ];
}
