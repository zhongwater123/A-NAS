// Sizes are counted in powers of 1024 everywhere in A-NAS and labelled with
// binary units, so the same volume reads the same in every window
// (docs/specs/size-units.md). Network rates in the status bar are the one
// exception: they count in powers of 1000, like a network link.

const units = ["KiB", "MiB", "GiB", "TiB", "PiB"];

// formatBytes shows a size with one decimal below 100 and none above:
// 980 B, 3.4 MiB, 13.9 GiB, 466 GiB.
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  if (bytes < 1024) return `${Math.round(bytes)} B`;
  let value = bytes / 1024;
  let unit = 0;
  // Promote on the rounded value, so 1023.7 KiB reads 1.0 MiB, not 1024 KiB.
  while (Math.round(value) >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  // 99.96 would print as "100.0": from there on whole numbers.
  const text = value < 99.95 ? value.toFixed(1) : String(Math.round(value));
  return `${text} ${units[unit]}`;
}

export function formatRate(bytesPerSecond: number): string {
  return `${formatBytes(bytesPerSecond)}/s`;
}

// formatNominalCapacity is how a disk's maker labels it: powers of 1000,
// whole gigabytes, or terabytes with at most one decimal (500 GB, 4 TB).
export function formatNominalCapacity(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "—";
  if (bytes >= 1e12) return `${Number((bytes / 1e12).toFixed(1))} TB`;
  return `${Math.round(bytes / 1e9)} GB`;
}
