// The size of what the browser downloads before the desktop or the sign-in
// page shows, compressed with Brotli as size-limit measures it: the chunks
// index.html loads with every chunk they import and the styles of all of
// them, read from Vite's manifest, and the response validators, which every
// visit downloads at once beside the first requests (src/api/client.ts).
// Windows load their own code when first opened and are not counted.
// `npm run build` fails above a limit; raise one only for something every
// visit needs, and say why in the pull request (docs/architecture/web-desktop.md).
import { readFileSync } from "node:fs";

const dist = "../internal/webui/dist";
const manifest = JSON.parse(readFileSync(`${dist}/.vite/manifest.json`, "utf8"));

const chunks = new Set();
const visit = (key) => {
  if (chunks.has(key)) return;
  if (!manifest[key]) throw new Error(`${key} is not in Vite's manifest`);
  chunks.add(key);
  for (const imported of manifest[key].imports ?? []) visit(imported);
};
visit("index.html");
visit("src/api.validators.generated.ts");
const loaded = [...chunks].map((key) => manifest[key]);
const files = (names) => [...new Set(names)].map((name) => `${dist}/${name}`);

export default [
  { name: "First load: JavaScript", path: files(loaded.map((chunk) => chunk.file)), limit: "170 kB" },
  { name: "First load: CSS", path: files(loaded.flatMap((chunk) => chunk.css ?? [])), limit: "16 kB" },
];
