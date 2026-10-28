import logo from "../assets/assistant-logo.webp";
import type { GlyphProps } from "../media/MediaCenterGlyph";

// AssistantGlyph is the AI 助手 mark (2026-10-16, the user's logo-v1): an ink
// portrait on white, so it fills a white tile edge to edge and takes the
// tile's corners. The file is 192 px square, three times the largest place it
// is drawn (the 64 px welcome mark), reduced from the 1254 px original as a
// WebP at quality 92.
export function AssistantGlyph({ size = 24 }: GlyphProps) {
  return <img className="assistant-glyph" src={logo} width={size} height={size} alt="" draggable={false} />;
}
