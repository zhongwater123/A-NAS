import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Separate offline playground. Not embedded in the NAS or its first load.
export default defineConfig({
  base: "./",
  plugins: [react()],
  css: { modules: { localsConvention: "camelCase" } },
  build: {
    outDir: "../desktop/companion/dist",
    emptyOutDir: true,
    rolldownOptions: { input: fileURLToPath(new URL("./companion.html", import.meta.url)) },
  },
  server: { host: "127.0.0.1" },
});
