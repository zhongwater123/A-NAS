import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The built page is what the desktop companion loads from disk: only its own files
// run there. The dev server keeps Vite's inline client, so the policy is build-only.
const policy = [
  "default-src 'self'",
  "script-src 'self'",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data: blob:",
  "font-src 'self' data:",
  "connect-src 'self'",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
].join("; ");

// Separate offline playground. Not embedded in the NAS or its first load.
export default defineConfig({
  base: "./",
  plugins: [
    react(),
    {
      name: "offline-content-security-policy",
      apply: "build",
      transformIndexHtml: () => [
        { tag: "meta", attrs: { "http-equiv": "Content-Security-Policy", content: policy }, injectTo: "head-prepend" },
      ],
    },
  ],
  css: { modules: { localsConvention: "camelCase" } },
  build: {
    outDir: "../desktop/companion/dist",
    emptyOutDir: true,
    rolldownOptions: { input: fileURLToPath(new URL("./companion.html", import.meta.url)) },
  },
  server: { host: "127.0.0.1" },
});
