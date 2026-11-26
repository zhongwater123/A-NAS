// The entry of a shared page's own tab (workspace.html), which internal/webui
// serves at /workspace/{pageId}. It loads none of the desktop.
import "../styles/index.css";

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { PageTab } from "./PageTab";

// The desktop opens this tab by name so that it can return to it, which
// leaves it the tab's opener: let go of it before the page's frame loads, so
// that the page cannot reach the desktop through top.opener.
window.opener = null;

const pageId = decodeURIComponent(window.location.pathname.split("/")[2] ?? "");

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <PageTab pageId={pageId} />
  </StrictMode>,
);
