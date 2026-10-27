import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

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
      "/api": { target: "http://127.0.0.1:8080", ws: true },
      "/healthz": "http://127.0.0.1:8080",
    },
  },
  test: {
    environment: "jsdom",
    // forms.ts replaces Request before MSW (setup.ts) takes it.
    setupFiles: ["./src/test/forms.ts", "./src/test/setup.ts"],
    css: true,
    pool: "threads",
    fileParallelism: false,
    maxWorkers: 1,
    // Development machines share their cores with other sessions' builds and
    // tests; the 5 s default failed tests that only waited for the CPU. A test
    // that never finishes still fails, 20 s later.
    testTimeout: 20_000,
    hookTimeout: 20_000,
  },
});
