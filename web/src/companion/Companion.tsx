// The companion on the desktop: 大肥鱼, dsh-pet's pet card (petCard.tsx, dsh-pet/README.md).
// This part only asks the Product Service which animations are installed, after the
// desktop has drawn; the card's code and animations load only when some are, so a NAS
// without them shows nothing and pays nothing.
import { Suspense, useEffect, useState } from "react";

import { lazyCode } from "../ui/lazyCode";

const CompanionCard = lazyCode(() => import("./petCard").then((module) => module.CompanionCard));

export type InstalledAnimations = Record<string, Partial<Record<"webm" | "mov", string>>>;

/** GET /companion/animations: the installed animations, and the pool they are from when the service says. */
export interface CompanionManifest {
  animations: InstalledAnimations;
  pool?: string;
}

export async function loadInstalledAnimations(): Promise<CompanionManifest> {
  const response = await fetch("/companion/animations", { cache: "no-store" });
  if (!response.ok) return { animations: {} };
  const manifest = (await response.json()) as { animations?: unknown; pool?: unknown };
  return {
    animations:
      manifest.animations && typeof manifest.animations === "object"
        ? (manifest.animations as InstalledAnimations)
        : {},
    ...(typeof manifest.pool === "string" && manifest.pool ? { pool: manifest.pool } : {}),
  };
}

export function Companion() {
  const [manifest, setManifest] = useState<CompanionManifest>();
  useEffect(() => {
    let current = true;
    const load = () =>
      void loadInstalledAnimations()
        .then((loaded) => current && Object.keys(loaded.animations).length > 0 && setManifest(loaded))
        .catch(() => undefined);
    // After the desktop's first screen: the companion is never what the member waits for.
    const idle = window.requestIdleCallback?.(load, { timeout: 3000 });
    const timer = idle === undefined ? window.setTimeout(load, 1000) : undefined;
    return () => {
      current = false;
      if (idle !== undefined) window.cancelIdleCallback(idle);
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, []);
  if (!manifest) return null;
  return (
    <Suspense fallback={null}>
      <CompanionCard installed={manifest.animations} pool={manifest.pool} />
    </Suspense>
  );
}
