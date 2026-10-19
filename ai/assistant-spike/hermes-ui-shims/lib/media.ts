// A-NAS shim for Hermes Desktop's lib/media: the two helpers the transcript
// model uses, without the Desktop file and session stores behind the rest.
const IMAGE = /\.(png|jpe?g|gif|webp|avif|heic|bmp|svg)$/i;
const VIDEO = /\.(mp4|m4v|mov|webm|mkv)$/i;
const AUDIO = /\.(mp3|m4a|wav|flac|ogg|opus|aac)$/i;

function mediaName(path: string): string {
  return path.split(/[\\/]/).filter(Boolean).pop() ?? path;
}

function mediaKind(path: string): string {
  if (IMAGE.test(path)) return "image";
  if (VIDEO.test(path)) return "video";
  if (AUDIO.test(path)) return "audio";
  return "file";
}

export function mediaDisplayLabel(path: string): string {
  const escaped = mediaName(path).replace(/[[\]\\]/g, "\\$&");
  const kind = mediaKind(path);
  return `${kind.charAt(0).toUpperCase()}${kind.slice(1)}: ${escaped}`;
}

export function mediaMarkdownHref(path: string): string {
  return `#media:${encodeURIComponent(path)}`;
}
