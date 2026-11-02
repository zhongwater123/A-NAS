const { isIP } = require("node:net");

function nasOrigin(input) {
  if (!input) return undefined;
  const url = new URL(input);
  const host = url.hostname;
  const octets = host.split(".").map(Number);
  const local = host === "localhost" || host.endsWith(".localhost") || host.endsWith(".local") ||
    (isIP(host) === 4 && (octets[0] === 127 || octets[0] === 10 ||
      (octets[0] === 192 && octets[1] === 168) || (octets[0] === 172 && octets[1] >= 16 && octets[1] <= 31)));
  if (url.username || url.password || (url.protocol !== "https:" && !(url.protocol === "http:" && local))) {
    throw new Error("NAS address must be HTTPS or a local HTTP address without embedded credentials");
  }
  return url.origin;
}

function safeDelta(value) {
  return typeof value === "number" && Number.isFinite(value) ? Math.max(-120, Math.min(120, value)) : 0;
}
function clampWindow(point, workArea, size) {
  return {
    x: Math.round(Math.max(workArea.x, Math.min(workArea.x + Math.max(0, workArea.width - size.width), point.x))),
    y: Math.round(Math.max(workArea.y, Math.min(workArea.y + Math.max(0, workArea.height - size.height), point.y))),
  };
}
function validConversation(value) { return typeof value === "string" && /^[0-9]{8}_[0-9]{6}_[0-9a-f]{6}$/.test(value); }
module.exports = { nasOrigin, safeDelta, clampWindow, validConversation };
