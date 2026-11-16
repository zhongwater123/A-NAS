import type { GlyphProps } from "../media/MediaCenterGlyph";

// ScoringGlyph is the 照片打分 mark: a landscape photo with a star at its
// corner, drawn in white for the app's tile.
export function ScoringGlyph({ size = 24, ...props }: GlyphProps) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      {...props}
    >
      <rect x="2.5" y="5" width="15" height="13" rx="2.2" />
      <path d="m3.5 16.5 4.3-4.6 3 3 2-2 3.7 3.6" />
      <circle cx="13" cy="9.2" r="1.3" />
      <path
        d="m19 2.6 1.1 2.2 2.4.35-1.75 1.7.42 2.4L19 8.1l-2.17 1.15.42-2.4-1.75-1.7 2.4-.35Z"
        fill="currentColor"
        strokeWidth="1.2"
      />
    </svg>
  );
}
