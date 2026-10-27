// The size of what the browser downloads before the desktop or the sign-in
// page shows: the scripts and styles index.html names, compressed with Brotli
// as size-limit measures them. Windows load their own code when first opened
// and are not counted. `npm run build` fails above a limit; raise one only for
// something every visit needs, and say why in the pull request
// (docs/architecture/web-desktop.md).
import { readFileSync } from "node:fs";

const dist = "../internal/webui/dist";
const html = readFileSync(`${dist}/index.html`, "utf8");
const firstLoad = (extension) =>
  [...html.matchAll(/(?:src|href)="\/(assets\/[^"]+)"/g)]
    .map((match) => match[1])
    .filter((file) => file.endsWith(extension))
    .map((file) => `${dist}/${file}`);

export default [
  { name: "First load: JavaScript", path: firstLoad(".js"), limit: "160 kB" },
  { name: "First load: CSS", path: firstLoad(".css"), limit: "25 kB" },
];
