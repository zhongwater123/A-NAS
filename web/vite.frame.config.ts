// The helper script of shared pages (src/workspace/frame/helper.ts, ADR 0040),
// built after the desktop as one classic script at a fixed name: pages load
// it from an opaque origin, where a module script would need CORS, and the
// workspace service writes its address into every page it shows. The build
// adds to the desktop's output and leaves its files and manifest alone;
// internal/webui serves the file with no-cache.
import { defineConfig } from "vite";

export default defineConfig({
  build: {
    outDir: "../internal/webui/dist/assets",
    emptyOutDir: false,
    sourcemap: false,
    copyPublicDir: false,
    lib: {
      entry: "src/workspace/frame/helper.ts",
      name: "anasWorkspaceFrame",
      formats: ["iife"],
      fileName: () => "workspace-frame.js",
    },
  },
});
