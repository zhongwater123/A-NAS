// E-ink screens (docs/specs/eink-screens.md): administrators pair, rename
// and remove them; the screens themselves never call from a browser.
import { call, client } from "../api/client";
import type { EinkPairedScreen, EinkScreen } from "../api.generated";

export type { EinkPairedScreen, EinkScreen } from "../api.generated";

export const listScreens = async (signal?: AbortSignal): Promise<EinkScreen[]> =>
  (await call(client.GET("/api/v1/eink/screens", { signal }))).items;

export const pairScreen = (name: string): Promise<EinkPairedScreen> =>
  call(client.POST("/api/v1/eink/screens", { body: { name } }));

export const renameScreen = (id: string, name: string): Promise<EinkScreen> =>
  call(client.PATCH("/api/v1/eink/screens/{screenId}", { params: { path: { screenId: id } }, body: { name } }));

export const removeScreen = (id: string): Promise<void> =>
  call(client.DELETE("/api/v1/eink/screens/{screenId}", { params: { path: { screenId: id } } }));

// A screen asks again by the time it was told, plus this grace; later than
// that it is overdue (该来没来).
export const overdueAfterMs = 10 * 60 * 1000;

// lowBattery is the charge at or below which settings ask to charge it.
export const lowBattery = 10;
