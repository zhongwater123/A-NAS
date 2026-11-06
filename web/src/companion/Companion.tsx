// The companion on the desktop: 大肥鱼, played with dsh-pet (dsh-pet/README.md).
// This part only asks the Product Service which animations are installed, after
// the desktop has drawn; the companion's own code and animations load only when
// some are, so a NAS without them shows nothing and pays nothing.
import { Suspense, useEffect, useState } from "react";

import { lazyCode } from "../ui/lazyCode";

const CompanionPet = lazyCode(() => import("./CompanionPet").then((module) => module.CompanionPet));

export type InstalledAnimations = Record<string, Partial<Record<"webm" | "mov", string>>>;

export async function loadInstalledAnimations(): Promise<InstalledAnimations> {
  const response = await fetch("/companion/animations", { cache: "no-store" });
  if (!response.ok) return {};
  const manifest = (await response.json()) as { animations?: unknown };
  return manifest.animations && typeof manifest.animations === "object"
    ? (manifest.animations as InstalledAnimations)
    : {};
}

export function Companion() {
  const [installed, setInstalled] = useState<InstalledAnimations>();
  useEffect(() => {
    let current = true;
    const load = () =>
      void loadInstalledAnimations()
        .then((animations) => current && Object.keys(animations).length > 0 && setInstalled(animations))
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
  if (!installed) return null;
  return (
    <Suspense fallback={null}>
      <CompanionPet installed={installed} />
    </Suspense>
  );
}
