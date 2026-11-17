import backup from "../assets/backup-center-icon.webp";
import music from "../assets/music-icon.webp";
import transfer from "../assets/transfer-center-icon.webp";
import type { GlyphProps } from "../media/MediaCenterGlyph";

function AppIconArtwork({ source, size = 24, ...props }: GlyphProps & { source: string }) {
  return (
    <svg width={size} height={size} viewBox="0 0 192 192" aria-hidden="true" data-app-artwork="" {...props}>
      <image href={source} width="192" height="192" />
    </svg>
  );
}

export function TransferCenterIcon(props: GlyphProps) {
  return <AppIconArtwork source={transfer} {...props} />;
}

export function BackupCenterIcon(props: GlyphProps) {
  return <AppIconArtwork source={backup} {...props} />;
}

export function MusicIcon(props: GlyphProps) {
  return <AppIconArtwork source={music} {...props} />;
}
