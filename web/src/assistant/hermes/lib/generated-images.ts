// A-NAS shim for Hermes Desktop's lib/generated-images. The assistant has no
// image generation (ADR 0024), so there are no generated-image echoes to strip.
export function dedupeGeneratedImageEchoesInParts<T>(parts: T[]): T[] {
  return parts;
}
