import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

const keepFile = fileURLToPath(new URL("../internal/webui/dist/.keep", import.meta.url));

export default defineConfig({
  plugins: [
    react(),
    {
      name: "preserve-go-embed-directory",
      closeBundle() {
        writeFileSync(keepFile, "Vite writes generated assets here before Go compilation.\n");
      },
    },
  ],
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    host: "127.0.0.1",
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/healthz": "http://127.0.0.1:8080",
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    css: true,
    pool: "threads",
    fileParallelism: false,
    maxWorkers: 1,
  },
});
