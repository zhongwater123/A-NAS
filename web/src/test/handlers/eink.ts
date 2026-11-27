// 墨水屏: the paired screens an administrator manages, kept in memory.
import type { EinkScreen } from "../../api.generated";
import { apiError, http } from "../server";

export function einkScreen(name: string, fields: Partial<EinkScreen> = {}): EinkScreen {
  return { id: `scr_${name}`, name, createdAt: "2026-11-20T09:00:00Z", ...fields };
}

export function einkHandlers(initial: EinkScreen[] = []) {
  const screens = [...initial];
  const taken = (name: string, id = "") => screens.some((screen) => screen.name === name && screen.id !== id);
  const handlers = [
    http.get("/api/v1/eink/screens", ({ response }) => response(200).json({ items: screens })),
    http.post("/api/v1/eink/screens", async ({ request, response }) => {
      const { name } = await request.json();
      if (taken(name)) return response(409).json(apiError("conflict", "a screen has this name"));
      const screen = einkScreen(name);
      screens.push(screen);
      return response(201).json({ screen, token: "TOKENTOKENTOKENTOKENTOKENTOKENTOKENTOKENTOKEN" });
    }),
    http.patch("/api/v1/eink/screens/{screenId}", async ({ params, request, response }) => {
      const { name } = await request.json();
      const screen = screens.find((item) => item.id === params.screenId);
      if (!screen) return response(404).json(apiError("not_found"));
      if (taken(name, screen.id)) return response(409).json(apiError("conflict", "a screen has this name"));
      screen.name = name;
      return response(200).json(screen);
    }),
    http.delete("/api/v1/eink/screens/{screenId}", ({ params, response }) => {
      const at = screens.findIndex((item) => item.id === params.screenId);
      if (at < 0) return response(404).json(apiError("not_found"));
      screens.splice(at, 1);
      return response(204).empty();
    }),
  ];
  return { handlers, screens };
}
