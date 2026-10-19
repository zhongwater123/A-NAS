// The photo tools ask the assistant to name a photo as a Markdown link to its
// id, which itself starts with "photo:" (ai/assistant-spike/anas_mcp.py). The
// markdown sanitizer keeps only web protocols, so such links become relative
// ones the window recognises and shows as the photo's thumbnail, loaded with
// the member's own session.
export const photoLinkPrefix = "#a-nas-photo/";

// An id written once more after the scheme ("photo:photo:…") is the same photo.
const photoLink = /\]\((?:photo:)?(photo:[A-Za-z0-9_.-]{1,128})\)/g;

export function linkPhotos(text: string): string {
  return text.includes("](photo:") ? text.replace(photoLink, (_, id: string) => `](${photoLinkPrefix}${encodeURIComponent(id)})`) : text;
}

/** The photo a link names, when it is one. */
export function photoOf(href: string | undefined): string | undefined {
  if (!href?.startsWith(photoLinkPrefix)) return undefined;
  try {
    return decodeURIComponent(href.slice(photoLinkPrefix.length)) || undefined;
  } catch {
    return undefined;
  }
}

export interface NamedPhoto {
  id: string;
  name: string;
}

const namedPhotoLink = /\[([^\]\n]{0,200})\]\((#a-nas-photo\/[^)\s]+)\)/g;

/** The photos a linked answer names, in order and each once, with the names it gives them. */
export function photosIn(text: string): NamedPhoto[] {
  const found: NamedPhoto[] = [];
  const seen = new Set<string>();
  for (const [, label, href] of text.matchAll(namedPhotoLink)) {
    const id = photoOf(href);
    if (!id || seen.has(id)) continue;
    seen.add(id);
    found.push({ id, name: label.replace(/[*`]/g, "").trim() || "照片" });
  }
  return found;
}
