import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import App from "./App";
import { BootSplash } from "./BootSplash";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
    <BootSplash />
  </StrictMode>,
);
