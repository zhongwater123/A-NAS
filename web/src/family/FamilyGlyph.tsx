import type { GlyphProps } from "../media/MediaCenterGlyph";

// FamilyGlyph is the 家庭中心 mark, drawn for the app's warm coral tile: a
// house of cream with a heart in its doorway, the family center's one idea
// of a home where everyone keeps the baby's days.
export function FamilyGlyph({ size = 24, ...props }: GlyphProps) {
  return (
    <svg xmlns="http://www.w3.org/2000/svg" width={size} height={size} viewBox="0 0 24 24" fill="none" {...props}>
      <path
        d="M3.6 10.6 11.1 4.3a1.4 1.4 0 0 1 1.8 0l7.5 6.3c.4.3.6.8.6 1.3v7.4c0 1-.8 1.8-1.8 1.8H4.8c-1 0-1.8-.8-1.8-1.8v-7.4c0-.5.2-1 .6-1.3Z"
        fill="#fff7ef"
      />
      <path
        d="M12 18.3s-3.9-2.3-3.9-5c0-1.3 1-2.3 2.2-2.3.7 0 1.3.3 1.7.9.4-.6 1-.9 1.7-.9 1.2 0 2.2 1 2.2 2.3 0 2.7-3.9 5-3.9 5Z"
        fill="#ef6a45"
      />
    </svg>
  );
}
