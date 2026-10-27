import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ErrorBoundary } from "react-error-boundary";

import App from "./App";
import { BootSplash } from "./boot/BootSplash";

const container = document.getElementById("root")!;

// The desktop's last resort: a window or a part of the desktop that fails
// has its own boundary, so this shows only when the desktop itself cannot.
function DesktopFailed() {
  return (
    <main className="auth-shell">
      <section className="auth-card" role="alert">
        <span className="brand-mark large">A</span>
        <h1>桌面暂时无法显示</h1>
        <p>文件与后台服务不受影响，重新载入页面后继续。</p>
        <button className="primary" onClick={() => window.location.reload()}>
          重新载入
        </button>
      </section>
    </main>
  );
}

createRoot(container, {
  // Only an error in the boundary's own fallback gets here; React has
  // removed the page by then, so say so in plain text rather than stay blank.
  onUncaughtError: (error, info) => {
    console.error("A-NAS desktop failed", error, info.componentStack);
    container.textContent = "A-NAS 桌面无法显示，请重新载入页面。";
  },
}).render(
  <StrictMode>
    <ErrorBoundary
      FallbackComponent={DesktopFailed}
      onError={(error, info) => console.error("A-NAS desktop failed", error, info.componentStack)}
    >
      <App />
    </ErrorBoundary>
    <BootSplash />
  </StrictMode>,
);
