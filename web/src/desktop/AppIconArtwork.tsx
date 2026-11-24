import appCenter from "../assets/app-center-icon.webp";
import backup from "../assets/backup-center-icon.webp";
import family from "../assets/family-center-icon.webp";
import files from "../assets/file-manager-icon.webp";
import music from "../assets/music-icon.webp";
import scoring from "../assets/photo-scoring-icon.webp";
import trash from "../assets/recycle-bin-icon.webp";
import transfer from "../assets/transfer-center-icon.webp";
import webSearch from "../assets/web-search-icon.webp";
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

export function FileManagerIcon(props: GlyphProps) {
  return <AppIconArtwork source={files} {...props} />;
}

export function FamilyCenterIcon(props: GlyphProps) {
  return <AppIconArtwork source={family} {...props} />;
}

export function PhotoScoringIcon(props: GlyphProps) {
  return <AppIconArtwork source={scoring} {...props} />;
}

export function WebSearchIcon(props: GlyphProps) {
  return <AppIconArtwork source={webSearch} {...props} />;
}

export function AppCenterIcon(props: GlyphProps) {
  return <AppIconArtwork source={appCenter} {...props} />;
}

export function RecycleBinIcon(props: GlyphProps) {
  return <AppIconArtwork source={trash} {...props} />;
}
