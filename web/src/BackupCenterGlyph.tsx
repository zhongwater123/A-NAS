import type { GlyphProps } from "./MediaCenterGlyph";

// BackupCenterGlyph is the 备份中心 mark: a white shield with a clock turning
// back, cut out in the deep teal of the app's tile.
export function BackupCenterGlyph({ size = 24, ...props }: GlyphProps) {
  return (
    <svg xmlns="http://www.w3.org/2000/svg" width={size} height={size} viewBox="0 0 24 24" fill="none" {...props}>
      <path
        d="M12 2.3c2.3 1.5 4.8 2.3 7.6 2.5.4 0 .6.3.6.7v6c0 4.9-3.2 8.7-7.9 10.4a.9.9 0 0 1-.6 0C7 20.2 3.8 16.4 3.8 11.5v-6c0-.4.3-.7.6-.7 2.8-.2 5.3-1 7.6-2.5Z"
        fill="#fff"
      />
      <g stroke="#0c7f8d" strokeWidth="1.55" strokeLinecap="round" strokeLinejoin="round">
        <path d="M7.9 12.4a4.1 4.1 0 1 0 4.1-4.1 4.4 4.4 0 0 0-3.07 1.25L7.9 10.6" />
        <path d="M7.9 8.4v2.2h2.2" />
        <path d="M12 10.3v2.3l1.75.9" />
      </g>
    </svg>
  );
}
