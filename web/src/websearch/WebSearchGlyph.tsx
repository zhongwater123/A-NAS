import type { GlyphProps } from "../media/MediaCenterGlyph";

// WebSearchGlyph is the 网页搜索 mark: a lens whose glass is a globe, drawn
// in white for the app's deep blue tile.
export function WebSearchGlyph({ size = 24, ...props }: GlyphProps) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.9"
      strokeLinecap="round"
      strokeLinejoin="round"
      {...props}
    >
      <circle cx="10.5" cy="10.5" r="7" />
      <ellipse cx="10.5" cy="10.5" rx="3" ry="7" strokeWidth="1.4" />
      <path d="M3.8 8.4h13.4M3.8 12.6h13.4" strokeWidth="1.4" />
      <path d="m15.6 15.6 5.4 5.4" strokeWidth="2.6" />
    </svg>
  );
}
