import { useId } from "react";

import type { GlyphProps } from "../media/MediaCenterGlyph";

// PhotosGlyph is the 相册 mark, flat and in colour (2026-10-13): a coral
// print behind a landscape print of sky, sun, a teal peak and a green hill,
// drawn for the app's white tile. A white rim parts the two prints. The view
// box fits the prints with no margin, so the mark fills what it is given.
// Each copy names its own clip path, because a copy inside a hidden window
// would hide a shared one.
export function PhotosGlyph({ size = 24, ...props }: GlyphProps) {
  const clip = `ph-glyph-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="1.2 1.6 20.8 20.8"
      fill="none"
      {...props}
    >
      <defs>
        <clipPath id={clip}>
          <rect x="2.4" y="7.4" width="15.6" height="13.4" rx="2.7" />
        </clipPath>
      </defs>
      <rect x="6.9" y="3.1" width="14.4" height="11.6" rx="2.5" transform="rotate(11 14.1 8.9)" fill="#ff6f86" />
      <rect x="1.55" y="6.55" width="17.3" height="15.1" rx="3.4" fill="#fff" />
      <g clipPath={`url(#${clip})`}>
        <rect x="2.4" y="7.4" width="15.6" height="13.4" fill="#3b9cff" />
        <circle cx="13.7" cy="11.4" r="1.9" fill="#ffc93e" />
        <path d="M1 20.8 7.4 13.6l5.4 7.2Z" fill="#20b5a4" />
        <path d="M5.2 21.5c2.6-4.1 7.1-5.7 13.6-4.6v4.6Z" fill="#62d26b" />
      </g>
    </svg>
  );
}
