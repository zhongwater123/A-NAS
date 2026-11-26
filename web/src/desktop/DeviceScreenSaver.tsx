import { Suspense } from "react";
import { ErrorBoundary } from "react-error-boundary";

import { isLocalConsole } from "../lib/localConsole";
import { lazyCode } from "../ui/lazyCode";

const LocalConsoleScreenSaver = lazyCode(() =>
  import("./LocalConsoleScreenSaver").then((module) => module.LocalConsoleScreenSaver),
);

// DeviceScreenSaver is the screen saver and 息屏 where the page is the
// device's own screen (LocalConsoleScreenSaver.tsx), on the desktop and on a
// shared page's own tab. Only that screen downloads their code and styles;
// other visitors' first load leaves them out. Without the code the screen
// stays lit and the page works on.
export function DeviceScreenSaver({ videos }: { videos: boolean }) {
  if (!isLocalConsole()) return null;
  return (
    <ErrorBoundary
      fallback={null}
      onError={(error, info) => console.error("A-NAS screen saver failed", error, info.componentStack)}
    >
      <Suspense fallback={null}>
        <LocalConsoleScreenSaver enabled videos={videos} />
      </Suspense>
    </ErrorBoundary>
  );
}
