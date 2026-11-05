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
// The character and its button row fill only the lower middle of the 350x400 window; the
// rest is transparent room for the speech bubble and the chat. Keeping the body (a
// rectangle inside the window) on the work area lets that room run off the screen, so
// the character itself reaches every edge.
function clampBody(point, workArea, body) {
  return {
    x: Math.round(Math.max(workArea.x - body.x,
      Math.min(workArea.x + Math.max(0, workArea.width - body.width) - body.x, point.x))),
    y: Math.round(Math.max(workArea.y - body.y,
      Math.min(workArea.y + Math.max(0, workArea.height - body.height) - body.y, point.y))),
  };
}
function validBody(value, size) {
  if (!value || typeof value !== "object") return undefined;
  const { x, y, width, height } = value;
  if (![x, y, width, height].every((n) => typeof n === "number" && Number.isFinite(n))) return undefined;
  // A page cannot claim a body outside its own window (1 px slack for rounding).
  if (x < 0 || y < 0 || width < 1 || height < 1 || x + width > size.width + 1 || y + height > size.height + 1) return undefined;
  return { x: Math.round(x), y: Math.round(y), width: Math.round(width), height: Math.round(height) };
}
function validConversation(value) { return typeof value === "string" && /^(?:[0-9]{8}_[0-9]{6}_[0-9a-f]{6}|cron_[0-9a-f]{12}_[0-9]{8}_[0-9]{6})$/.test(value); }
module.exports = { nasOrigin, safeDelta, clampWindow, clampBody, validBody, validConversation };
