import { useId, type ComponentType, type SVGProps } from "react";

export type GlyphProps = SVGProps<SVGSVGElement> & { size?: number | string };
// AppGlyph is what windows and the dock draw an app with: a Lucide icon or
// an app's own mark.
export type AppGlyph = ComponentType<Omit<GlyphProps, "ref">>;

// MediaCenterGlyph is the 影视中心 mark: a clapperboard with a play cut-out in
// a sunset gradient, drawn for the app's black tile. Each copy names its own
// gradient, because a copy inside a hidden window would hide a shared one.
export function MediaCenterGlyph({ size = 24, ...props }: GlyphProps) {
  const gradient = `mc-glyph-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
  const fill = `url(#${gradient})`;
  return (
    <svg xmlns="http://www.w3.org/2000/svg" width={size} height={size} viewBox="0 0 24 24" fill="none" {...props}>
      <defs>
        <linearGradient id={gradient} x1="3" y1="2" x2="19" y2="22" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#ffe066" />
          <stop offset=".45" stopColor="#ff9a3c" />
          <stop offset="1" stopColor="#ff4470" />
        </linearGradient>
      </defs>
      <path
        d="M2.6 10.1h18.8v8a3 3 0 0 1-3 3H5.6a3 3 0 0 1-3-3Z"
        fill={fill}
        stroke={fill}
        strokeWidth=".6"
        strokeLinejoin="round"
      />
      <path d="M2.3 6.7 20.3 2.2l.68 2.72-18 4.5Z" fill={fill} stroke={fill} strokeWidth=".8" strokeLinejoin="round" />
      <path d="m7.16 5.49 1.84 2.43M11.66 4.36l1.84 2.43M16.16 3.24l1.84 2.43" stroke="#0d0d10" strokeWidth="1.35" />
      <path d="M10.2 13.1v5.1c0 .5.5.8.9.5l3.9-2.5c.4-.3.4-.8 0-1.1l-3.9-2.5c-.4-.3-.9 0-.9.5Z" fill="#0d0d10" />
    </svg>
  );
}
