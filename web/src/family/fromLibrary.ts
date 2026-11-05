// Photos chosen from the photo library join a channel as assets of the
// shared library (ADR 0029): a shared photo as it is, a photo of the
// member's own library by the shared copy it already has, or else by a new
// copy. The family center and the photos window both add them this way.

import { copyPhoto, type PhotoAsset } from "../photos/api";
import { albumContains } from "./api";
import { isoDate } from "./format";

// containsBatch is how many photos one albumContains call may name.
const containsBatch = 500;

// sharedIdOf is the shared-library asset a photo joins as, when it has one.
export function sharedIdOf(asset: PhotoAsset, sharedLibraryId: string): string | undefined {
  return asset.libraryId === sharedLibraryId ? asset.id : asset.sharedCopy;
}

// inAlbum asks which of ids are in a baby's album already.
export async function inAlbum(babyId: string, ids: string[]): Promise<Set<string>> {
  const found = new Set<string>();
  for (let start = 0; start < ids.length; start += containsBatch)
    for (const id of await albumContains(babyId, ids.slice(start, start + containsBatch))) found.add(id);
  return found;
}

// toShared returns each photo's shared-library asset, copying the photos that
// have none. copies remembers the copies made, so trying again after a
// failure does not copy a photo twice.
export async function toShared(
  assets: PhotoAsset[],
  sharedLibraryId: string,
  copies: Map<string, string>,
  onCopied: (done: number) => void = () => undefined,
): Promise<string[]> {
  const ids: string[] = [];
  for (const asset of assets) {
    let id = sharedIdOf(asset, sharedLibraryId) ?? copies.get(asset.id);
    if (!id) {
      id = (await copyPhoto(asset.id, sharedLibraryId)).id;
      copies.set(asset.id, id);
      onCopied(copies.size);
    }
    ids.push(id);
  }
  return ids;
}

// needsCopy counts the photos that a channel takes as new shared copies.
export function needsCopy(assets: PhotoAsset[], sharedLibraryId: string): number {
  return assets.filter((asset) => !sharedIdOf(asset, sharedLibraryId)).length;
}

// maxPhotos is how many photos one record request takes (internal/family
// MaxPhotos).
export const maxPhotos = 200;

// dayOf is the day a photo was taken, or uploaded when it does not say: the
// day its record falls on.
export function dayOf(photo: Pick<PhotoAsset, "takenAt" | "importedAt">): string {
  return isoDate(new Date(photo.takenAt ?? photo.importedAt));
}

// batchByDay packs photos into requests of at most maxPhotos, a day's photos
// together where they fit, so that each day stays one record.
export function batchByDay(photos: Array<{ id: string; day: string }>): string[][] {
  const days = new Map<string, string[]>();
  for (const photo of [...photos].sort((a, b) => a.day.localeCompare(b.day)))
    days.set(photo.day, [...(days.get(photo.day) ?? []), photo.id]);
  const batches: string[][] = [];
  let batch: string[] = [];
  for (const group of days.values()) {
    if (batch.length && batch.length + group.length > maxPhotos) {
      batches.push(batch);
      batch = [];
    }
    for (const id of group) {
      if (batch.length === maxPhotos) {
        batches.push(batch);
        batch = [];
      }
      batch.push(id);
    }
  }
  if (batch.length) batches.push(batch);
  return batches;
}
