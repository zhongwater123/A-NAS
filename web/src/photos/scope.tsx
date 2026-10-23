import { UsersRound } from "lucide-react";

import { type Caller, libraryName, libraryOf, ownLibrary, sharedLibrary } from "./model";
import type { PhotoAsset, PhotoLibrary } from "./photosApi";

// A search covers the caller's library, the shared one and a member library
// viewed read-only; it can be narrowed to one of them. An empty choice
// searches them all.

export function searchLibraries(caller: Caller, viewing: string): PhotoLibrary[] {
  return [ownLibrary(caller), sharedLibrary(caller), viewing ? libraryOf(caller, viewing) : undefined].filter(
    (library): library is PhotoLibrary => Boolean(library),
  );
}

// scopeText names what a search covers, for the toolbar.
export function scopeText(caller: Caller, viewing: string, library: string): string {
  if (library) return libraryName(caller, libraryOf(caller, library));
  const viewed = viewing ? libraryOf(caller, viewing) : undefined;
  return viewed ? `我的图库、共享图库和${libraryName(caller, viewed)}` : "我的图库和共享图库";
}

const shortName = (caller: Caller, library: PhotoLibrary) =>
  library.kind === "shared" ? "共享" : library === ownLibrary(caller) ? "我的" : `${library.ownerName ?? "成员"}的`;

export function SearchScope({
  caller,
  viewing,
  value,
  onChange,
}: {
  caller: Caller;
  viewing: string;
  value: string;
  onChange: (library: string) => void;
}) {
  const options = [
    { id: "", label: "全部" },
    ...searchLibraries(caller, viewing).map((library) => ({ id: library.id, label: shortName(caller, library) })),
  ];
  return (
    <div className="ph-scope" role="radiogroup" aria-label="搜索范围">
      {options.map((option) => (
        <button
          key={option.id || "all"}
          type="button"
          role="radio"
          aria-checked={value === option.id}
          className={value === option.id ? "active" : ""}
          title={
            option.id ? `只搜${libraryName(caller, libraryOf(caller, option.id))}` : scopeText(caller, viewing, "")
          }
          onClick={() => onChange(option.id)}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}

// SourceBadge marks a photo that is not in the caller's own library, where
// results mix libraries.
export function SourceBadge({ caller, asset }: { caller: Caller; asset: PhotoAsset }) {
  const library = libraryOf(caller, asset.libraryId);
  if (!library || library === ownLibrary(caller)) return null;
  return (
    <span className="ph-tile-source" title={`来自${libraryName(caller, library)}`}>
      {library.kind === "shared" && <UsersRound />}
      {shortName(caller, library)}
    </span>
  );
}
