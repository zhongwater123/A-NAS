// The entry of a shared page's own tab (workspace.html), which internal/webui
// serves at /workspace/{pageId}. It loads none of the desktop.
import "../styles/index.css";

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { PageTab } from "./PageTab";

const pageId = decodeURIComponent(window.location.pathname.split("/")[2] ?? "");

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <PageTab pageId={pageId} />
  </StrictMode>,
);
